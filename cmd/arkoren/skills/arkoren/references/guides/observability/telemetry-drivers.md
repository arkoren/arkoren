---
title: Drivers
description: Choose local capture, OTLP export, or Prometheus scraping and manage each driver's resources.
order: 20
summary:
  - Compare memory capture, OTLP export, and scraped metrics by supported signal.
  - Configure each driver with explicit retention, transport, and native-resource ownership boundaries.
  - Verify observations and compose signals without assuming startup proves delivery.
---

# Drivers

Choose **Memory** for bounded process-local history and correlation without a
backend, **OpenTelemetry** for OTLP export, or **Prometheus** for scraped
metrics. You can select a different driver per signal. Start with the signals
you need, and review [Telemetry Privacy](telemetry-privacy.md) before exporting
logs or exposing a metrics endpoint.

## Compatibility Matrix

This compares Arkoren's adapters, not the vendors' entire products. Unsupported
signals are no-ops unless a capable observer supplies them. Recording also
depends on startup and the selected signal configuration.

| Driver | Traces | Metrics | Logs | OTLP Export | Scrape Handler |
| --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | Yes | Yes | Yes — bounded local capture | No | No |
| [OpenTelemetry](#opentelemetry) | Yes | Yes | Yes | Yes — owned pipelines; supplied providers choose export | No |
| [Prometheus](#prometheus) | No | Yes | No | No | Yes — requires a usable gatherer |

Startup does not prove remote delivery. Memory is not durable storage;
Prometheus does not create trace IDs. Its exemplars require sampled context
from another trace source.

For all following alternatives, import `application` from
`arkoren.dev/core/application` and `telemetry` from
`arkoren.dev/core/service/telemetry`. Give your chosen `driver` to the application:

```go
app := application.New(func(app *application.Builder) {
	app.Telemetry(telemetry.New(driver))
})
```

Construction does no lifecycle I/O. The application starts telemetry before
user startup callbacks and stops it last. Name, version, and environment become
the resource identity. [Lifecycle Rules](telemetry.md#lifecycle-rules) covers
manual startup, terminal facade failures, and shutdown budgets.

## Memory

The bundled `arkoren.dev/core/service/telemetry/memorytelemetry` package needs
no additional module or backend. It generates W3C trace IDs, propagates
`traceparent` and `tracestate`, retains finished spans and logs, and aggregates
metrics locally. Your normal log handler can still send logs elsewhere.

### Local Capture

Keep the store handle so your local inspector can read it, then use `driver`
in the application wiring above:

```go
import "arkoren.dev/core/service/telemetry/memorytelemetry"

driver := memorytelemetry.New()
```

After startup, run a request through [Request Tracing](request-tracing.md).
A `traceresponse` header and a retained span confirm identity and capture.
With standard-library `fmt`, inspect completed work:

```go
for _, span := range driver.Spans() {
	fmt.Println(span.Name, span.Context.TraceID.String())
}
```

This is a process-local snapshot, not a query across replicas. Protect any
operator interface that reads it. [Capture Setup](../diagnostics/capture-setup.md)
owns application capture and tool registration; this constructor opens no
listener. For assertions rather than operator inspection, use
[telemetry fakes](../testing/fakes.md).

For a standalone store's tool definitions, `driver.MCPTools("memory")` returns
prefixed trace, log, and metric tools without registering or starting anything.
Check their `Err()` results and select/register them on a protected endpoint as
in [Capture Setup](../diagnostics/capture-setup.md#protect-registration).
The store still needs its owner to start and stop it; do not start it separately
if application telemetry already owns that lifecycle. See the
[tool lookup](../diagnostics/diagnostic-tools.md#owner-prefixes) for names.

### Retention Limits

If the default history is too large, construct an alternative store:

```go
driver := memorytelemetry.New(func(store *memorytelemetry.Builder) {
	store.MaxSpans(300)
	store.MaxLogs(300)
	store.MaxSeries(100)
})
```

Each default is 1000. Span and log rings evict their oldest records at capacity.
`MaxSeries` bounds label combinations **per instrument**; a new combination
past capacity is dropped without replacing existing series. Zero or negative
limits disable the corresponding retention. Setting all three to zero preserves
ID generation and propagation without retaining history; nonpositive span capacity
also disables span attribute recording in this driver.

These are count limits, not a total byte or instrument-definition budget. Keep
instrument names, keys, and value sizes fixed and trusted.

### Snapshot Ownership

`Spans()`, `Logs()`, and `Metrics()` return detached eager collections. Spans and
logs are oldest first; metrics follow instrument creation order. Later writes,
ring eviction, and `Reset()` do not alter an existing snapshot. Reset clears
points and history but retains metric registrations and does not restart log
sequence numbers. `LogsAfter(seq)` selects retained entries with greater
sequence numbers; advance from the last observed sequence, not a filtered count.
Empty log snapshots remain nil; empty span and metric snapshots are allocated
empty collections. Snapshot copies detach owned attributes, events, keys,
points, and label values; the collection wrapper itself does not acquire ownership.

Malformed W3C context, uppercase hex, and zero IDs are rejected. Valid parent
identity is reported by the recorder; generated IDs are correlation values,
not credentials. Resource containers are copied at startup and inspection.
Stop synchronizes with storage commits but cannot interrupt caller-supplied
error formatting or `LogValuer` work. Formatting that finishes after stop cannot
append a new log. Process exit discards all history.

For all settings and snapshot types, see the
[`memorytelemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry/memorytelemetry).

## OpenTelemetry

Choose this adapter when your collector accepts OTLP traces, metrics, or logs.
It uses the OpenTelemetry Go SDK and enables Go runtime metrics by default.
Install from your application's module directory:

```sh
go get arkoren.dev/driver/otel
```

Import the adapter and construct the driver for the shared application wiring:

```go
import "arkoren.dev/driver/otel/oteltelemetry"

driver := oteltelemetry.New()
```

Standard `OTEL_*` variables configure driver-owned pipelines. HTTP/protobuf is
the default protocol (port 4318); `grpc` uses port 4317. Signal-specific endpoint
and protocol variables take precedence over generic environment settings;
explicit nonempty builder settings take precedence over the environment.

### Collector Settings

Use HTTPS/TLS for a remote collector. For an approved endpoint and credentials
loaded by your application as `endpoint` and `headers`:

```go
driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
	instrumentation.Endpoint(endpoint)
	instrumentation.Headers(headers)
})
```

`Endpoint` accepts `host[:port]` or an HTTP(S) URL. Explicit endpoints for owned
exporters are validated at startup: unsupported schemes, malformed addresses,
URL credentials, nonempty queries, and fragments are rejected without echoing
the address. Put authentication in headers. Headers are copied; empty settings
retain native fallback behavior.

An `http` URL or `Insecure(true)` disables TLS. `Insecure(false)` does not force
TLS against an HTTP URL or native environment policy. For a trusted local gRPC
receiver only, use `Protocol(oteltelemetry.ProtocolGRPC)` and `Insecure(true)`.

To retain a fraction of new traces, import `sdktrace` from
`go.opentelemetry.io/otel/sdk/trace` and use a separate construction variant:

```go
driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
	instrumentation.Sampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.1)))
})
```

This samples about ten percent of new traces while following a parent's sampling
decision. It reduces volume, not the sensitivity of retained fields. Incoming
parent trust remains a separate [HTTP policy](request-tracing.md#incoming-trust).

For owned pipelines, `OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER`, and
`OTEL_LOGS_EXPORTER` accept `otlp` (default) or `none`. Other exporters and
`http/json` are unsupported and fail startup. `OTEL_SDK_DISABLED=true` disables
this adapter's recording and export, including supplied providers, but leaves
the configured propagator active. None of these settings removes propagation
headers or disables independent native SDK pipelines.

### Native Providers

If you already own a native tracer provider `provider`, share it explicitly:

```go
driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
	instrumentation.TracerProvider(provider)
})
```

`MeterProvider`, `LoggerProvider`, and `Propagator` accept their corresponding
native interfaces too. Keep those handles for native calls: the adapter exposes
no provider getters and does not replace global providers. Supplied providers
retain their resource, sampling, and exporter configuration; connection settings
and `Sampler` do not reconfigure them. Per-signal exporter environment variables
do not disable them either.

The adapter calls a borrowed provider's `ForceFlush(context.Context) error` if
available, but never shuts that provider down. Stop the adapter before your own
final provider shutdown. Runtime callbacks installed on a borrowed meter remain
the adapter's responsibility and are unregistered on stop or failed startup.

Nil interface settings restore fallback construction. Typed-nil provider or
propagator pointers are invalid at startup; a typed-nil sampler is invalid when
the adapter builds the tracer provider. An unused sampler is ignored with a
supplied tracer provider.

`RuntimeMetrics(false)` disables runtime instrumentation.
`GlobalErrorHandler(false)` leaves the process-wide SDK error policy alone.
Otherwise startup installs an SDK error handler using the application's
unwrapped logger and does not restore it on stop or failure. Native exporter
errors can contain sensitive diagnostics; this is not a redaction boundary.

### Export Lifecycle

Startup builds the resource and owned providers; partial or canceled acquisition
releases acquired resources. A failed **facade** startup is terminal even if the
underlying driver could retry. The application supplies `service.name`,
`service.version`, and `deployment.environment.name`. Empty identity fields
fall back to environment configuration; without a service name the fallback is
`unknown_service:<executable>`. Review extra resource attributes as exported data.

Default trace/log batching and periodic metric export run outside the ordinary
request path. Startup success does not prove a later export reached a collector.
Drain work, close response bodies, and end spans before shutdown. For each owned
pipeline, stop preserves `ForceFlush` errors using the supplied context, then
completes SDK shutdown with an uncanceled context in reverse acquisition order.
Shutdown can perform final exports and outlast the flush deadline; native
timeouts still apply. No teardown goroutine is abandoned. Borrowed flush hooks
and serialized lifecycle calls also require cooperation.

A valid native OpenTelemetry parent wins over a conflicting facade parent,
including remote or non-recording native parents. Otherwise a valid facade
parent is adopted. Recorder and observer parent snapshots preserve the selected
IDs, sampling, remote bit, and tracestate; roots report zero. The returned context
still carries the native child. Native-only spans are not automatically part of
facade observation.

Metric values use string labels with empty values omitted. Log export maps slog
severity, flattens groups into dotted keys, and attaches the context's span;
all emitted record content remains subject to your [log policy](telemetry-privacy.md#logs).

### Verify Exports

With your collector configured, start the application, send a request through
[Request Tracing](request-tracing.md#incoming-requests), and find the span under
your service name in the backend. Check asynchronous exporter errors if it is
missing; a response trace ID alone does not prove export.

For a trusted local OTLP/HTTP receiver that accepts traces only, run your
already-instrumented application with metrics and logs disabled:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
  OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
  OTEL_METRICS_EXPORTER=none OTEL_LOGS_EXPORTER=none go run .
```

The receiver must already be running and configured to retain or forward traces.
Verify the request there, then stop your application normally to flush its last
batch. See the
[`oteltelemetry` API reference](https://pkg.go.dev/arkoren.dev/driver/otel/oteltelemetry)
for every provider and connection option.

## Prometheus

Choose this adapter for metrics pulled from an HTTP endpoint. It creates an
isolated registry by default and supplies no traces or exported logs. Install:

```sh
go get arkoren.dev/driver/prometheus
```

### Scrape Setup

Import `prometheustelemetry` from
`arkoren.dev/driver/prometheus/prometheustelemetry`. Retain the handle to mount
its scrape handler, and give it to the shared application wiring:

```go
driver := prometheustelemetry.New()
```

Before mounting, establish a restricted [operator listener](../application/health-and-readiness.md)
or authenticated gateway. The handler has no built-in access control and can
expose label values, resource metadata, and native collector errors. With that
listener's router `r` and `router` from `arkoren.dev/core/http/router`:

```go
r.Get("/metrics", router.HTTP(driver.Handler()))
```

The handler serves Prometheus text or negotiated OpenMetrics. It can serve
caller-owned collectors before startup and after stop; a successful scrape is
not proof that Arkoren recording has started.

### Metric Budgets

To lower retained state, construct an alternative driver with explicit budgets:

```go
driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
	metrics.MaxSeries(200)
	metrics.MaxInstruments(100)
})
```

Both defaults are 1000. `MaxSeries` bounds actual label combinations per
instrument, including an unlabeled combination. At capacity, new combinations
are dropped before children, buckets, or exemplars allocate; admitted values
continue without eviction or resets. There is no overflow series or per-drop
notification. `MaxInstruments` rejects new definitions beyond its limit while
retaining existing instruments. Zero restores the finite default; negative
disables the corresponding admission. Neither means unlimited.

These limits exclude runtime collectors, `target_info`, and your native
collectors. They bound counts, not bytes or the facade's definition cache.
Keep names, keys, bucket lists, and label sizes trusted and bounded; scrape-side
limits cannot protect already-retained process memory.

### Native Registries

To serve existing native collectors and Arkoren from one registry, import
`prometheus` from `github.com/prometheus/client_golang/prometheus` and retain your
own registry handle:

```go
registry := prometheus.NewRegistry()
driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
	metrics.Registry(registry)
	metrics.Namespace("reports")
})
```

`Registry` sets registration and gathering together. Alternatively supply
`Registerer` and `Gatherer`, including `prometheus.DefaultRegisterer` and
`prometheus.DefaultGatherer` for the shared process registry. Omitted or nil
interfaces infer compatible counterparts; a registerer without a usable
gatherer yields HTTP 501. Typed-nil pointers, including `Registry(nil)`, instead
fail startup and yield a fixed HTTP 503 even before startup.

The namespace prefixes managed instruments, not runtime metrics or `target_info`.
`RuntimeMetrics` and `TargetInfo` default to true; explicit false disables each.
Keep the native registry for your own collector registration and removal: there
is no registry getter, and your collectors remain yours after driver stop.
Prevent generated sample-name conflicts yourself. Managed validation cannot
contain arbitrary native collector panics or inspect every emitted sample name.

### Metric Names

Dots and other invalid characters become underscores; units add suffixes and
counters gain `_total`. For example, `http.server.request.duration` with unit
`s` becomes `http_server_request_duration_seconds`, with `_bucket`, `_sum`, and
`_count` series. Up-down counters and gauges both become Prometheus gauges.
The instrumentation scope is omitted, and label keys undergo the same spelling
translation. Avoid names that collide after translation.

Translated name/key collisions, descriptor conflicts, overlapping managed
histogram sample names, and invalid buckets reject an instrument. The facade
warns and disables that instrument; other instruments continue. Retained keys
and buckets are copied. Invalid UTF-8 label values and negative counter
increments are dropped.

Startup registers Go/process collectors and `target_info`, whose labels hold
the resource rather than being copied onto every metric. Invalid resource
labels fail startup; validation does not remove sensitive content. Failed
startup rolls back newly acquired collectors. Stop removes only collectors the
driver registered, leaving equivalent preexisting and caller-owned collectors.
It waits for native `Unregister` calls even with an expired context.

Counter and histogram exemplars carry IDs when recording inside a sampled span
from a trace driver or capable observer. Reading them requires OpenMetrics
negotiation and exemplar storage on the Prometheus server; they are not traces.

### Verify Scraping

After startup, send a request through [Request Tracing](request-tracing.md).
Read the protected `/metrics` endpoint and look for
`http_server_request_duration_seconds_count` (with your namespace prefix, if
configured). This confirms an observed request, not successful remote scraping.

To connect a Prometheus server, save an application-specific `prometheus.yml`
using a private address reachable by that server:

```yaml
scrape_configs:
  - job_name: reports
    static_configs:
      - targets: ["reports.internal:8081"]
```

Configure authentication/TLS there if the listener requires it. Confirm the
`reports` target is up and query its request-count series. Containerized
scrapers need a reachable private host address; host loopback is not universally
reachable from a container. Do not expose diagnostic tools to solve scrape
connectivity. Normal application shutdown unregisters managed collectors;
retire temporary scrape configuration separately.

See the
[`prometheustelemetry` API reference](https://pkg.go.dev/arkoren.dev/driver/prometheus/prometheustelemetry)
for all registry and budget options.

## Combining Drivers

Use `telemetry.Compose` when each signal needs its own destination. With the
OpenTelemetry and Prometheus imports above, send traces and logs over OTLP and
serve metrics locally:

```go
exporter := oteltelemetry.New()
scraper := prometheustelemetry.New()
driver := telemetry.Compose(
	telemetry.Traces(exporter),
	telemetry.Logs(exporter),
	telemetry.Metrics(scraper),
)
```

Give `driver` to the application and mount `scraper.Handler()` on its protected
route. Composition routes facade signals; it does not reconfigure another
driver's native pipelines. Disable unwanted owned OTLP metrics with
`OTEL_METRICS_EXPORTER=none`. To keep logs on your existing handler only, omit
the log selection and disable the unwanted owned OTLP log pipeline too.

The last selection for a signal wins. A shared pointer driver starts once;
distinct selected drivers start in first-selection order and stop in reverse.
Unselected drivers never start. On failure, already-started drivers stop and
errors are joined; the failing driver owns its partial acquisition. Rollback
passes the startup context, including cancellation, and still attempts every
acquired stop. Lifecycle calls serialize, so native callbacks must return and
must not synchronously reenter the same composite.

Use pointer handles to share identity across signals: noncomparable value
drivers in separate selections are distinct. Missing signals remain no-ops
unless an observer supplies them. Selecting only Prometheus gives no trace IDs;
select `telemetry.Traces(memorytelemetry.New())` as well if you want local
correlation without trace export. `DriverAs[T]()` can find components behind a
composite or an unwrapping decorator.

Custom implementations follow the [driver guide](../drivers/writing-a-driver.md)
and [conformance suites](../testing/conformance-suites.md). Every span recorder
and decorator must report the immutable actual selected parent through
`Parent() telemetry.SpanContext`, preserving IDs, sampled flag, remote bit, and
tracestate, or zero for a root. Do not infer it from the child or a conflicting
facade identity. For contracts and composition options, see the
[`telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
