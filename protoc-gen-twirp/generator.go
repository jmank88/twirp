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
	"text/template"

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
	t.registerPackageName("io")
	t.registerPackageName("ioutil")
	t.registerPackageName("json")
	t.registerPackageName("jsonpb")
	t.registerPackageName("log")
	t.registerPackageName("proto")
	t.registerPackageName("strconv")
	t.registerPackageName("twirp")
	t.registerPackageName("url")
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
	fd, fdl := t.generateFileDescriptor(file)
	data := tmplData{
		Version:            gen.Version,
		SourceName:         file.GetName(),
		FirstFile:          t.filesHandled == 0,
		FileComment:        t.fileComment(file),
		Deps:               t.deps(file),
		GenPackageName:     t.genPkgName,
		PackageName:        file.GetPackage(),
		ProtoFiles:         t.genFiles,
		FileDescriptorName: t.serviceMetadataVarName(),
		FileDescriptorLen:  strconv.Itoa(fdl),
		FileDescriptor:     fd,
		ServiceTypeNames:   []string{"Protobuf", "JSON"},
	}

	// For each service, generate client stubs and server
	for _, service := range file.Service {
		var m []Method
		for _, method := range service.Method {
			var comment string
			if c, err := t.reg.MethodComments(file, service, method); err == nil {
				comment = t.formatComments(c)
			}
			m = append(m, Method{
				Name:    stringutils.CamelCase(method.GetName()),
				Req:     t.goTypeName(method.GetInputType()),
				Resp:    t.goTypeName(method.GetOutputType()),
				Comment: comment,
			})
		}
		var comment string
		if c, err := t.reg.ServiceComments(file, service); err == nil {
			comment = t.formatComments(c)
		}
		name := stringutils.CamelCase(service.GetName())
		data.Services = append(data.Services, Service{
			Name:      name,
			NameLower: unexported(name),
			Comment:   comment,
			Methods:   m,
			FullName:  pathPrefix(file, service),
		})
	}

	resp.Name = proto.String(goFileName(file))
	resp.Content = proto.String(t.execute(&data))
	t.output.Reset()

	t.filesHandled++
	return resp
}

func (t *twirp) fileComment(file *descriptor.FileDescriptorProto) string {
	comment, err := t.reg.FileComments(file)
	var buf bytes.Buffer
	if err == nil && comment.Leading != "" {
		for _, line := range strings.Split(comment.Leading, "\n") {
			line = strings.TrimPrefix(line, " ")
			// ensure we don't escape from the block comment
			line = strings.Replace(line, "*/", "* /", -1)
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}
	return buf.String()
}

func (t *twirp) deps(file *descriptor.FileDescriptorProto) map[string]string {
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
	return deps
}

// pathPrefix returns the base path for all methods handled by a particular
// service. It includes a trailing slash. (for example
// "/twirp/twitch.example.Haberdasher/").
func pathPrefix(file *descriptor.FileDescriptorProto, service *descriptor.ServiceDescriptorProto) string {
	return fmt.Sprintf("/twirp/%s/", fullServiceName(file, service))
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

func (t *twirp) generateFileDescriptor(file *descriptor.FileDescriptorProto) (string, int) {
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
	l := len(b)

	var buf2 bytes.Buffer
	for len(b) > 0 {
		n := 16
		if n > len(b) {
			n = len(b)
		}

		buf2.WriteString("\t")
		for _, c := range b[:n] {
			fmt.Fprintf(&buf2, "0x%02x,", c)
		}
		buf2.WriteString("\n")

		b = b[n:]
	}
	return buf2.String(), l
}

func (t *twirp) formatComments(comments typemap.DefinitionComments) string {
	text := strings.TrimSuffix(comments.Leading, "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	split := strings.Split(text, "\n")
	var b bytes.Buffer
	for _, line := range split {
		b.WriteString("// ")
		b.WriteString(strings.TrimPrefix(line, " "))
		b.WriteByte('\n')
	}
	return b.String()
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

func (t *twirp) execute(data *tmplData) string {
	// Generate code from the template.
	tmpl := template.Must(template.New("").Funcs(
		map[string]interface{}{
			"sectionComment": sectionComment,
			"pkgs": func(p string) string {
				return t.pkgs[p]
			},
		},
	).Parse(fileTmplStr))
	if err := tmpl.Execute(t.output, data); err != nil {
		gen.Fail("failed to execute template:", err.Error())
	}

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
	if pkg := file.GetPackage(); pkg != "" {
		name = pkg + "." + name
	}
	return name
}

func fileDescSliceContains(slice []*descriptor.FileDescriptorProto, f *descriptor.FileDescriptorProto) bool {
	for _, sf := range slice {
		if f == sf {
			return true
		}
	}
	return false
}

// Big header comments to makes it easier to visually parse a generated file.
func sectionComment(sectionTitle string) string {
	return `// ` + strings.Repeat("=", len(sectionTitle)) + "\n" +
		`// ` + sectionTitle + "\n" +
		`// ` + strings.Repeat("=", len(sectionTitle))
}
