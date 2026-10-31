package requestlog_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/middleware/requestlog"
	"arkoren.dev/core/http/router"
)

// ExampleBuilderFunc selects a logger and observes the absence of a failure
// record for a successful request, without opening a listener.
func ExampleBuilderFunc() {
	var output bytes.Buffer
	configure := requestlog.BuilderFunc(func(log *requestlog.Builder) {
		log.Logger(slog.New(slog.NewTextHandler(&output, nil)))
	})

	routes := router.New()
	routes.Use(requestlog.Middleware(configure))
	routes.Get("/", func(http.ResponseWriter, *http.Request) error {
		return nil
	})
	routes.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	fmt.Println(output.Len())
	// Output: 0
}

// TestMiddlewareLogsFailedRequest checks that a handler error produces a
// failure log containing the resulting HTTP 500 status.
func TestMiddlewareLogsFailedRequest(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	handler := router.Handler(requestlog.Middleware(func(log *requestlog.Builder) {
		log.Logger(logger)
	})(func(http.ResponseWriter, *http.Request) error {
		return errors.New("boom")
	}))

	_ = handler.Record(httptest.NewRequest(http.MethodGet, "/x", nil))

	if !strings.Contains(output.String(), "request failed") || !strings.Contains(output.String(), "status=500") {
		t.Fatalf("log = %q", output.String())
	}
}

// TestMiddlewareSkipsSuccessfulRequest checks that a successful handler leaves
// the failure-only request log empty.
func TestMiddlewareSkipsSuccessfulRequest(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	handler := router.Handler(requestlog.Middleware(func(log *requestlog.Builder) {
		log.Logger(logger)
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	}))

	_ = handler.Record(httptest.NewRequest(http.MethodGet, "/x", nil))

	if output.Len() != 0 {
		t.Fatalf("log = %q", output.String())
	}
}

// TestMiddlewareLogsWrittenServerFailure verifies that an observed 503 remains
// a failure even when its handler returns no error.
func TestMiddlewareLogsWrittenServerFailure(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	handler := router.Handler(requestlog.Middleware(func(log *requestlog.Builder) {
		log.Logger(slog.New(slog.NewTextHandler(&output, nil)))
	})(func(writer http.ResponseWriter, _ *http.Request) error {
		writer.WriteHeader(http.StatusServiceUnavailable)

		return nil
	}))

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))
	t.Cleanup(func() {
		// The recorder owns no external resources; follow response-body ownership.
		_ = response.Body.Close()
	})

	if !strings.Contains(output.String(), "status=503") {
		t.Fatalf("failure log = %q, want the observed 503", output.String())
	}
}
