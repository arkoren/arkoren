package telemetry

import (
	"net/http"
	"net/netip"
	"slices"

	"arkoren.dev/core/service/telemetry/internal/httptrack"
)

// MiddlewareBuilder configures Middleware before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	r := router.New()
//	r.Use(telemetry.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
//		middleware.TraceHeader("X-Trace-ID")
//	}))
type MiddlewareBuilder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings middlewareSettings
}

// MiddlewareBuilderFunc configures one construction draft before middleware
// publication. Callbacks run synchronously in order; retained drafts do not
// change the published settings.
//
//	var configure telemetry.MiddlewareBuilderFunc = func(middleware *telemetry.MiddlewareBuilder) {
//		middleware.TraceResponse(false)
//	}
//	routes.Use(service.Middleware(configure))
type MiddlewareBuilderFunc = func(*MiddlewareBuilder)

// TraceResponse sets the W3C traceresponse header on responses.
// [Middleware] defaults to true.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(telemetry.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
//		middleware.TraceResponse(false)
//	}))
func (middleware *MiddlewareBuilder) TraceResponse(enabled bool) *MiddlewareBuilder {
	middleware.settings.TraceResponse = enabled

	return middleware
}

// TraceHeader names an extra response header that carries the trace ID,
// such as "X-Trace-ID". Empty sets no extra header.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(telemetry.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
//		middleware.TraceHeader("X-Trace-ID")
//	}))
func (middleware *MiddlewareBuilder) TraceHeader(header string) *MiddlewareBuilder {
	middleware.settings.TraceHeader = header

	return middleware
}

// TrustRemote decides, per request, whether the caller's trace context
// (traceparent, baggage, and whatever else the driver's propagator reads)
// is extracted and used as the parent of the server span. When it
// returns false the request starts a new root span, so a browser or any
// other untrusted client can neither join a trace it does not own nor
// inject baggage into the application's outgoing calls. The default,
// nil, is [TrustNone], even for loopback and private peers. To preserve the
// former internal-peer default, explicitly select [TrustInternal] only when
// those peers supply trusted context. See [TrustAll] and
// [MiddlewareBuilder.TrustedNetworks] for other explicit policies.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(telemetry.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
//		middleware.TrustRemote(nil)
//	}))
func (middleware *MiddlewareBuilder) TrustRemote(trust func(*http.Request) bool) *MiddlewareBuilder {
	middleware.settings.TrustRemote = trust

	return middleware
}

// TrustedNetworks trusts requests whose RemoteAddr lies in one of
// prefixes and no others, replacing the default [TrustNone]. IPv4-mapped IPv6
// addresses are matched as IPv4. With no prefixes nothing is trusted. The
// prefixes are copied. A trusted proxy must strip untrusted incoming context;
// its address alone does not make browser-supplied headers trustworthy.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(telemetry.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
//		middleware.TrustedNetworks(netip.MustParsePrefix("10.0.0.0/8"))
//	}))
func (middleware *MiddlewareBuilder) TrustedNetworks(prefixes ...netip.Prefix) *MiddlewareBuilder {
	prefixes = slices.Clone(prefixes)

	return middleware.TrustRemote(func(r *http.Request) bool {
		addr, ok := httptrack.RemoteIP(r)

		return ok && slices.ContainsFunc(prefixes, func(prefix netip.Prefix) bool {
			return prefix.Contains(addr)
		})
	})
}

// ProblemKey is the RFC 9457 problem extension member that carries the
// trace ID on error responses. Empty disables it; [Middleware]
// defaults to [DefaultProblemKey].
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(telemetry.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
//		middleware.ProblemKey("trace_id")
//	}))
func (middleware *MiddlewareBuilder) ProblemKey(key string) *MiddlewareBuilder {
	middleware.settings.ProblemKey = key

	return middleware
}
