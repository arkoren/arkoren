package protocoltest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

// GRPC serves isolated native RPCs using the pinned SDK's registered
// protobuf descriptors. Handler receives the full method and protobuf JSON;
// returning a gRPC status tests real SDK error conversion, not private adapters.
// A server-streaming call sends one initial message and remains open until
// cancellation. Cleanup cancels active RPCs and joins the serving goroutine.
//
//	address := protocoltest.GRPC(t, func(ctx context.Context, method, input string) (string, error) {
//		return "", status.Error(codes.NotFound, "missing run")
//	})
//	host := protocoltest.Host(t, http.NotFoundHandler(), func(host *hatchet.Builder) {
//		host.HostPort(address)
//	})
func GRPC(t *testing.T, handler func(context.Context, string, string) (string, error)) string {
	t.Helper()

	config := net.ListenConfig{}
	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		return serveRPC(stream, handler)
	}))
	done := make(chan error, 1)

	go func() {
		done <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()

		if err := <-done; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("serve native protocol: %v", err)
		}
	})

	return listener.Addr().String()
}

// serveRPC preserves native stream shape. Client streams exchange until EOF;
// server streams remain open until cancellation. Empty replies send no message.
func serveRPC(stream grpc.ServerStream, handler func(context.Context, string, string) (string, error)) error {
	method, _ := grpc.MethodFromServerStream(stream)
	definition, err := rpcMethod(method)

	if err != nil {
		return err
	}

	for {
		err := exchangeRPC(stream, definition, handler)

		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		if !definition.IsStreamingClient() {
			break
		}
	}

	if definition.IsStreamingServer() {
		<-stream.Context().Done()
	}

	return nil
}

// exchangeRPC converts one native protobuf request and optional response using
// the SDK's descriptors. Transport and encoding failures remain real RPC errors.
func exchangeRPC(stream grpc.ServerStream, definition protoreflect.MethodDescriptor,
	handler func(context.Context, string, string) (string, error),
) error {
	request := dynamicpb.NewMessage(definition.Input())

	if err := stream.RecvMsg(request); err != nil {
		return err
	}

	input, err := protojson.Marshal(request)

	if err != nil {
		return err
	}

	method, _ := grpc.MethodFromServerStream(stream)

	body, err := handler(stream.Context(), method, string(input))

	if err != nil {
		return err
	}

	if body == "" {
		return nil
	}

	response := dynamicpb.NewMessage(definition.Output())

	if err := protojson.Unmarshal([]byte(body), response); err != nil {
		return err
	}

	return stream.SendMsg(response)
}

// rpcMethod resolves native service schemas already registered by the SDK.
// Reflection avoids importing vendor-internal generated Go packages or copying
// their wire schema into the test fixture.
func rpcMethod(method string) (protoreflect.MethodDescriptor, error) {
	service, operation, _ := strings.Cut(strings.TrimPrefix(method, "/"), "/")
	descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))

	if err != nil {
		return nil, fmt.Errorf("resolve native service %s: %w", service, err)
	}

	definition, ok := descriptor.(protoreflect.ServiceDescriptor)

	if !ok {
		return nil, fmt.Errorf("native descriptor %s is not a service", service)
	}

	operationDescriptor := definition.Methods().ByName(protoreflect.Name(operation))

	if operationDescriptor == nil {
		return nil, fmt.Errorf("native method %s is not registered", method)
	}

	return operationDescriptor, nil
}
