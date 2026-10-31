# Client

`arkoren.dev/core/http/client` retries outgoing HTTP requests. `client.Transport` wraps any
`http.RoundTripper`, so it plugs into a standard `http.Client`:

```go
outbound := &http.Client{
	Transport: client.Transport(nil, func(transport *client.Builder) {
		transport.Attempts(5)
	}),
	Timeout: 10 * time.Second,
}
```

Only requests that are safe to repeat are retried: idempotent methods (GET, HEAD,
OPTIONS, TRACE, PUT, DELETE) and any request with an `Idempotency-Key` header, as long as
the body can be rewound with `GetBody`. Transport errors and the status codes 408, 429,
502, 503, and 504 are retried with exponential backoff and full jitter; a 500 is not.
`Retry-After` on a 429 or 503 replaces the backoff, unless it asks for more than 10
seconds or outlasts the context deadline, in which case the response is returned as is.
Valid decimal values remain subject to that cap even when they exceed machine
integer ranges. Signed or malformed numbers use the configured backoff instead.

This snippet uses `net/http`, `time`, and `arkoren.dev/core/http/client`.
Only use an idempotency key when the destination implements deduplication for it.
Inspect final HTTP statuses and close each returned response body; a non-2xx
response is not itself a Go error. Body-read failures are not retried.
A native writable `101 Switching Protocols` body remains an `io.ReadWriteCloser`;
read-only bodies do not gain writing. Close the body even after I/O errors.
Closure and attempt-context cancellation run once, and repeated closes retain
the first Close result. The native body controls how cancellation affects upgrade I/O.

Each attempt is its own telemetry client span, and resends carry
`http.request.resend_count`. Retries are logged at Debug level without paths, queries,
headers, or bodies.

Construction callbacks set attempts, backoff, per-attempt timeout, retry predicate,
telemetry, and logger. Zero attempts selects three; a negative count permits one
attempt. Nonpositive attempt timeouts disable that timeout, and negative custom
backoff results mean no wait. `Backoff` keeps a zero initial delay at zero even if
growth overflows; factors below one or NaN count as one. Its internal sampling
bound is `1<<62` nanoseconds even without a configured maximum. See the package
documentation for details.
