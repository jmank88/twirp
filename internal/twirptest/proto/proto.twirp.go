// Code generated by protoc-gen-twirp v5.1.0, DO NOT EDIT.
// source: proto.proto

/*
Package proto is a generated twirp stub package.
This code was generated with github.com/twitchtv/twirp/protoc-gen-twirp v5.1.0.

Test to make sure that a package named proto doesn't break


It is generated from these files:
	proto.proto
*/
package proto

import bytes "bytes"
import context "context"
import fmt "fmt"
import ioutil "io/ioutil"
import http "net/http"
import strings "strings"

import jsonpb "github.com/golang/protobuf/jsonpb"
import proto "github.com/golang/protobuf/proto"
import twirp "github.com/twitchtv/twirp"
import ctxsetters "github.com/twitchtv/twirp/ctxsetters"

// =============
// Svc Interface
// =============

type Svc interface {
	Send(context.Context, *Msg) (*Msg, error)
}

// ===================
// Svc Protobuf Client
// ===================

type svcProtobufClient struct {
	client twirp.HTTPClient
	urls   [1]string
}

// NewSvcProtobufClient creates a Protobuf client that implements the Svc interface.
// It communicates using Protobuf and can be configured with a custom twirp.HTTPClient.
func NewSvcProtobufClient(addr string, client twirp.HTTPClient) Svc {
	prefix := twirp.URLBase(addr) + SvcPathPrefix
	urls := [1]string{
		prefix + "Send",
	}
	if httpClient, ok := client.(*http.Client); ok {
		return &svcProtobufClient{
			client: twirp.WithoutRedirects(httpClient),
			urls:   urls,
		}
	}
	return &svcProtobufClient{
		client: client,
		urls:   urls,
	}
}

func (c *svcProtobufClient) Send(ctx context.Context, in *Msg) (*Msg, error) {
	out := new(Msg)
	err := twirp.DoProtobufRequest(ctx, c.client, c.urls[0], in, out)
	return out, err
}

// ===============
// Svc JSON Client
// ===============

type svcJSONClient struct {
	client twirp.HTTPClient
	urls   [1]string
}

// NewSvcJSONClient creates a JSON client that implements the Svc interface.
// It communicates using JSON and can be configured with a custom twirp.HTTPClient.
func NewSvcJSONClient(addr string, client twirp.HTTPClient) Svc {
	prefix := twirp.URLBase(addr) + SvcPathPrefix
	urls := [1]string{
		prefix + "Send",
	}
	if httpClient, ok := client.(*http.Client); ok {
		return &svcJSONClient{
			client: twirp.WithoutRedirects(httpClient),
			urls:   urls,
		}
	}
	return &svcJSONClient{
		client: client,
		urls:   urls,
	}
}

func (c *svcJSONClient) Send(ctx context.Context, in *Msg) (*Msg, error) {
	out := new(Msg)
	err := twirp.DoJSONRequest(ctx, c.client, c.urls[0], in, out)
	return out, err
}

// ==================
// Svc Server Handler
// ==================

type svcServer struct {
	Svc
	*twirp.ServerHooks
}

func NewSvcServer(svc Svc, hooks *twirp.ServerHooks) twirp.Server {
	return &svcServer{
		Svc:         svc,
		ServerHooks: hooks,
	}
}

// SvcPathPrefix is used for all URL paths on a twirp Svc server.
// Requests are always: POST SvcPathPrefix/method
// It can be used in an HTTP mux to route twirp requests along with non-twirp requests on other routes.
const SvcPathPrefix = "/twirp/twirp.internal.twirptest.proto.Svc/"

func (s *svcServer) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	ctx = ctxsetters.WithPackageName(ctx, "twirp.internal.twirptest.proto")
	ctx = ctxsetters.WithServiceName(ctx, "Svc")
	ctx = ctxsetters.WithResponseWriter(ctx, resp)

	var err error
	ctx, err = s.CallRequestReceived(ctx)
	if err != nil {
		s.WriteError(ctx, resp, err)
		return
	}

	if req.Method != "POST" {
		msg := fmt.Sprintf("unsupported method %q (only POST is allowed)", req.Method)
		err = twirp.BadRouteError(msg, req.Method, req.URL.Path)
		s.WriteError(ctx, resp, err)
		return
	}

	if strings.HasPrefix(req.URL.Path, SvcPathPrefix) {
		switch req.URL.Path[len(SvcPathPrefix):] {
		case "Send":
			s.serveSend(ctx, resp, req)
			return
		}
	}
	msg := fmt.Sprintf("no handler for path %q", req.URL.Path)
	err = twirp.BadRouteError(msg, req.Method, req.URL.Path)
	s.WriteError(ctx, resp, err)
	return
}

func (s *svcServer) serveSend(ctx context.Context, resp http.ResponseWriter, req *http.Request) {
	switch req.Header.Get("Content-Type") {
	case "application/json":
		s.serveSendJSON(ctx, resp, req)
	case "application/protobuf":
		s.serveSendProtobuf(ctx, resp, req)
	default:
		msg := fmt.Sprintf("unexpected Content-Type: %q", req.Header.Get("Content-Type"))
		twerr := twirp.BadRouteError(msg, req.Method, req.URL.Path)
		s.WriteError(ctx, resp, twerr)
	}
}

func (s *svcServer) serveSendJSON(ctx context.Context, resp http.ResponseWriter, req *http.Request) {
	var err error
	ctx = ctxsetters.WithMethodName(ctx, "Send")
	ctx, err = s.CallRequestRouted(ctx)
	if err != nil {
		s.WriteError(ctx, resp, err)
		return
	}

	defer func() {
		if err := req.Body.Close(); err != nil {
			s.Printf("error closing body: %q", err)
		}
	}()
	reqContent := new(Msg)
	unmarshaler := jsonpb.Unmarshaler{AllowUnknownFields: true}
	if err = unmarshaler.Unmarshal(req.Body, reqContent); err != nil {
		err = twirp.WrapErr(err, "failed to parse request json")
		s.WriteError(ctx, resp, twirp.InternalErrorWith(err))
		return
	}

	// Call service method
	var respContent *Msg
	func() {
		defer func() {
			// In case of a panic, serve a 500 error and then panic.
			if r := recover(); r != nil {
				s.WriteError(ctx, resp, twirp.InternalError("Internal service panic"))
				panic(r)
			}
		}()
		respContent, err = s.Send(ctx, reqContent)
	}()

	if err != nil {
		s.WriteError(ctx, resp, err)
		return
	}
	if respContent == nil {
		s.WriteError(ctx, resp, twirp.InternalError("received a nil *Msg and nil error while calling Send. nil responses are not supported"))
		return
	}

	ctx = s.CallResponsePrepared(ctx)

	var buf bytes.Buffer
	marshaler := &jsonpb.Marshaler{OrigName: true}
	if err = marshaler.Marshal(&buf, respContent); err != nil {
		err = twirp.WrapErr(err, "failed to marshal json response")
		s.WriteError(ctx, resp, twirp.InternalErrorWith(err))
		return
	}

	ctx = ctxsetters.WithStatusCode(ctx, http.StatusOK)
	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(http.StatusOK)
	if _, err = resp.Write(buf.Bytes()); err != nil {
		s.Printf("errored while writing response to client, but already sent response status code to 200: %s", err)
	}
	s.CallResponseSent(ctx)
}

func (s *svcServer) serveSendProtobuf(ctx context.Context, resp http.ResponseWriter, req *http.Request) {
	var err error
	ctx = ctxsetters.WithMethodName(ctx, "Send")
	ctx, err = s.CallRequestRouted(ctx)
	if err != nil {
		s.WriteError(ctx, resp, err)
		return
	}

	defer func() {
		if err := req.Body.Close(); err != nil {
			s.Printf("error closing body: %q", err)
		}
	}()
	buf, err := ioutil.ReadAll(req.Body)
	if err != nil {
		err = twirp.WrapErr(err, "failed to read request body")
		s.WriteError(ctx, resp, twirp.InternalErrorWith(err))
		return
	}
	reqContent := new(Msg)
	if err = proto.Unmarshal(buf, reqContent); err != nil {
		err = twirp.WrapErr(err, "failed to parse request proto")
		s.WriteError(ctx, resp, twirp.InternalErrorWith(err))
		return
	}

	// Call service method
	var respContent *Msg
	func() {
		defer func() {
			// In case of a panic, serve a 500 error and then panic.
			if r := recover(); r != nil {
				s.WriteError(ctx, resp, twirp.InternalError("Internal service panic"))
				panic(r)
			}
		}()
		respContent, err = s.Send(ctx, reqContent)
	}()

	if err != nil {
		s.WriteError(ctx, resp, err)
		return
	}
	if respContent == nil {
		s.WriteError(ctx, resp, twirp.InternalError("received a nil *Msg and nil error while calling Send. nil responses are not supported"))
		return
	}

	ctx = s.CallResponsePrepared(ctx)

	respBytes, err := proto.Marshal(respContent)
	if err != nil {
		err = twirp.WrapErr(err, "failed to marshal proto response")
		s.WriteError(ctx, resp, twirp.InternalErrorWith(err))
		return
	}

	ctx = ctxsetters.WithStatusCode(ctx, http.StatusOK)
	resp.Header().Set("Content-Type", "application/protobuf")
	resp.WriteHeader(http.StatusOK)
	if _, err = resp.Write(respBytes); err != nil {
		s.Printf("errored while writing response to client, but already sent response status code to 200: %s", err)
	}
	s.CallResponseSent(ctx)
}

func (s *svcServer) ServiceDescriptor() ([]byte, int) {
	return twirpFileDescriptor0, 0
}

func (s *svcServer) ProtocGenTwirpVersion() string {
	return "v5.1.0"
}

var twirpFileDescriptor0 = []byte{
	// 103 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0xe2, 0xe2, 0x2e, 0x28, 0xca, 0x2f,
	0xc9, 0xd7, 0x03, 0x93, 0x42, 0x72, 0x25, 0xe5, 0x99, 0x45, 0x05, 0x7a, 0x99, 0x79, 0x25, 0xa9,
	0x45, 0x79, 0x89, 0x39, 0x7a, 0x60, 0x6e, 0x49, 0x6a, 0x71, 0x09, 0x44, 0x5e, 0x89, 0x95, 0x8b,
	0xd9, 0xb7, 0x38, 0xdd, 0x28, 0x9c, 0x8b, 0x39, 0xb8, 0x2c, 0x59, 0x28, 0x80, 0x8b, 0x25, 0x38,
	0x35, 0x2f, 0x45, 0x48, 0x59, 0x0f, 0xbf, 0x36, 0x3d, 0xdf, 0xe2, 0x74, 0x29, 0x62, 0x14, 0x39,
	0xb1, 0x47, 0xb1, 0x82, 0x39, 0x49, 0x6c, 0x60, 0xca, 0x18, 0x10, 0x00, 0x00, 0xff, 0xff, 0xd1,
	0x75, 0x6b, 0x74, 0x9e, 0x00, 0x00, 0x00,
}
