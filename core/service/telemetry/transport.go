package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"arkoren.dev/core/internal/resend"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/telemetry/internal/httptrack"
	"arkoren.dev/core/service/telemetry/internal/lazy"
)

// httpClient holds the instruments of the client transport for one
// [Telemetry].
type httpClient struct {
	// telemetry owns the signals.
	telemetry *Telemetry

	// tracer starts client spans.
	tracer Tracer

	// duration records http.client.request.duration.
	duration Histogram
}

// newHTTPClient creates the client instrumentation for telemetry.
func newHTTPClient(telemetry *Telemetry) *httpClient {
	return &httpClient{
		telemetry: telemetry,
		tracer:    telemetry.Tracer(httpScope),
		duration: telemetry.Meter(httpScope).Histogram(semconv.HTTPClientRequestDuration,
			Unit("s"), Description("Duration of outgoing HTTP requests."),
			Keys(semconv.HTTPRequestMethod, semconv.ServerAddress, semconv.ServerPort,
				semconv.HTTPResponseStatusCode, semconv.ErrorType),
			Buckets(httpDurationBuckets...)),
	}
}

// transport is the instrumented [http.RoundTripper].
type transport struct {
	// base performs the requests.
	base http.RoundTripper

	// cache holds the client instrumentation for the resolved telemetry.
	cache lazy.Cache[*Telemetry, httpClient]

	// resolve returns the telemetry to instrument with.
	resolve func() *Telemetry

	// propagate decides per request whether trace context is injected.
	propagate func(*http.Request) bool
}

// PropagateAll is a [TransportBuilder.Propagation] predicate that injects trace
// context into every request, third parties included.
//
// Example:
//
//	httpClient := &http.Client{Transport: telemetry.Transport(nil, func(transport *telemetry.TransportBuilder) {
//		transport.Propagation(telemetry.PropagateAll)
//	})}
//	_ = httpClient
func PropagateAll(*http.Request) bool {
	return true
}

// PropagateNone is a [TransportBuilder.Propagation] predicate that never
// injects trace context.
//
// Example:
//
//	httpClient := &http.Client{Transport: telemetry.Transport(nil, func(transport *telemetry.TransportBuilder) {
//		transport.Propagation(telemetry.PropagateNone)
//	})}
//	_ = httpClient
func PropagateNone(*http.Request) bool {
	return false
}

// PropagateInternal is the default [TransportBuilder.Propagation] predicate. It
// accepts requests whose host is a loopback, private, or link-local IP
// literal, a single-label name without a dot (such as "users" or
// "localhost", as service discovery and Compose provide), or a name ending
// in ".internal", ".local", ".svc", or ".cluster.local" (case-insensitive,
// port ignored). Everything else, including public IPs and names that merely
// resolve to a private address, is external.
//
// Example:
//
//	httpClient := &http.Client{Transport: telemetry.Transport(nil, func(transport *telemetry.TransportBuilder) {
//		transport.Propagation(telemetry.PropagateInternal)
//	})}
//	_ = httpClient
func PropagateInternal(req *http.Request) bool {
	return httptrack.InternalDestination(httptrack.NormalizeHost(req.URL.Hostname()))
}

// Transport returns a round tripper that traces and measures outgoing
// requests with this instance and delegates them to base. A nil base uses
// [http.DefaultTransport].
//
// Each request gets a client span named after the method, parented to the span
// in the request context, and the driver's propagation headers (such as
// traceparent) are injected into a clone of the request; the caller's request
// is never modified. Headers are injected only when
// [TransportBuilder.Propagation] allows the destination; the default,
// [PropagateInternal], skips third parties so trace IDs and baggage do not
// leak, while the span and metric are still recorded for every host. Add
// partners with [TransportBuilder.PropagationHosts] or inject everywhere with
// [PropagateAll]. The span ends when the response body is read to the end
// or closed, so its duration covers reading the body; callers must close the
// body, as net/http already requires. A transport error or a 5xx response
// marks the span as an error; error.type is the status code or the Go type
// name of the error, never its text.
// Unknown methods use "HTTP" as the span name and "_OTHER" as the bounded
// http.request.method attribute; no request path becomes a span name.
//
// A request resent by arkoren.dev/core/http/client also carries
// http.request.resend_count on its span.
//
// Only the method, scheme, host, port, and status are recorded. Paths, query
// strings, headers, and bodies never are. With a no-op or not-yet-started
// telemetry requests pass straight to base.
//
// Example:
//
//	client := &http.Client{Transport: app.Telemetry().Transport(nil)}
func (telemetry *Telemetry) Transport(base http.RoundTripper, configure ...TransportBuilderFunc) http.RoundTripper {
	return newTransport(base, func() *Telemetry {
		return telemetry
	}, configure)
}

// Transport is [Telemetry.Transport] for the [Default] telemetry, resolved on
// every request. It takes the same role-specific construction callbacks.
//
// Example:
//
//	client := &http.Client{Transport: telemetry.Transport(nil)}
func Transport(base http.RoundTripper, configure ...TransportBuilderFunc) http.RoundTripper {
	return newTransport(base, Default, configure)
}

// newTransport builds a transport for the telemetry that resolve returns.
func newTransport(
	base http.RoundTripper,
	resolve func() *Telemetry,
	configure []TransportBuilderFunc,
) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}

	config := newTransportSettings(configure...)

	if config.Propagate == nil {
		config.Propagate = PropagateInternal
	}

	return &transport{
		base:      base,
		cache:     lazy.NewCache(newHTTPClient),
		resolve:   resolve,
		propagate: config.Propagate,
	}
}

// RoundTrip implements [http.RoundTripper].
func (transport *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	client := transport.cache.Get(transport.resolve())

	if !client.telemetry.active.Load() {
		return transport.base.RoundTrip(req)
	}

	method := httptrack.Method(req.Method)
	target := clientTarget{
		method:  method,
		address: req.URL.Hostname(),
		port:    httptrack.Port(req),
		start:   time.Now(),
	}

	startAttrs := []Attr{
		String(semconv.HTTPRequestMethod, method),
		String(semconv.ServerAddress, target.address),
		Int(semconv.ServerPort, target.port),
		String(semconv.URLScheme, req.URL.Scheme),
	}

	if resends := resend.FromContext(req.Context()); resends > 0 {
		startAttrs = append(startAttrs, Int(semconv.HTTPRequestResendCount, resends))
	}

	ctx, span := client.tracer.Start(req.Context(), httptrack.SpanName(method, ""), Kind(Client), Attrs(startAttrs...))

	outgoing := req.Clone(ctx)

	if outgoing.Header == nil {
		outgoing.Header = make(http.Header)
	}

	if transport.propagate(req) {
		client.telemetry.Inject(ctx, HeaderCarrier(outgoing.Header))
	}

	response, err := transport.base.RoundTrip(outgoing)

	if err != nil {
		client.finish(ctx, span, target, clientOutcome{errorType: fmt.Sprintf("%T", err)})

		return nil, err
	}

	target.status = response.StatusCode

	if response.Body == nil || response.Body == http.NoBody || response.StatusCode == http.StatusSwitchingProtocols {
		client.finish(ctx, span, target, clientOutcome{})

		return response, nil
	}

	response.Body = httptrack.NewBody(response.Body, func(readErr error) {
		client.finish(ctx, span, target, clientOutcome{readErr: readErr})
	})

	return response, nil
}

// clientTarget describes one outgoing request for recording.
type clientTarget struct {
	// method is the normalised request method.
	method string

	// address is the server host name.
	address string

	// port is the server port.
	port int

	// status is the response status code, zero before a response arrives.
	status int

	// start is when the request began.
	start time.Time
}

// clientOutcome describes how an outgoing request ended.
type clientOutcome struct {
	// errorType is the Go type name of a transport error, empty otherwise.
	errorType string

	// readErr is the error that ended reading the body, nil for EOF or Close.
	readErr error
}

// finish sets the outcome on span, ends it, and records the duration.
func (client *httpClient) finish(ctx context.Context, span *Span, target clientTarget, outcome clientOutcome) {
	attrs := []Attr{
		String(semconv.HTTPRequestMethod, target.method),
		String(semconv.ServerAddress, target.address),
		Int(semconv.ServerPort, target.port),
	}

	if target.status != 0 {
		attrs = append(attrs, Int(semconv.HTTPResponseStatusCode, target.status))
	}

	if errorType, description := outcome.failure(target.status); errorType != "" {
		attrs = append(attrs, String(semconv.ErrorType, errorType))
		span.SetStatus(Error, description)
	}

	span.SetAttributes(attrs[3:]...)
	span.End()
	client.duration.Record(ctx, time.Since(target.start).Seconds(), attrs...)
}

// failure returns the error.type value and span status description for the
// outcome, both empty when the request succeeded. status is the response
// status code, zero when there was none.
func (outcome clientOutcome) failure(status int) (string, string) {
	switch {
	case outcome.errorType != "":
		return outcome.errorType, outcome.errorType
	case outcome.readErr != nil:
		errorType := fmt.Sprintf("%T", outcome.readErr)

		return errorType, errorType
	case status >= http.StatusInternalServerError:
		return strconv.Itoa(status), fmt.Sprintf("HTTP %d", status)
	}

	return "", ""
}
