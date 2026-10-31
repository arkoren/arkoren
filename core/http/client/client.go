package client

import (
	"log/slog"
	"net/http"
	"time"

	"arkoren.dev/core/http/client/internal/retry"
	"arkoren.dev/core/service/telemetry"
)

// defaultAttempts is settings.Attempts when it is zero.
const defaultAttempts = 3

// defaultBackoff is Builder.Backoff when it is nil.
var defaultBackoff = Backoff(100*time.Millisecond, 2, retry.MaxRetryAfter)

// retryTransport is the retrying http.RoundTripper.
type retryTransport struct {
	// next instruments one native attempt.
	next http.RoundTripper
	// config holds settings with defaults applied.
	config settings
}

// Transport returns a round tripper that retries failed requests through base.
// A nil base uses [http.DefaultTransport].
//
// A request is retried only when its method is idempotent (GET, HEAD,
// OPTIONS, TRACE, PUT, DELETE) or it has an Idempotency-Key header, and when
// it has no body or [http.Request.GetBody] can rewind it. These rules always
// apply; [Builder.RetryIf] only decides on the outcome of an attempt. Each
// attempt uses a clone of the request, so the caller's request is never
// modified, and the previous response body is drained (up to 4 KiB) and
// closed before the next attempt.
//
// Between attempts it waits [Builder.Backoff], or the Retry-After header of a
// 429 or 503 response. If that header asks for more than 10 seconds (a fixed
// cap, independent of [Builder.Backoff]) or for more time than the context
// deadline allows, retrying stops and the response is returned as is. A wait
// ends early when the request context is done, and Transport then returns the
// context's error. When attempts run out, the last response or error is
// returned unchanged. With [Builder.Events], returning a transport error, or a
// response [Builder.RetryIf] still wanted retried, publishes [RequestFailed];
// a done request context publishes nothing.
//
// The transport wraps the telemetry transport, so every attempt is its own
// client span, and every resend has the http.request.resend_count attribute.
// Trace context is injected only into internal destinations unless
// [Builder.Propagate] says otherwise.
//
// A 101 Switching Protocols response retains [io.Writer] when the native body
// provides it, so callers can assert [io.ReadWriteCloser] for an upgraded stream.
// Read-only bodies do not gain write capability. Callers own the returned body:
// reads and writes preserve native results without closing it or canceling its
// attempt context. Close closes the native body once, then releases the attempt
// context, even if closing fails; repeated or concurrent closes return the same
// error. An upgrade retains the attempt context and its deadline until Close;
// the native body determines how cancellation affects its I/O.
//
// Example:
//
//	httpClient := &http.Client{Transport: client.Transport(nil, func(transport *client.Builder) {
//		transport.Attempts(5)
//	})}
func Transport(base http.RoundTripper, configure ...BuilderFunc) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}

	config := newSettings(configure...)

	if config.Attempts == 0 {
		config.Attempts = defaultAttempts
	}

	if config.Backoff == nil {
		config.Backoff = defaultBackoff
	}

	if config.RetryIf == nil {
		config.RetryIf = retryable
	}

	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	propagation := func(transport *telemetry.TransportBuilder) {
		transport.Propagation(config.Propagate)
	}

	next := telemetry.Transport(base, propagation)

	if config.Telemetry != nil {
		next = config.Telemetry.Transport(base, propagation)
	}

	return &retryTransport{next: next, config: config}
}

// retryable is the default Builder.RetryIf.
func retryable(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}

	switch resp.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}

	return false
}
