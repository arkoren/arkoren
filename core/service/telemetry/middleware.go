package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/internal/hook"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry/internal/httptrack"
	"arkoren.dev/core/service/telemetry/internal/lazy"
	"arkoren.dev/foundation/problem"
)

// httpScope is the instrumentation scope of the HTTP server and client
// instrumentation.
const httpScope = "arkoren.dev/core/service/telemetry"

// DefaultTraceResponseHeader is the W3C Trace Context response header that
// [Middleware] sets by default.
// Read it on an HTTP response to correlate client reports with a server span.
//
//	value := response.Header.Get(telemetry.DefaultTraceResponseHeader)
//	fmt.Println(value)
const DefaultTraceResponseHeader = "traceresponse"

// DefaultProblemKey is the problem extension member that [Middleware] adds to
// error responses by default.
// It names the extension member, not the problem type or the response header.
//
//	var body map[string]any
//	if err := json.Unmarshal(responseBody, &body); err != nil {
//		return err
//	}
//	fmt.Println(body[telemetry.DefaultProblemKey])
const DefaultProblemKey = "trace_id"

// httpDurationBuckets are the default explicit histogram bounds, in seconds,
// recommended by the HTTP semantic conventions.
var httpDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

// TrustAll is a [MiddlewareBuilder.TrustRemote] predicate that trusts every
// caller. Use it inside a service mesh or private network where only
// trusted workloads can reach the server.
//
// Example:
//
//	r := router.New()
//	r.Use(telemetry.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
//		middleware.TrustRemote(telemetry.TrustAll)
//	}))
func TrustAll(*http.Request) bool {
	return true
}

// TrustNone is a [MiddlewareBuilder.TrustRemote] predicate that trusts no
// caller, so every request starts a new root trace. It is the default,
// including when TrustRemote is nil, and is suitable for a public edge.
//
// Example:
//
//	r := router.New()
//	r.Use(telemetry.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
//		middleware.TrustRemote(telemetry.TrustNone)
//	}))
func TrustNone(*http.Request) bool {
	return false
}

// TrustInternal is an opt-in [MiddlewareBuilder.TrustRemote] predicate. It
// trusts a request whose r.RemoteAddr is a loopback, private (RFC 1918 or
// IPv6 unique local fc00::/7), or link-local unicast address, and rejects
// public and unparseable addresses. It never reads X-Forwarded-For or
// similar headers, which the caller controls.
//
// Behind a load balancer or reverse proxy that connects from a private
// address every request looks trusted, even when browser-supplied trace
// headers pass through unchanged. A private proxy address does not establish
// who supplied the headers. Keep [TrustNone] unless the proxy strips untrusted
// context and only forwards context from trusted workloads; restricting proxy
// addresses with [MiddlewareBuilder.TrustedNetworks] alone is not sufficient.
//
// Example:
//
//	r := router.New()
//	r.Use(telemetry.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
//		middleware.TrustRemote(telemetry.TrustInternal)
//	}))
func TrustInternal(r *http.Request) bool {
	addr, ok := httptrack.RemoteIP(r)

	return ok && (addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast())
}

// httpServer holds the instruments of the server middleware for one
// [Telemetry].
type httpServer struct {
	// telemetry owns the signals.
	telemetry *Telemetry

	// config is the middleware configuration.
	config middlewareSettings

	// tracer starts server spans.
	tracer Tracer

	// duration records http.server.request.duration.
	duration Histogram

	// active records http.server.active_requests.
	active UpDownCounter
}

// newHTTPServer creates the server instrumentation for telemetry.
func newHTTPServer(telemetry *Telemetry, config middlewareSettings) *httpServer {
	meter := telemetry.Meter(httpScope)

	return &httpServer{
		telemetry: telemetry,
		config:    config,
		tracer:    telemetry.Tracer(httpScope),
		duration: meter.Histogram(semconv.HTTPServerRequestDuration,
			Unit("s"), Description("Duration of HTTP server requests."),
			Keys(semconv.HTTPRequestMethod, semconv.HTTPRoute, semconv.HTTPResponseStatusCode,
				semconv.URLScheme, semconv.ErrorType),
			Buckets(httpDurationBuckets...)),
		active: meter.UpDownCounter(semconv.HTTPServerActiveRequests,
			Unit("{request}"), Description("Number of in-flight HTTP server requests."),
			Keys(semconv.HTTPRequestMethod, semconv.URLScheme)),
	}
}

// Middleware returns middleware that traces and measures requests with this instance.
//
// It extracts the remote parent through the driver's propagator, but only
// from callers that [MiddlewareBuilder.TrustRemote] trusts. The default,
// including nil, is [TrustNone]: all requests start a new root span, so a
// caller cannot choose the trace ID, force sampling, or smuggle baggage into
// the application's outgoing calls through incoming headers. This replaces
// the former [TrustInternal] default. Opt in to [TrustInternal],
// [MiddlewareBuilder.TrustedNetworks], or [TrustAll] only for trusted workloads.
// A private proxy can forward browser-supplied context unchanged; trusting its
// address does not authenticate that context. Keep [TrustNone] at a public
// edge unless the proxy strips untrusted context. X-Forwarded-For is never
// consulted. Outgoing propagation policy is independent and unchanged.
//
// The middleware then starts a server span named "<METHOD> <route>" (the
// method alone when the request matched no route). Unknown methods use "HTTP"
// in the name and "_OTHER" in http.request.method, as required by the HTTP span
// naming conventions. It records
// http.server.request.duration and http.server.active_requests. The span and metric finish after Arkoren has
// rendered returned errors, and also when a panic is recovered, so the status
// is the one sent to the client. Only 5xx responses mark the span as an error,
// with the description "HTTP <code>" and error.type set to the status code.
//
// Only low-cardinality method, scheme, protocol, route, and status values are
// recorded. URLs, query strings, headers, bodies, and error text never are.
//
// When the span context is valid the middleware sets the W3C traceresponse
// header (see [MiddlewareBuilder.TraceResponse]), the optional [MiddlewareBuilder.TraceHeader] header, and
// the [MiddlewareBuilder.ProblemKey] property on problem responses. With a no-op or
// not-yet-started telemetry the middleware passes requests straight through
// and sets nothing.
//
// Register it before other middleware, including router.Recover, so it
// observes the complete request. Enabled instrumentation requires the router's
// lifecycle hooks: invoking the wrapped handler directly without that context
// panics before starting a span or recording an active request.
//
// Example:
//
//	t := app.Telemetry()
//	r.Use(t.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
//		middleware.TraceHeader("X-Trace-ID")
//	}))
func (telemetry *Telemetry) Middleware(configure ...MiddlewareBuilderFunc) router.Middleware {
	return serverMiddleware(newServerCache(configure), func() *Telemetry {
		return telemetry
	})
}

// Middleware is [Telemetry.Middleware] for the [Default] telemetry, resolved
// on every request, so it may be registered before [SetDefault] runs.
//
// Example:
//
//	r.Use(telemetry.Middleware())
func Middleware(configure ...MiddlewareBuilderFunc) router.Middleware {
	return serverMiddleware(newServerCache(configure), Default)
}

// newServerCache returns a cache that builds the server instrumentation with
// the completed construction settings describe.
func newServerCache(configure []MiddlewareBuilderFunc) *lazy.Cache[*Telemetry, httpServer] {
	config := newMiddlewareSettings(configure...)

	cache := lazy.NewCache(func(telemetry *Telemetry) *httpServer {
		return newHTTPServer(telemetry, config)
	})

	return &cache
}

// serverMiddleware adapts the server instrumentation for the telemetry that
// resolve returns at request time.
func serverMiddleware(cache *lazy.Cache[*Telemetry, httpServer], resolve func() *Telemetry) router.Middleware {
	return func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			server := cache.Get(resolve())

			if !server.telemetry.active.Load() {
				return next(w, r)
			}

			return server.serve(w, r, next)
		}
	}
}

// serve runs next inside a server span and records the request metrics.
func (server *httpServer) serve(w http.ResponseWriter, r *http.Request, next router.Handler) error {
	hooks, ok := hook.FromContext(r.Context())

	if !ok {
		panic(hook.ErrNoHooks)
	}

	start := time.Now()
	method := httptrack.Method(r.Method)
	scheme := httptrack.Scheme(r)
	route := httptrack.Route(r)

	ctx := r.Context()

	if server.config.TrustRemote(r) {
		ctx = server.telemetry.Extract(ctx, HeaderCarrier(r.Header))
	}

	attrs := []Attr{
		String(semconv.HTTPRequestMethod, method),
		String(semconv.URLScheme, scheme),
		String(semconv.NetworkProtocolName, "http"),
		String(semconv.NetworkProtocolVersion, httptrack.ProtocolVersion(r.Proto)),
	}

	if route != "" {
		attrs = append(attrs, String(semconv.HTTPRoute, route))
	}

	ctx, span := server.tracer.Start(ctx, httptrack.SpanName(method, route), Kind(Server), Attrs(attrs...))
	ctx = server.correlate(ctx, w, span)

	activeAttrs := []Attr{String(semconv.HTTPRequestMethod, method), String(semconv.URLScheme, scheme)}
	server.active.Add(ctx, 1, activeAttrs...)

	status := http.StatusOK
	hooks.BeforeWriteHeader(func(_ http.ResponseWriter, value int) {
		status = value
	})
	hooks.AfterResponse(func(_ error) {
		server.active.Add(ctx, -1, activeAttrs...)
		server.finish(ctx, span, serverResult{
			method: method,
			route:  route,
			scheme: scheme,
			status: status,
			start:  start,
		})
	})

	return next(w, r.WithContext(ctx))
}

// serverResult is the outcome of one server request.
type serverResult struct {
	// method is the normalised request method.
	method string

	// route is the matched route pattern, empty when none matched.
	route string

	// scheme is the request scheme.
	scheme string

	// status is the response status code.
	status int

	// start is when the request began.
	start time.Time
}

// finish records the response status on span, ends it, and records the
// duration.
func (server *httpServer) finish(ctx context.Context, span *Span, result serverResult) {
	statusAttr := Int(semconv.HTTPResponseStatusCode, result.status)
	attrs := []Attr{
		String(semconv.HTTPRequestMethod, result.method),
		String(semconv.HTTPRoute, result.route),
		statusAttr,
		String(semconv.URLScheme, result.scheme),
	}
	span.SetAttributes(statusAttr)

	if result.status >= http.StatusInternalServerError {
		errorType := String(semconv.ErrorType, strconv.Itoa(result.status))
		span.SetAttributes(errorType)
		span.SetStatus(Error, fmt.Sprintf("HTTP %d", result.status))

		attrs = append(attrs, errorType)
	}

	span.End()
	server.duration.Record(ctx, time.Since(result.start).Seconds(), attrs...)
}

// correlate exposes the trace ID on the response and on problem responses
// when span has a valid context. It returns the context to pass on.
func (server *httpServer) correlate(ctx context.Context, w http.ResponseWriter, span *Span) context.Context {
	spanContext := span.Context()

	if !spanContext.IsValid() {
		return ctx
	}

	traceID := spanContext.TraceID.String()

	if server.config.TraceResponse {
		flags := "00"

		if spanContext.Sampled {
			flags = "01"
		}

		w.Header().Set(DefaultTraceResponseHeader, "00-"+traceID+"-"+spanContext.SpanID.String()+"-"+flags)
	}

	if server.config.TraceHeader != "" {
		w.Header().Set(server.config.TraceHeader, traceID)
	}

	if server.config.ProblemKey == "" {
		return ctx
	}

	responseContext, ok := problem.FromContext(ctx)

	if !ok {
		responseContext = problem.NewResponseContext()
		ctx = problem.NewContext(ctx, responseContext)
	}

	responseContext.SetProperty(server.config.ProblemKey, traceID)

	return ctx
}
