---
title: HTTP Client
nav: Client
description: Make outgoing HTTP calls more reliable with a retrying transport that respects idempotency, Retry-After, and deadlines, and traces attempts.
order: 20
---

# HTTP Client

## Introduction

An outgoing HTTP call can fail because a connection resets, a load balancer
answers `503` during a deploy, or an API asks you to slow down with `429`.
A retry gives a transient failure another chance, but repeating an operation
carelessly can do harm, such as charging a card twice.

Arkoren's `client` package provides an `http.RoundTripper` that retries
eligible requests with backoff. It plugs into the standard `http.Client`, so you
keep Go's request, response, cookie, and redirect APIs. With telemetry configured,
each attempt that reaches the underlying transport has its own client span.

Install the core module:

```sh
go get arkoren.dev/core
```

## Creating a client

A transport sends HTTP requests over the network. `client.Transport` wraps an
existing transport to add retries; you still send requests through Go's standard
`http.Client`.

The snippets use `net/http`, `time`, and `arkoren.dev/core/http/client`.
Use Go's default transport as the base and set an overall timeout:

```go
outgoing := &http.Client{
	Transport: client.Transport(http.DefaultTransport),
	Timeout:   10 * time.Second,
}
```

This enables up to three attempts per eligible request: the first call and at
most two retries. The default backoff starts at 100 milliseconds, doubles up to
10 seconds, and uses random jitter so clients do not retry in lockstep. The
10-second client timeout covers all attempts, waits, and reading the response body.

Passing `nil` as the base is shorthand for `http.DefaultTransport`; it does not
disable retries. If you have a transport with custom TLS or proxy settings,
pass that transport instead, as shown in [Using a custom transport](#using-a-custom-transport).

Create the client once and reuse it across requests and goroutines. Configure it
before sharing it; do not change its fields while requests are running.
Construction callbacks run once, synchronously in order, and defaults apply to
the completed settings. Retaining a builder does not let you reconfigure a
published transport; supplied nil callbacks panic.

## Sending a request

Send requests through `outgoing.Do`, not through the transport directly. With an
existing GET request `req` created by `http.NewRequestWithContext`, fetch a small
report and check the final status before reading it. This fragment also uses
`fmt` and `io`:

```go
res, err := outgoing.Do(req)
if err != nil {
	return err
}
defer res.Body.Close()

if res.StatusCode < 200 || res.StatusCode >= 300 {
	return fmt.Errorf("fetch report: unexpected status %s", res.Status)
}

data, err := io.ReadAll(res.Body)
```

`Do` performs any retries before returning. A `503` after the last attempt is
still an HTTP response, not a Go error; the status check handles that case.
Close the body even for non-2xx responses. Reading it to the end before closing
also lets Go reuse the connection when possible.

`io.ReadAll` buffers the entire response. For large or untrusted responses,
enforce an application-specific size limit or stream the body instead.

Focused snippets keep error results visible without routine checks. Handle
request-construction and body-read errors before using their results; keep the
`Do` check before accessing a response body. If your operation reports close
failures, capture the result of `Body.Close` as well.

## Which requests are retried

Retrying is only safe when sending a request twice has the same effect as
sending it once. The transport therefore only retries **idempotent** requests:
`GET`, `HEAD`, `OPTIONS`, `TRACE`, `PUT`, and `DELETE`. A `POST` or `PATCH` is
never retried unless it carries an `Idempotency-Key` header. Use this only when
the receiving API implements that key to recognize duplicate operations. Here
`payment` is the stable identifier for this payment operation, reused across retries:

```go
req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(payload))
req.Header.Set("Idempotency-Key", payment)

res, err := outgoing.Do(req)
```

Close `res.Body` whenever `Do` returns no error, including for non-2xx responses.
Adding the header alone
does not make a server deduplicate requests; follow that API's key and retention
rules before enabling retries for a side effect such as a payment.

A request with a body must also be able to resend it. `http.NewRequestWithContext`
sets `GetBody` for `*strings.Reader`, `*bytes.Reader`, and `*bytes.Buffer`.
The example uses the `strings` package to make `payload` replayable; an arbitrary
streaming reader is not enough.

The transport clones the request for each attempt and rewinds the body for
retries; it does not modify your request. If a body has no `GetBody`, the request
is sent once even when its method or idempotency key would otherwise allow retries.

## Which failures are retried

A request is retried when it fails with a transport error, such as a refused
or reset connection, or when the response status is `408`, `429`, `502`,
`503`, or `504`. A `500` is not retried by the default policy. If the destination
documents it as transient, you can [supply a retry policy](#choosing-a-retry-policy).

When a `429` or `503` response carries a `Retry-After` header, the transport
waits as long as it asks instead of using its own backoff. If the server asks
for more than 10 seconds, or longer than the request's deadline allows, the
transport stops retrying and returns that response, so your code can decide
what to do.

After trimming surrounding whitespace, `Retry-After` accepts unsigned decimal
digits for whole seconds, such as `0` or `10`, or an HTTP date accepted by
`http.ParseTime`. Signs, fractions, and malformed numbers such as `+2`, `-1`,
or `1.5` are invalid and fall back to the configured backoff. A valid decimal
that exceeds machine integer ranges still stops retries at the ten-second cap;
it does not fall back. A date in the past means zero wait.

The ten-second limit is fixed; changing the backoff maximum does not change it.
Other response statuses ignore `Retry-After`, even if a custom retry policy
allows them to retry.

### When retries stop

There is no special "retries exhausted" error. Your code receives the final
outcome:

| Outcome | What your code receives |
| --- | --- |
| The destination returns a non-retryable response | That response immediately, even if it is a `4xx` or `5xx`. |
| Every allowed attempt returns `503` | The last `503` response, with `err == nil` from `Do`. |
| Every allowed attempt fails to connect | An error from `Do`; `http.Client` wraps transport errors in `*url.Error`. |
| `Retry-After` exceeds the cap or remaining deadline | The last response without another attempt. |
| The request is canceled while waiting | A context error from `Do`; no further attempt runs. |

While the caller's context is active, `RetryIf` runs once after each attempt,
including the last attempt and requests that cannot be replayed. Only when
another attempt is allowed does `Backoff` run, before inspecting `Retry-After`.
It runs even if that header overrides the delay or stops retries.

Once a retry delay is selected, the transport logs the retry, drains up to 4 KiB
of the discarded response body, closes it, waits, then rewinds the request body
and sends the next attempt. Drain and close failures do not prevent a retry.
A rewind failure returns an error wrapping its cause without sending that
attempt. If `Retry-After` stops retries, there is no retry log or body cleanup:
you still own that response. You own only the final body returned by `Do`.

Retries cover errors returned while sending the request and receiving response
headers. They do not restart a request if reading its returned body fails.
Handle read errors separately; restarting a partially read download requires
application-specific handling.

## Deadlines

The request's context is passed to every attempt and bounds retry waits. If it
is canceled during a wait, the transport returns its error without another
attempt. If it is already done when an underlying attempt returns, the transport
returns that attempt's response or error without calling retry hooks or
publishing a failure event. The `http.Client.Timeout` in the first example
bounds the whole call; you can also set a deadline on the request context.

If one slow attempt should not use the whole time budget, add an attempt timeout:

```go
outgoing := &http.Client{
	Transport: client.Transport(http.DefaultTransport, func(transport *client.Builder) {
		transport.AttemptTimeout(2 * time.Second)
	}),
	Timeout: 10 * time.Second,
}
```

This retains the default of at most three attempts, each with a two-second limit that includes
reading its response body. The overall ten-second timeout still applies, so
the call can end before all three attempts run.

There is no per-attempt timeout by default; zero or negative values disable it.
Setting `AttemptTimeout` does not replace the overall deadline: backoff waits
are outside an individual attempt's budget. If an attempt times out before
returning a response, the default policy
can retry it while the overall context remains active. If it times out while your
code reads the returned body, Go's native transport fails the read without an
automatic retry. A custom transport must implement its own context-aware I/O;
the wrapper cannot force an arbitrary blocked read, write, or close to return.

For an operation-specific budget, use the caller's context rather than changing
a shared client's timeout. With an existing `ctx` and `endpoint`, import
`context` and bound this request to three seconds:

```go
bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
defer cancel()

req, err := http.NewRequestWithContext(bounded, http.MethodGet, endpoint, nil)
res, err := outgoing.Do(req)
```

The earliest applicable deadline wins. Read and close the response body before
calling `cancel`; canceling immediately after `Do` can interrupt body reads.

## Response-body ownership

Always close the final response body, even after EOF or a read or write error.
The retry wrapper calls the native body's `Close` once, then releases the
attempt context, even if native closure returns an error. Repeated or concurrent
`Close` calls wait for that cleanup and return the first close result; they do
not close the native body again. EOF and I/O errors alone do not perform this
cleanup. This guarantee does not make concurrent reads and writes safe; those
operations retain the native body's concurrency rules.

For a `101 Switching Protocols` response, a native writable body remains writable:
the retry wrapper preserves `io.Writer` and `io.ReadWriteCloser`. A read-only
native body does not gain those capabilities. Reads and writes preserve native byte counts
and errors; your protocol code still owns closing the upgraded stream. Native
I/O determines how cancellation affects that stream. The wrapper neither adds
write deadlines nor forcibly interrupts it.

If your protocol code needs that writable interface through `http.Client`, leave
`http.Client.Timeout` at zero: Go's client timeout adds a read/close-only body
wrapper. Use the request context or `AttemptTimeout` for the relevant budget,
subject to the native I/O limits above.

## Tuning the backoff

If the default retry delays do not suit the destination, set a different backoff
in the transport's configuration callback. `client.Backoff` takes an initial
delay, a growth factor, and a maximum:

```go
transport := client.Transport(http.DefaultTransport, func(transport *client.Builder) {
	transport.Backoff(client.Backoff(50*time.Millisecond, 3, time.Second))
})
```

Use this transport in `http.Client.Transport`. Retry delays use full jitter,
with an initial upper bound of 50 milliseconds, tripling up to one second.

`client.Backoff` normalizes negative initial and maximum durations to zero.
A zero maximum omits the configured cap; a growth factor below one or `NaN`
acts as one. A zero initial duration always produces zero waits, even if growth
would overflow. Infinite growth saturates at the maximum when set, and jitter
sampling always has an internal bound of `1<<62` nanoseconds. Attempt numbers
below `2` use the first retry's bound.

Custom backoff functions receive the number of the attempt about to run: `2`
for the first retry, `3` for the next. A zero or negative delay retries without
waiting. Keep nonzero delays in production to avoid overwhelming an unavailable
destination. Passing `nil` to the builder's `Backoff` restores the default delay
function; it does not disable waiting.

## Choosing a retry policy

Use `RetryIf` when the destination's documented failure behavior differs from
the defaults. For example, if its `500` responses are transient, define a policy
that adds `500` while retaining the other retryable outcomes:

```go
func retryService(res *http.Response, err error) bool {
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

Attach the function when constructing the transport:

```go
transport := client.Transport(http.DefaultTransport, func(transport *client.Builder) {
	transport.RetryIf(retryService)
})
```

This replaces the default predicate; it does not add to it. Passing `nil` to
`RetryIf` restores the default predicate. Exactly one of the response and error
passed to the predicate is non-nil. Request method, body
replayability, attempt limits, and cancellation still restrict retries. Returning
`true` cannot make an unsafe POST retryable.

To disable retries for a client while retaining instrumentation, set
`transport.Attempts(1)` in its configuration callback. Omitting `Attempts`, or
setting it to zero, selects the default of three total attempts.
A negative attempt count also sends once; it does not disable requests.

## Using a custom transport

If you already configure TLS, proxies, or connection limits, pass that transport
as the base. Do not replace it with `nil`, which would discard those settings.
For example, clone Go's stock default transport before changing its pool limits:

```go
base := http.DefaultTransport.(*http.Transport).Clone()
base.MaxIdleConnsPerHost = 20

outgoing := &http.Client{
	Transport: client.Transport(base),
	Timeout:   10 * time.Second,
}
```

The assertion assumes `http.DefaultTransport` has its stock `*http.Transport`
type. If your application replaced it with another implementation, pass that
implementation directly or clone the concrete transport you own. Cloning avoids
mutating the shared default. Configure the base before sharing it and keep TLS
certificate verification enabled.

If you only need custom TLS policy, use `client.NewTransport` instead of cloning
the default yourself. First [load a pool of trusted server CAs](certificates.md#trusting-a-private-ca-in-an-http-client).
With that `pool` and `crypto/tls` imported:

```go
transport, err := client.NewTransport(&tls.Config{
	MinVersion: tls.VersionTLS12,
	RootCAs:    pool,
})
if err != nil {
	return err
}
outgoing := &http.Client{Transport: transport, Timeout: 10 * time.Second}
```

The new transport retains the default's native proxy, pooling, and HTTP/2
configuration, and applies the same retry and telemetry policy as `Transport`.
It opens no connections during construction. Callbacks run once in order;
supplied nil callbacks panic. If the current `http.DefaultTransport` is nil,
typed-nil, or another implementation, construction returns an error before
callbacks run. Use `Transport` for that custom implementation instead.

`NewTransport(nil)` preserves the cloned default's TLS settings. A nonnil config
replaces them with a shallow clone without changing the caller's config or the
default. `RootCAs` verifies servers; for mutual TLS, also set `Certificates` to
present a client identity. These settings have different roles.
The certificates guide shows private-only pools, additive system roots, and the full native
TLS setup. `tls.Config.Clone` is not a recursive copy of its pools or
certificates: finish configuration before sharing these values.

The retry wrapper does not change TLS verification. Under the default retry
policy, certificate-verification failures are transport errors and can be
retried for eligible requests; retries do not repair invalid trust, names, or
expiry. This is separate from the CLI's condition waits, which treat
certificate-verification failures as terminal.

An SDK that accepts `*http.Client` can use `outgoing` too. Check whether that SDK
already retries: three SDK attempts wrapped around three transport attempts can
send the same operation nine times. Choose one layer to own retries and disable
them in the other.

For `NewTransport`, call `outgoing.CloseIdleConnections()` when retiring the
owned connection pool; this closes unused connections without interrupting
active requests. For a custom base passed to `Transport`, retain the base and
call `base.CloseIdleConnections()` yourself: `Transport` does not forward its
base's connection-management methods.

## Telemetry

Every attempt that reaches the underlying transport is recorded as a client span
when telemetry is enabled. A resent request carries `http.request.resend_count`,
so a trace shows each retry. An ordinary response's span includes body reading
and ends on EOF, a read error, or `Close`. For `101 Switching Protocols` and
responses with no body, the span ends when headers arrive, not when the upgraded
stream closes. Rewind failures before transport I/O create no network-attempt
span. Without configured telemetry, instrumentation is a no-op.

By default the transport follows `telemetry.Default()`. With an existing
application, use its telemetry explicitly:

```go
transport := client.Transport(http.DefaultTransport, func(transport *client.Builder) {
	transport.Telemetry(app.Telemetry())
})
```

The retry transport already instruments each attempt. Do not wrap its base in
`telemetry.Transport` as well, or the same attempt can produce duplicate spans.

Trace context is propagated to your own internal services by default, but not
to third-party hosts, so outgoing calls to external APIs do not leak your trace
identifiers. [Telemetry](../observability/request-tracing.md) explains how to
change which hosts receive it.

If a trusted partner should receive trace context, `Propagate` can select it
while preserving the default internal-host policy. This also imports
`arkoren.dev/core/service/telemetry`:

```go
func propagatePartner(req *http.Request) bool {
	return telemetry.PropagateInternal(req) || req.URL.Hostname() == "api.partner.com"
}
```

Set `transport.Propagate(propagatePartner)` in the configuration callback. This
replaces the propagation predicate, not the retry policy. Only allow trusted
destinations: propagation can disclose trace identifiers and baggage.

## Logging retries

Each retry produces a debug log with the method, destination host, attempt,
delay, and status code or error type. The default logger is `slog.Default()`;
its normal info-level threshold hides these entries.

To inspect retries, create a debug-enabled logger with `log/slog` and `os`, then
pass it to the transport:

```go
logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
	Level: slog.LevelDebug,
}))

transport := client.Transport(http.DefaultTransport, func(transport *client.Builder) {
	transport.Logger(logger)
})
```

These records describe retries, not every request or terminal failure. They omit
the URL path, query, request body, and error text.

## Observing terminal failures

To observe requests that the transport gives up on, pass an existing
[`*event.Bus`](../events/overview.md). With an application, use its shared bus:

```go
bus := app.Events()
transport := client.Transport(http.DefaultTransport, func(transport *client.Builder) {
	transport.Events(bus)
})
```

Register a listener on that bus. With the `logger` above, record the final status
and the number of attempts:

```go
unsubscribe, err := bus.Listen(client.RequestFailed, func(ctx context.Context, failure client.RequestFailedEvent) error {
	logger.WarnContext(ctx, "outgoing request failed",
		"host", failure.Host,
		"attempts", failure.Attempts,
		"status", failure.StatusCode,
	)
	return nil
})
```

Handle the registration error, and call `unsubscribe` when the listener is no
longer needed. Keep it registered for the lifetime of the clients you observe;
do not defer its removal in a short-lived setup function. Listeners run
synchronously, so keep them short rather than making slow network calls.

`client.RequestFailed` (`http.client.request.failed`) is emitted for a terminal
transport error, or when a response still matches `RetryIf` but the attempt
limit or `Retry-After` limit stops retries. It is not an event for every non-2xx
response: a default-policy `400` or `500` does not emit it. If the caller's context
is already done at the post-attempt check, no event is published. Cancellation
during a retry wait and body-read failures after `Do` returns also publish nothing.

The payload contains `Method`, `Host` (hostname only, without port, path, or query),
`Attempts`, `StatusCode`, and `Err`. A transport error has a zero status; an HTTP
response has a nil error. Events are disabled unless you supply a bus. They are in-process
notifications, not a durable failure queue.

Unlike retry logs, `Err` retains the original error and may contain sensitive
diagnostics. Review and redact it before exporting it; do not return the whole
payload to clients. See [Telemetry Privacy](../observability/telemetry-privacy.md)
for export and propagation boundaries.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| A POST runs only once | The destination must support `Idempotency-Key`; provide a stable key and a replayable body. |
| A PUT with a streamed body runs only once | Its body needs `GetBody`; an arbitrary `io.Reader` cannot be rewound automatically. |
| `Do` returns nil error but the operation failed | Check the final HTTP status, not just `err`. |
| A response-body read fails without a retry | Retries finish before your code consumes the final body; handle the read failure separately. |
| Fewer attempts run than configured | Check deadlines, request eligibility, the retry predicate, and `Retry-After`. |
| No retry logs appear | Enable debug output on the logger supplied to the transport. |
| No failure event arrives | Supply a bus and check whether the final outcome is retryable; cancellation and body-read failures do not publish. |
| More calls arrive than expected | Check SDK or application retries in addition to this transport's attempts. |

For every option, see the [`client` API reference](https://pkg.go.dev/arkoren.dev/core/http/client).
