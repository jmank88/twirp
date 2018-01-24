// Copyright 2018 Twitch Interactive, Inc.  All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the License is
// located at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// or in the "license" file accompanying this file. This file is distributed on
// an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"go/parser"
	"go/printer"
	"go/token"
	"path"
	"strconv"
	"strings"

	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/protoc-gen-go/descriptor"
	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"
	"github.com/pkg/errors"
	"github.com/twitchtv/twirp/internal/gen"
	"github.com/twitchtv/twirp/internal/gen/stringutils"
	"github.com/twitchtv/twirp/internal/gen/typemap"
)

type twirp struct {
	filesHandled   int
	currentPackage string // Go name of current package we're working on

	reg *typemap.Registry

	// Map to record whether we've built each package
	pkgs          map[string]string
	pkgNamesInUse map[string]bool

	// Package naming:
	genPkgName          string // Name of the package that we're generating
	fileToGoPackageName map[*descriptor.FileDescriptorProto]string

	// List of files that were inputs to the generator. We need to hold this in
	// the struct so we can write a header for the file that lists its inputs.
	genFiles []*descriptor.FileDescriptorProto

	// Output buffer that holds the bytes we want to write out for a single file.
	// Gets reset after working on a file.
	output *bytes.Buffer
}

func newGenerator() *twirp {
	t := &twirp{
		pkgs:                make(map[string]string),
		pkgNamesInUse:       make(map[string]bool),
		fileToGoPackageName: make(map[*descriptor.FileDescriptorProto]string),
		output:              bytes.NewBuffer(nil),
	}

	return t
}

func (t *twirp) Generate(in *plugin.CodeGeneratorRequest) *plugin.CodeGeneratorResponse {
	t.genFiles = gen.FilesToGenerate(in)

	// Collect information on types.
	t.reg = typemap.New(in.ProtoFile)

	// Register names of packages that we import.
	t.registerPackageName("bytes")
	t.registerPackageName("strings")
	t.registerPackageName("ctxsetters")
	t.registerPackageName("context")
	t.registerPackageName("http")
	t.registerPackageName("ioutil")
	t.registerPackageName("jsonpb")
	t.registerPackageName("proto")
	t.registerPackageName("twirp")
	t.registerPackageName("fmt")

	// Time to figure out package names of objects defined in protobuf. First,
	// we'll figure out the name for the package we're generating.
	genPkgName, err := deduceGenPkgName(t.genFiles)
	if err != nil {
		gen.Fail(err.Error())
	}
	t.genPkgName = genPkgName

	// Next, we need to pick names for all the files that are dependencies.
	for _, f := range in.ProtoFile {
		if fileDescSliceContains(t.genFiles, f) {
			// This is a file we are generating. It gets the shared package name.
			t.fileToGoPackageName[f] = t.genPkgName
		} else {
			// This is a dependency. Use its package name.
			name := f.GetPackage()
			if name == "" {
				name = stringutils.BaseName(f.GetName())
			}
			name = stringutils.CleanIdentifier(name)
			t.fileToGoPackageName[f] = name
			t.registerPackageName(name)
		}
	}

	// Showtime! Generate the response.
	resp := new(plugin.CodeGeneratorResponse)
	for _, f := range t.genFiles {
		respFile := t.generate(f)
		if respFile != nil {
			resp.File = append(resp.File, respFile)
		}
	}
	return resp
}

func (t *twirp) registerPackageName(name string) (alias string) {
	alias = name
	i := 1
	for t.pkgNamesInUse[alias] {
		alias = name + strconv.Itoa(i)
		i++
	}
	t.pkgNamesInUse[alias] = true
	t.pkgs[name] = alias
	return alias
}

// deduceGenPkgName figures out the go package name to use for generated code.
// Will try to use the explicit go_package setting in a file (if set, must be
// consistent in all files). If no files have go_package set, then use the
// protobuf package name (must be consistent in all files)
func deduceGenPkgName(genFiles []*descriptor.FileDescriptorProto) (string, error) {
	var genPkgName string
	for _, f := range genFiles {
		name, explicit := goPackageName(f)
		if explicit {
			name = stringutils.CleanIdentifier(name)
			if genPkgName != "" && genPkgName != name {
				// Make sure they're all set consistently.
				return "", errors.Errorf("files have conflicting go_package settings, must be the same: %q and %q", genPkgName, name)
			}
			genPkgName = name
		}
	}
	if genPkgName != "" {
		return genPkgName, nil
	}

	// If there is no explicit setting, then check the implicit package name
	// (derived from the protobuf package name) of the files and make sure it's
	// consistent.
	for _, f := range genFiles {
		name, _ := goPackageName(f)
		name = stringutils.CleanIdentifier(name)
		if genPkgName != "" && genPkgName != name {
			return "", errors.Errorf("files have conflicting package names, must be the same or overridden with go_package: %q and %q", genPkgName, name)
		}
		genPkgName = name
	}

	// All the files have the same name, so we're good.
	return genPkgName, nil
}

func (t *twirp) generate(file *descriptor.FileDescriptorProto) *plugin.CodeGeneratorResponse_File {
	resp := new(plugin.CodeGeneratorResponse_File)
	if len(file.Service) == 0 {
		return nil
	}

	t.generateFileHeader(file)

	t.generateImports(file)

	// For each service, generate client stubs and server
	for i, service := range file.Service {
		t.generateService(file, service, i)
	}

	t.generateFileDescriptor(file)

	resp.Name = proto.String(goFileName(file))
	resp.Content = proto.String(t.formattedOutput())
	t.output.Reset()

	t.filesHandled++
	return resp
}

func (t *twirp) generateFileHeader(file *descriptor.FileDescriptorProto) {
	t.P("// Code generated by protoc-gen-twirp ", gen.Version, ", DO NOT EDIT.")
	t.P("// source: ", file.GetName())
	t.P()
	if t.filesHandled == 0 {
		t.P("/*")
		t.P("Package ", t.genPkgName, " is a generated twirp stub package.")
		t.P("This code was generated with github.com/twitchtv/twirp/protoc-gen-twirp ", gen.Version, ".")
		t.P()
		comment, err := t.reg.FileComments(file)
		if err == nil && comment.Leading != "" {
			for _, line := range strings.Split(comment.Leading, "\n") {
				line = strings.TrimPrefix(line, " ")
				// ensure we don't escape from the block comment
				line = strings.Replace(line, "*/", "* /", -1)
				t.P(line)
			}
			t.P()
		}
		t.P("It is generated from these files:")
		for _, f := range t.genFiles {
			t.P("\t", f.GetName())
		}
		t.P("*/")
	}
	t.P(`package `, t.genPkgName)
	t.P()
}

func (t *twirp) generateImports(file *descriptor.FileDescriptorProto) {
	if len(file.Service) == 0 {
		return
	}
	t.P(`import `, t.pkgs["bytes"], ` "bytes"`)
	t.P(`import `, t.pkgs["context"], ` "context"`)
	t.P(`import `, t.pkgs["fmt"], ` "fmt"`)
	t.P(`import `, t.pkgs["ioutil"], ` "io/ioutil"`)
	t.P(`import `, t.pkgs["http"], ` "net/http"`)
	t.P(`import `, t.pkgs["strings"], ` "strings"`)
	t.P()
	t.P(`import `, t.pkgs["jsonpb"], ` "github.com/golang/protobuf/jsonpb"`)
	t.P(`import `, t.pkgs["proto"], ` "github.com/golang/protobuf/proto"`)
	t.P(`import `, t.pkgs["twirp"], ` "github.com/twitchtv/twirp"`)
	t.P(`import `, t.pkgs["ctxsetters"], ` "github.com/twitchtv/twirp/ctxsetters"`)
	t.P()

	// It's legal to import a message and use it as an input or output for a
	// method. Make sure to import the package of any such message. First, dedupe
	// them.
	deps := make(map[string]string) // Map of package name to quoted import path.
	ourImportPath := path.Dir(goFileName(file))
	for _, s := range file.Service {
		for _, m := range s.Method {
			defs := []*typemap.MessageDefinition{
				t.reg.MethodInputDefinition(m),
				t.reg.MethodOutputDefinition(m),
			}
			for _, def := range defs {
				importPath := path.Dir(goFileName(def.File))
				if importPath != ourImportPath {
					pkg := t.goPackageName(def.File)
					deps[pkg] = strconv.Quote(importPath)
				}
			}
		}
	}
	for pkg, importPath := range deps {
		t.P(`import `, pkg, ` `, importPath)
	}
	if len(deps) > 0 {
		t.P()
	}
}

// P forwards to g.gen.P, which prints output.
func (t *twirp) P(args ...string) {
	for _, v := range args {
		t.output.WriteString(v)
	}
	t.output.WriteByte('\n')
}

// Big header comments to makes it easier to visually parse a generated file.
func (t *twirp) sectionComment(sectionTitle string) {
	t.P()
	t.P(`// `, strings.Repeat("=", len(sectionTitle)))
	t.P(`// `, sectionTitle)
	t.P(`// `, strings.Repeat("=", len(sectionTitle)))
	t.P()
}

func (t *twirp) generateService(file *descriptor.FileDescriptorProto, service *descriptor.ServiceDescriptorProto, index int) {
	servName := serviceName(service)

	t.sectionComment(servName + ` Interface`)
	t.generateTwirpInterface(file, service)

	t.sectionComment(servName + ` Protobuf Client`)
	t.generateClient("Protobuf", file, service)

	t.sectionComment(servName + ` JSON Client`)
	t.generateClient("JSON", file, service)

	// Server
	t.sectionComment(servName + ` Server Handler`)
	t.generateServer(file, service)
}

func (t *twirp) generateTwirpInterface(file *descriptor.FileDescriptorProto, service *descriptor.ServiceDescriptorProto) {
	servName := serviceName(service)

	comments, err := t.reg.ServiceComments(file, service)
	if err == nil {
		t.printComments(comments)
	}
	t.P(`type `, servName, ` interface {`)
	for _, method := range service.Method {
		comments, err = t.reg.MethodComments(file, service, method)
		if err == nil {
			t.printComments(comments)
		}
		t.P(t.generateSignature(method))
		t.P()
	}
	t.P(`}`)
}

func (t *twirp) generateSignature(method *descriptor.MethodDescriptorProto) string {
	methName := methodName(method)
	inputType := t.goTypeName(method.GetInputType())
	outputType := t.goTypeName(method.GetOutputType())
	return fmt.Sprintf(`	%s(%s.Context, *%s) (*%s, error)`, methName, t.pkgs["context"], inputType, outputType)
}

func (t *twirp) generateClient(name string, file *descriptor.FileDescriptorProto, service *descriptor.ServiceDescriptorProto) {
	servName := serviceName(service)
	pathPrefixConst := servName + "PathPrefix"
	structName := unexported(servName) + name + "Client"
	newJSONClientFunc := "New" + servName + name + "Client"

	methCnt := strconv.Itoa(len(service.Method))
	t.P(`type `, structName, ` struct {`)
	t.P(`  client `, t.pkgs["twirp"], `.HTTPClient`)
	t.P(`  urls   [`, methCnt, `]string`)
	t.P(`}`)
	t.P()
	t.P(`// `, newJSONClientFunc, ` creates a `, name, ` client that implements the `, servName, ` interface.`)
	t.P(`// It communicates using `, name, ` and can be configured with a custom `, t.pkgs["twirp"], `.HTTPClient.`)
	t.P(`func `, newJSONClientFunc, `(addr string, client `, t.pkgs["twirp"], `.HTTPClient) `, servName, ` {`)
	t.P(`  prefix := `, t.pkgs["twirp"], `.URLBase(addr) + `, pathPrefixConst)
	t.P(`  urls := [`, methCnt, `]string{`)
	for _, method := range service.Method {
		t.P(`    	prefix + "`, methodName(method), `",`)
	}
	t.P(`  }`)
	t.P(`  if httpClient, ok := client.(*`, t.pkgs["http"], `.Client); ok {`)
	t.P(`    return &`, structName, `{`)
	t.P(`      client: `, t.pkgs["twirp"], `.WithoutRedirects(httpClient),`)
	t.P(`      urls:   urls,`)
	t.P(`    }`)
	t.P(`  }`)
	t.P(`  return &`, structName, `{`)
	t.P(`    client: client,`)
	t.P(`    urls:   urls,`)
	t.P(`  }`)
	t.P(`}`)
	t.P()

	for i, method := range service.Method {
		methName := methodName(method)
		inputType := t.goTypeName(method.GetInputType())
		outputType := t.goTypeName(method.GetOutputType())

		t.P(`func (c *`, structName, `) `, methName, `(ctx `, t.pkgs["context"], `.Context, in *`, inputType, `) (*`, outputType, `, error) {`)
		t.P(`  out := new(`, outputType, `)`)
		t.P(`  err := `, t.pkgs["twirp"], `.Do`, name, `Request(ctx, c.client, c.urls[`, strconv.Itoa(i), `], in, out)`)
		t.P(`  return out, err`)
		t.P(`}`)
		t.P()
	}
}

func (t *twirp) generateServer(file *descriptor.FileDescriptorProto, service *descriptor.ServiceDescriptorProto) {
	servName := serviceName(service)

	// Server implementation.
	servStruct := serviceStruct(service)
	t.P(`type `, servStruct, ` struct {`)
	t.P(`  `, servName)
	t.P(`  *`, t.pkgs["twirp"], `.ServerHooks`)
	t.P(`}`)
	t.P()

	// Constructor for server implementation
	t.P(`func New`, servName, `Server(svc `, servName, `, hooks *`, t.pkgs["twirp"], `.ServerHooks) `, t.pkgs["twirp"], `.Server {`)
	t.P(`  return &`, servStruct, `{`)
	t.P(`    `, servName, `: svc,`)
	t.P(`    ServerHooks: hooks,`)
	t.P(`  }`)
	t.P(`}`)
	t.P()

	// Routing.
	t.generateServerRouting(servStruct, file, service)

	// Methods.
	for _, method := range service.Method {
		t.generateServerMethod(service, method)
	}

	t.generateServiceMetadataAccessors(file, service)
}

// pathPrefix returns the base path for all methods handled by a particular
// service. It includes a trailing slash. (for example
// "/twirp/twitch.example.Haberdasher/").
func pathPrefix(file *descriptor.FileDescriptorProto, service *descriptor.ServiceDescriptorProto) string {
	return fmt.Sprintf("/twirp/%s/", fullServiceName(file, service))
}

func (t *twirp) generateServerRouting(servStruct string, file *descriptor.FileDescriptorProto, service *descriptor.ServiceDescriptorProto) {
	pkgName := pkgName(file)
	servName := serviceName(service)

	pathPrefixConst := servName + "PathPrefix"
	t.P(`// `, pathPrefixConst, ` is used for all URL paths on a twirp `, servName, ` server.`)
	t.P(`// Requests are always: POST `, pathPrefixConst, `/method`)
	t.P(`// It can be used in an HTTP mux to route twirp requests along with non-twirp requests on other routes.`)
	t.P(`const `, pathPrefixConst, ` = `, strconv.Quote(pathPrefix(file, service)))
	t.P()

	t.P(`func (s *`, servStruct, `) ServeHTTP(resp `, t.pkgs["http"], `.ResponseWriter, req *`, t.pkgs["http"], `.Request) {`)
	t.P(`  ctx := req.Context()`)
	t.P(`  ctx = `, t.pkgs["ctxsetters"], `.WithPackageName(ctx, "`, pkgName, `")`)
	t.P(`  ctx = `, t.pkgs["ctxsetters"], `.WithServiceName(ctx, "`, servName, `")`)
	t.P(`  ctx = `, t.pkgs["ctxsetters"], `.WithResponseWriter(ctx, resp)`)
	t.P()
	t.P(`  var err error`)
	t.P(`  ctx, err = s.CallRequestReceived(ctx)`)
	t.P(`  if err != nil {`)
	t.P(`    s.WriteError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  if req.Method != "POST" {`)
	t.P(`    msg := `, t.pkgs["fmt"], `.Sprintf("unsupported method %q (only POST is allowed)", req.Method)`)
	t.P(`    err = `, t.pkgs["twirp"], `.BadRouteError(msg, req.Method, req.URL.Path)`)
	t.P(`    s.WriteError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  if strings.HasPrefix(req.URL.Path, `, pathPrefixConst, `) {`)
	t.P(`    switch req.URL.Path[len(`, pathPrefixConst, `):] {`)
	for _, method := range service.Method {
		methName := stringutils.CamelCase(method.GetName())
		t.P(`    case `, strconv.Quote(methName), `:`)
		t.P(`      s.serve`, methName, `(ctx, resp, req)`)
		t.P(`      return`)
	}
	t.P(`    }`)
	t.P(`  }`)
	t.P(`  msg := `, t.pkgs["fmt"], `.Sprintf("no handler for path %q", req.URL.Path)`)
	t.P(`  err = `, t.pkgs["twirp"], `.BadRouteError(msg, req.Method, req.URL.Path)`)
	t.P(`  s.WriteError(ctx, resp, err)`)
	t.P(`  return`)
	t.P(`}`)
	t.P()
}

func (t *twirp) generateServerMethod(service *descriptor.ServiceDescriptorProto, method *descriptor.MethodDescriptorProto) {
	methName := stringutils.CamelCase(method.GetName())
	servStruct := serviceStruct(service)
	t.P(`func (s *`, servStruct, `) serve`, methName, `(ctx `, t.pkgs["context"], `.Context, resp `, t.pkgs["http"], `.ResponseWriter, req *`, t.pkgs["http"], `.Request) {`)
	t.P(`  switch req.Header.Get("Content-Type") {`)
	t.P(`  case "application/json":`)
	t.P(`    s.serve`, methName, `JSON(ctx, resp, req)`)
	t.P(`  case "application/protobuf":`)
	t.P(`    s.serve`, methName, `Protobuf(ctx, resp, req)`)
	t.P(`  default:`)
	t.P(`    msg := `, t.pkgs["fmt"], `.Sprintf("unexpected Content-Type: %q", req.Header.Get("Content-Type"))`)
	t.P(`    twerr := `, t.pkgs["twirp"], `.BadRouteError(msg, req.Method, req.URL.Path)`)
	t.P(`    s.WriteError(ctx, resp, twerr)`)
	t.P(`  }`)
	t.P(`}`)
	t.P()
	t.generateServerJSONMethod(service, method)
	t.generateServerProtobufMethod(service, method)
}

func (t *twirp) generateServerJSONMethod(service *descriptor.ServiceDescriptorProto, method *descriptor.MethodDescriptorProto) {
	servStruct := serviceStruct(service)
	methName := stringutils.CamelCase(method.GetName())
	t.P(`func (s *`, servStruct, `) serve`, methName, `JSON(ctx `, t.pkgs["context"], `.Context, resp `, t.pkgs["http"], `.ResponseWriter, req *`, t.pkgs["http"], `.Request) {`)
	t.P(`  var err error`)
	t.P(`  ctx = `, t.pkgs["ctxsetters"], `.WithMethodName(ctx, "`, methName, `")`)
	t.P(`  ctx, err = s.CallRequestRouted(ctx)`)
	t.P(`  if err != nil {`)
	t.P(`    s.WriteError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  defer func() {`)
	t.P(`    if err := req.Body.Close(); err != nil {`)
	t.P(`      s.Printf("error closing body: %q", err)`)
	t.P(`    }`)
	t.P(`  }()`)
	t.P(`  reqContent := new(`, t.goTypeName(method.GetInputType()), `)`)
	t.P(`  unmarshaler := `, t.pkgs["jsonpb"], `.Unmarshaler{AllowUnknownFields: true}`)
	t.P(`  if err = unmarshaler.Unmarshal(req.Body, reqContent); err != nil {`)
	t.P(`    err = `, t.pkgs["twirp"], `.WrapErr(err, "failed to parse request json")`)
	t.P(`    s.WriteError(ctx, resp, `, t.pkgs["twirp"], `.InternalErrorWith(err))`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  // Call service method`)
	t.P(`  var respContent *`, t.goTypeName(method.GetOutputType()))
	t.P(`  func() {`)
	t.P(`    defer func() {`)
	t.P(`      // In case of a panic, serve a 500 error and then panic.`)
	t.P(`      if r := recover(); r != nil {`)
	t.P(`        s.WriteError(ctx, resp, `, t.pkgs["twirp"], `.InternalError("Internal service panic"))`)
	t.P(`        panic(r)`)
	t.P(`      }`)
	t.P(`    }()`)
	t.P(`    respContent, err = s.`, methName, `(ctx, reqContent)`)
	t.P(`  }()`)
	t.P()
	t.P(`  if err != nil {`)
	t.P(`    s.WriteError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P(`  if respContent == nil {`)
	t.P(`    s.WriteError(ctx, resp, `, t.pkgs["twirp"], `.InternalError("received a nil *`, t.goTypeName(method.GetOutputType()), ` and nil error while calling `, methName, `. nil responses are not supported"))`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  ctx = s.CallResponsePrepared(ctx)`)
	t.P()
	t.P(`  var buf `, t.pkgs["bytes"], `.Buffer`)
	t.P(`  marshaler := &`, t.pkgs["jsonpb"], `.Marshaler{OrigName: true}`)
	t.P(`  if err = marshaler.Marshal(&buf, respContent); err != nil {`)
	t.P(`    err = `, t.pkgs["twirp"], `.WrapErr(err, "failed to marshal json response")`)
	t.P(`    s.WriteError(ctx, resp, `, t.pkgs["twirp"], `.InternalErrorWith(err))`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  ctx = `, t.pkgs["ctxsetters"], `.WithStatusCode(ctx, `, t.pkgs["http"], `.StatusOK)`)
	t.P(`  resp.Header().Set("Content-Type", "application/json")`)
	t.P(`  resp.WriteHeader(`, t.pkgs["http"], `.StatusOK)`)
	t.P(`  if _, err = resp.Write(buf.Bytes()); err != nil {`)
	t.P(`    s.Printf("errored while writing response to client, but already sent response status code to 200: %s", err)`)
	t.P(`  }`)
	t.P(`  s.CallResponseSent(ctx)`)
	t.P(`}`)
	t.P()
}

func (t *twirp) generateServerProtobufMethod(service *descriptor.ServiceDescriptorProto, method *descriptor.MethodDescriptorProto) {
	servStruct := serviceStruct(service)
	methName := stringutils.CamelCase(method.GetName())

	t.P(`func (s *`, servStruct, `) serve`, methName, `Protobuf(ctx `, t.pkgs["context"], `.Context, resp `, t.pkgs["http"], `.ResponseWriter, req *`, t.pkgs["http"], `.Request) {`)
	t.P(`  var err error`)
	t.P(`  ctx = `, t.pkgs["ctxsetters"], `.WithMethodName(ctx, "`, methName, `")`)
	t.P(`  ctx, err = s.CallRequestRouted(ctx)`)
	t.P(`  if err != nil {`)
	t.P(`    s.WriteError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  defer func() {`)
	t.P(`    if err := req.Body.Close(); err != nil {`)
	t.P(`      s.Printf("error closing body: %q", err)`)
	t.P(`    }`)
	t.P(`  }()`)
	t.P(`  buf, err := `, t.pkgs["ioutil"], `.ReadAll(req.Body)`)
	t.P(`  if err != nil {`)
	t.P(`    err = `, t.pkgs["twirp"], `.WrapErr(err, "failed to read request body")`)
	t.P(`    s.WriteError(ctx, resp, `, t.pkgs["twirp"], `.InternalErrorWith(err))`)
	t.P(`    return`)
	t.P(`  }`)
	t.P(`  reqContent := new(`, t.goTypeName(method.GetInputType()), `)`)
	t.P(`  if err = `, t.pkgs["proto"], `.Unmarshal(buf, reqContent); err != nil {`)
	t.P(`    err = `, t.pkgs["twirp"], `.WrapErr(err, "failed to parse request proto")`)
	t.P(`    s.WriteError(ctx, resp, `, t.pkgs["twirp"], `.InternalErrorWith(err))`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  // Call service method`)
	t.P(`  var respContent *`, t.goTypeName(method.GetOutputType()))
	t.P(`  func() {`)
	t.P(`    defer func() {`)
	t.P(`      // In case of a panic, serve a 500 error and then panic.`)
	t.P(`      if r := recover(); r != nil {`)
	t.P(`        s.WriteError(ctx, resp, `, t.pkgs["twirp"], `.InternalError("Internal service panic"))`)
	t.P(`        panic(r)`)
	t.P(`      }`)
	t.P(`    }()`)
	t.P(`    respContent, err = s.`, methName, `(ctx, reqContent)`)
	t.P(`  }()`)
	t.P()
	t.P(`  if err != nil {`)
	t.P(`    s.WriteError(ctx, resp, err)`)
	t.P(`    return`)
	t.P(`  }`)
	t.P(`  if respContent == nil {`)
	t.P(`    s.WriteError(ctx, resp, `, t.pkgs["twirp"], `.InternalError("received a nil *`, t.goTypeName(method.GetOutputType()), ` and nil error while calling `, methName, `. nil responses are not supported"))`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  ctx = s.CallResponsePrepared(ctx)`)
	t.P()
	t.P(`  respBytes, err := `, t.pkgs["proto"], `.Marshal(respContent)`)
	t.P(`  if err != nil {`)
	t.P(`    err = `, t.pkgs["twirp"], `.WrapErr(err, "failed to marshal proto response")`)
	t.P(`    s.WriteError(ctx, resp, `, t.pkgs["twirp"], `.InternalErrorWith(err))`)
	t.P(`    return`)
	t.P(`  }`)
	t.P()
	t.P(`  ctx = `, t.pkgs["ctxsetters"], `.WithStatusCode(ctx, `, t.pkgs["http"], `.StatusOK)`)
	t.P(`  resp.Header().Set("Content-Type", "application/protobuf")`)
	t.P(`  resp.WriteHeader(`, t.pkgs["http"], `.StatusOK)`)
	t.P(`  if _, err = resp.Write(respBytes); err != nil {`)
	t.P(`    s.Printf("errored while writing response to client, but already sent response status code to 200: %s", err)`)
	t.P(`  }`)
	t.P(`  s.CallResponseSent(ctx)`)
	t.P(`}`)
	t.P()
}

// serviceMetadataVarName is the variable name used in generated code to refer
// to the compressed bytes of this descriptor. It is not exported, so it is only
// valid inside the generated package.
//
// protoc-gen-go writes its own version of this file, but so does
// protoc-gen-gogo - with a different name! Twirp aims to be compatible with
// both; the simplest way forward is to write the file descriptor again as
// another variable that we control.
func (t *twirp) serviceMetadataVarName() string {
	return fmt.Sprintf("twirpFileDescriptor%d", t.filesHandled)
}

func (t *twirp) generateServiceMetadataAccessors(file *descriptor.FileDescriptorProto, service *descriptor.ServiceDescriptorProto) {
	servStruct := serviceStruct(service)
	index := 0
	for i, s := range file.Service {
		if s.GetName() == service.GetName() {
			index = i
		}
	}
	t.P(`func (s *`, servStruct, `) ServiceDescriptor() ([]byte, int) {`)
	t.P(`  return `, t.serviceMetadataVarName(), `, `, strconv.Itoa(index))
	t.P(`}`)
	t.P()
	t.P(`func (s *`, servStruct, `) ProtocGenTwirpVersion() (string) {`)
	t.P(`  return `, strconv.Quote(gen.Version))
	t.P(`}`)
}

func (t *twirp) generateFileDescriptor(file *descriptor.FileDescriptorProto) {
	// Copied straight of of protoc-gen-go, which trims out comments.
	pb := proto.Clone(file).(*descriptor.FileDescriptorProto)
	pb.SourceCodeInfo = nil

	b, err := proto.Marshal(pb)
	if err != nil {
		gen.Fail(err.Error())
	}

	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	w.Write(b)
	w.Close()
	b = buf.Bytes()

	v := t.serviceMetadataVarName()
	t.P()
	t.P("var ", v, " = []byte{")
	t.P("	// ", fmt.Sprintf("%d", len(b)), " bytes of a gzipped FileDescriptorProto")
	for len(b) > 0 {
		n := 16
		if n > len(b) {
			n = len(b)
		}

		s := ""
		for _, c := range b[:n] {
			s += fmt.Sprintf("0x%02x,", c)
		}
		t.P(`	`, s)

		b = b[n:]
	}
	t.P("}")
}

func (t *twirp) printComments(comments typemap.DefinitionComments) bool {
	text := strings.TrimSuffix(comments.Leading, "\n")
	if len(strings.TrimSpace(text)) == 0 {
		return false
	}
	split := strings.Split(text, "\n")
	for _, line := range split {
		t.P("// ", strings.TrimPrefix(line, " "))
	}
	return len(split) > 0
}

// Given a protobuf name for a Message, return the Go name we will use for that
// type, including its package prefix.
func (t *twirp) goTypeName(protoName string) string {
	def := t.reg.MessageDefinition(protoName)
	if def == nil {
		gen.Fail("could not find message for", protoName)
	}

	var prefix string
	if pkg := t.goPackageName(def.File); pkg != t.genPkgName {
		prefix = pkg + "."
	}

	var name string
	for _, parent := range def.Lineage() {
		name += parent.Descriptor.GetName() + "_"
	}
	name += def.Descriptor.GetName()
	return prefix + name
}

func (t *twirp) goPackageName(file *descriptor.FileDescriptorProto) string {
	return t.fileToGoPackageName[file]
}

func (t *twirp) formattedOutput() string {
	// Reformat generated code.
	fset := token.NewFileSet()
	raw := t.output.Bytes()
	ast, err := parser.ParseFile(fset, "", raw, parser.ParseComments)
	if err != nil {
		// Print out the bad code with line numbers.
		// This should never happen in practice, but it can while changing generated code,
		// so consider this a debugging aid.
		var src bytes.Buffer
		s := bufio.NewScanner(bytes.NewReader(raw))
		for line := 1; s.Scan(); line++ {
			fmt.Fprintf(&src, "%5d\t%s\n", line, s.Bytes())
		}
		gen.Fail("bad Go source code was generated:", err.Error(), "\n"+src.String())
	}

	out := bytes.NewBuffer(nil)
	err = (&printer.Config{Mode: printer.TabIndent | printer.UseSpaces, Tabwidth: 8}).Fprint(out, fset, ast)
	if err != nil {
		gen.Fail("generated Go source code could not be reformatted:", err.Error())
	}

	return out.String()
}

func unexported(s string) string { return strings.ToLower(s[:1]) + s[1:] }

func fullServiceName(file *descriptor.FileDescriptorProto, service *descriptor.ServiceDescriptorProto) string {
	name := stringutils.CamelCase(service.GetName())
	if pkg := pkgName(file); pkg != "" {
		name = pkg + "." + name
	}
	return name
}

func pkgName(file *descriptor.FileDescriptorProto) string {
	return file.GetPackage()
}

func serviceName(service *descriptor.ServiceDescriptorProto) string {
	return stringutils.CamelCase(service.GetName())
}

func serviceStruct(service *descriptor.ServiceDescriptorProto) string {
	return unexported(serviceName(service)) + "Server"
}

func methodName(method *descriptor.MethodDescriptorProto) string {
	return stringutils.CamelCase(method.GetName())
}

func fileDescSliceContains(slice []*descriptor.FileDescriptorProto, f *descriptor.FileDescriptorProto) bool {
	for _, sf := range slice {
		if f == sf {
			return true
		}
	}
	return false
}
