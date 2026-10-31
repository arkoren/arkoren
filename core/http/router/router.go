package router

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"

	"arkoren.dev/foundation/problem"
)

// Middleware constructs a handler around next. Registered routes capture the
// chain once and share it across requests and explicit methods; unmatched
// responses use their own lazily constructed chain. Request-specific state
// belongs inside the returned handler, not in a shared mutable capture.
// Forwarded requests must preserve context values used by hooks and adapters.
//
// Example:
//
//	var header router.Middleware = func(next router.Handler) router.Handler {
//		return func(writer http.ResponseWriter, request *http.Request) error {
//			writer.Header().Set("X-Service", "example")
//			return next(writer, request)
//		}
//	}
//	routes := router.New()
//	routes.Use(header)
type Middleware = func(Handler) Handler

// Router routes Arkoren [Handler] values, wrapping [http.ServeMux] with
// middleware, route groups, and automatic trailing-slash handling.
//
// A Router is not safe for concurrent use during route registration.
// All routes and middleware must be registered before calling ServeHTTP
// or passing the router to an HTTP server.
// Do not copy a Router after first use; use [Router.Clone] for an independent
// middleware scope sharing the same multiplexer.
//
// Example:
//
//	routes := router.New()
//	routes.Get("/health", func(writer http.ResponseWriter, request *http.Request) error {
//		writer.WriteHeader(http.StatusNoContent)
//		return nil
//	})
//	response := routes.Record(httptest.NewRequest(http.MethodGet, "/health", nil))
//	fmt.Println(response.StatusCode) // 204
type Router struct {
	// native stores the actual [http.ServeMux] that's used internally to
	// register the routes.
	native *http.ServeMux

	// pattern stores the current pattern that will be used as a prefix to all
	// route registrations on this router.
	pattern string

	// parent stores the parent Router if any. Sub-routers use it to resolve
	// the shared [http.ServeMux].
	parent *Router

	// middlewares stores the middlewares applied to route registrations on this
	// router, including inherited parent middleware.
	middlewares []Middleware

	// patterns contains every native pattern registered on the root router.
	patterns []string

	// routes contains every route the application registered on the tree, in
	// registration order, kept on the root router.
	routes []routeRecord

	// sealed reports that a route was registered through this router or one
	// derived from it. [Router.Use] panics once it is set, because the routes
	// already registered captured the middleware stack as it was.
	sealed bool

	// unmatchedOnce publishes one middleware chain for native 404/405 responses
	// after registration is complete, without mutating it on later requests.
	unmatchedOnce sync.Once

	// unmatchedHandler shares fallback middleware state across unmatched paths
	// and methods. unmatchedOnce initializes it before any invocation.
	unmatchedHandler Handler
}

// unmatchedKey carries a captured native 404/405 response through the fallback
// middleware chain. The response remains local to one request.
type unmatchedKey struct{}

// allMethods stores the HTTP methods registered by [Router.Any].
// TRACE and CONNECT are intentionally excluded: TRACE enables
// cross-site tracing (XST) attacks that can leak credentials,
// and CONNECT is intended for HTTP proxies only.
var allMethods = []string{
	http.MethodGet,
	http.MethodHead,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
	http.MethodOptions,
	"QUERY",
}

// New creates a new [Router] with an empty [http.ServeMux]. Unmatched
// routes are rendered as RFC 9457 Problem Details responses. It installs no
// application middleware or routes; complete registration before serving.
//
// Example:
//
//	routes := router.New()
//	routes.Get("/health", func(http.ResponseWriter, *http.Request) error {
//		return nil
//	})
//	fmt.Println(routes.Has(http.MethodGet, "/health")) // true
func New() *Router {
	return &Router{
		native:      http.NewServeMux(),
		middlewares: make([]Middleware, 0),
	}
}

// Group uses the given pattern to automatically mount a sub-router that has
// that pattern as a prefix. See [Router.Method] for path normalization rules.
// It inherits the current middleware stack and shares the root route registry;
// the callback runs synchronously during registration.
//
// Example:
//
//	routes := router.New()
//	routes.Group("/users", func(users *router.Router) {
//		users.Get("/{id}", router.HTTP(http.NotFoundHandler()))
//	})
//	fmt.Println(routes.Has(http.MethodGet, "/users/7")) // true
func (router *Router) Group(pattern string, subrouter func(*Router)) {
	subrouter(&Router{
		native:      nil, // parent's native will be used
		pattern:     path.Join(router.pattern, pattern),
		parent:      router,
		middlewares: slices.Clone(router.middlewares),
	})
}

// Grouped creates a cloned sub-router for scoping middleware without a path
// prefix. The callback runs synchronously; its routes join the shared registry
// but its middleware edits do not change the parent stack.
//
// Example:
//
//	routes := router.New()
//	routes.Grouped(func(public *router.Router) {
//		public.Use(router.Recover())
//		public.Get("/health", router.HTTP(http.NotFoundHandler()))
//	})
func (router *Router) Grouped(subrouter func(*Router)) {
	subrouter(router.Clone())
}

// Clone creates a sub-router that shares the same [http.ServeMux] but has an
// independent copy of the current middleware stack and the same path prefix.
// Handler functions and their captures remain shared. Registering through the
// clone also seals ancestor middleware configuration.
//
// Example:
//
//	routes := router.New()
//	public := routes.Clone()
//	public.Use(router.Recover())
//	public.Get("/health", router.HTTP(http.NotFoundHandler()))
func (router *Router) Clone() *Router {
	return &Router{
		native:      nil, // parent's native will be used
		pattern:     router.pattern,
		parent:      router,
		middlewares: slices.Clone(router.middlewares),
	}
}

// With creates a new sub-router that applies the given middlewares in addition
// to any inherited ones. In contrast to [Router.Use], it does not modify the
// current router. It retains middleware functions, not copies of their captures;
// route registration and the path prefix are shared as with Clone.
//
// Example:
//
//	routes := router.New()
//	routes.With(router.Recover()).Get("/health", router.HTTP(http.NotFoundHandler()))
func (router *Router) With(middlewares ...Middleware) *Router {
	return &Router{
		native:      nil, // parent's native will be used
		pattern:     router.pattern,
		parent:      router,
		middlewares: append(slices.Clone(router.middlewares), middlewares...),
	}
}

// mux returns the root router's shared HTTP multiplexer.
func (router *Router) mux() *http.ServeMux {
	if router.parent != nil {
		return router.parent.mux()
	}

	return router.native
}

// wrap applies the router's middleware in registration order.
func (router *Router) wrap(handler Handler) Handler {
	for _, middleware := range slices.Backward(router.middlewares) {
		handler = middleware(handler)
	}

	return handler
}

// Use appends to the current router the given middlewares. Subsequent route
// registrations are wrapped with both existing and newly registered middleware.
//
// Use panics when a route was already registered through this router or any
// router derived from it ([Router.Group], [Router.Grouped], [Router.Clone],
// [Router.With]), because those routes were wrapped with the middleware as it
// was and would silently skip the new one, for example an authentication
// check. Call Use before registering routes. A sub-router created before its
// parent registered routes may still call Use as long as it has registered
// nothing itself.
//
// Example:
//
//	r.Use(auth.Middleware(authenticator), auth.Require[User]()) // before the routes it protects
//	r.Get("/me", me)
func (router *Router) Use(middlewares ...Middleware) {
	if router.sealed {
		panic("router: middlewares must be defined before routes")
	}

	router.middlewares = append(router.middlewares, middlewares...)
}

// register registers an already wrapped handler for method and pattern.
func (router *Router) register(method string, pattern string, handler Handler) {
	nativePattern := fmt.Sprintf("%s %s", method, pattern)
	router.mux().Handle(nativePattern, handler)

	root := router.root()
	root.patterns = append(root.patterns, nativePattern)
}

// registered reports whether method and pattern are already registered.
func (router *Router) registered(method string, pattern string) bool {
	return slices.Contains(router.root().patterns, fmt.Sprintf("%s %s", method, pattern))
}

// root returns the root router that owns the native multiplexer.
func (router *Router) root() *Router {
	if router.parent == nil {
		return router
	}

	return router.parent.root()
}

// registerRoot registers handler for the root pattern.
func (router *Router) registerRoot(method string, handler Handler) {
	router.register(method, "/{$}", handler)
}

// registerTrailing registers handler for a trailing-slash pattern.
func (router *Router) registerTrailing(method, pattern string, handler Handler) {
	pattern = fmt.Sprintf("%s/{$}", pattern)
	router.register(method, pattern, handler)
}

// registerPair registers handler for both trailing-slash forms. A catch-all
// route also serves its parent path, unless that path already has a route;
// register such a parent route before its catch-all.
func (router *Router) registerPair(method, pattern string, handler Handler) {
	router.register(method, pattern, handler)

	if strings.HasSuffix(pattern, "...}") {
		segments := strings.Split(pattern, "/")
		parent := strings.Join(segments[:len(segments)-1], "/")

		if len(segments) > 2 && !router.registered(method, parent) {
			router.register(method, parent, handler)
		}

		return
	}

	router.registerTrailing(method, pattern, handler)
}

// Method registers a new handler to the router with the given method and
// pattern. Non-root routes match both trailing-slash forms.
// Metadata callbacks follow [Router.Methods]; they never affect dispatch.
//
// Example:
//
//	routes.Method("COPY", "/users/{id}", copyUser, func(operation *router.OperationBuilder) {
//		operation.Summary("Copy a user")
//	})
func (router *Router) Method(method string, pattern string, handler Handler, configure ...OperationBuilderFunc) {
	router.Methods([]string{method}, pattern, handler, configure...)
}

// Methods registers a handler for each method in the given slice. The
// middleware chain is built once and shared by every registration.
// Optional descriptive callbacks run directly once, synchronously in order
// on one fresh draft, even when metadata is invalid. Nil entries panic;
// expanded nil or empty slices omit metadata. Publication detaches owned
// containers and rendered schema bytes, not handler functions or captures.
// Errors in metadata are reported only by [Router.OpenAPI]. Each explicit
// method receives the completed metadata; a shared ID will therefore fail
// export's uniqueness check. No implicit trailing-slash or parent-path routes
// are described. Routes with omitted callbacks are excluded from export.
//
// Example:
//
//	routes.Methods([]string{"GET", "HEAD"}, "/users", list, func(operation *router.OperationBuilder) {
//		operation.Summary("List users")
//	})
func (router *Router) Methods(methods []string, pattern string, handler Handler, configure ...OperationBuilderFunc) {
	// The handler is described before wrapping so Routes names the
	// application's function, not a middleware closure.
	name, source := describe(handler)
	record := routeRecord{Route: Route{Handler: name, Source: source}, operation: newOperation(configure)}
	router.methods(methods, pattern, handler, record)
}

// methods registers handler for each method after wrapping it with the
// router's middleware. record describes the handler for
// [Router.Routes]; callers that adapt another handler pass the description of
// the original, so a route lists the application's function and not the
// adapter.
func (router *Router) methods(methods []string, pattern string, handler Handler, record routeRecord) {
	if invalidRoutePattern(pattern) {
		panic("router: pattern must not contain '..' segments")
	}

	for sealed := router; sealed != nil; sealed = sealed.parent {
		sealed.sealed = true
	}

	handler = router.wrap(handler)
	pattern = path.Join(router.pattern, pattern)

	for _, method := range methods {
		if method == "" {
			panic("router: method must not be empty")
		}

		root := router.root()
		record.Method = method
		record.Pattern = pattern
		root.routes = append(root.routes, record)

		if pattern == "/" {
			router.registerRoot(method, handler)

			continue
		}

		router.registerPair(method, pattern, handler)
	}
}

// invalidRoutePattern detects parent-directory path segments that route
// registration must reject. Ordinary dotted segment names remain valid.
func invalidRoutePattern(pattern string) bool {
	return strings.Contains(pattern, "/../") || strings.HasPrefix(pattern, "../") || strings.HasSuffix(pattern, "/..") || pattern == ".."
}

// Any registers a handler for all standard HTTP methods except TRACE and CONNECT.
// Metadata callbacks run once, not once per method; see [Router.Methods].
//
// Example:
//
//	routes.Any("/health", health, func(operation *router.OperationBuilder) {
//		operation.Response(200, "Healthy")
//	})
func (router *Router) Any(pattern string, handler Handler, configure ...OperationBuilderFunc) {
	router.Methods(allMethods, pattern, handler, configure...)
}

// Query registers a handler for QUERY using [Router.Method].
//
// Example:
//
//	routes.Query("/users", search, func(operation *router.OperationBuilder) {
//		operation.Summary("Search users")
//	})
func (router *Router) Query(pattern string, handler Handler, configure ...OperationBuilderFunc) {
	router.Method("QUERY", pattern, handler, configure...)
}

// Get registers a handler for [http.MethodGet] using [Router.Method].
//
// Example:
//
//	routes.Get("/users", list, func(operation *router.OperationBuilder) {
//		operation.Summary("List users")
//	})
func (router *Router) Get(pattern string, handler Handler, configure ...OperationBuilderFunc) {
	router.Method(http.MethodGet, pattern, handler, configure...)
}

// Head registers a handler for [http.MethodHead] using [Router.Method].
//
// Example:
//
//	routes.Head("/users", headers)
func (router *Router) Head(pattern string, handler Handler, configure ...OperationBuilderFunc) {
	router.Method(http.MethodHead, pattern, handler, configure...)
}

// Post registers a handler for [http.MethodPost] using [Router.Method].
//
// Example:
//
//	routes.Post("/users", create)
func (router *Router) Post(pattern string, handler Handler, configure ...OperationBuilderFunc) {
	router.Method(http.MethodPost, pattern, handler, configure...)
}

// Put registers a handler for [http.MethodPut] using [Router.Method].
//
// Example:
//
//	routes.Put("/users/{id}", replace)
func (router *Router) Put(pattern string, handler Handler, configure ...OperationBuilderFunc) {
	router.Method(http.MethodPut, pattern, handler, configure...)
}

// Patch registers a handler for [http.MethodPatch] using [Router.Method].
//
// Example:
//
//	routes.Patch("/users/{id}", update)
func (router *Router) Patch(pattern string, handler Handler, configure ...OperationBuilderFunc) {
	router.Method(http.MethodPatch, pattern, handler, configure...)
}

// Delete registers a handler for [http.MethodDelete] using [Router.Method].
//
// Example:
//
//	routes.Delete("/users/{id}", remove)
func (router *Router) Delete(pattern string, handler Handler, configure ...OperationBuilderFunc) {
	router.Method(http.MethodDelete, pattern, handler, configure...)
}

// Connect registers a handler for [http.MethodConnect] using [Router.Method].
//
// Example:
//
//	routes.Connect("/tunnel", connect)
func (router *Router) Connect(pattern string, handler Handler, configure ...OperationBuilderFunc) {
	router.Method(http.MethodConnect, pattern, handler, configure...)
}

// Options registers a handler for [http.MethodOptions] using [Router.Method].
//
// Example:
//
//	routes.Options("/users", options)
func (router *Router) Options(pattern string, handler Handler, configure ...OperationBuilderFunc) {
	router.Method(http.MethodOptions, pattern, handler, configure...)
}

// Trace registers a handler for [http.MethodTrace] using [Router.Method].
//
// Example:
//
//	routes.Trace("/diagnostics", trace)
func (router *Router) Trace(pattern string, handler Handler, configure ...OperationBuilderFunc) {
	router.Method(http.MethodTrace, pattern, handler, configure...)
}

// ServeHTTP implements [http.Handler]. It delegates matched routes to the
// underlying [http.ServeMux] and renders unmatched 404 and 405 responses as
// Problem Details through the router's middleware, so middleware such as
// CORS also answers requests that match no route.
// The fallback middleware chain is constructed on the first unmatched request
// and shared by subsequent unmatched requests. Configure middleware before
// serving requests, including on routers with no registered routes.
// Middleware forwarding a modified request must preserve its context values,
// which carry lifecycle hooks and the captured native fallback response.
//
// Example:
//
//	routes := router.New()
//	response := httptest.NewRecorder()
//	routes.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))
//	fmt.Println(response.Code) // 404
func (router *Router) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	response := router.unmatchedResponse(request)

	if response == nil {
		router.mux().ServeHTTP(writer, request)

		return
	}

	router.unmatchedOnce.Do(func() {
		router.unmatchedHandler = router.wrap(unmatched)
	})

	ctx := context.WithValue(request.Context(), unmatchedKey{}, response)

	router.unmatchedHandler.ServeHTTP(writer, request.WithContext(ctx))
}

// unmatchedResponse captures only ServeMux-generated 404 and 405 responses for
// later problem rendering through router middleware. Matched routes and other
// native mux responses return nil.
func (router *Router) unmatchedResponse(request *http.Request) *httptest.ResponseRecorder {
	mux := router.mux()
	_, pattern := mux.Handler(request)

	if !shouldCheckUnmatchedResponse(pattern, request) {
		return nil
	}

	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if !isProblemResponseStatus(response.Code) {
		return nil
	}

	return response
}

// shouldCheckUnmatchedResponse selects requests that may need an unmatched-
// route problem. The server-wide asterisk target retains its native HTTP
// handling.
func shouldCheckUnmatchedResponse(pattern string, request *http.Request) bool {
	return pattern == "" && request.RequestURI != "*"
}

// isProblemResponseStatus identifies the two mux error statuses that the
// router converts to Problem Details, leaving redirects and successful
// responses untouched.
func isProblemResponseStatus(status int) bool {
	return status == http.StatusNotFound || status == http.StatusMethodNotAllowed
}

// unmatched answers with the request-local ServeMux status captured by
// ServeHTTP, keeping headers such as Allow. It is the terminal handler of the
// shared fallback middleware chain and never serves matched routes.
func unmatched(writer http.ResponseWriter, request *http.Request) error {
	response := request.Context().Value(unmatchedKey{}).(*httptest.ResponseRecorder)

	for header, values := range response.Header() {
		writer.Header()[header] = slices.Clone(values)
	}

	return problem.New(func(details *problem.Builder) {
		details.Error(nil)
		details.Status(response.Code)
	})
}

// Has reports whether method and target match a registered route, including
// trailing-slash variants and net/http method matching. Invalid request targets
// return false; it does not call the matched handler or its middleware.
//
// Example:
//
//	routes := router.New()
//	routes.Get("/users/{id}", router.HTTP(http.NotFoundHandler()))
//	fmt.Println(routes.Has(http.MethodGet, "/users/7")) // true
func (router *Router) Has(method, pattern string) bool {
	request, err := http.NewRequestWithContext(context.Background(), method, pattern, nil)

	return err == nil && router.Matches(request)
}

// Matches reports whether request resolves to an Arkoren handler in the shared
// multiplexer. It does not execute handlers; native redirects or generated
// 404/405 responses are not registered-handler matches.
//
// Example:
//
//	routes := router.New()
//	routes.Get("/users/{id}", router.HTTP(http.NotFoundHandler()))
//	r := httptest.NewRequest(http.MethodGet, "/users/7", nil)
//	fmt.Println(routes.Matches(r)) // true
func (router *Router) Matches(request *http.Request) bool {
	_, ok := router.HandlerMatch(request)

	return ok
}

// Handler returns the wrapped handler matching method and target, or false for
// invalid targets, native responses, and missing routes. It performs lookup
// only; call ServeHTTP on the result to install hooks and render returned errors.
// Lookup does not populate request path values as multiplexer dispatch does.
//
// Example:
//
//	routes := router.New()
//	routes.Get("/health", router.HTTP(http.NotFoundHandler()))
//	if handler, ok := routes.Handler(http.MethodGet, "/health"); ok {
//		recorder := httptest.NewRecorder()
//		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
//		fmt.Println(recorder.Code) // 404
//	}
func (router *Router) Handler(method, pattern string) (handler Handler, ok bool) {
	request, err := http.NewRequestWithContext(context.Background(), method, pattern, nil)

	if err != nil {
		return handler, false
	}

	return router.HandlerMatch(request)
}

// HandlerMatch returns the wrapped Arkoren handler for request, or false for
// a native response or missing route. It neither executes the handler nor sets
// request path values; use Router.ServeHTTP when dispatch needs those values.
//
// Example:
//
//	routes := router.New()
//	routes.Get("/health", router.HTTP(http.NotFoundHandler()))
//	r := httptest.NewRequest(http.MethodGet, "/health", nil)
//	if handler, ok := routes.HandlerMatch(r); ok {
//		recorder := httptest.NewRecorder()
//		handler.ServeHTTP(recorder, r)
//		fmt.Println(recorder.Code) // 404
//	}
func (router *Router) HandlerMatch(request *http.Request) (handler Handler, ok bool) {
	matched, pattern := router.mux().Handler(request)

	if pattern == "" {
		return handler, false
	}

	handler, ok = matched.(Handler)

	return handler, ok
}

// Record returns an [http.Response] produced by dispatching the given HTTP
// request through the router's full middleware and handler pipeline. It uses an
// in-memory recorder, not a network server; the caller owns the response body.
// Panics retain the same behavior as ServeHTTP.
//
// Example:
//
//	routes := router.New()
//	response := routes.Record(httptest.NewRequest(http.MethodGet, "/missing", nil))
//	defer response.Body.Close()
//	fmt.Println(response.StatusCode) // 404
func (router *Router) Record(request *http.Request) *http.Response {
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	return recorder.Result()
}
