package server_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/server"
)

// TestServerStartPublishesListening checks that startup publishes the actual
// bound address rather than the requested ephemeral-port address.
func TestServerStartPublishesListening(t *testing.T) {
	bus := event.New()
	var got server.ListeningEvent

	if _, err := bus.Listen(server.Listening, func(_ context.Context, payload server.ListeningEvent) error {
		got = payload

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	srv := server.New(helloHandler, func(serverBuilder *server.Builder) {
		serverBuilder.Addr("127.0.0.1:0")
	}, func(serverBuilder *server.Builder) {
		serverBuilder.Events(bus)
	})

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = srv.Stop(context.Background())
	}) // Releasing the listener cannot fail the test.

	if got.Addr == "" || got.Addr != srv.Addr() || got.Addr == "127.0.0.1:0" {
		t.Fatalf("listening addr = %q, want the bound address %q", got.Addr, srv.Addr())
	}
}

// stuckHandler returns a handler that signals started and then blocks until
// release is closed.
func stuckHandler(started chan<- struct{}, release <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		started <- struct{}{}
		<-release
	})
}

// TestServeShutdownTimeoutPublishesTimedOut checks that an undrained request
// produces a timeout event containing the listening address and shutdown
// budget.
func TestServeShutdownTimeoutPublishesTimedOut(t *testing.T) {
	bus := event.New()
	got := make(chan server.ShutdownTimedOutEvent, 1)

	if _, err := bus.Listen(server.ShutdownTimedOut, func(_ context.Context, payload server.ShutdownTimedOutEvent) error {
		got <- payload

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	started, release := make(chan struct{}, 1), make(chan struct{})
	t.Cleanup(func() {
		close(release)
	})

	srv, cancel, done := startServer(t, stuckHandler(started, release),
		func(serverBuilder *server.Builder) {
			serverBuilder.Events(bus)
		}, func(serverBuilder *server.Builder) {
			serverBuilder.ShutdownTimeout(50 * time.Millisecond)
		})

	go func() {
		if response, err := http.Get("http://" + srv.Addr()); err == nil {
			_ = response.Body.Close() // The forced close makes the outcome irrelevant.
		}
	}()

	receive(t, started, "request start")

	cancel()
	receive(t, done, "Serve to return")

	payload := receive(t, got, "ShutdownTimedOut")

	if payload.Addr != srv.Addr() || payload.Timeout != 50*time.Millisecond {
		t.Fatalf("ShutdownTimedOut = %+v, want %s and 50ms", payload, srv.Addr())
	}
}

// TestServeCleanShutdownPublishesNothing checks that a completed graceful
// shutdown does not emit a shutdown-timeout event.
func TestServeCleanShutdownPublishesNothing(t *testing.T) {
	bus := event.New()
	published := make(chan struct{}, 1)

	if _, err := bus.Listen(server.ShutdownTimedOut, func(context.Context, server.ShutdownTimedOutEvent) error {
		published <- struct{}{}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, cancel, done := startServer(t, helloHandler, func(serverBuilder *server.Builder) {
		serverBuilder.Events(bus)
	})
	cancel()
	receive(t, done, "Serve to return")

	if len(published) != 0 {
		t.Fatal("ShutdownTimedOut published for a clean shutdown")
	}
}

// TestServeShutdownTimeoutWithNilBusPublishesNothing checks that shutdown
// without an event bus still returns the drain timeout for a blocked request.
func TestServeShutdownTimeoutWithNilBusPublishesNothing(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	t.Cleanup(func() {
		close(release)
	})

	srv, cancel, done := startServer(t, stuckHandler(started, release), func(serverBuilder *server.Builder) {
		serverBuilder.ShutdownTimeout(50 * time.Millisecond)
	})

	go func() {
		if response, err := http.Get("http://" + srv.Addr()); err == nil {
			_ = response.Body.Close() // The forced close makes the outcome irrelevant.
		}
	}()

	receive(t, started, "request start")

	cancel()

	if err := receive(t, done, "Serve to return"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Serve = %v, want the timeout", err)
	}
}
