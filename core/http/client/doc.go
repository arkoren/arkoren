// Package client provides an [http.RoundTripper] that retries safe outgoing requests with backoff and
// jitter, honoring Retry-After. [Transport] wraps an
// [http.RoundTripper], so it plugs into any [http.Client]; there is no client
// type of its own.
// [NewTransport] instead creates an owned clone of the current native default
// transport, optionally applying a shallow-cloned tls.Config before wrapping
// retries. A nil TLS configuration preserves the default's TLS settings. Retire
// that owned pool through http.Client.CloseIdleConnections; Transport leaves
// connection-pool ownership with the caller.
//
//	httpClient := &http.Client{Transport: client.Transport(nil, func(transport *client.Builder) {
//		transport.Attempts(5)
//	})}
//
//	resp, err := httpClient.Get("https://api.example.com/items")
//
// # What is retried
//
// A request is retried only when it is safe to send twice: its method is GET,
// HEAD, OPTIONS, TRACE, PUT, or DELETE, or it carries an Idempotency-Key
// header; and it has no body or a body that [http.Request.GetBody] can
// rewind ([http.NewRequest] sets GetBody for bytes, strings, and readers it
// recognizes). Eligibility is always enforced; [Builder.RetryIf] only judges
// the outcome of an attempt. The default judges transport errors and the
// status codes 408, 429, 502, 503, and 504 retryable. A 500 is not, because
// it usually signals a bug a repeat would not fix.
//
// # Waiting
//
// Between attempts the transport waits the [Builder.Backoff] delay, by
// default exponential with full jitter ([Backoff]). On 429 and 503 responses
// a Retry-After header (seconds or an HTTP date) replaces that delay; a wait
// longer than 10 seconds, or one that would outlast the request context's
// deadline, is not taken: the response is returned as is. Canceling the
// request context ends a wait immediately.
// A valid decimal Retry-After cannot bypass that cap by overflowing a machine
// integer; signed or malformed numbers instead use the configured backoff.
// Backoff preserves zero initial delays even for infinite exponential growth.
// Negative custom backoff delays count as zero; nonpositive attempt timeouts
// are disabled, and negative attempt counts permit only one attempt.
//
// # Upgraded response bodies
//
// A native writable 101 Switching Protocols body retains [io.Writer], so callers
// can assert [io.ReadWriteCloser]. Read-only bodies do not gain writing, and
// unrelated native optional interfaces are not preserved. Reads and writes keep
// their native results; the caller must close the body. Close closes the native
// body once, then cancels the attempt context even after a close error. Repeated
// and concurrent closes return the first result. Upgrade I/O still follows the
// native body's cancellation behavior.
//
// # Observability
//
// Each attempt goes through the telemetry transport, so each is its own
// client span; resends carry http.request.resend_count. Retries are logged at
// Debug level with the method, host, attempt, delay, and status code or error
// type; paths, queries, headers, and bodies are never logged.
//
// # Caveats
//
// When attempts run out, the last response or error is returned unchanged. The
// transport never modifies the caller's request. Wrap the transport of a
// client that follows redirects once: each redirect is a new request with its
// own attempts.
//
// With [Builder.Events], a request the transport gives up on publishes
// [RequestFailed] ("http.client.request.failed") with the method, host,
// attempts, and the final status or error.
package client
