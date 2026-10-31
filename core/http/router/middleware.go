package router

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"arkoren.dev/core/internal/hook"
)

// httpMiddlewareKey associates one native middleware invocation with its error
// result. Nested adapters read their own result before invoking the next layer.
type httpMiddlewareKey struct{}

// httpMiddlewareResult keeps errors request-local even when a native middleware
// runs next on another goroutine, as [http.TimeoutHandler] does.
type httpMiddlewareResult struct {
	// mutex protects err while the native middleware and next run concurrently.
	mutex sync.Mutex

	// err is the last error returned by next for this invocation.
	err error

	// source tracks writes by native middleware before or outside its call to
	// next, even when it passes next a wrapper that hides Unwrap.
	source *hook.ResponseWriter

	// parent retains outer native invocation state when an opaque wrapper hides
	// its writer. It is nil for the outermost adapter and immutable after setup.
	parent *httpMiddlewareResult
}

// responseCommitted checks every enclosing native adapter's source writer.
// Looking only through Unwrap misses outer writes hidden by opaque middleware;
// the request-local invocation chain retains those sources independently.
func (result *httpMiddlewareResult) responseCommitted() bool {
	for current := result; current != nil; current = current.parent {
		if current.source.WriteHeaderCalled() {
			return true
		}
	}

	return false
}

// HTTPMiddleware adapts standard HTTP middleware to [Middleware]. The native
// constructor runs once when the adapter wraps next, normally during route
// registration. Its handler and captured state are shared across requests, so
// they must support concurrent use, just as with net/http.
//
// Errors from next are rendered inside the wrapped middleware, so it observes
// the real status and body, and are still returned for outer middleware and
// hooks. Recovered errors rendered before any prior response writes are wrapped
// to distinguish them from panics after commitment; errors.Is and errors.As
// preserve their identities and diagnostics. Native middleware must preserve
// the request context when calling next: it carries lifecycle hooks and the
// request-local error result.
// Rendering is skipped when next wrote a final response, or an enclosing native
// boundary observed commitment, including through opaque wrappers. Output
// buffered privately by native middleware before calling next is not observable
// until that middleware forwards it through a tracked boundary.
//
// Example:
//
//	routes := router.New()
//	routes.Use(router.HTTPMiddleware(func(next http.Handler) http.Handler {
//		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
//			writer.Header().Set("X-Service", "example")
//			next.ServeHTTP(writer, request)
//		})
//	}))
func HTTPMiddleware(middleware func(http.Handler) http.Handler) Middleware {
	return func(next Handler) Handler {
		wrapped := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			result := r.Context().Value(httpMiddlewareKey{}).(*httpMiddlewareResult)
			tracked := hook.NewResponseWriter(w, hook.New())

			err := next(tracked, r)
			committed := tracked.WriteHeaderCalled() || result.responseCommitted()

			if err != nil && !committed {
				handleError(tracked, r, err)

				if _, ok := errors.AsType[*recoverError](err); ok {
					err = renderedRecoveryError{error: err}
				}
			}

			result.mutex.Lock()

			result.err = err
			result.mutex.Unlock()
		}))

		return func(w http.ResponseWriter, r *http.Request) error {
			source := hook.Lookup(w)

			if source == nil {
				source = hook.NewResponseWriter(w, hook.New())
				w = source
			}

			parent, _ := r.Context().Value(httpMiddlewareKey{}).(*httpMiddlewareResult)
			result := &httpMiddlewareResult{source: source, parent: parent}
			ctx := context.WithValue(r.Context(), httpMiddlewareKey{}, result)

			wrapped.ServeHTTP(w, r.WithContext(ctx))

			result.mutex.Lock()
			defer result.mutex.Unlock()

			return result.err
		}
	}
}

// ProvideFunc resolves a context for the current request. Successful contexts
// must derive from r.Context() to retain cancellation, lifecycle hooks, and
// upstream middleware state. An error stops the chain without calling next.
//
// Example:
//
//	type valueKey struct{}
//	var resolve router.ProvideFunc = func(w http.ResponseWriter, r *http.Request) (context.Context, error) {
//		return context.WithValue(r.Context(), valueKey{}, "value"), nil
//	}
//	routes := router.New()
//	routes.Use(router.Provide(resolve))
type ProvideFunc = func(w http.ResponseWriter, r *http.Request) (context.Context, error)

// ProvideStatic returns middleware that adds a static value to the request
// context while preserving upstream values and cancellation. The key and value
// are retained, not copied; mutable values must be safe for concurrent requests.
//
// Example:
//
//	type serviceKey struct{}
//	routes := router.New()
//	routes.Use(router.ProvideStatic(serviceKey{}, "billing"))
func ProvideStatic(key any, val any) Middleware {
	return Provide(func(w http.ResponseWriter, r *http.Request) (context.Context, error) {
		return context.WithValue(r.Context(), key, val), nil
	})
}

// Provide resolves a context for each request and passes it to next. The
// resolver must derive it from the supplied request context so lifecycle hooks
// and upstream middleware state remain available. Resolver errors are returned
// without calling next; functions and their captured values remain shared.
//
// Example:
//
//	type valueKey struct{}
//	routes := router.New()
//	routes.Use(router.Provide(func(w http.ResponseWriter, r *http.Request) (context.Context, error) {
//		return context.WithValue(r.Context(), valueKey{}, "value"), nil
//	}))
func Provide(f ProvideFunc) Middleware {
	return func(next Handler) Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			ctx, err := f(w, r)

			if err != nil {
				return err
			}

			return next(w, r.WithContext(ctx))
		}
	}
}
