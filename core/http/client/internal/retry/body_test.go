package retry_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"arkoren.dev/core/http/client"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// roundTripFunc supplies deterministic native outcomes without network I/O.
type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip delegates to the supplied native function.
func (send roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return send(request)
}

// do retains normal http.Client behavior and closes the response at test exit.
func do(t *testing.T, transport http.RoundTripper, req *http.Request) (*http.Response, error) {
	t.Helper()

	resp, err := (&http.Client{Transport: transport}).Do(req)

	if resp != nil {
		t.Cleanup(func() {
			_ = resp.Body.Close()
		})
	}

	return resp, err
}

// upgradeReadCloser supplies deterministic response bytes and observable native
// cleanup. Keeping writing separate lets tests detect fabricated capabilities.
type upgradeReadCloser struct {
	// Reader supplies response data or a deliberately injected read error.
	io.Reader

	// close performs the native cleanup; the transport must call it only once.
	close func() error
}

// Close delegates to the native cleanup spy without making it idempotent, so
// repeated underlying closes cannot be hidden by the fixture.
func (body *upgradeReadCloser) Close() error {
	return body.close()
}

// upgradeWriterFunc injects partial writes and errors without a network peer.
// It reports exactly the count and error that the caller's write should return.
type upgradeWriterFunc func([]byte) (int, error)

// Write invokes the injected write operation without normalizing its result.
// This makes it possible to check that the transport preserves native errors.
func (write upgradeWriterFunc) Write(data []byte) (int, error) {
	return write(data)
}

// TestTransportUpgradeBodyPreservesReadWriteAndCleanup exercises the exported
// retry/telemetry composition with an upgrade body and a live attempt deadline.
// Reading to EOF must not cancel the still-writable upgraded stream.
func TestTransportUpgradeBodyPreservesReadWriteAndCleanup(t *testing.T) {
	t.Parallel()

	var attempt context.Context
	closes := 0
	closedBeforeCancel := false
	written := &bytes.Buffer{}
	native := struct {
		io.ReadCloser
		io.Writer
	}{
		ReadCloser: &upgradeReadCloser{
			Reader: strings.NewReader("server bytes"),
			close: func() error {
				closes++
				closedBeforeCancel = attempt.Err() == nil

				return nil
			},
		},
		Writer: written,
	}

	transport := client.Transport(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempt = request.Context()

		return &http.Response{StatusCode: http.StatusSwitchingProtocols, Body: native}, nil
	}), func(transport *client.Builder) {
		transport.Telemetry(telemetrytest.New().Telemetry(t))
		transport.AttemptTimeout(time.Hour)
	})

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "upgrade://local", nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := do(t, transport, request)

	if err != nil {
		t.Fatal(err)
	}

	stream, ok := response.Body.(io.ReadWriteCloser)

	if !ok {
		t.Fatalf("upgrade body %T lost io.ReadWriteCloser", response.Body)
	}

	verifyUpgradeExchange(t, stream, written)

	if attempt.Err() != nil || closes != 0 {
		t.Fatalf("stream canceled or closed before Close: %v, %d", attempt.Err(), closes)
	}

	verifyUpgradeConcurrentClose(t, stream, nil)
	verifyUpgradeCleanup(t, closes, attempt)

	if !closedBeforeCancel {
		t.Fatal("attempt canceled before native Close")
	}

	if request.Context().Err() != nil {
		t.Fatal("closing response canceled caller's context")
	}
}

// TestNewTransportUpgradeBodyPreservesReadWrite checks a native HTTP/1 upgrade
// over net.Pipe, without sockets. Replacing http.DefaultTransport to inject the
// native dialer requires this test to remain nonparallel.
func TestNewTransportUpgradeBodyPreservesReadWrite(t *testing.T) {
	connection, peer := net.Pipe()
	results := make(chan error, 1)
	workers := sync.WaitGroup{}
	workers.Go(func() {
		results <- serveUpgradeExchange(peer)
		// The peer owns its pipe end; cleanup also closes it on test failure.
		_ = peer.Close()
	})
	t.Cleanup(func() {
		// Closing both pipe ends unblocks the exchange on an earlier assertion failure.
		_ = connection.Close()
		_ = peer.Close()
		workers.Wait()
	})

	base := &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return connection, nil
		},
	}

	original := http.DefaultTransport
	http.DefaultTransport = base
	t.Cleanup(func() {
		http.DefaultTransport = original
		base.CloseIdleConnections()
	})

	transport, err := client.NewTransport(nil)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup((&http.Client{Transport: transport}).CloseIdleConnections)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://upgrade.invalid", nil)

	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "fixture")

	response, err := do(t, transport, request)

	if err != nil {
		t.Fatal(err)
	}

	stream, ok := response.Body.(io.ReadWriteCloser)

	if !ok {
		t.Fatalf("owned transport upgrade body %T lost io.ReadWriteCloser", response.Body)
	}

	data := make([]byte, len("server bytes"))

	if _, err := io.ReadFull(stream, data); err != nil {
		t.Fatal(err)
	}

	if string(data) != "server bytes" {
		t.Fatalf("ReadFull = %q", data)
	}

	count, err := stream.Write([]byte("client bytes"))

	if err != nil || count != len("client bytes") {
		t.Fatalf("Write = %d, %v", count, err)
	}

	if err := <-results; err != nil {
		t.Fatal(err)
	}
}

// serveUpgradeExchange speaks just enough HTTP/1 to obtain net/http's actual
// writable 101 body. The fixed-size exchange avoids EOF or wall-clock waits.
func serveUpgradeExchange(peer net.Conn) error {
	if _, err := http.ReadRequest(bufio.NewReader(peer)); err != nil {
		return err
	}

	_, err := io.WriteString(peer, "HTTP/1.1 101 Switching Protocols\r\n"+
		"Connection: Upgrade\r\nUpgrade: fixture\r\n\r\nserver bytes")

	if err != nil {
		return err
	}

	data := make([]byte, len("client bytes"))

	if _, err := io.ReadFull(peer, data); err != nil {
		return err
	}

	if string(data) != "client bytes" {
		return fmt.Errorf("native upgrade received %q, want client bytes", data)
	}

	return nil
}

// verifyUpgradeExchange checks both directions against the native in-memory
// body. Reading through EOF first verifies that writes remain usable afterward.
func verifyUpgradeExchange(t *testing.T, stream io.ReadWriteCloser, written *bytes.Buffer) {
	t.Helper()

	data, err := io.ReadAll(stream)

	if err != nil || string(data) != "server bytes" {
		t.Fatalf("ReadAll = %q, %v", data, err)
	}

	count, err := stream.Write([]byte("client bytes"))

	if err != nil || count != len("client bytes") {
		t.Fatalf("Write = %d, %v", count, err)
	}

	if written.String() != "client bytes" {
		t.Fatalf("native bytes = %q", written.String())
	}
}

// TestTransportUpgradeBodyPreservesErrors checks partial writes, read errors,
// and concurrent closes through Transport. I/O errors leave cleanup to the
// caller, while the first native Close error is retained for every close.
func TestTransportUpgradeBodyPreservesErrors(t *testing.T) {
	t.Parallel()

	readErr := errors.New("native read failed")
	writeErr := errors.New("native write failed")
	closeErr := errors.New("native close failed")
	closes := atomic.Int32{}
	var attempt context.Context
	native := struct {
		io.ReadCloser
		io.Writer
	}{
		ReadCloser: &upgradeReadCloser{
			Reader: io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(readErr)),
			close: func() error {
				closes.Add(1)

				return closeErr
			},
		},
		Writer: upgradeWriterFunc(func(data []byte) (int, error) {
			return len(data) - 1, writeErr
		}),
	}

	transport := client.Transport(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempt = request.Context()

		return &http.Response{StatusCode: http.StatusSwitchingProtocols, Body: native}, nil
	}), func(transport *client.Builder) {
		transport.Telemetry(telemetrytest.New().Telemetry(t))
		transport.AttemptTimeout(time.Hour)
	})

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "upgrade://local", nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := do(t, transport, request)

	if err != nil {
		t.Fatal(err)
	}

	stream, ok := response.Body.(io.ReadWriteCloser)

	if !ok {
		t.Fatalf("upgrade body %T lost io.ReadWriteCloser", response.Body)
	}

	data, err := io.ReadAll(stream)

	if !errors.Is(err, readErr) || string(data) != "partial" {
		t.Fatalf("ReadAll = %q, %v; want partial data and native error", data, err)
	}

	count, err := stream.Write([]byte("bytes"))

	if !errors.Is(err, writeErr) || count != len("bytes")-1 {
		t.Fatalf("Write = %d, %v; want native count and error", count, err)
	}

	if closes.Load() != 0 || attempt.Err() != nil {
		t.Fatal("I/O errors closed the caller-owned stream or canceled its context")
	}

	verifyUpgradeConcurrentClose(t, stream, closeErr)
	verifyUpgradeCleanup(t, int(closes.Load()), attempt)
}

// verifyUpgradeCleanup checks the two responsibilities shared by writable and
// read-only cancellation wrappers after the caller closes the response.
func verifyUpgradeCleanup(t *testing.T, closes int, attempt context.Context) {
	t.Helper()

	if closes != 1 || attempt.Err() != context.Canceled {
		t.Fatalf("cleanup: closes = %d, context = %v", closes, attempt.Err())
	}
}

// verifyUpgradeConcurrentClose checks that concurrent consumers all observe the
// first Close result. The caller separately checks native cleanup and context.
func verifyUpgradeConcurrentClose(t *testing.T, body io.Closer, expected error) {
	t.Helper()

	const callers = 8
	results := make(chan error, callers)
	workers := sync.WaitGroup{}

	for range callers {
		workers.Go(func() {
			results <- body.Close()
		})
	}

	workers.Wait()
	close(results)

	for err := range results {
		if !errors.Is(err, expected) {
			t.Fatalf("Close = %v, want original error %v", err, expected)
		}
	}
}

// TestTransportOrdinaryBodyRemainsReadOnly checks that capability preservation
// does not advertise writes on an ordinary native io.ReadCloser response. With
// active telemetry it must still close once and cancel despite a Close error.
func TestTransportOrdinaryBodyRemainsReadOnly(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("ordinary close failed")
	closes := atomic.Int32{}
	var attempt context.Context
	transport := client.Transport(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempt = request.Context()

		return &http.Response{
			StatusCode: http.StatusOK,
			Body: &upgradeReadCloser{
				Reader: strings.NewReader("ordinary body"),
				close: func() error {
					closes.Add(1)

					return closeErr
				},
			},
		}, nil
	}), func(transport *client.Builder) {
		transport.Telemetry(telemetrytest.New().Telemetry(t))
		transport.AttemptTimeout(time.Hour)
	})

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "upgrade://local", nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := do(t, transport, request)

	if err != nil {
		t.Fatal(err)
	}

	if _, ok := response.Body.(io.Writer); ok {
		t.Fatalf("read-only body %T unexpectedly exposes io.Writer", response.Body)
	}

	data, err := io.ReadAll(response.Body)

	if err != nil || string(data) != "ordinary body" {
		t.Fatalf("ReadAll = %q, %v", data, err)
	}

	if closes.Load() != 0 || attempt.Err() != nil {
		t.Fatal("reading body closed it or canceled its context")
	}

	verifyUpgradeConcurrentClose(t, response.Body, closeErr)
	verifyUpgradeCleanup(t, int(closes.Load()), attempt)
}

// TestTransportUpgradeReadOnlyBodyRemainsReadOnly checks a custom 101 response
// whose native body lacks writing: status alone must not add the capability.
func TestTransportUpgradeReadOnlyBodyRemainsReadOnly(t *testing.T) {
	t.Parallel()

	transport := client.Transport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusSwitchingProtocols,
			Body:       io.NopCloser(strings.NewReader("read-only upgrade")),
		}, nil
	}))

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "upgrade://local", nil)

	if err != nil {
		t.Fatal(err)
	}

	response, err := do(t, transport, request)

	if err != nil {
		t.Fatal(err)
	}

	if _, ok := response.Body.(io.Writer); ok {
		t.Fatalf("read-only upgrade body %T unexpectedly exposes io.Writer", response.Body)
	}

	data, err := io.ReadAll(response.Body)

	if err != nil || string(data) != "read-only upgrade" {
		t.Fatalf("ReadAll = %q, %v", data, err)
	}
}
