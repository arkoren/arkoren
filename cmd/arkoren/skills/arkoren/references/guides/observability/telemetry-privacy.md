---
title: Telemetry Privacy
description: Review recorded fields, trace propagation, log exposure, and telemetry export controls.
order: 40
---

# Telemetry Privacy

## Introduction

Exported telemetry leaves your process. Spans, metrics, and logs go to a collector, a
vendor, or a Prometheus server, where more people can read them, they are kept
for longer, and they are rarely protected as strictly as your database. Anything
that reaches a span attribute, a metric label, or an exported log record has
to be something you are content to hand to those systems.

Arkoren's own instrumentation is built on that assumption. Fixed operation and
outcome classifications have a bounded set of values. Names and destinations
also appear in some components and need your cardinality and privacy review;
low cardinality alone does not make data nonsensitive. The instrumentation
records durations, but not the content that flowed through. A request span
carries the route template and the status code, not the URL. Database SQL text is
off by default; explicit `QueryText(true)` exports compiled SQL verbatim, including
literals and comments. A queue span names the job, not the payload.
This page lists what is recorded, what is left out, which
options change it, and where the responsibility passes to you: the spans,
attributes, and logs that your own code adds.

## What Arkoren never records

Arkoren's default built-in span and metric instrumentation excludes the
following content. This is field selection, not a universal redaction filter:
it does not sanitize your explicit attributes, exception text, metric labels,
resource attributes, baggage, native SDK instrumentation, or supplied exporters.
It does not filter log records; [Logs](#logs) describes that separate boundary.
The deliberate SQL-text opt-in is described under [Query text](#query-text).

- **HTTP content.** Request and response bodies, headers (including
  `Authorization` and `Cookie`), query strings, and full paths. The server span
  uses the matched route template such as `/users/{id}`, not the path
  `/users/42`.
- **Query arguments.** The values you pass to `Where` or `Insert`.
- **Cache keys and values**, **storage object keys, prefixes, list cursors, and
  signed URLs**, and **mail addresses, subjects, and bodies**.
- **Queue and broadcast payloads**, and the values in message metadata.
- **Push recipients and contents:** subscription endpoints and keys, device
  identifiers, notification titles, bodies, application data, and receipt IDs.
- **Error text.** Failures record an `error.type` classification: a known
  sentinel, a fixed fallback such as `other` or `error`, or a Go type name,
  depending on the component. Traced operations also set the span status. The
  built-in components never call `RecordError`, because the text of errors often
  quotes data: a duplicate-key error quotes the row, a storage error quotes the
  key.
- **Scheduled task payloads.**
- **Secret names, versions, and values**, and **hashing/encryption inputs and
  outputs**, including passwords, hashes, plaintext, ciphertext, and keys.

The same exclusions apply to metrics. Their label keys are declared when the
instrument is created, and recordings keep only those keys. This restricts the
label schema, not every value's cardinality: fixed operation/outcome classes are
bounded, but caller-selected store/index names and destination hosts can vary.
Keep those names stable and free of personal or tenant-specific information.

## What is recorded and where it could matter

Some facts are not content but can still be sensitive in your environment.
Review this list once, against your own threat model:

| Component | Recorded | Consider |
| --- | --- | --- |
| HTTP server | Method, route template, scheme, protocol, status code | Route templates reveal your API's shape, usually fine |
| HTTP client | Method, `server.address`, `server.port`, scheme, status code | The destination host of every outgoing call, for every host, including third parties |
| Database | Table, SQL verb, database name; SQL text only with QueryText opt-in | Table names still identify schema; opted-in SQL includes literals and comments |
| Feature flags | Flag key, provider, reason, and span-only variant | Keep flag keys and variants free of personal data; subject keys, attributes, and values are omitted |
| Search | Operation, engine and index names, span-only hit count | Index names can identify tenants; queries and documents are omitted |
| Payments | Operation name and outcome | No keys, IDs, amounts, approval URLs, or customer information; see [payment telemetry](instrumented-components.md#payments) |
| MCP | Registered tool name and outcome | Tool names reveal application capabilities; arguments, results, and raw error text are omitted from call telemetry |
| Cache | Operation, store name, hit or miss | Store names |
| Storage | Operation, store name, object size after a write | Sizes can reveal the kind of file |
| Mail | Number of recipients and attachments | Nothing identifying |
| Push notifications | Operation name and outcome | No subscription, token, content, or receipt values |
| Secrets | Operation name and bounded error class | No names, versions, values, or value sizes |
| Hashing and encryption | Aggregate operation durations and outcomes | No inputs or outputs; these contextless operations create no spans |
| Queue, broadcast | Queue or broadcast name, job name, message ID, attempt | Message IDs; job and broadcast names |
| Schedule | Entry or task name, kind, outcome | Names you chose |
| Workflows | Operation, outcome, classified error type | No workflow/step names, run IDs, payloads, signals, or metadata in automatic observations |
| Language models | Model ID, token counts, finish reasons, agent and tool names | Never prompts, replies, tool arguments, or results |
| Resource | Service name, version, environment, supplied extra attributes and native resource metadata | Deployment topology; review application attributes and OTEL_RESOURCE_ATTRIBUTES |

### Query text

`db.query.text` is disabled by default. Leave it off unless you have audited the
statements as safe to export. Opting in records compiled SQL verbatim: separately
bound arguments are omitted, but literal values and comments are not redacted.
For example, bound arguments to `Where` or `Insert` stay separate, whereas SQL
that embeds `'alice@example.com'` or a sensitive comment exports that text when
query text is enabled. Parameter binding alone does not make the whole SQL safe.

If your statements are approved for export, opt in explicitly for that database.
Import `database` from `arkoren.dev/core/service/database`; `driver` is your
existing database driver:

```go
db := database.New(driver, func(connection *database.Builder) {
	connection.QueryText(true)
})
```

Omit this setting, or use `QueryText(false)`, to keep SQL text out. Query execution,
operation labels, and metrics are unchanged. Table names still appear in
`db.collection.name` and in the span name; this option does not hide them. Query
text is a span attribute only and is never a metric label. See the
[database telemetry guidance](../database/query-monitoring.md) before opting in.

### Trace IDs

The server middleware returns the trace ID to the client in the W3C
`traceresponse` header and as a `trace_id` member of error responses. A trace
ID is a correlation value, not an access credential, and it lets a user quote
the exact request to support. Protect access to the trace backend and diagnostic
tools separately. If you do not want clients to see it, set `TraceResponse(false)`
and `ProblemKey("")` in the `telemetry.MiddlewareBuilder` callback. These remove the header and problem
member, respectively. The extra bare-ID header exists only if you request it
with `TraceHeader`. The
[HTTP page](request-tracing.md) shows the options in context.

## Trust at the edge

Trace context also travels in the other direction, as headers on incoming
requests and as headers on outgoing ones. Both directions are gated by default,
because trace headers from the internet are untrusted input and trace headers
sent to a third party leak information.

**Incoming.** The middleware defaults to `telemetry.TrustNone`, including when
you set `TrustRemote(nil)`. It does not extract incoming trace headers from any
peer, including loopback and private addresses. Without a parent already in the
request context, it starts a root trace. The header `X-Forwarded-For` is never
consulted. This blocks header adoption at this middleware, not at every upstream
instrumentation layer: existing valid native OpenTelemetry context still takes
precedence over facade context. Enforce trust before other instrumentation
extracts headers; this middleware does not scrub existing native context or
baggage. Observers report the backend-selected parent, not a different parent
inferred from the facade.

The default changed from `telemetry.TrustInternal`. To continue traces between
trusted workloads, explicitly select `TrustInternal`, `TrustedNetworks`, or
`TrustAll` as described in [Request Tracing](request-tracing.md#trace-context-from-callers).
A private proxy address does not authenticate browser-supplied headers that pass
through it. Keep the default at a public edge unless the proxy strips untrusted
context; narrowing the accepted proxy addresses alone does not solve that risk.

**Outgoing.** The client transport injects `traceparent` and, with the default
OpenTelemetry propagator, `baggage` only for internal destinations: private
and loopback IPs, single-label names such as `users`, and names ending in
`.internal`, `.local`, `.svc`, or `.cluster.local`. Calls to a payment provider
or any public API get no automatically injected trace headers. The transport
does not remove propagation headers your code already placed on the request;
do not copy inbound headers wholesale to third-party calls. The client span and
its metric are recorded for every host regardless of propagation policy and can
still reach your configured telemetry backend. Extend propagation to a partner
you trust with `telemetry.TransportBuilder.PropagationHosts`.

> [!WARNING]
> Baggage is data attached to a trace that every downstream service can read.
> Do not put user identifiers or other personal data in baggage unless every
> service that receives it, and every backend that stores it, may hold that
> data.

Queue and broadcast messages carry the trace context in their metadata,
as `traceparent`, so a consumer can continue the trace. Anyone with read access
to your broker can see those trace IDs, but not the contents of any trace.

Workflows carry trace fields separately from application metadata in
`StartOptions.Trace`. Drivers store or forward them in engine inputs or headers
so worker attempts can continue the trace. Engine readers may see those fields,
including any baggage your propagator injects; restrict submission and engine
access to trusted actors. A carrier is not an authentication mechanism. See
[workflow telemetry](../workflows/overview.md#events-and-telemetry).

## Logs

Logs are the signal most likely to hold personal data, because they hold
whatever your code logged. The telemetry handler adds only two correlation attributes,
`trace_id` and `span_id`, to records logged with a span in the context.

Built-in logs can also contain sensitive data. The
[request logger](request-logging.md) records concrete paths and raw errors, and
[health logs](../application/health-and-readiness.md) include check errors that can
reveal connection details. Span and metric exclusions do not redact those logs.

What happens next depends on the driver. The memory driver exports nothing, so
your logs go only where your log handler sends them. A driver that implements
the log signal, such as the OpenTelemetry driver, additionally exports every
record that its level check accepts, with all of its attributes, to the
collector. If you have been careful about what your logs contain because they
only reached your own disk, check again before you enable log export, or
leave logs out with [`telemetry.Compose`](telemetry.md#combining-drivers) and
keep the log signal on your existing pipeline.

The memory driver can retain a diagnostic copy of logs and explicit exception
text even without external export; restrict access to that history too. Native
SDK logging, custom exporters, and application log handlers have their own data
and error behavior. By default the OTEL SDK error handler writes native errors
to the application's unwrapped logger, and Prometheus gathering errors can reach
logs or a scrape error response when no metrics can be served. These are not
universal redaction boundaries.

The OTEL driver omits the supplied address from explicit endpoint-validation
errors and rejects URL credentials, nonempty query strings, and fragments.
That protects this validation path, not every native SDK or exporter diagnostic.
Use header settings for collector authentication and protect native error logs; see
[Telemetry Drivers](telemetry-drivers.md#opentelemetry).

## Your own instrumentation

You own the spans, attributes, events, and metrics you add; the toolkit cannot
know which of their values are sensitive. These habits help keep them clean:

- Put identifiers of kinds, not of people, into attributes: `plan=pro`,
  not `email=alice@example.com`. If you need to find one user's requests, put
  the user ID in a log line that carries the trace ID, not in a span attribute,
  and restrict that log's retention and readers. If log export is enabled,
  that identifier leaves the process too.
- Keep metric keys low-cardinality. A user ID or a URL as a label produces one
  series per value, adding cost and exposing the identifier for as long as the
  backend retains it.
- Name spans from a fixed set: `charge`, not `charge order 123`.
- Treat `span.RecordError(err)` as exporting `err.Error()`. Call it only for
  errors whose text you know, and prefer `span.SetStatus(telemetry.Error, "...")`
  with a fixed description, plus an `error.type`-style attribute, for errors that
  may quote data.
- Do not set a span attribute from a request header, cookie, or body without
  deciding that the value may leave the process.
- Review resource attributes and `OTEL_RESOURCE_ATTRIBUTES` as exported data.
  Supplied native providers own their resource and export configuration. A
  Prometheus `target_info` metric exposes resource label values on the scrape
  endpoint; UTF-8 and descriptor validation do not remove sensitive content.
- Declared label keys filter the schema, not the meaning of label values.
  Admission limits and copied instrument specifications protect counts and
  ownership, not confidentiality. Native collectors are outside those managed
  instrument checks.

## Reducing what leaves the process

Beyond the options above, you can reduce volume and exposure at the driver. The
OpenTelemetry driver accepts a sampler, so only a share of traces is recorded
and exported. Import `oteltelemetry` from
`arkoren.dev/driver/otel/oteltelemetry` and `sdktrace` from
`go.opentelemetry.io/otel/sdk/trace`:

```go
driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
	instrumentation.Sampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.1)))
})
```

Sampling reduces how much is exposed but is not a privacy control, since the
sampled traces carry the same attributes. Use `OTEL_TRACES_EXPORTER=none`,
`OTEL_METRICS_EXPORTER=none`, or `OTEL_LOGS_EXPORTER=none` to switch a signal
off without changing code, and `OTEL_SDK_DISABLED=true` to disable recording and
export for every signal; see [Drivers](telemetry-drivers.md#opentelemetry).
The per-signal exporter variables control providers built by this driver, not
native providers your application supplies. Configure supplied providers to
disable their export, or disable this driver's recording/export with
`OTEL_SDK_DISABLED`. The configured propagator remains active even with the SDK
disabled. Use the HTTP trust and propagation settings above to control headers;
disabling export is not a substitute for a propagation policy.
Audit independently used native SDK pipelines separately; disabling this adapter
does not establish that no other telemetry leaves the process.

The Prometheus driver serves its metrics from a handler that you register on your
router, like any other route. Built-in field selection omits operation content,
but your label values, resource metadata, and native collectors can still expose
sensitive data. Put the endpoint on an internal port or behind authentication
rather than on your public listener.

## Recommendations

1. Keep the default `QueryText(false)`. Opt in with `QueryText(true)` only after
   auditing complete SQL statements, including literals and comments, as safe
   for verbatim export.
2. Write values into SQL only as arguments.
3. Decide per environment whether log export is acceptable before enabling a
   driver that implements it.
4. At a public edge, keep the default rejection of incoming trace context.
   Opt in only for trusted workloads, and do not put personal data in baggage.
5. Keep metric keys bounded and span names fixed.
6. Protect the metrics endpoint.
7. Review the attributes your own code adds, as you would review a log line.

For the options and functions named here, see [the `telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry)
and [the `database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
