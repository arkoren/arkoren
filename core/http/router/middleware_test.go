package router_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/http/router"
)

// TestHandlerControllerPreservesNativeDeadline verifies Unwrap-based controller
// operations retain native inputs and errors through the router's writer.
func TestHandlerControllerPreservesNativeDeadline(t *testing.T) {
	t.Parallel()

	cause := errors.New("native deadline failure")
	want := time.Unix(123, 456)
	writer := &nativeWriter{ResponseRecorder: httptest.NewRecorder(), err: cause}
	handler := router.HTTPMiddleware(func(next http.Handler) http.Handler {
		return next
	})(func(writer http.ResponseWriter, _ *http.Request) error {
		return http.NewResponseController(writer).SetWriteDeadline(want)
	})

	err := handler(writer, httptest.NewRequest(http.MethodGet, "/", nil))

	if !writer.deadline.Equal(want) || !errors.Is(err, cause) {
		t.Fatalf("deadline = %v, returned error = %v", writer.deadline, err)
	}
}

// TestProvideStaticAddsValue verifies ProvideStatic sets a context value.
func TestProvideStaticAddsValue(t *testing.T) {
	t.Parallel()

	type key struct{}
	var got any
	handler := router.ProvideStatic(key{}, "v")(func(_ http.ResponseWriter, r *http.Request) error {
		got = r.Context().Value(key{})

		return nil
	})

	if err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)); err != nil || got != "v" {
		t.Fatalf("got = %v, err = %v", got, err)
	}
}

// TestHTTPMiddlewareObservesErrorStatus checks that standard middleware
// observes the rendered error status, not the status before problem rendering.
func TestHTTPMiddlewareObservesErrorStatus(t *testing.T) {
	t.Parallel()

	var observed int
	observe := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
			recorder := &statusRecorder{
				ResponseWriter: writer,
			}

			next.ServeHTTP(recorder, r)

			observed = recorder.status
		})
	}

	handler := router.HTTPMiddleware(observe)(func(http.ResponseWriter, *http.Request) error {
		return errors.New("failed")
	})

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if observed != http.StatusInternalServerError || response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("observed = %d, response = %d, want 500", observed, response.StatusCode)
	}
}

// statusRecorder records the status written through it.
type statusRecorder struct {
	// ResponseWriter forwards output while WriteHeader records the status seen by
	// standard middleware.
	http.ResponseWriter

	// status is the last status passed to WriteHeader.
	status int
}

// WriteHeader records status before delegating.
func (recorder *statusRecorder) WriteHeader(status int) {
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

// Unwrap exposes the wrapped writer.
func (recorder *statusRecorder) Unwrap() http.ResponseWriter {
	return recorder.ResponseWriter
}

// TestHTTPMiddlewareBuildsOncePerRoute verifies standard middleware captures
// route-local state during registration, rather than resetting on each request.
func TestHTTPMiddlewareBuildsOncePerRoute(t *testing.T) {
	t.Parallel()

	builds := 0
	routes := router.New()
	routes.Use(router.HTTPMiddleware(func(next http.Handler) http.Handler {
		builds++
		requests := 0

		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			requests++
			writer.Header().Set("X-Requests", fmt.Sprint(requests))
			next.ServeHTTP(writer, request)
		})
	}))
	routes.Any("/items", func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	if builds != 1 {
		t.Fatalf("constructors at registration = %d, want 1", builds)
	}

	serve(routes, http.MethodGet, "/items")

	response := serve(routes, http.MethodPost, "/items/")

	if builds != 1 || response.Header().Get("X-Requests") != "2" {
		t.Fatalf("constructors = %d, requests = %q", builds, response.Header().Get("X-Requests"))
	}
}

// TestHTTPMiddlewareKeepsConcurrentErrorsSeparate verifies a shared native
// middleware chain returns each request's error to its own outer middleware.
func TestHTTPMiddlewareKeepsConcurrentErrorsSeparate(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	handler := router.HTTPMiddleware(func(next http.Handler) http.Handler {
		return next
	})(func(_ http.ResponseWriter, request *http.Request) error {
		entered <- struct{}{}
		<-release

		return errors.New(request.URL.Path)
	})

	var workers sync.WaitGroup

	for _, target := range []string{"/first", "/second"} {
		workers.Go(func() {
			err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))

			if err == nil || err.Error() != target {
				t.Errorf("%s: returned %v", target, err)
			}
		})
	}

	<-entered
	<-entered
	close(release)
	workers.Wait()
}

// opaqueWriter models native middleware that delegates writes without exposing
// Unwrap. Such middleware must not make a committed response replaceable.
type opaqueWriter struct {
	// ResponseWriter receives writes without exposing its concrete capabilities.
	http.ResponseWriter
}

// TestHTTPMiddlewareDoesNotAppendAfterOpaqueWriterCommit verifies a native
// wrapper without Unwrap cannot hide a response that next has already written.
func TestHTTPMiddlewareDoesNotAppendAfterOpaqueWriterCommit(t *testing.T) {
	t.Parallel()

	handler := router.HTTPMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			next.ServeHTTP(opaqueWriter{ResponseWriter: writer}, request)
		})
	})(func(writer http.ResponseWriter, _ *http.Request) error {
		_, _ = io.WriteString(writer, "partial")

		return errors.New("failed after write")
	})

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	body, err := io.ReadAll(response.Body)

	if err != nil {
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusOK || string(body) != "partial" {
		t.Fatalf("status = %d, body = %q", response.StatusCode, body)
	}
}

// TestHTTPMiddlewareDoesNotAppendToBufferedResponse verifies local writes to a
// native buffer count as commitment even before it forwards anything outward.
func TestHTTPMiddlewareDoesNotAppendToBufferedResponse(t *testing.T) {
	t.Parallel()

	handler := router.HTTPMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			buffer := httptest.NewRecorder()

			next.ServeHTTP(buffer, request)
			writer.WriteHeader(buffer.Code)

			_, _ = writer.Write(buffer.Body.Bytes())
		})
	})(func(writer http.ResponseWriter, _ *http.Request) error {
		_, _ = io.WriteString(writer, "partial")

		return errors.New("failed after write")
	})

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	body, err := io.ReadAll(response.Body)

	if err != nil {
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusOK || string(body) != "partial" {
		t.Fatalf("status = %d, body = %q", response.StatusCode, body)
	}
}

// TestHTTPMiddlewarePreservesNativePreamble verifies a status written before
// calling next is not hidden by an opaque writer supplied to next.
func TestHTTPMiddlewarePreservesNativePreamble(t *testing.T) {
	t.Parallel()

	handler := router.HTTPMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusAccepted)
			next.ServeHTTP(opaqueWriter{ResponseWriter: writer}, request)
		})
	})(func(http.ResponseWriter, *http.Request) error {
		return errors.New("failed after native status")
	})

	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusAccepted || response.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
}

// TestHTTPMiddlewareNestedAdapterPreservesOpaquePreamble verifies adding another
// native adapter cannot hide an outer middleware's already committed status.
func TestHTTPMiddlewareNestedAdapterPreservesOpaquePreamble(t *testing.T) {
	t.Parallel()

	routes := router.New()
	routes.Use(router.HTTPMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusAccepted)
			next.ServeHTTP(opaqueWriter{ResponseWriter: writer}, request)
		})
	}))
	routes.Use(router.HTTPMiddleware(func(next http.Handler) http.Handler {
		return next
	}))
	routes.Get("/items", func(http.ResponseWriter, *http.Request) error {
		return errors.New("failed after outer native status")
	})

	response := serve(routes, http.MethodGet, "/items")

	if response.Code != http.StatusAccepted || response.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
}

// TestHTTPMiddlewareRenderedRecoveryDoesNotAbort verifies rendering a recovered
// pre-response panic inside native middleware is not mistaken for a panic that
// occurred after commitment. Native middleware still observes the rendered 500.
func TestHTTPMiddlewareRenderedRecoveryDoesNotAbort(t *testing.T) {
	t.Parallel()

	observed := 0
	routes := router.New()
	routes.Use(router.HTTPMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			status := &statusRecorder{ResponseWriter: writer}

			next.ServeHTTP(status, request)

			observed = status.status
		})
	}), router.Recover())
	routes.Get("/items", func(http.ResponseWriter, *http.Request) error {
		panic("failed before writing")
	})

	response := serve(routes, http.MethodGet, "/items")

	if response.Code != http.StatusInternalServerError || observed != http.StatusInternalServerError {
		t.Fatalf("status = %d, native observation = %d", response.Code, observed)
	}
}

// TestHTTPMiddlewareRenderedRecoveryPreservesDiagnostics verifies marking an
// already-rendered recovery does not hide its original cause or stack from
// outer middleware and after-response error classification.
func TestHTTPMiddlewareRenderedRecoveryPreservesDiagnostics(t *testing.T) {
	t.Parallel()

	cause := errors.New("private diagnostic fixture")
	handler := router.HTTPMiddleware(func(next http.Handler) http.Handler {
		return next
	})(router.Recover()(func(http.ResponseWriter, *http.Request) error {
		panic(cause)
	}))

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(err, cause) || !errors.Is(err, router.ErrRecoverUnexpected) {
		t.Fatalf("error chain = %v", err)
	}

	var diagnostic interface{ Stack() []byte }

	if !errors.As(err, &diagnostic) || len(diagnostic.Stack()) == 0 {
		t.Fatal("rendered recovery lost its diagnostic stack")
	}
}

// TestHTTPMiddlewareCommittedRecoveryStillAborts verifies native adaptation does
// not label a panic following application output as a safely rendered recovery.
func TestHTTPMiddlewareCommittedRecoveryStillAborts(t *testing.T) {
	t.Parallel()

	handler := router.HTTPMiddleware(func(next http.Handler) http.Handler {
		return next
	})(router.Recover()(func(writer http.ResponseWriter, _ *http.Request) error {
		_, _ = io.WriteString(writer, "partial")
		panic("failed after writing")
	}))

	response := httptest.NewRecorder()
	defer func() {
		if err, _ := recover().(error); !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("panic = %v, want ErrAbortHandler", err)
		}

		if response.Body.String() != "partial" {
			t.Fatalf("body = %q, want only application output", response.Body.String())
		}
	}()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
}

// TestRecoverNewCommittedPanicDoesNotInheritRenderedDisposition verifies a
// previously rendered error used as a new panic cause does not mark the new
// failure handled. The new panic still occurred after this response started.
func TestRecoverNewCommittedPanicDoesNotInheritRenderedDisposition(t *testing.T) {
	t.Parallel()

	previous := router.HTTPMiddleware(func(next http.Handler) http.Handler {
		return next
	})(router.Recover()(func(http.ResponseWriter, *http.Request) error {
		panic("previous request")
	}))

	previousError := previous(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/previous", nil))

	if previousError == nil {
		t.Fatal("previous request did not return its recovery error")
	}

	handler := router.Recover()(func(writer http.ResponseWriter, _ *http.Request) error {
		_, _ = io.WriteString(writer, "partial")
		panic(previousError)
	})
	defer func() {
		if err, _ := recover().(error); !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("panic = %v, want ErrAbortHandler for the new failure", err)
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/current", nil))
}

// TestHTTPMiddlewareTimeoutKeepsLateErrorRequestLocal verifies native timeout
// responses remain intact when next returns an error after the timeout handler.
func TestHTTPMiddlewareTimeoutKeepsLateErrorRequestLocal(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		handler := router.HTTPMiddleware(func(next http.Handler) http.Handler {
			return http.TimeoutHandler(next, time.Second, "timed out")
		})(func(http.ResponseWriter, *http.Request) error {
			<-release

			return errors.New("late request-local error")
		})

		response := httptest.NewRecorder()

		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
		close(release)
		synctest.Wait()

		if response.Code != http.StatusServiceUnavailable || response.Body.String() != "timed out" {
			t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
		}
	})
}

// TestHTTPMiddlewareConcurrentNativeCommit verifies native middleware may
// commit its own response while next finishes against a separate private buffer.
// Only commitment observation crosses goroutines; no writer has concurrent writes.
func TestHTTPMiddlewareConcurrentNativeCommit(t *testing.T) {
	t.Parallel()

	handler := router.HTTPMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			done := make(chan struct{})

			go func() {
				next.ServeHTTP(httptest.NewRecorder(), request)
				close(done)
			}()

			writer.WriteHeader(http.StatusAccepted)
			<-done
		})
	})(func(http.ResponseWriter, *http.Request) error {
		return nil
	})

	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.Code)
	}
}
