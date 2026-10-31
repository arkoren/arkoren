---
title: Request Tracing
description: Trace incoming requests with the server middleware and outgoing ones with the instrumented transport and correlate logs.
order: 30
---

# Request Tracing

## Introduction

To follow an incoming request into the services it calls, instrument both sides
of the HTTP boundary. The **server middleware** wraps every incoming request
in a span, records how long it took and how many requests are in flight, and
hands the request's trace ID to your logs and to your clients. The **client
transport** wraps an `http.RoundTripper`, so every outgoing call becomes a span
that is a child of the request that caused it. It forwards trace context only
to destinations allowed by your propagation policy; internal destinations are
allowed by default.

Both come from the [`*telemetry.Telemetry`](telemetry.md) that you give the
application, and both do nothing until that telemetry is started with a
driver. With the default no-op telemetry the middleware calls the next handler
directly and the transport passes requests straight to its base transport, so
leaving them in place costs nothing in a service that does not collect
telemetry yet.

Neither one records URLs, query strings, headers, bodies, or error text. They
record the method, the route template, the status, and a handful of other
low-cardinality facts. [Privacy](telemetry-privacy.md) explains why.

## Tracing incoming requests

With an existing `app` configured as in
[Telemetry](telemetry.md#turning-telemetry-on), register the middleware on your
router `r` before everything else:

Import `telemetry` from `arkoren.dev/core/service/telemetry`, `router` from
`arkoren.dev/core/http/router`, and `requestlog` from
`arkoren.dev/core/http/middleware/requestlog`. Later fragments use standard
packages `net/http`, `net/netip`, and `net/http/httptest`, plus `telemetrytest`
from `arkoren.dev/core/service/telemetry/telemetrytest`.

```go
t := app.Telemetry()

r.Use(
	t.Middleware(),
	requestlog.Middleware(func(log *requestlog.Builder) {
		log.Logger(app.Logger())
	}),
	router.Recover(),
)
```

Order matters. The span has to be open while the later middleware and the
handler run, and it must end after Arkoren has rendered a returned error and
after `router.Recover` has turned a panic into a `500`. Registered first, the
middleware sees the status the client received, not the status the handler
intended. It is also the reason the request log, which comes after it, can
carry the trace ID (see [Correlating logs](#correlating-logs)).

If you do not hold the application's telemetry at the point where you build the
router, `telemetry.Middleware()` does the same for the process default. It looks
the default up on every request, so it can be registered before
`app.SetDefaults()` runs.

### Spans

For each request the middleware starts a server span and ends it when the
response is complete. The span is named after the method and the route template
that matched, such as `GET /invoices/{id}`, never after the concrete path, so
the name has few distinct values. A request that matched no route, such as a
`404` for an unknown path, is named after the method alone.

The span carries the method, the URL scheme, the protocol name and version, and
the route. When the response is done it also carries the status code. An
unusual method such as `PURGE` is recorded as `_OTHER`, so a client cannot
create new values by inventing methods.

Only server failures mark a span as failed. A `5xx` response sets the span's
status to error with a description such as `HTTP 503` and records the status code as
`error.type`. A `4xx` response is the client's mistake and the service behaved
correctly, so the span status stays unset and the code is only visible in the
status attribute.

### Metrics

The same middleware records two metrics:

- `http.server.request.duration` is a histogram of request durations in seconds.
   It is labeled by method, route, status code, scheme, and `error.type`, and
  uses the bucket boundaries recommended by the OpenTelemetry HTTP conventions.
- `http.server.active_requests` is an up-down counter of requests in flight,
  labeled by method and scheme.

The route label is the template, so `GET /invoices/7` and `GET /invoices/8`
fall into one series. The duration is recorded when the span ends, so it covers
the whole stack the middleware wraps, including error rendering.

## Trace context from callers

When one of your services calls another, the caller sends its trace context in
the `traceparent` header (and `baggage`, if the driver propagates it). The
server middleware can adopt that context, so both services appear in one trace.
But a header is input from the network, and a caller who controls it can pick
trace IDs, force sampling on, or plant baggage that your service then forwards
to its own dependencies.

For that reason the middleware defaults to `telemetry.TrustNone`: it does not
extract incoming trace headers, including from loopback, private, and link-local
peers. Without a parent already in the request context, the request starts a new
root trace. An explicit `TrustRemote(nil)` selects the same default.
`X-Forwarded-For` and similar headers are never consulted.

This policy gates header extraction; it does not erase context installed by
other instrumentation. With OpenTelemetry, a valid native parent already in
`req.Context()` takes precedence over a conflicting facade identity, including
a non-recording or remote native parent. Otherwise a valid facade parent is
adopted. Apply your trust policy before any upstream instrumentation extracts
headers: `TrustNone` here cannot undo an earlier extraction. Facade observers
receive that backend-selected parent through `FinishedSpan.Parent`, with its
IDs, sampled flag, remote bit, and tracestate preserved; see
[observing telemetry](telemetry.md#observing-telemetry).

> [!WARNING]
> Behind a load balancer or reverse proxy that connects from a private address,
> a browser's trace headers can pass through unchanged. Trusting the proxy's IP
> address does not authenticate whoever supplied those headers. Keep the default
> at a public edge unless the proxy strips untrusted context and forwards only
> context from trusted workloads. Listing the proxy in `TrustedNetworks` alone
> does not make browser-supplied context safe.

If only trusted workloads can reach your service, opt in to continuing their
traces. For example, accept context from a restricted workload network:

```go
r.Use(t.Middleware(
	func(middleware *telemetry.MiddlewareBuilder) {
		middleware.TrustedNetworks(netip.MustParsePrefix("10.20.0.0/16"))
	},
))
```

`MiddlewareBuilder.TrustedNetworks` trusts only peers inside the listed prefixes.
For a decision that depends on the request, pass a function to `TrustRemote`.
Use `TrustRemote(telemetry.TrustAll)` only where access is restricted to trusted
workloads. To reject incoming context explicitly, use
`TrustRemote(telemetry.TrustNone)` or `TrustRemote(nil)`.

### Migrating from the internal-peer default

The default changed from `telemetry.TrustInternal` to `telemetry.TrustNone`.
Services that previously relied on implicit internal header extraction now create
separate root traces unless a parent was already installed in the context.
After checking that only trusted workloads can supply context, preserve the
former behavior explicitly:

```go
r.Use(t.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
	middleware.TrustRemote(telemetry.TrustInternal)
}))
```

`TrustInternal` accepts loopback, private (`10.0.0.0/8`, `172.16.0.0/12`,
`192.168.0.0/16`, and IPv6 `fc00::/7`), and link-local peers. It rejects public
and unparseable addresses. Prefer `TrustedNetworks` when a narrower network
matches your deployment. This change affects incoming context acceptance only;
outgoing propagation still allows internal destinations by default.

## Exposing the trace ID

A trace ID is most useful when the person who hits an error can quote it. When
the span has a valid context, the middleware therefore puts the ID in three
places.

It sets the W3C `traceresponse` header on every response, in the form
`00-<trace-id>-<span-id>-<flags>`. To also expose the bare trace ID under a name
of your choice, pass `MiddlewareBuilder.TraceHeader`:

```go
r.Use(t.Middleware(func(middleware *telemetry.MiddlewareBuilder) {
	middleware.TraceHeader("X-Trace-ID")
}))
```

And on [error responses](../http-fundamentals/error-handling.md) it adds a `trace_id`
member to the problem document, so a client that only shows the body still
shows the ID:

```json
{
  "type": "about:blank",
  "title": "Service Unavailable",
  "status": 503,
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736"
}
```

`MiddlewareBuilder.ProblemKey("request_id")` renames the member, `MiddlewareBuilder.ProblemKey("")` leaves
it out, and `MiddlewareBuilder.TraceResponse(false)` drops the `traceresponse` header. With a
no-op telemetry, or a span that is not valid, none of these are set.

Clients propagate context with `traceparent`. Arkoren has no arbitrary
correlation-ID header: the trace ID is the correlation ID.

## Correlating logs

The application wraps its logger so that every record logged with a context that
holds a span gets `trace_id` and `span_id` attributes. The server middleware
puts the request's span in the request context. In your existing handler, use
the logger from `app.Logger()` with `req.Context()`:

```go
logger := app.Logger()
logger.InfoContext(req.Context(), "charging invoice", "invoice", "7")
```

With the JSON handler of a production application, the record is:

```json
{
  "time": "2026-10-12T09:30:00Z",
  "level": "INFO",
  "msg": "charging invoice",
  "env": "production",
  "invoice": "7",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id": "00f067aa0ba902b7"
}
```

Searching your log store for that `trace_id` finds every record of the request,
and the same ID opens the trace in your tracing backend. The correlation works
only with the context methods (`InfoContext`, `ErrorContext`, ...); plain
`Info` carries no context and so no IDs.

With the application's logger wired as above, the request-log middleware needs
no separate correlation setup. It logs failed requests with the request context,
so registering it after telemetry gives those records the request's IDs; see
[Request logging](request-logging.md).

If the driver can export logs, as the OpenTelemetry driver does, the same
records are also sent to the backend with the trace attached. The memory driver
does not export, but it still produces IDs, so logs gain `trace_id` and
`span_id` even with no backend.

Correlation does not redact application logs, supplied exceptions, native SDK
logs, or baggage. Review those values separately before enabling export; the
HTTP span's field exclusions do not apply to the whole logging pipeline. See
[Telemetry Privacy](telemetry-privacy.md#logs).

## Tracing outgoing requests

Wrap the transport of the `http.Client` you use for outgoing calls. Here `req`
is your existing outgoing `*http.Request`, created with the incoming request's
context so the client span becomes a child of the server span:

```go
sender := &http.Client{Transport: t.Transport(nil)}

resp, err := sender.Do(req)
defer resp.Body.Close()
```

A `nil` base transport means `http.DefaultTransport`. The request must carry
the context of the incoming request so that the client span becomes
a child of the server span. The transport starts a client span named after the
method (`GET`), records `http.client.request.duration` for the call, and injects
the trace headers into a clone of the request, so the request you passed is
never modified.

The span records the method, the destination host and port, the scheme, and
the status code. A transport error, or a `5xx` response, marks the span as
failed; `error.type` is the status code or the Go type name of the error, never
its text. The duration histogram is labeled by method, host, port, status, and
`error.type`.

> [!NOTE]
> The span ends when you read the response body to the end or close it, so its
> duration includes reading the body. Always close response bodies, as
> `net/http` already requires. A body that is never closed leaves the span open.

End application work and close response bodies before stopping telemetry.
`Span.End` waits for already-admitted recorder calls; those callbacks must return
and must not synchronously end their own span. `Telemetry.Stop` suppresses new
observer notifications but does not drain or cancel callbacks already admitted.
A timeout cannot forcibly interrupt native work or an earlier serialized
lifecycle call; see [lifecycle](telemetry.md#lifecycle).

If you use the [retrying transport](../http-networking/http-client.md) from the `client`
package, you do not wrap anything yourself: it builds on this transport, so each
attempt is its own client span and each resend carries
`http.request.resend_count`.

### Which hosts receive trace headers

The `traceparent` header, and baggage with it, tells the receiver which trace to
join. Sending it to a third party leaks your trace identifiers and whatever
baggage you carry, so the transport injects headers only into internal
destinations: a loopback, private, or link-local IP address, a name without a
dot (`billing`, as service discovery and Compose provide), and names that end
in `.internal`, `.local`, `.svc`, or `.cluster.local`. The span and the metric
are recorded for every host regardless.

This gate controls injection, not removal: headers already on the caller's
request remain on its clone. Do not forward inbound trace or baggage headers
to an untrusted destination yourself.

To include a partner that you want in your traces, list it. A name starting
with `*.` matches every subdomain but not the bare domain:

```go
sender := &http.Client{Transport: t.Transport(nil,
	func(transport *telemetry.TransportBuilder) {
		transport.PropagationHosts("api.partner.com", "*.mycompany.com")
	},
)}
```

`TransportBuilder.Propagation` replaces the decision with your own function of the request,
`telemetry.PropagateAll` injects everywhere, and `telemetry.PropagateNone`
nowhere. The retrying transport takes the same decision through its `Propagate`
option.

## Testing

The fake telemetry in `telemetrytest` records spans and metrics in memory, so a
test can drive the router and assert what was recorded. In your existing test,
`fake.Telemetry(t)` starts the handle and registers cleanup. `showInvoice` is
your application's handler:

```go
fake := telemetrytest.New()
r := router.New()
r.Use(fake.Telemetry(t).Middleware())
r.Get("/invoices/{id}", showInvoice)

rec := httptest.NewRecorder()
r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/invoices/7", nil))

fake.AssertSpan(t, "GET /invoices/{id}")
```

Set the request's `RemoteAddr` explicitly when testing a trust policy; do not
assume `httptest.NewRequest` supplies a loopback peer. The `traceresponse`
header is on `rec.Header()`.
[Fakes](../testing/fakes.md) describes the fake in full.

For every option, see the [`telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry),
and for the retrying client the [`client` API reference](https://pkg.go.dev/arkoren.dev/core/http/client).
