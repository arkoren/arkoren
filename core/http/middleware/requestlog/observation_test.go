package requestlog_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/middleware/requestlog"
	"arkoren.dev/core/http/router"
)

// TestMiddlewareSkipsSuccessfulHijackedRequest verifies that a handshake written
// directly to an owned connection is not reported as an HTTP 500 failure.
func TestMiddlewareSkipsSuccessfulHijackedRequest(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	handler := router.Handler(requestlog.Middleware(func(log *requestlog.Builder) {
		log.Logger(slog.New(slog.NewTextHandler(&output, nil)))
	})(func(writer http.ResponseWriter, _ *http.Request) error {
		connection, buffered, err := http.NewResponseController(writer).Hijack()

		if err != nil {
			return err
		}

		defer func() {
			// The handler owns the hijacked connection; closure ends this handshake.
			_ = connection.Close()
		}()

		handshake := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\nUpgrade: example\r\n\r\n"

		if _, err := buffered.WriteString(handshake); err != nil {
			return err
		}

		return buffered.Flush()
	}))

	handled := make(chan struct{})
	native := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler.ServeHTTP(writer, request)
		close(handled)
	}))
	t.Cleanup(native.Close)

	response, err := native.Client().Get(native.URL)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		// Closing an already-closed upgraded connection is best-effort cleanup.
		_ = response.Body.Close()
	})

	<-handled

	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", response.StatusCode)
	}

	if output.Len() != 0 {
		t.Fatalf("successful hijack logged as a failure: %s", output.String())
	}
}

// TestMiddlewareLogsErrorAfterHijackWithUnknownStatus verifies that returned
// errors remain visible without inventing a status for direct connection writes.
func TestMiddlewareLogsErrorAfterHijackWithUnknownStatus(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	handler := router.Handler(requestlog.Middleware(func(log *requestlog.Builder) {
		log.Logger(slog.New(slog.NewTextHandler(&output, nil)))
	})(func(writer http.ResponseWriter, _ *http.Request) error {
		connection, _, err := http.NewResponseController(writer).Hijack()

		if err != nil {
			return err
		}

		// The handler owns the connection; the empty handshake ends with EOF.
		_ = connection.Close()

		return errors.New("upgraded session failed")
	}))

	handled := make(chan struct{})
	native := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler.ServeHTTP(writer, request)
		close(handled)
	}))
	t.Cleanup(native.Close)

	response, err := native.Client().Get(native.URL)

	if err == nil {
		// An unexpected HTTP response still belongs to the client.
		_ = response.Body.Close()
		t.Fatal("hijacked connection unexpectedly returned an HTTP response")
	}

	<-handled

	if !strings.Contains(output.String(), "status=0") || !strings.Contains(output.String(), "upgraded session failed") {
		t.Fatalf("failure log = %q, want the error and unknown status", output.String())
	}
}
