package bodylimit_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/middleware/bodylimit"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
)

// ExampleBuilderFunc rejects a declared oversized body before route handling,
// demonstrating the configured ceiling through an HTTP response.
func ExampleBuilderFunc() {
	configure := bodylimit.BuilderFunc(func(limiter *bodylimit.Builder) {
		limiter.Limit(1024)
	})

	routes := router.New()
	routes.Use(bodylimit.Middleware(configure))

	request := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(strings.Repeat("x", 1025)))
	recorder := httptest.NewRecorder()

	routes.ServeHTTP(recorder, request)
	fmt.Println(recorder.Code)
	// Output: 413
}

// TestMiddlewareNilBodyReturnsInvalidJSON exercises an explicitly absent body
// through middleware and the exported JSON reader. Empty JSON must return the
// ordinary input problem and render 400 without changing the caller's request.
func TestMiddlewareNilBodyReturnsInvalidJSON(t *testing.T) {
	t.Parallel()

	var readErr error
	handler := bodylimit.Middleware()(func(_ http.ResponseWriter, r *http.Request) error {
		_, err := request.JSON[struct{}](r)
		readErr = err

		return err
	})

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Body = nil // httptest otherwise supplies http.NoBody, which cannot reproduce the panic.
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, r)

	if !errors.Is(readErr, request.ErrInvalidJSON) {
		t.Fatalf("error = %v, want ErrInvalidJSON", readErr)
	}

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}

	if r.Body != nil {
		t.Fatal("middleware replaced the caller's nil body")
	}
}

// TestMiddlewareNilBodyPreservesRequestOwnership calls the public middleware
// handler directly so router-level copies cannot hide a mutation of the input
// request. An unknown-length nil body still decodes as empty JSON input.
func TestMiddlewareNilBodyPreservesRequestOwnership(t *testing.T) {
	t.Parallel()

	handler := bodylimit.Middleware()(func(_ http.ResponseWriter, r *http.Request) error {
		_, err := request.JSON[struct{}](r)

		return err
	})

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Body = nil
	r.ContentLength = -1
	originalContext := r.Context()

	err := handler(httptest.NewRecorder(), r)

	if !errors.Is(err, request.ErrInvalidJSON) {
		t.Fatalf("error = %v, want ErrInvalidJSON", err)
	}

	if r.Body != nil {
		t.Fatal("middleware replaced the caller's nil body")
	}

	if r.Context() != originalContext {
		t.Fatal("middleware replaced the caller's context")
	}
}

// readAllBody is a handler that reads the whole body with the request helpers
// and records whether it ran.
func readAllBody(called *bool) router.Handler {
	return func(_ http.ResponseWriter, r *http.Request) error {
		*called = true

		_, err := request.Bytes(r)

		return err
	}
}

// TestMiddlewareRejectsContentLengthOverLimit checks that an oversized
// declared body is rejected before the downstream handler runs.
func TestMiddlewareRejectsContentLengthOverLimit(t *testing.T) {
	t.Parallel()

	var called bool
	handler := bodylimit.Middleware(func(limiter *bodylimit.Builder) {
		limiter.Limit(4)
	})(readAllBody(&called))

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))

	if err := handler(httptest.NewRecorder(), r); !errors.Is(err, request.ErrBodyTooLarge) {
		t.Fatalf("error = %v, want ErrBodyTooLarge", err)
	}

	if called {
		t.Fatal("handler ran")
	}
}

// TestMiddlewareRejectsStreamedBodyOverLimit checks that a body without a
// declared length is capped when the downstream handler reads it.
func TestMiddlewareRejectsStreamedBodyOverLimit(t *testing.T) {
	t.Parallel()

	var called bool
	handler := bodylimit.Middleware(func(limiter *bodylimit.Builder) {
		limiter.Limit(4)
	})(readAllBody(&called))

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	r.ContentLength = -1

	if err := handler(httptest.NewRecorder(), r); !errors.Is(err, request.ErrBodyTooLarge) {
		t.Fatalf("error = %v, want ErrBodyTooLarge", err)
	}

	if !called {
		t.Fatal("handler did not run")
	}
}

// TestMiddlewareAllowsBodyAtLimitAndSetsContext checks the inclusive size
// boundary and the context limit made available to request helpers.
func TestMiddlewareAllowsBodyAtLimitAndSetsContext(t *testing.T) {
	t.Parallel()

	var limit int64
	handler := bodylimit.Middleware(func(limiter *bodylimit.Builder) {
		limiter.Limit(4)
	})(func(_ http.ResponseWriter, r *http.Request) error {
		limit, _ = request.MaxBodySizeFromContext(r.Context())

		_, err := request.Bytes(r)

		return err
	})

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("1234"))

	if err := handler(httptest.NewRecorder(), r); err != nil {
		t.Fatal(err)
	}

	if limit != 4 {
		t.Fatalf("context limit = %d, want 4", limit)
	}
}

// TestMiddlewareRaisesHelperDefault checks that an explicit middleware limit
// above the helper default permits a body at that larger limit.
func TestMiddlewareRaisesHelperDefault(t *testing.T) {
	t.Parallel()

	limit := request.DefaultMaxBodySize + 1
	handler := bodylimit.Middleware(func(limiter *bodylimit.Builder) {
		limiter.Limit(limit)
	})(func(_ http.ResponseWriter, r *http.Request) error {
		_, err := request.Bytes(r)

		return err
	})

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", int(limit))))

	if err := handler(httptest.NewRecorder(), r); err != nil {
		t.Fatalf("body at a limit above the helper default rejected: %v", err)
	}
}

// TestMiddlewareNilBodyStillRejectsOversizedContentLength keeps declared-size
// admission ahead of empty-body normalization. Even an absent stream cannot
// bypass the configured ceiling by declaring more bytes than it allows.
func TestMiddlewareNilBodyStillRejectsOversizedContentLength(t *testing.T) {
	t.Parallel()

	handler := bodylimit.Middleware(func(limiter *bodylimit.Builder) {
		limiter.Limit(4)
	})(func(http.ResponseWriter, *http.Request) error {
		t.Fatal("oversized declared body reached the handler")

		return nil
	})

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Body = nil
	r.ContentLength = 5
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, r)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
}

// TestMiddlewareNilBodyDoesNotReplaceCommittedResponse keeps an empty JSON
// failure available to the handler without appending a problem response after
// the handler has already committed its chosen status.
func TestMiddlewareNilBodyDoesNotReplaceCommittedResponse(t *testing.T) {
	t.Parallel()

	var readErr error
	handler := bodylimit.Middleware()(func(writer http.ResponseWriter, r *http.Request) error {
		writer.WriteHeader(http.StatusAccepted)

		_, err := request.JSON[struct{}](r)
		readErr = err

		return err
	})

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Body = nil
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, r)

	if !errors.Is(readErr, request.ErrInvalidJSON) {
		t.Fatalf("error = %v, want ErrInvalidJSON", readErr)
	}

	if recorder.Code != http.StatusAccepted || recorder.Body.Len() != 0 {
		t.Fatalf("response = %d %q, want 202 with no appended problem", recorder.Code, recorder.Body.String())
	}
}

// TestMiddlewarePreservesNativeLimitAndRequestOwnership uses a raw body reader
// to verify the native size error remains available. Calling the public handler
// directly also checks the middleware, rather than the router, copies the request.
func TestMiddlewarePreservesNativeLimitAndRequestOwnership(t *testing.T) {
	t.Parallel()

	handler := bodylimit.Middleware(func(limiter *bodylimit.Builder) {
		limiter.Limit(4)
	})(func(_ http.ResponseWriter, r *http.Request) error {
		_, err := io.ReadAll(r.Body)

		return err
	})

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	r.ContentLength = -1
	originalBody := r.Body
	originalContext := r.Context()

	err := handler(httptest.NewRecorder(), r)

	var limitErr *http.MaxBytesError

	if !errors.As(err, &limitErr) {
		t.Fatalf("error = %v, want *http.MaxBytesError", err)
	}

	if limitErr.Limit != 4 {
		t.Fatalf("native limit = %d, want 4", limitErr.Limit)
	}

	if r.Body != originalBody {
		t.Fatal("middleware replaced the caller's body")
	}

	if r.Context() != originalContext {
		t.Fatal("middleware replaced the caller's context")
	}
}

// TestMiddlewareNonpositiveLimitsUseDefault verifies zero and negative builder
// settings select the helper default rather than disabling the ceiling or
// rejecting nonempty input. Both the context and body reader expose that policy.
func TestMiddlewareNonpositiveLimitsUseDefault(t *testing.T) {
	t.Parallel()

	for _, configured := range []int64{0, -1} {
		handler := bodylimit.Middleware(func(limiter *bodylimit.Builder) {
			limiter.Limit(configured)
		})(func(_ http.ResponseWriter, r *http.Request) error {
			limit, ok := request.MaxBodySizeFromContext(r.Context())

			if !ok || limit != request.DefaultMaxBodySize {
				t.Fatalf("configured %d: context limit = %d, %t; want default", configured, limit, ok)
			}

			_, err := request.Bytes(r)

			return err
		})

		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("1234")))

		if recorder.Code != http.StatusNoContent {
			t.Fatalf("configured %d: status = %d, want 204", configured, recorder.Code)
		}
	}
}
