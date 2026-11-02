---
title: HTTP Client
nav: Client
description: Send outgoing requests with explicit replay safety, bounded retries, response-body ownership, and trace-context policy.
order: 20
summary:
  - Build reusable HTTP clients and check the final response while retaining ownership of its body.
  - Decide which operations can be replayed and interpret retry limits, backoff, and Retry-After.
  - Bound whole requests and individual attempts, including response reads and upgraded streams.
  - Own native connection pools and keep trace injection separate from retry and incoming trust policies.
  - Observe retry logs and terminal failure events without treating them as a durable delivery record.
---

# HTTP Client

An outgoing request can fail before you know whether the receiving service
processed it. A retry gives transient failures another chance, but repeating a
write without the receiver's deduplication contract can duplicate its effects.
Arkoren's `client` package adds retry policy to a standard `http.RoundTripper`;
you still use Go's `http.Client` for requests, redirects, cookies, and responses.

If you are adding this to an existing project, follow
[existing application installation](../first-steps/installation.md#existing-applications).
No separate client module is required.

## Create Clients

For a client used to read reports, import `net/http`, `time`, and
`arkoren.dev/core/http/client`. Set a whole-operation timeout when constructing
the client:

```go
outgoing := &http.Client{
	Transport: client.Transport(http.DefaultTransport),
	Timeout:   8 * time.Second,
}
```

The default permits three total attempts, including the first. It retries
transport errors and responses with status `408`, `429`, `502`, `503`, or `504`;
`500` is not retryable by default. Eligibility also depends on the request's
method and body, as described in [Permit Retries](#permit-retries).
In particular, the default considers `PUT` and `DELETE` eligible, not only reads.
Do not share this policy with writes unless the receiving API honors their
idempotent semantics.

Reuse clients across requests and goroutines. Configure the client and base
transport before sharing them. Construction opens no connections. Passing nil
as the base selects `http.DefaultTransport`; it does not disable retries.

The default backoff uses full jitter, starting with a 100-millisecond upper
bound, doubling up to 10 seconds. The client's timeout covers all attempts,
retry waits, and reading the final response body—not eight seconds per attempt.

## Read Responses

With your caller's `ctx` and report URL `endpoint`, create a request carrying
that context:

```go
req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
```

Handle a construction error before passing the request to `Do`. Then inspect
the final response status before consuming its data. This fragment also uses
`fmt` and `io` inside a function returning an error:

```go
res, err := outgoing.Do(req)
if err != nil {
	return err
}
defer res.Body.Close()

if res.StatusCode != http.StatusOK {
	return fmt.Errorf("read report: unexpected status %s", res.Status)
}
data, err := io.ReadAll(res.Body)
```

Retries finish before `Do` returns. A final `503` is an HTTP response with a nil
Go error, so checking `err` alone does not establish success. Close the final
body on every successful `Do`, including non-2xx responses and body-read errors.
Reading to EOF before closing permits connection reuse when the native
transport can reuse it.

`ReadAll` is suitable only for a bounded response. Stream large bodies or impose
an application-specific size limit on untrusted data. A read error after `Do`
returns does not trigger another request; restarting a partial download is your
application's decision.

The retry wrapper closes the native body once and then releases the attempt
context. Repeated or concurrent `Close` calls return the first close result;
EOF and I/O errors alone do not release that ownership. This does not make
concurrent reads safe. Capture close errors too if your operation needs to
report them.

## Permit Retries

A request is eligible for another attempt only if both conditions hold:

- Its method is `GET`, `HEAD`, `OPTIONS`, `TRACE`, `PUT`, or `DELETE`, or it has
  a nonempty `Idempotency-Key` header.
- It has no body, or `Request.GetBody` can recreate that body.

These are transport rules, not proof that an endpoint is safe to repeat. For a
write API without a verified replay contract, use an explicitly single-attempt
client:

```go
writes := &http.Client{
	Transport: client.Transport(http.DefaultTransport, func(transport *client.Builder) {
		transport.Attempts(1)
	}),
	Timeout: 8 * time.Second,
}
```

This disables Arkoren's retries while retaining instrumentation. Inspect any
SDK, proxy, application, and native transport replay behavior separately; this
setting is not an exactly-once delivery guarantee.

Only opt a `POST` or `PATCH` into retries if the destination documents how it
deduplicates an operation using `Idempotency-Key`. The header itself does not
create that behavior. For a report-creation API with that contract, use an
application-persisted `operationID` for the same logical operation on every
retry. With `strings` imported and the operation's JSON in `payload`:

```go
req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(payload))
if err != nil {
	return err
}
req.Header.Set("Idempotency-Key", operationID)
```

Send this request through `outgoing.Do` and handle its final response as above.
Do not generate a new key for an uncertain retry, and follow the receiver's key
retention and payload-matching rules. A body from `strings.Reader`,
`bytes.Reader`, or `bytes.Buffer` gets `GetBody` from Go's request constructor;
an arbitrary streaming reader does not. Without `GetBody`, a nonempty body is
sent once even if its method or key would otherwise permit retries.

Each attempt clones the request. Before a resend, the transport drains up to
4 KiB of the discarded response, closes it, waits, then recreates the request
body. Drain or close errors do not prevent a retry. A body-recreation failure
returns an error wrapping its cause without sending that attempt. You own only
the final response body returned to you.

## Respect Retry-After

For retryable `429` and `503` responses, a valid `Retry-After` replaces the
configured backoff. A wait greater than 10 seconds, or greater than the remaining
request deadline, stops retries and returns that response with its body still
yours to close. The ten-second cap is fixed; increasing the backoff maximum
does not increase it.

The header accepts unsigned whole decimal seconds or an HTTP date parsed by
`http.ParseTime`, after trimming whitespace. A past date means no wait.
Signed or fractional values such as `+2`, `-1`, and `1.5` fall back to backoff;
an oversized unsigned decimal instead exceeds the cap and stops retries.
Other statuses ignore this header, even if a custom policy permits retrying them.

There is no separate exhaustion error. The final outcome remains a response
or transport error:

| Final Outcome | Caller Receives |
| --- | --- |
| All attempts return `503` | The final `503`, with nil error from `Do`. |
| All attempts fail at the transport | An error from `Do`, normally wrapped in `*url.Error`. |
| A response is not retryable | That response immediately. |
| `Retry-After` exceeds the allowed wait | That response without another attempt. |
| The context ends during a retry wait | A context error, with no next attempt. |

## Bound Requests

Use a request context for an operation-specific budget instead of mutating a
shared client. With `context` imported:

```go
bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
defer cancel()

req, err := http.NewRequestWithContext(bounded, http.MethodGet, endpoint, nil)
```

Keep this context alive until you have read and closed the response body.
Canceling immediately after `Do` can interrupt subsequent reads. The earliest
applicable deadline wins; the request context reaches every attempt and bounds
retry waits.

If one attempt must not consume the whole operation budget, set an additional
per-attempt limit:

```go
outgoing := &http.Client{
	Transport: client.Transport(http.DefaultTransport, func(transport *client.Builder) {
		transport.AttemptTimeout(2 * time.Second)
	}),
	Timeout: 8 * time.Second,
}
```

An attempt timeout includes reading its response body. Backoff is outside that
individual budget, so retain an overall deadline. Zero or negative attempt
timeouts set no per-attempt limit, which is the default. A timeout before
response headers can be retried while the overall context remains active;
a timeout reading the final body cannot.

Custom transports must implement context-aware I/O. The retry wrapper cannot
force an arbitrary blocked read, write, or close to return.

### Upgraded Streams

For `101 Switching Protocols`, the body retains `io.Writer` and
`io.ReadWriteCloser` only if the native body supports writing. Native byte counts
and errors pass through unchanged. Your protocol code owns closure, and the
attempt context stays active until `Close`.

If you need the writable interface through `http.Client`, leave its `Timeout`
at zero: Go's client timeout adds a read/close-only wrapper. Use a request
context or `AttemptTimeout` for the applicable budget, subject to the native
stream's cancellation behavior. Arkoren adds no write deadline or forced
interruption to upgraded I/O.

## Tune Policy

Choose retry settings from the destination's documented behavior. For an API
that explicitly treats `500` as transient, replace the outcome predicate while
retaining the other default outcomes:

```go
func retryReports(res *http.Response, err error) bool {
	if err != nil {
		return true
	}
	switch res.StatusCode {
	case 408, 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}
```

Attach this function and, if needed, a different jittered backoff during
construction:

```go
transport := client.Transport(http.DefaultTransport, func(transport *client.Builder) {
	transport.RetryIf(retryReports)
	transport.Backoff(client.Backoff(200*time.Millisecond, 2, 2*time.Second))
})
```

`RetryIf` replaces rather than extends the predicate. Exactly one of its
response and error is nonnil. It cannot override method safety, body
replayability, the attempt limit, or cancellation. Nil restores the default
predicate; nil `Backoff` restores the default delay function.

`Attempts(0)` selects three total attempts; negative values send once. A custom
backoff receives the attempt about to run: `2` for the first retry. Nonpositive
delays mean no wait; avoid repeated immediate retries against a struggling
service. While the caller context is active, `RetryIf` runs after every attempt,
including the last. `Backoff` runs only when another attempt is eligible, before
`Retry-After` selection—even if that header ultimately stops the retry.

## Own Transports

If you already own a transport with proxy, TLS, or pool settings, pass it as
the base rather than replacing it with nil:

```go
outgoing := &http.Client{
	Transport: client.Transport(base),
	Timeout:   8 * time.Second,
}
```

Configure `base` before sharing it. Retain it for pool management:
`Transport` does not forward `CloseIdleConnections` to its base. When retiring
an owned native pool, call `base.CloseIdleConnections()` yourself; do not close
a shared pool on behalf of unrelated users.

If you want an owned copy of Go's default transport, use `NewTransport`. With
`crypto/tls` imported, keep normal server verification enabled:

```go
transport, err := client.NewTransport(&tls.Config{
	MinVersion: tls.VersionTLS12,
})
if err != nil {
	return err
}
outgoing := &http.Client{Transport: transport, Timeout: 8 * time.Second}
```

Construction clones the current nonnil `*http.Transport` default and preserves
its proxy, pooling, and HTTP/2 settings. Another default implementation returns
an error before configuration callbacks run; wrap that implementation with
`Transport` instead. Call `outgoing.CloseIdleConnections()` when retiring this
owned pool; active requests are not interrupted.

`NewTransport(nil)` preserves the default's TLS settings. A nonnil configuration
replaces them with a shallow clone. Prepare nested pools, certificates, and
callback state before sharing; neither the supplied configuration nor the
default is modified. For private roots and client identities, follow
[TLS Certificates](certificates.md#choose-client-trust). Certificate errors can
be retried as transport errors, but retrying cannot fix invalid trust, expiry,
or names.

An SDK can use this `*http.Client` too, but choose one layer to own retries.
Three SDK attempts around three transport attempts can send nine requests.

## Trace Attempts

Every attempt that reaches the underlying transport gets a client span when
telemetry is active. Retries carry `http.request.resend_count`. Ordinary spans
include body reading and end on EOF, a read error, or closure; no-body and
upgrade spans end at headers. Rewind failures before network I/O create no
network-attempt span.

By default instrumentation follows `telemetry.Default()`. With an existing
application, choose its [shared handle](../observability/telemetry.md#shared-handle)
explicitly:

```go
transport := client.Transport(http.DefaultTransport, func(transport *client.Builder) {
	transport.Telemetry(app.Telemetry())
})
```

Do not also wrap the base with `telemetry.Transport`; the retry transport
already instruments attempts and a second wrapper can duplicate spans.

Trace injection defaults to `telemetry.PropagateInternal`: internal IP literals,
single-label hosts, and recognized internal suffixes qualify; resolving a
public-looking name to a private address does not. This is a destination-name
policy, not authentication. It does not sanitize trace headers you manually
put on the request. Keep sensitive headers out of caller-owned requests and
apply an explicit [outgoing policy](../observability/request-tracing.md#outgoing-policy)
when a destination requires a different boundary.

For a client that must not inject trace context, import
`arkoren.dev/core/service/telemetry` and set the predicate:

```go
transport := client.Transport(http.DefaultTransport, func(transport *client.Builder) {
	transport.Propagate(telemetry.PropagateNone)
})
```

Spans still record the request. `Propagate` replaces injection policy, not retry
policy. Expanding it to a partner can disclose trace identifiers and baggage;
allow only destinations you trust to receive that data. Incoming remote-parent
acceptance is a separate [incoming trust](../observability/request-tracing.md#incoming-trust)
decision.

## Observe Failures

Retries produce debug records on `slog.Default()` unless you supply a logger
with `transport.Logger(logger)`. Records contain the method, hostname, attempt,
delay, and status or error type—not paths, queries, bodies, or error text.
Enable debug level on that logger to see them. These are planned retry records,
not a log of every call.

To receive terminal outcome events, set `transport.Events(app.Events())` in the
construction callback and subscribe to `client.RequestFailed` on that bus.
Its payload includes `Method`, hostname-only `Host`, `Attempts`, `StatusCode`,
and `Err`. A transport failure has zero status; an HTTP response has nil error.
Keep listeners short because dispatch is synchronous, and
[own the subscription](../application/composition.md#own-subscriptions) for the
observed client's lifetime.

The event reports terminal transport errors and responses that still match
`RetryIf` when attempts or allowed `Retry-After` waits run out. It does not
report every non-2xx response: default-policy `400` and `500` responses do not
emit it. No event is emitted if the caller context is already done at the
post-attempt check, during cancellation of a retry wait, or for body-read errors
after `Do`. Events are disabled without a bus and are not a durable retry queue.

Unlike retry logs, event `Err` retains the original error and can expose
sensitive diagnostics. Redact it before export; see
[Telemetry Privacy](../observability/telemetry-privacy.md).

For backoff edge cases, construction rules, and every option, see the
[`client` API reference](https://pkg.go.dev/arkoren.dev/core/http/client).
