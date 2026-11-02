---
title: Telemetry Privacy
description: Review automatic fields, logs, diagnostic capture, and propagation before retaining or exporting observations.
order: 60
summary:
  - Separate automatic field exclusions from raw logs, native telemetry, and application-supplied data.
  - Review SQL-text opt-in, propagated context, response identity, and diagnostic exposure.
  - Control each export path without confusing sampling or disabled export with privacy protection.
---

# Telemetry Privacy

Before retaining or exporting telemetry, decide who may read each signal and
how long it may remain available. A collector, scrape endpoint, or local
diagnostic tool can expose information outside the application operation that
produced it. Low cardinality does not make a field nonsensitive.

Arkoren limits fields in its automatic span and metric instrumentation. That
is not a universal redaction layer: your logs, attributes, exceptions, resource
metadata, baggage, native SDKs, exporters, and tool results have separate
boundaries.

## Field Boundaries

By default, built-in spans and metrics omit operation content:

- HTTP bodies, headers, query strings, and concrete paths. Server spans use
  route templates; client spans still record destination hosts.
- Database bound arguments and SQL text. Explicit SQL-text opt-in is a
  separate exception described below.
- Cache keys/values; storage object keys, prefixes, cursors, and signed URLs;
  mail addresses, subjects, bodies, and receipts.
- Queue/broadcast payloads and metadata values as observation fields;
  scheduled payloads; workflow inputs, outputs, run IDs, signals, and metadata.
- Push recipients, endpoints, tokens, encryption material, contents, application
  data, and receipts; payment IDs, amounts, customer details, and approval URLs.
- Secret names, versions, values, and sizes; hashing/encryption inputs, outputs,
  passwords, hashes, ciphertext, plaintext, and keys.
- Model prompts/replies and tool arguments/results. Provider response IDs and
  other model metadata can still appear on spans.

Automatic failures use classified `error.type`, not raw diagnostic text. The
classification can be a known sentinel, fixed fallback, or Go type depending
on the component. These exclusions do not sanitize data passed through an
application logger or explicit `span.RecordError(err)`.

Metric `Keys` filters the **schema**, not the values. Only declared keys survive,
but a declared user-ID label still creates high-cardinality personal data. The
[component catalog](instrumented-components.md) lists exact labels and exceptions.

## Recorded Metadata

Review these retained names and measurements against your own data policy:

| Boundary | Recorded Metadata | Review Concern |
| --- | --- | --- |
| HTTP server | Method, route template, scheme, protocol, status | API structure |
| HTTP client | Method, host/port, scheme, status | Destination topology, including third parties |
| Database | System, namespace, recognized verb, known table | Schema and database identity |
| Cache/storage | Operation and store name; cache hit state; span-only write size | Store naming and file-size patterns |
| Feature flags | Flag key, provider, reason; span-only variant | Tenant or personal data in developer-selected names |
| Search | Operation, engine/index names, span-only hit count | Tenant-specific index names |
| Mail | Operation, span-only recipient/attachment counts | Traffic patterns without addresses |
| Push/payments/secrets | Operation and classified failures | Operational activity without business payloads |
| Hash/crypto | Aggregate operations, outcomes, bounded errors | Activity and upgrade-needed observations, not inputs |
| Queue/broadcast | Destination/system; span-only message ID, job, attempt | Identifiers and destination names |
| Scheduling | Entry/task name, kind, outcome | Developer-selected names |
| Workflows | Operation, outcome, classified failure | Command/attempt activity, not terminal execution proof |
| MCP | Managed registered tool name and outcome | Available application capabilities |
| Language models | Provider/model IDs, response ID, finish reasons, usage, agent/tool names | Provider correlation values and capability names |
| Resource | Service, version, environment, extra/native attributes | Deployment identity and topology |

Keep names stable and free of personal or tenant-specific data. Native resource
attributes and `OTEL_RESOURCE_ATTRIBUTES` are exported data too. Prometheus
`target_info` exposes resource label values on its scrape endpoint; descriptor
or UTF-8 validation is not confidentiality protection.

## Query Text

Leave `database.Builder.QueryText` off unless you have audited complete SQL
statements for export. Enabling it records compiled SQL **verbatim**: separately
bound arguments remain omitted, but embedded literals and comments are not
redacted. Parameter binding alone does not make the whole statement safe.

If your application has approved those statements, import `database` from
`arkoren.dev/core/service/database` and opt in for that configured `backend`:

```go
db := database.New(backend, func(connection *database.Builder) {
	connection.QueryText(true)
})
```

This enables the span-only `db.query.text` field; it never adds SQL text to
metric labels. Omission or `QueryText(false)` keeps it out without disabling
execution measurements. Table names remain visible in spans and
`db.collection.name`. See [database monitoring](../database/query-monitoring.md).

## Context Boundaries

Trace propagation is serialized context crossing a trust boundary. It is not
authentication, authorization, or a consequence of whether export is enabled.

### Incoming Context

HTTP defaults to `TrustNone`, including `TrustRemote(nil)`. The middleware
extracts no incoming headers, even from private or loopback peers; it starts a
root only if no parent already exists in the context. `X-Forwarded-For` is
ignored. Existing valid native OpenTelemetry context can still take precedence
over facade identity, so enforce trust before upstream instrumentation extracts
headers. This middleware does not scrub previously installed context or baggage.

Opt into `TrustedNetworks`, `TrustInternal`, or another predicate only for
trusted workloads. A private proxy can forward an untrusted browser's headers
unchanged: trusting the proxy address does not establish who supplied them.
The former implicit internal-peer trust now requires explicit opt-in. See
[Incoming Trust](request-tracing.md#incoming-trust).

### Outgoing Context

HTTP injects context by default only into internal IP literals and names:
loopback/private/link-local addresses, single-label names, and `.internal`,
`.local`, `.svc`, or `.cluster.local` suffixes. It still records client spans
and metrics for external destinations. Allowlisting a partner changes
injection, not observation.

Denied injection does not remove headers already present on the caller's
request. Do not forward inbound headers wholesale. Baggage can contain
application values readable by every receiving service; keep it nonsensitive
and review each receiver's access and retention. The default OpenTelemetry
propagator supports baggage; the memory driver propagates trace context and
tracestate. [Outgoing Policy](request-tracing.md#outgoing-policy) owns configuration.

Queue and broadcast metadata can carry trace context readable by broker users.
Workflow `StartOptions.Trace` carries it separately from application metadata
through supported engine inputs/headers; engine readers can see those values
too. Restrict submission and engine access. HTTP trust settings do not
authenticate these carriers, and raw `Extract` does no peer authentication.
See [workflow observation](../workflows/running-workflows.md#events-and-telemetry).

### Response Context

The server normally returns `traceresponse` and a problem `trace_id`. These are
correlation values, not backend access credentials. If your policy prohibits
them, construct middleware with both outputs disabled. Here `handle` is your
configured telemetry and `r` your existing router; import `telemetry` from
`arkoren.dev/core/service/telemetry`:

```go
r.Use(handle.Middleware(func(policy *telemetry.MiddlewareBuilder) {
	policy.TraceResponse(false)
	policy.ProblemKey("")
}))
```

Also omit or clear `TraceHeader` if you previously selected an extra bare-ID
header. These settings suppress response identity, not recording or outgoing
propagation. Protect the backend and [diagnostic endpoint](../diagnostics/capture-setup.md#protect-registration)
independently.

## Logs

Logs contain what their producer supplies. The telemetry handler adds correlation
IDs to normal context-aware log output; it does not remove message text or
attributes. [Request Logging](request-logging.md) records concrete paths and
raw errors, and [health reporting](../application/health-and-readiness.md) can
include check errors revealing connection details.

Memory telemetry exports nothing externally but can retain a local copy of
emitted logs. OpenTelemetry can additionally export every accepted record with
its attributes. Review the complete logging pipeline before enabling that
signal; log handlers and native SDK instrumentation have their own behavior.

To leave application logs on your existing pipeline, omit the log selection
from [driver composition](telemetry-drivers.md#combining-drivers) and disable
any unwanted driver-owned log exporter. Composition routes facade signals;
it does not reconfigure a supplied native provider's exporter.

Native errors are not universally redacted. The default OpenTelemetry error
handler writes SDK errors to the application's unwrapped logger. Prometheus
gathering errors can reach logs or the scrape error response if no metrics can
be served. OpenTelemetry's explicit endpoint-validation errors omit the
supplied address and reject URL credentials, nonempty queries, and fragments;
that protection does not extend to every native diagnostic. Use header-based
collector authentication and protect the logs containing native failures.

## Diagnostic Access

Local capture is another retention destination, even without external export.
It can retain your explicit exception text, arbitrary attributes, and raw log
records. Passive observation reflects facade calls even when the backend span
is unsampled, so backend sampling does not promise equivalent local omission.

Diagnostic MCP call telemetry omits arguments and results, but that does not
redact the tool's returned data. Read-only trace and log tools can disclose raw
application content. Restrict tool selection, authenticate operators, authorize
capabilities, and bound capture before exposing them; follow
[Capture Setup](../diagnostics/capture-setup.md#protect-registration).
Retention count limits and cursor paging are not secrecy or durability controls.

## Custom Attributes

For your own instrumentation, choose values you are willing to retain in every
enabled destination:

- Use fixed span names such as `report render`, not `render report 19`.
- Use bounded kind labels, not user IDs, email addresses, full paths, or
  arbitrary request values. Moving personal data from a name to an attribute
  does not make it private.
- Prefer fixed failure descriptions and classes. `RecordError(err)` captures
  `err.Error()`; use it only for approved error text.
- Review event attributes, resource fields, and baggage just like log fields.
  A copied container protects ownership, not its contents.
- Keep native collectors and exporters under their own policy. Managed
  instrument admission limits do not cover arbitrary native collectors or
  other independently configured pipelines.

If you need a user identifier for an investigation, put it only in an explicitly
approved, access-controlled log destination. Enabling log export or diagnostic
capture changes that destination set too.

## Export Controls

Sampling reduces trace volume, not the sensitivity of sampled fields. For
driver-owned OpenTelemetry pipelines, use `OTEL_TRACES_EXPORTER=none`,
`OTEL_METRICS_EXPORTER=none`, or `OTEL_LOGS_EXPORTER=none` to disable an unwanted
signal. These do not disable caller-supplied providers: configure those providers
yourself. `OTEL_SDK_DISABLED=true` disables recording/export in this adapter,
including supplied providers, but leaves configured propagation active.

Audit other native pipelines independently. Disabling this adapter is neither
a promise that no telemetry leaves the process nor a way to strip trace headers.
Use [Drivers](telemetry-drivers.md#opentelemetry) for native ownership and
shutdown details, and the HTTP boundary policies for headers.

Prometheus exposes labels, resource metadata, and native collectors through the
handler you mount. Keep that endpoint on a restricted listener or behind
authentication; do not assume its field validation supplies access control.
Memory capture stays local only until your own code or tools expose it.

For the options discussed here, see the
[`telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry)
and [`database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
