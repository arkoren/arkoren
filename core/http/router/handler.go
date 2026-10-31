package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"

	"arkoren.dev/core/internal/hook"
	"arkoren.dev/foundation/problem"
)

// Handler handles an HTTP request and may return an error for centralized
// rendering by ServeHTTP. Errors cannot replace a response already started.
// Returning nil without writing produces 204; use HTTP for net/http's default
// 200 behavior. Captures must support concurrent requests when shared by a server.
//
// Example:
//
//	handler := router.Handler(func(http.ResponseWriter, *http.Request) error {
//		return problem.NotFound()
//	})
//	response := httptest.NewRecorder()
//	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users/7", nil))
//	fmt.Println(response.Code) // 404
type Handler func(writer http.ResponseWriter, request *http.Request) error

// HTTP adapts a standard [http.Handler] to a [Handler] that always returns nil.
// As in net/http, a handler that writes nothing, or only headers, responds 200.
// [Router.Routes] reports the adapted handler, not the adapter.
//
// Example:
//
//	handler := router.HTTP(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
//		writer.Header().Set("X-Service", "example")
//	}))
//	response := httptest.NewRecorder()
//	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
//	fmt.Println(response.Code) // 200
func HTTP(handler http.Handler) Handler {
	return func(writer http.ResponseWriter, request *http.Request) error {
		if sink, ok := request.Context().Value(probeKey{}).(*http.Handler); ok {
			*sink = handler // answer a route-description probe; see describe

			return nil
		}

		handler.ServeHTTP(writer, request)

		if hooked := hook.Lookup(writer); hooked != nil && !hooked.WriteHeaderCalled() {
			writer.WriteHeader(http.StatusOK)
		}

		return nil
	}
}

// HTTPStatusError supplies the HTTP status for centralized error rendering.
// The router discovers it through the error chain; its error text remains a
// server-side diagnostic unless the error also implements HTTPHandlerError.
//
// Example:
//
//	var failure router.HTTPStatusError = problem.NotFound()
//	fmt.Println(failure.HTTPStatus()) // 404
type HTTPStatusError interface {
	error

	// HTTPStatus returns the status code used for the error response. Return a
	// valid HTTP status; the router passes it to the problem renderer.
	//
	// Example:
	//
	//	var failure router.HTTPStatusError = problem.Forbidden()
	//	fmt.Println(failure.HTTPStatus()) // 403
	HTTPStatus() int
}

// HTTPHandlerError is an error that renders its own response. A handler that
// returns one has it served directly instead of a generated problem. Discovery
// follows the error chain; rendering runs only before the response is committed.
// The implementation owns content negotiation and client-safe output.
//
// Example:
//
//	var failure router.HTTPHandlerError = problem.NotFound()
//	response := httptest.NewRecorder()
//	failure.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users/7", nil))
//	fmt.Println(response.Code) // 404
type HTTPHandlerError interface {
	error
	http.Handler
}

// renderedRecoveryError marks a recovered failure that native middleware
// already rendered while the response was still unwritten. Without this
// distinction, ServeHTTP would mistake the generated error response itself for
// output committed before the panic and unnecessarily abort the connection.
type renderedRecoveryError struct {
	// error retains the entire error chain, including middleware mappings and
	// the recovery stack reachable through errors.As.
	error
}

// Unwrap preserves error identity and diagnostics after native error rendering.
// It returns the original chain, not a redacted or reconstructed failure, so
// errors.Is and errors.As remain the caller's classification interface.
//
// Example:
//
//	handler := router.HTTPMiddleware(func(next http.Handler) http.Handler {
//		return next
//	})(router.Recover()(func(http.ResponseWriter, *http.Request) error {
//		panic("example")
//	}))
//	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
//	fmt.Println(errors.Is(errors.Unwrap(err), router.ErrRecoverUnexpected)) // true
func (rendered renderedRecoveryError) Unwrap() error {
	return rendered.error
}

// StatusClientClosedRequest is the non-standard HTTP status code used
// when the request context ends before an error response is rendered. It is
// useful for server-side classification; a disconnected client may receive no
// response at all.
//
// Example:
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Status(router.StatusClientClosedRequest)
//	})
//	fmt.Println(failure.HTTPStatus()) // 499
const StatusClientClosedRequest = 499

// withResponseContext attaches a response context when ctx has none.
func withResponseContext(ctx context.Context) context.Context {
	if _, ok := problem.FromContext(ctx); ok {
		return ctx
	}

	return problem.NewContext(ctx, problem.NewResponseContext())
}

// ServeHTTP implements [http.Handler], running lifecycle hooks and rendering
// errors. After-response hooks run even when the handler panics. A recovered
// panic after the response started aborts the connection, so the client sees
// a failed response instead of a truncated one that looks complete.
// A recovery response rendered by HTTPMiddleware before any prior output is
// complete error handling, not evidence that output preceded the panic.
//
// Example:
//
//	handler := router.Handler(func(writer http.ResponseWriter, request *http.Request) error {
//		writer.WriteHeader(http.StatusAccepted)
//		return nil
//	})
//	response := httptest.NewRecorder()
//	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/jobs", nil))
//	fmt.Println(response.Code) // 202
func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	registry := hook.New()
	wrapped := hook.NewResponseWriter(writer, registry)
	request = request.WithContext(withResponseContext(hook.NewContext(request.Context(), registry)))

	var err error
	defer func() {
		registry.RunAfterResponse(err)
	}()

	err = handler(wrapped, request)

	// An older rendered error can be a new panic's cause. Only the exact
	// recovery rendered by the adapter is exempt from the committed abort.
	failure, _ := errors.AsType[*recoverError](err)
	rendered, _ := errors.AsType[renderedRecoveryError](err)
	completed, _ := errors.AsType[*recoverError](rendered.error)
	abort := failure != nil && failure != completed

	if wrapped.WriteHeaderCalled() && abort {
		panic(http.ErrAbortHandler)
	}

	if err != nil && !wrapped.WriteHeaderCalled() {
		handleError(wrapped, request, err)
	}

	if !wrapped.WriteHeaderCalled() {
		wrapped.WriteHeader(http.StatusNoContent)
	}
}

// Record runs the full handler lifecycle against an in-memory recorder and
// returns its HTTP response. No server or network is used. The caller owns the
// returned response body; request handling and panic behavior match ServeHTTP.
//
// Example:
//
//	handler := router.HTTP(http.NotFoundHandler())
//	response := handler.Record(httptest.NewRequest(http.MethodGet, "/missing", nil))
//	defer response.Body.Close()
//	fmt.Println(response.StatusCode) // 404
func (handler Handler) Record(request *http.Request) *http.Response {
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	return recorder.Result()
}
