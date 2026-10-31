package server_test

import (
	"fmt"
	"net/http"
	"testing"

	"arkoren.dev/core/http/server"
)

// ExampleBuilderFunc configures a server without binding a listener, showing
// that Addr reports configuration until Start acquires the native address.
func ExampleBuilderFunc() {
	configure := server.BuilderFunc(func(listener *server.Builder) {
		listener.Addr("127.0.0.1:8080")
	})

	srv := server.New(http.NotFoundHandler(), configure)
	fmt.Println(srv.Addr())
	// Output: 127.0.0.1:8080
}

// TestNewDisablesNegativeWriteTimeout checks that a negative construction
// timeout disables the native HTTP server write deadline.
func TestNewDisablesNegativeWriteTimeout(t *testing.T) {
	t.Parallel()

	httpServer := server.New(http.NotFoundHandler(), func(serverBuilder *server.Builder) {
		serverBuilder.WriteTimeout(-1)
	}).HTTP()

	if httpServer.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want disabled", httpServer.WriteTimeout)
	}
}

// TestNewUsesHandlerAndAddr checks that construction publishes the supplied
// handler and configured address to the native HTTP server.
func TestNewUsesHandlerAndAddr(t *testing.T) {
	t.Parallel()

	handler := http.NotFoundHandler()
	httpServer := server.New(handler, func(serverBuilder *server.Builder) {
		serverBuilder.Addr("127.0.0.1:9000")
	}).HTTP()

	if httpServer.Addr != "127.0.0.1:9000" || httpServer.Handler == nil {
		t.Fatalf("server = %#v", httpServer)
	}
}
