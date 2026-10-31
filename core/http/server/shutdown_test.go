package server_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/server"
)

// ExampleServer_Stop shows that cleanup is safe even when startup never acquired
// a listener. No network operation is needed for this ownership path.
func ExampleServer_Stop() {
	srv := server.New(http.NotFoundHandler())

	if err := srv.Stop(context.Background()); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println("stopped")
	// Output: stopped
}

// TestStopLeavesHijackedConnectionToCaller verifies that stopping HTTP serving
// neither waits for nor closes a connection whose native ownership was handed
// to the application. The owner can still exchange bytes after Stop returns.
func TestStopLeavesHijackedConnectionToCaller(t *testing.T) {
	t.Parallel()

	acquired := make(chan net.Conn, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		connection, _, err := http.NewResponseController(writer).Hijack()

		if err != nil {
			t.Error(err)
		}

		acquired <- connection
	})

	srv, _, _ := startServer(t, handler)

	connection, err := net.DialTimeout("tcp", srv.Addr(), shutdownWait)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		// The test owns the client endpoint, including failure paths.
		_ = connection.Close()
	})

	if err := connection.SetDeadline(time.Now().Add(shutdownWait)); err != nil {
		t.Fatal(err)
	}

	if _, err := io.WriteString(connection, "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
		t.Fatal(err)
	}

	owned := receive(t, acquired, "connection ownership")

	if owned == nil {
		t.Fatal("native hijack did not hand over a connection")
	}

	t.Cleanup(func() {
		// Only the hijacked connection's owner closes this server endpoint.
		_ = owned.Close()
	})

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := io.WriteString(owned, "still-owned"); err != nil {
		t.Fatalf("owner write after Stop: %v", err)
	}

	buffer := make([]byte, len("still-owned"))

	if _, err := io.ReadFull(connection, buffer); err != nil {
		t.Fatalf("read owned connection after Stop: %v", err)
	}

	if string(buffer) != "still-owned" {
		t.Fatalf("owned connection returned %q", buffer)
	}
}

// shutdownWait bounds how long a test waits for an asynchronous event.
const shutdownWait = 5 * time.Second

// startServer starts and serves handler on a free port. Cancel stops serving
// and the returned channel receives the Serve result.
func startServer(
	t *testing.T,
	handler http.Handler,
	opts ...func(*server.Builder),
) (*server.Server, context.CancelFunc, <-chan error) {
	t.Helper()

	srv := server.New(handler, append([]func(*server.Builder){func(serverBuilder *server.Builder) {
		serverBuilder.Addr("127.0.0.1:0")
	}}, opts...)...)

	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})

	go func() {
		done <- srv.Serve(ctx)
		close(finished)
	}()

	t.Cleanup(func() {
		cancel()
		<-finished
	})

	return srv, cancel, done
}

// receive fails the test when ch does not deliver within shutdownWait.
func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()

	select {
	case value := <-ch:
		return value
	case <-time.After(shutdownWait):
		t.Fatalf("timed out waiting for %s", what)

		panic("unreachable")
	}
}

// TestShuttingDownWithoutServerIsNil verifies a bare context yields a channel
// that never fires.
func TestShuttingDownWithoutServerIsNil(t *testing.T) {
	t.Parallel()

	if server.ShuttingDown(context.Background()) != nil {
		t.Fatal("ShuttingDown returned a channel without a server")
	}
}

// TestShuttingDownFiresWhileNormalRequestCompletes verifies shutdown signals
// handlers without canceling request contexts, so a normal request finishes.
func TestShuttingDownFiresWhileNormalRequestCompletes(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	observed := make(chan bool, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(started)

		select {
		case <-server.ShuttingDown(request.Context()):
		case <-time.After(shutdownWait):
		}

		<-release
		observed <- request.Context().Err() == nil
		_, _ = io.WriteString(writer, "done")
	})

	srv, cancel, done := startServer(t, handler)

	result := make(chan string, 1)

	go func() {
		httpResponse, err := http.Get("http://" + srv.Addr() + "/")

		if err != nil {
			result <- err.Error()

			return
		}

		defer func() {
			_ = httpResponse.Body.Close()
		}()
		body, _ := io.ReadAll(httpResponse.Body)
		result <- string(body)
	}()

	receive(t, started, "handler start")

	cancel()

	// Serve is draining now: it must not return while the request is open.
	select {
	case <-done:
		t.Fatal("Serve returned before the in-flight request finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	if !receive(t, observed, "handler finish") {
		t.Fatal("request context was canceled by shutdown")
	}

	if body := receive(t, result, "response"); body != "done" {
		t.Fatalf("body = %q, want done", body)
	}

	if err := receive(t, done, "Serve"); err != nil {
		t.Fatal(err)
	}
}

// TestShuttingDownEndsSSEBeforeDrainTimeout verifies an SSE stream returns
// promptly on shutdown instead of waiting for the drain timeout.
func TestShuttingDownEndsSSEBeforeDrainTimeout(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		values := make(chan []byte, 1)
		values <- []byte("hello")
		_ = response.SSEChan(writer, request, values)
	})

	srv, cancel, done := startServer(t, handler, func(serverBuilder *server.Builder) {
		serverBuilder.ShutdownTimeout(time.Minute)
	})

	reply, err := http.Get("http://" + srv.Addr() + "/")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = reply.Body.Close()
	})

	line, err := bufio.NewReader(reply.Body).ReadString('\n')

	if err != nil || line != "data: hello\n" {
		t.Fatalf("first event line = %q, %v", line, err)
	}

	started := time.Now()
	cancel()

	if err := receive(t, done, "Serve"); err != nil {
		t.Fatal(err)
	}

	if elapsed := time.Since(started); elapsed > shutdownWait {
		t.Fatalf("shutdown took %s", elapsed)
	}
}

// TestStopTwiceIsSafe verifies the shutdown signal tolerates Serve and Stop,
// and repeated Stop calls, all closing it.
func TestStopTwiceIsSafe(t *testing.T) {
	t.Parallel()

	srv, cancel, done := startServer(t, helloHandler)

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	cancel()

	if err := receive(t, done, "Serve"); err != nil {
		t.Fatal(err)
	}
}

// TestShutdownLeavesNoGoroutines verifies serving and stopping a server with
// a streaming client does not leak goroutines. It is not parallel so other
// tests do not skew the count.
func TestShutdownLeavesNoGoroutines(t *testing.T) {
	baseline := runtime.NumGoroutine()

	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = response.SSEChan(writer, request, make(chan []byte))
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

	client := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	reply, err := client.Get("http://" + srv.Addr() + "/")

	if err != nil {
		t.Fatal(err)
	}

	cancel()

	if err := receive(t, done, "Serve"); err != nil {
		t.Fatal(err)
	}

	if _, err := io.Copy(io.Discard, reply.Body); err != nil {
		t.Fatal(err)
	}

	_ = reply.Body.Close()
	client.CloseIdleConnections()

	deadline := time.Now().Add(shutdownWait)

	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if now := runtime.NumGoroutine(); now > baseline {
		t.Fatalf("goroutines = %d, baseline %d", now, baseline)
	}
}
