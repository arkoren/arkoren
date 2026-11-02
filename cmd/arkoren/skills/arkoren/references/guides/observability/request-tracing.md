---
title: Request Tracing
description: Trace HTTP boundaries, correlate failures, and control incoming trust and outgoing propagation.
order: 50
summary:
  - Trace router requests and outgoing calls with accurate names, fields, and response-body lifetimes.
  - Return correlation IDs and connect context-aware logs to request spans.
  - Choose incoming trust and outgoing injection independently and verify instrumentation locally.
---

# Request Tracing

An incoming request can wait on several outgoing calls. Instrument both HTTP
boundaries to see where that time goes: server middleware starts the request's
span, and the client transport creates child spans from the context you pass
on. A started [telemetry handle](telemetry.md#shared-handle) supplies the trace
driver and metric driver independently.

With an unstarted handle, both integrations pass work straight through. With
only a metrics backend, measurements can work without creating trace IDs. Neither
integration records bodies, headers, query strings, full paths, or raw error
text; [privacy exclusions](telemetry-privacy.md) do not apply to your logs.

## Incoming Requests

With existing application `app` and Arkoren router `r`, register telemetry before
the rest of the stack. Import `requestlog` from
`arkoren.dev/core/http/middleware/requestlog` and `router` from
`arkoren.dev/core/http/router`:

```go
handle := app.Telemetry()
r.Use(
	handle.Middleware(),
	requestlog.Middleware(func(log *requestlog.Builder) {
		log.Logger(app.Logger())
	}),
	router.Recover(),
)
```

The span remains open through downstream middleware, error rendering, and
recovered panics. Request logging uses the span context and observes errors
produced by recovery. Mount this middleware through an Arkoren router: enabled
instrumentation requires its lifecycle hooks and panics if those hooks are
absent, before starting an observation.

If explicit handle wiring is unavailable, package-level
`telemetry.Middleware()` resolves the process default on each request. Import
`telemetry` from `arkoren.dev/core/service/telemetry`; later options use that
package too. Setting the default later takes effect, but does not start it.

## Recorded Fields

A matched request creates a server span such as `GET /reports/{id}`. The
template, not `/reports/19`, keeps names bounded. Unmatched routes use the
method alone. Unknown methods use `HTTP` in names and `_OTHER` in method
attributes, so arbitrary method strings do not create new values.

| Signal | Fields Or Label Keys |
| --- | --- |
| Server span | `http.request.method`, `url.scheme`, `network.protocol.name`, `network.protocol.version`, matched `http.route`, final `http.response.status_code` |
| `http.server.request.duration` (seconds) | `http.request.method`, `http.route`, `http.response.status_code`, `url.scheme`, `error.type` |
| `http.server.active_requests` (up-down counter) | `http.request.method`, `url.scheme` |

Only 5xx responses mark a server span failed, with `error.type` equal to the
status code and a fixed description such as `HTTP 503`. A 4xx leaves status
unset; it does not mean the caller achieved its goal. A returned error can
therefore produce a [failure log](request-logging.md) without a failed span.

Duration ends after router response handling, including returned-error
rendering. The observed writer status does not prove the caller received the
response. Raw writes after a connection hijack are not ordinary response-body
observations.

## Incoming Trust

The default is `telemetry.TrustNone`, including `TrustRemote(nil)`: no peer's
incoming propagation headers are extracted, even from loopback, private, or
link-local addresses. Without an existing parent in the request context, the
request starts a root trace. `X-Forwarded-For` is never consulted.

This prevents callers from choosing trace identities, influencing sampling, or
introducing baggage through this middleware. It does not undo extraction by
another instrumentation layer. A valid native OpenTelemetry parent already in
the context wins over conflicting facade identity, even when remote or
non-recording; otherwise a valid facade parent is adopted. Apply trust policy
before any upstream extraction. Facade observers report that actual selected
parent, preserving IDs, sampling, remote bit, and tracestate.

> [!WARNING]
> A private reverse-proxy address does not authenticate browser-supplied trace
> headers. Keep `TrustNone` at a public edge unless the proxy strips untrusted
> context and forwards only context from trusted workloads. Narrowing proxy
> addresses alone does not make those headers safe.

If a restricted workload network establishes that trust, opt in narrowly.
Import standard-library `net/netip`; this is an alternative middleware
construction, not an additional tracing layer:

```go
r.Use(handle.Middleware(func(policy *telemetry.MiddlewareBuilder) {
	policy.TrustedNetworks(netip.MustParsePrefix("10.42.0.0/16"))
}))
```

`TrustedNetworks` tests the immediate peer against those prefixes. For a more
specific request decision, use `TrustRemote` with your own predicate.
`TrustAll` is appropriate only when all reachable callers are trusted workloads.

`TrustInternal` is another explicit opt-in: it accepts loopback, RFC 1918
private IPv4, IPv6 `fc00::/7`, and link-local unicast peers, rejecting public or
unparseable addresses. It was the former default. Services migrating from that
behavior must explicitly choose it after checking their deployment boundary;
outgoing propagation defaults have not changed.

## Response Identity

When a span has valid identity, the middleware returns the W3C `traceresponse`
header as `00-<trace-id>-<span-id>-<flags>` and adds `trace_id` to problem
responses. A client can quote that identity to support; it is not an access
credential or a guarantee the backend retained the trace.

If your clients also need a bare-ID header, configure it at construction:

```go
r.Use(handle.Middleware(func(policy *telemetry.MiddlewareBuilder) {
	policy.TraceHeader("X-Trace-ID")
}))
```

No extra bare-ID header exists by default. `ProblemKey("request_id")` renames
the problem member; `ProblemKey("")` omits it. `TraceResponse(false)` disables
the W3C response header. Disable any configured `TraceHeader` separately if
you want no response identity. Invalid/no-op spans add none of these fields.
Request propagation uses `traceparent`, not an arbitrary correlation header.

## Correlated Logs

With the application logger and the request context, your handler can emit a
record tied to the same span:

```go
app.Logger().InfoContext(req.Context(), "report requested", "format", "pdf")
```

The wrapped handler adds `trace_id` and `span_id`. Plain `Info` has no context
and cannot add them; an unrelated slog handler does not gain correlation from
middleware alone. Memory telemetry supplies IDs without external trace export;
a log-capable driver can export the whole record too.

This does not redact application attributes, exceptions, baggage, or native
SDK logs. In particular, request failure logs include concrete paths and raw
errors even though HTTP spans omit them. Review [Logs](telemetry-privacy.md#logs)
before enabling export.

## Outgoing Requests

Wrap the base `http.RoundTripper` used by your existing client. Import
standard-library `net/http`; nil selects `http.DefaultTransport`:

```go
sender := &http.Client{Transport: handle.Transport(nil)}
```

Create outgoing requests with the incoming context so the client span can be a
child of the server span. With a validated internal service URL `endpoint`:

```go
outgoing, err := http.NewRequestWithContext(req.Context(), http.MethodGet, endpoint, nil)
```

After handling request-construction errors, send and retain the success guard
before accessing the returned body:

```go
response, err := sender.Do(outgoing)
if err != nil {
	return err
}
defer response.Body.Close()
```

Consume the response as your application requires and handle any body read or
close failure. The span ends at EOF or close, so duration includes reading the
body. Always close response bodies to release HTTP resources and complete spans.
End this work before stopping telemetry.

The transport clones requests before injection and does not mutate the caller's
request. Client spans use method-only names and record method, scheme,
`server.address`, `server.port`, and status. The seconds histogram
`http.client.request.duration` uses `http.request.method`, `server.address`,
`server.port`, `http.response.status_code`, and `error.type`.
Transport failures, non-EOF body-read failures, and 5xx responses mark the span
failed. Error labels are the Go type or status code, not diagnostic text.
Responses without a body and protocol upgrades finish without waiting for body
consumption.

The [retrying HTTP client](../http-networking/http-client.md) already uses this
instrumentation: each attempt has its own span and resends add
`http.request.resend_count`. Do not wrap it again with telemetry. Package-level
`telemetry.Transport` instead follows the process default per request.

## Outgoing Policy

The transport records every destination but injects context only where its
propagation predicate allows it. By default `PropagateInternal` accepts
loopback/private/link-local IP literals, single-label names such as `reports`,
and names ending in `.internal`, `.local`, `.svc`, or `.cluster.local`.
Matching is case-insensitive and ignores the port; a public name merely
resolving to a private address is not automatically internal.

Injection can reveal trace IDs and, with the default OpenTelemetry propagator,
baggage. Do not copy inbound headers wholesale: a denied injection does not
remove propagation headers you already placed on the request.

If an approved partner should receive context, construct this alternative
client transport:

```go
sender := &http.Client{Transport: handle.Transport(nil,
	func(policy *telemetry.TransportBuilder) {
		policy.PropagationHosts("api.partner.example", "*.services.example")
	},
)}
```

The `*.` form matches subdomains, not the bare domain. `Propagation` replaces
the predicate with your own request function; `PropagateAll` injects everywhere
and `PropagateNone` nowhere. These choices govern injection, not collection or
removal of existing headers. The retrying client's equivalent option is
`Propagate`. Keep baggage nonsensitive and restrict which receivers may see it.

## Verify Tracing

In an existing router test, use `telemetrytest` from
`arkoren.dev/core/service/telemetry/telemetrytest`, plus standard packages
`net/http` and `net/http/httptest`. The fake starts its handle and registers test
cleanup. With your application's existing `showReport` handler:

```go
fake := telemetrytest.New()
r := router.New()
r.Use(fake.Telemetry(t).Middleware())
r.Get("/reports/{id}", showReport)
```

Drive the router, not its isolated middleware wrapper, and assert the template
name:

```go
response := httptest.NewRecorder()
r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/reports/19", nil))
fake.AssertSpan(t, "GET /reports/{id}")
```

Inspect `response.Header()` for `traceresponse`. Set `RemoteAddr` explicitly
when testing trust: `httptest.NewRequest` is not evidence of a loopback peer.
The fake proves local instrumentation, not remote export; [Drivers](telemetry-drivers.md)
provides backend verification.

For every HTTP option, see the
[`telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
