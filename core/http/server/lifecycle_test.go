package server_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"arkoren.dev/core/application"
	"arkoren.dev/core/http/server"
)

// helloHandler answers every request with "hi".
var helloHandler = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
	_, _ = io.WriteString(writer, "hi")
})

// TestServerServesAfterStart checks the startup and serving sequence against a
// real local listener and verifies the handler response.
func TestServerServesAfterStart(t *testing.T) {
	srv := server.New(helloHandler, func(serverBuilder *server.Builder) {
		serverBuilder.Addr("127.0.0.1:0")
	})

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- srv.Serve(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	response, err := http.Get("http://" + srv.Addr() + "/")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = response.Body.Close()
	})

	body, err := io.ReadAll(response.Body)

	if err != nil {
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusOK || string(body) != "hi" {
		t.Fatalf("response = %d %q, want 200 hi", response.StatusCode, body)
	}
}

// TestServerStartFailsWhenAddressInUse checks that startup reports a binding
// failure when another listener already owns the requested address.
func TestServerStartFailsWhenAddressInUse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = listener.Close()
	})

	srv := server.New(helloHandler, func(serverBuilder *server.Builder) {
		serverBuilder.Addr(listener.Addr().String())
	})

	if err := srv.Start(context.Background()); err == nil {
		t.Fatal("start succeeded on a bound address")
	}
}

// TestServerStartTwice checks that starting an already started server reports
// an error without replacing its listener.
func TestServerStartTwice(t *testing.T) {
	srv := server.New(helloHandler, func(serverBuilder *server.Builder) {
		serverBuilder.Addr("127.0.0.1:0")
	})

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = srv.Stop(context.Background())
	})

	if err := srv.Start(context.Background()); err == nil {
		t.Fatal("second start succeeded")
	}
}

// TestServerServeWithoutStart checks that serving before listener creation
// reports the lifecycle misuse instead of starting implicitly.
func TestServerServeWithoutStart(t *testing.T) {
	srv := server.New(helloHandler, func(serverBuilder *server.Builder) {
		serverBuilder.Addr("127.0.0.1:0")
	})

	if err := srv.Serve(context.Background()); err == nil {
		t.Fatal("serve succeeded without start")
	}
}

// TestServerServeReturnsNilOnCancel checks that normal serving cancellation
// drains cleanly and remains compatible with a later Stop call.
func TestServerServeReturnsNilOnCancel(t *testing.T) {
	srv := server.New(helloHandler, func(serverBuilder *server.Builder) {
		serverBuilder.Addr("127.0.0.1:0")
	})

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- srv.Serve(ctx)
	}()

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("serve = %v, want nil", err)
	}

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("stop after serve = %v", err)
	}
}

// TestServerStopClosesListenerWhenNeverServed checks that stopping after Start
// releases the bound port even when Serve was never called.
func TestServerStopClosesListenerWhenNeverServed(t *testing.T) {
	srv := server.New(helloHandler, func(serverBuilder *server.Builder) {
		serverBuilder.Addr("127.0.0.1:0")
	})

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	addr := srv.Addr()

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if connection, err := net.Dial("tcp", addr); err == nil {
		_ = connection.Close()
		t.Fatal("listener still accepting after stop")
	}
}

// TestServerDrainsInFlightRequests checks that cancellation allows an already
// running handler to complete its successful response before Serve returns.
func TestServerDrainsInFlightRequests(t *testing.T) {
	entered := make(chan struct{})
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(entered)
		time.Sleep(50 * time.Millisecond)

		_, _ = io.WriteString(writer, "done")
	})

	srv := server.New(handler, func(serverBuilder *server.Builder) {
		serverBuilder.Addr("127.0.0.1:0")
	})

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- srv.Serve(ctx)
	}()

	results := make(chan int, 1)

	go func() {
		response, err := http.Get("http://" + srv.Addr() + "/")

		if err != nil {
			results <- 0

			return
		}

		_ = response.Body.Close()
		results <- response.StatusCode
	}()

	<-entered
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("serve = %v, want nil", err)
	}

	if status := <-results; status != http.StatusOK {
		t.Fatalf("in-flight status = %d, want 200", status)
	}
}

// TestServerRunsUnderApplication checks that the server composes with
// application startup, background serving, and shutdown hooks.
func TestServerRunsUnderApplication(t *testing.T) {
	srv := server.New(helloHandler, func(serverBuilder *server.Builder) {
		serverBuilder.Addr("127.0.0.1:0")
	})

	app := application.New()
	compose := func(scope *application.Composer) {
		scope.Startup(srv.Start)
		scope.Run(srv.Serve)
		scope.Cleanup(srv.Stop)
	}

	if err := app.Start(context.Background(), compose); err != nil {
		t.Fatal(err)
	}

	response, err := http.Get("http://" + srv.Addr() + "/")

	if err != nil {
		t.Fatal(err)
	}

	_ = response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
