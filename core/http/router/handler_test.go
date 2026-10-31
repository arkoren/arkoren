package router_test

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// TestHandlerInjectsResponseContextForMiddleware supplies response context to middleware.
func TestHandlerInjectsResponseContextForMiddleware(t *testing.T) {
	t.Parallel()

	handler := router.Middleware(func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, request *http.Request) error {
			responseContext, ok := problem.FromContext(request.Context())

			if !ok {
				t.Fatal("expected response context in middleware request")
			}

			responseContext.SetProperty("request_id", "request-123")

			return next(writer, request)
		}
	})(func(http.ResponseWriter, *http.Request) error {
		return (problem.Details{}).Derive(func(details *problem.Builder) {
			details.Status(http.StatusBadRequest)
		})
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept", "application/problem+json")

	response := handler.Record(request)

	var body map[string]any

	if err := json.UnmarshalRead(response.Body, &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if body["request_id"] != "request-123" {
		t.Fatalf("expected request_id request-123, got %v", body["request_id"])
	}
}

// TestHandlerRunsAfterResponseHooksOnPanic checks that response-finalization
// hooks execute even when the handler panics.
func TestHandlerRunsAfterResponseHooksOnPanic(t *testing.T) {
	t.Parallel()

	ran := false
	handler := router.Handler(func(_ http.ResponseWriter, r *http.Request) error {
		request.Hooks(r).AfterResponse(func(error) {
			ran = true
		})
		panic("boom")
	})

	func() {
		defer func() {
			_ = recover()
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()

	if !ran {
		t.Fatal("after-response hook did not run")
	}
}

// TestHandlerAbortsWhenPanicAfterResponseStarted checks that recovery aborts a
// partially written response instead of appending an incompatible problem
// body.
func TestHandlerAbortsWhenPanicAfterResponseStarted(t *testing.T) {
	t.Parallel()

	handler := router.Recover()(func(writer http.ResponseWriter, _ *http.Request) error {
		_, _ = io.WriteString(writer, "partial")
		panic("boom")
	})

	defer func() {
		if err, _ := recover().(error); !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("recovered = %v, want http.ErrAbortHandler", err)
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

// TestHTTPDriverHeaderOnlyRespondsOK checks that an adapted standard handler
// that sets only headers still completes with HTTP 200.
func TestHTTPDriverHeaderOnlyRespondsOK(t *testing.T) {
	t.Parallel()

	handler := router.HTTP(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Test", "1")
	}))

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
}

// ExampleHTTPStatusError_HTTPStatus checks the public status interface against
// the problem implementation used in its adjacent documentation.
func ExampleHTTPStatusError_HTTPStatus() {
	var failure router.HTTPStatusError = problem.Forbidden()
	fmt.Println(failure.HTTPStatus())
	// Output: 403
}

// ExampleHTTPHandlerError checks that a problem can own rendering through the
// public error interface, without requiring a router implementation detail.
func ExampleHTTPHandlerError() {
	var failure router.HTTPHandlerError = problem.NotFound()
	response := httptest.NewRecorder()

	failure.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users/7", nil))
	fmt.Println(response.Code)
	// Output: 404
}

// ExampleHandler checks the error-returning handler example through ServeHTTP,
// including centralized rendering rather than invoking a private renderer.
func ExampleHandler() {
	handler := router.Handler(func(http.ResponseWriter, *http.Request) error {
		return problem.NotFound()
	})

	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users/7", nil))
	fmt.Println(response.Code)
	// Output: 404
}

// TestHandlerInformationalResponseAllowsFinalProblem verifies real net/http
// informational responses leave error rendering and final-status hooks pending.
func TestHandlerInformationalResponseAllowsFinalProblem(t *testing.T) {
	t.Parallel()

	final := make(chan []int, 1)
	handler := router.Handler(func(writer http.ResponseWriter, r *http.Request) error {
		var statuses []int
		request.Hooks(r).BeforeWriteHeader(func(_ http.ResponseWriter, status int) {
			statuses = append(statuses, status)
		})
		request.Hooks(r).AfterResponse(func(error) {
			final <- slices.Clone(statuses)
		})
		writer.WriteHeader(http.StatusEarlyHints)

		return errors.New("failed before final response")
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	r, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)

	if err != nil {
		t.Fatal(err)
	}

	var hints atomic.Int32
	r = r.WithContext(httptrace.WithClientTrace(r.Context(), &httptrace.ClientTrace{
		Got1xxResponse: func(status int, _ textproto.MIMEHeader) error {
			hints.Store(int32(status))

			return nil
		},
	}))

	response, err := server.Client().Do(r)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = response.Body.Close()
	})

	if response.StatusCode != http.StatusInternalServerError || hints.Load() != http.StatusEarlyHints {
		t.Fatalf("final status = %d, informational status = %d", response.StatusCode, hints.Load())
	}

	if statuses := <-final; !slices.Equal(statuses, []int{http.StatusInternalServerError}) {
		t.Fatalf("before-header statuses = %v", statuses)
	}
}

// TestHandlerSwitchingProtocolsIsCommitted verifies 101 is a final status,
// unlike other 1xx responses, and is never followed by a replacement problem.
func TestHandlerSwitchingProtocolsIsCommitted(t *testing.T) {
	t.Parallel()

	var statuses []int
	handler := router.Handler(func(writer http.ResponseWriter, r *http.Request) error {
		request.Hooks(r).BeforeWriteHeader(func(_ http.ResponseWriter, status int) {
			statuses = append(statuses, status)
		})
		writer.WriteHeader(http.StatusSwitchingProtocols)

		return errors.New("upgraded protocol failure")
	})

	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusSwitchingProtocols || response.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}

	if !slices.Equal(statuses, []int{http.StatusSwitchingProtocols}) {
		t.Fatalf("before-header statuses = %v", statuses)
	}
}

// nativeWriter records native protocol operations while exposing a standard
// response writer. Tests exercise it only through public router/controller APIs.
type nativeWriter struct {
	// ResponseRecorder captures unexpected HTTP output after a hijack.
	*httptest.ResponseRecorder

	// connection is the connection returned by a successful Hijack.
	connection net.Conn

	// err is the native operation failure preserved by the controller.
	err error

	// statuses records explicit status writes, including default fallbacks.
	statuses []int

	// deadline records the deadline delegated through router writer wrappers.
	deadline time.Time
}

// WriteHeader records the attempted status before forwarding it to the recorder.
func (writer *nativeWriter) WriteHeader(status int) {
	writer.statuses = append(writer.statuses, status)
	writer.ResponseRecorder.WriteHeader(status)
}

// Hijack returns the configured native failure or transfers the test connection.
// Cleanup remains with the test, which owns both ends of its in-memory pipe.
func (writer *nativeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if writer.err != nil {
		return nil, nil, writer.err
	}

	buffer := bufio.NewReadWriter(bufio.NewReader(writer.connection), bufio.NewWriter(writer.connection))

	return writer.connection, buffer, nil
}

// SetWriteDeadline captures the delegated deadline and returns the native error.
func (writer *nativeWriter) SetWriteDeadline(deadline time.Time) error {
	writer.deadline = deadline

	return writer.err
}

// TestHandlerSuccessfulHijackSuppressesHTTPFallback verifies taking over a
// connection prevents both the default 204 and later ordinary error rendering.
func TestHandlerSuccessfulHijackSuppressesHTTPFallback(t *testing.T) {
	t.Parallel()

	connection, peer := net.Pipe()
	t.Cleanup(func() {
		_ = connection.Close()
		_ = peer.Close()
	})

	writer := &nativeWriter{ResponseRecorder: httptest.NewRecorder(), connection: connection}
	handler := router.Handler(func(writer http.ResponseWriter, _ *http.Request) error {
		if _, _, err := http.NewResponseController(writer).Hijack(); err != nil {
			return err
		}

		return errors.New("connection now belongs to caller")
	})

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	if len(writer.statuses) != 0 || writer.Body.Len() != 0 {
		t.Fatalf("HTTP writes after hijack: statuses = %v, body = %q", writer.statuses, writer.Body.String())
	}
}

// TestHandlerFailedHijackLeavesErrorRenderable verifies an unsuccessful native
// hijack preserves the failure in AfterResponse and still permits a 500 response.
func TestHandlerFailedHijackLeavesErrorRenderable(t *testing.T) {
	t.Parallel()

	cause := errors.New("native hijack failure")
	writer := &nativeWriter{ResponseRecorder: httptest.NewRecorder(), err: cause}
	var observed error
	handler := router.Handler(func(writer http.ResponseWriter, r *http.Request) error {
		request.Hooks(r).AfterResponse(func(err error) {
			observed = err
		})

		_, _, err := http.NewResponseController(writer).Hijack()

		return err
	})

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	if writer.Code != http.StatusInternalServerError || !errors.Is(observed, cause) {
		t.Fatalf("status = %d, observed = %v", writer.Code, observed)
	}
}
