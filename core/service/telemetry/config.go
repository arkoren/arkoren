package telemetry

import "net/http"

// middlewareSettings holds completed server propagation and correlation policy.
type middlewareSettings struct {
	// TraceResponse enables the W3C traceresponse header.
	TraceResponse bool

	// TraceHeader names an optional trace ID response header.
	TraceHeader string

	// ProblemKey names the optional trace ID problem extension.
	ProblemKey string

	// TrustRemote authenticates incoming context before extraction.
	TrustRemote func(*http.Request) bool
}

// transportSettings holds the completed outgoing propagation policy.
type transportSettings struct {
	// Propagate decides whether to inject trace context for each request.
	Propagate func(*http.Request) bool
}

// newMiddlewareSettings applies callbacks directly once in order and publishes
// an independent root. Nil remote trust normalizes to TrustNone after callbacks.
func newMiddlewareSettings(configure ...MiddlewareBuilderFunc) middlewareSettings {
	middleware := &MiddlewareBuilder{
		settings: middlewareSettings{
			TraceResponse: true,
			ProblemKey:    DefaultProblemKey,
		},
	}

	for _, configure := range configure {
		configure(middleware)
	}

	config := middleware.settings

	if config.TrustRemote == nil {
		config.TrustRemote = TrustNone
	}

	return config
}

// newTransportSettings applies callbacks directly once in order and publishes
// an independent root. Request predicates are retained without invocation.
func newTransportSettings(configure ...TransportBuilderFunc) transportSettings {
	transport := &TransportBuilder{}

	for _, configure := range configure {
		configure(transport)
	}

	config := transport.settings

	return config
}
