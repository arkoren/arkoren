package client

import (
	"log/slog"
	"net/http"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/telemetry"
)

// BuilderFunc configures one Transport construction draft. Policies run once,
// synchronously in order; supplied nil functions panic, while an expanded nil
// slice supplies no policies. Retaining the draft cannot change the published
// transport. Services and function captures remain shared, not cloned.
//
// Example:
//
//	var retries client.BuilderFunc = func(transport *client.Builder) {
//		transport.Attempts(5)
//	}
//	outgoing := &http.Client{Transport: client.Transport(nil, retries)}
//	_ = outgoing
type BuilderFunc = func(*Builder)

// settings configures [Transport].
type settings struct {
	// Attempts is the total number of attempts, including the first, as in
	// queue.JobBuilder.Retry and workflow.StepBuilder.Retry. The default, zero, is 3. A
	// negative value disables retries: every request is attempted once.
	Attempts int

	// Backoff returns the delay before attempt number attempt, which is at
	// least 2 (the first retry). The default, nil, is [Backoff] with an
	// initial delay of 100ms, factor 2, and maximum of 10s. A negative result
	// counts as zero.
	Backoff func(attempt int) time.Duration

	// AttemptTimeout bounds each attempt, from sending the request to
	// reading the response body. Nonpositive values set none; zero is the
	// default. The total time is bounded by the request context.
	AttemptTimeout time.Duration

	// RetryIf reports whether the outcome of an attempt should be retried:
	// exactly one of the response and the error is non-nil. It never
	// overrides the method and body rules of [Transport], and is not called
	// once the request context is done. The default, nil, retries transport
	// errors and the status codes 408, 429, 502, 503, and 504.
	RetryIf func(*http.Response, error) bool

	// Telemetry instruments each attempt as its own client span. The default,
	// nil, follows [telemetry.Default], resolved per request.
	Telemetry *telemetry.Telemetry

	// Propagate decides, per outgoing request, whether trace context is
	// injected; see [telemetry.TransportBuilder.Propagation]. The default, nil,
	// injects only to internal destinations ([telemetry.PropagateInternal]).
	Propagate func(*http.Request) bool

	// Logger receives one Debug record per retry. The default, nil, is
	// [slog.Default].
	Logger *slog.Logger

	// Events receives [RequestFailed]. The default, nil, publishes nothing.
	Events *event.Bus
}

// Builder configures Transport before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	httpClient := &http.Client{Transport: client.Transport(nil, func(transport *client.Builder) {
//		transport.Attempts(5)
//	})}
//	_ = httpClient
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// newSettings applies construction callbacks and publishes independent completed
// settings. Normalization stays after the final callback.
func newSettings(configure ...BuilderFunc) settings {
	transport := &Builder{}

	for _, configure := range configure {
		configure(transport)
	}

	config := transport.settings

	return config
}

// Attempts is the total number of attempts, including the first, as in
// queue.JobBuilder.Retry and workflow.StepBuilder.Retry. The default, zero, is 3. A
// negative value disables retries: every request is attempted once.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	httpClient := &http.Client{Transport: client.Transport(nil, func(transport *client.Builder) {
//		transport.Attempts(5)
//	})}
//	_ = httpClient
func (transport *Builder) Attempts(attempts int) *Builder {
	transport.settings.Attempts = attempts

	return transport
}

// Backoff returns the delay before attempt number attempt, which is at
// least 2 (the first retry). The default, nil, is [Backoff] with an
// initial delay of 100ms, factor 2, and maximum of 10s. A negative result
// counts as zero.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	httpClient := &http.Client{Transport: client.Transport(nil, func(transport *client.Builder) {
//		transport.Backoff(nil)
//	})}
//	_ = httpClient
func (transport *Builder) Backoff(backoff func(attempt int) time.Duration) *Builder {
	transport.settings.Backoff = backoff

	return transport
}

// AttemptTimeout bounds each attempt, from sending the request to
// reading the response body. Nonpositive values set none; zero is the default.
// The total time is bounded by the request context. Upgrades retain this context
// and its deadline until Body.Close; native I/O determines how it honors them.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	httpClient := &http.Client{Transport: client.Transport(nil, func(transport *client.Builder) {
//		transport.AttemptTimeout(time.Second)
//	})}
//	_ = httpClient
func (transport *Builder) AttemptTimeout(timeout time.Duration) *Builder {
	transport.settings.AttemptTimeout = timeout

	return transport
}

// RetryIf reports whether the outcome of an attempt should be retried:
// exactly one of the response and the error is non-nil. It never
// overrides the method and body rules of [Transport], and is not called
// once the request context is done. The default, nil, retries transport
// errors and the status codes 408, 429, 502, 503, and 504.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	httpClient := &http.Client{Transport: client.Transport(nil, func(transport *client.Builder) {
//		transport.RetryIf(nil)
//	})}
//	_ = httpClient
func (transport *Builder) RetryIf(retryIf func(*http.Response, error) bool) *Builder {
	transport.settings.RetryIf = retryIf

	return transport
}

// Telemetry instruments each attempt as its own client span. The default,
// nil, follows [telemetry.Default], resolved per request.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	httpClient := &http.Client{Transport: client.Transport(nil, func(transport *client.Builder) {
//		transport.Telemetry(nil)
//	})}
//	_ = httpClient
func (transport *Builder) Telemetry(instance *telemetry.Telemetry) *Builder {
	transport.settings.Telemetry = instance

	return transport
}

// Propagate decides, per outgoing request, whether trace context is
// injected; see [telemetry.TransportBuilder.Propagation]. The default, nil,
// injects only to internal destinations ([telemetry.PropagateInternal]).
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	httpClient := &http.Client{Transport: client.Transport(nil, func(transport *client.Builder) {
//		transport.Propagate(nil)
//	})}
//	_ = httpClient
func (transport *Builder) Propagate(propagate func(*http.Request) bool) *Builder {
	transport.settings.Propagate = propagate

	return transport
}

// Logger receives one Debug record per retry. The default, nil, is
// [slog.Default].
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	httpClient := &http.Client{Transport: client.Transport(nil, func(transport *client.Builder) {
//		transport.Logger(slog.Default())
//	})}
//	_ = httpClient
func (transport *Builder) Logger(logger *slog.Logger) *Builder {
	transport.settings.Logger = logger

	return transport
}

// Events receives [RequestFailed]. The default, nil, publishes nothing.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	httpClient := &http.Client{Transport: client.Transport(nil, func(transport *client.Builder) {
//		transport.Events(event.New())
//	})}
//	_ = httpClient
func (transport *Builder) Events(bus *event.Bus) *Builder {
	transport.settings.Events = bus

	return transport
}
