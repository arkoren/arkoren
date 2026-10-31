---
title: Telemetry Drivers
description: Choose between the memory, OpenTelemetry, and Prometheus telemetry drivers, configure each, combine them, and run a collector locally.
order: 60
---

# Telemetry Drivers

## Introduction

Choose a telemetry driver based on where your traces, metrics, and logs must go:

- **Memory** retains bounded history in one process without a backend. Choose it
  for tests, local diagnostics, or trace IDs in logs and error responses.
- **OpenTelemetry** exports all three signals over OTLP. Choose it for a
  collector or vendor that accepts that protocol.
- **Prometheus** exposes metrics for a scraper to pull. Pair it with another
  driver when you also need traces or exported logs.

The [facade](telemetry.md), [instrumented components](instrumented-components.md),
and [HTTP middleware](request-tracing.md) use the same setup with all three.

## Compatibility matrix

Compare Arkoren's signals and delivery paths, not a monitoring vendor's features.
An unimplemented signal is a no-op, not an error. Support assumes the signal is
selected in your composition and recording has not been disabled.

| Driver | Traces | Metrics | Logs | OTLP export | Metrics scrape handler |
| --- | --- | --- | --- | --- | --- |
| [Memory](#memory) | Yes | Yes | Yes — bounded local capture | No | No |
| [OpenTelemetry](#opentelemetry) | Yes | Yes | Yes | Yes — driver-owned pipelines; supplied providers choose export | No |
| [Prometheus](#prometheus) | No | Yes | No | No | Yes — requires a usable gatherer |

Memory retains bounded process-local history and exports nothing. OpenTelemetry
exports over OTLP HTTP/protobuf or gRPC; startup does not prove remote delivery.
Mount Prometheus's handler on a restricted listener. Its exemplars need sampled
span context from a trace driver or trace-capable observer; Prometheus does not export traces. Use
[driver composition](#combining-drivers) to combine delivery paths.

Trace-capable drivers report their actual selected parent through the mandatory
`SpanRecorder.Parent()` contract; facade observers use that result too. The
matrix describes normal library capabilities, not guaranteed delivery or
redaction by arbitrary native providers, exporters, collectors, or log handlers.
Review [privacy boundaries](telemetry-privacy.md) before enabling a signal.

## Wiring a driver

Wrap your driver in `telemetry.New` and give it to the application. These
fragments use `application` from `arkoren.dev/core/application`,
`telemetry` from `arkoren.dev/core/service/telemetry`, and `router` from
`arkoren.dev/core/http/router`. Here `driver` is your selected driver below.
Later inspection snippets use standard-library `fmt`:

```go
app := application.New(func(app *application.Builder) {
	app.Telemetry(telemetry.New(driver))
})
```

Construction starts no exporters and makes no network calls. The application
starts telemetry first and stops it last, flushing final spans and logs.
The application's name, version, and environment become the telemetry **resource**,
the identity attached to exports. If you need to set them explicitly, add these
settings to that same application callback; `version` is your build-version string:

```go
app.Name("billing")
app.Version(version)
app.Environment(application.Production)
```

See [Telemetry](telemetry.md#lifecycle) for
startup failures and shutdown deadlines.

## Memory

Bundled with `arkoren.dev/core`, the memory driver generates W3C trace and span
IDs, propagates `traceparent` and `tracestate`, retains recent finished spans in
a bounded ring, and aggregates metrics in memory. No extra module is needed;
nothing leaves the process.

### Connecting

To retain local telemetry, give the bundled driver to your application:

```go
import "arkoren.dev/core/service/telemetry/memorytelemetry"

app := application.New(func(app *application.Builder) {
	app.Telemetry(telemetry.New(memorytelemetry.New()))
})
```

After startup, logs with a request context carry `trace_id` and `span_id`; the
[HTTP middleware](request-tracing.md) adds the trace ID to responses. Read back
recorded spans for a debug endpoint or local inspector:

```go
if driver, ok := app.Telemetry().DriverAs[*memorytelemetry.Driver](); ok {
	for _, span := range driver.Spans() {
		fmt.Println(span.Name, span.Context.TraceID)
	}
}
```

### Configuration

Three limits bound the memory it uses:

```go
driver := memorytelemetry.New(func(store *memorytelemetry.Builder) {
	store.MaxSpans(5000)
	store.MaxLogs(2000)
	store.MaxSeries(500)
})
```

`MaxSpans` is the size of the ring of finished spans; when it is full the
oldest span is dropped. The default is 1000. A value of zero or less keeps no
spans at all: IDs are still generated and propagated, but nothing is stored and
spans do not record attributes. `MaxLogs` bounds the log ring (default
1000); zero or negative keeps no logs. `MaxSeries` is the number of label
combinations each metric keeps (default 1000); new combinations past the limit
are dropped, and zero or negative retains no metric series. To keep correlation IDs
without recording any history, set all three limits to zero.

`MaxSeries` applies to each instrument, not the total number of instruments or
bytes. Keep instrument names, keys, and label-value sizes bounded in application
code; these capacities do not make untrusted dynamic instrumentation safe.

### Inspecting recent spans

The [Connecting](#connecting) fragment reads only completed retained operations.
Use protected [diagnostic tools](#publishing-memory-diagnostics), not a public
debug route, for operator access to this store.

### Behavior and limits

The IDs come from the runtime's random generator and are intended for
correlation, not secrets or access tokens. The driver reads incoming trace
headers strictly: a malformed `traceparent`, uppercase hex, or an all-zero ID
yields no remote context, and the request starts a new trace.

It exports nothing outside the process. Your logs still reach their normal
handler with trace and span IDs added, and the driver keeps a bounded copy for
inspection. `Spans()`, `Logs()`, and `Metrics()` return copies, and `Reset()`
clears the recorded data while keeping the metric instruments. These rings
are development and diagnostic history, not durable log storage.

The memory recorder reports the valid facade parent selected at span creation,
or zero for a root or inactive recorder. Resource attribute containers are copied
at startup and when read back. `Stop` synchronizes with ring and aggregate
commits, but does not wait for or cancel caller-provided `LogValuer` or error
formatting. Work that finishes formatting after Stop cannot append a new log.

For tests of your own code, prefer the `telemetrytest` fake, which wraps this
driver and adds assertions; see [Fakes](../testing/fakes.md).

### Publishing memory diagnostics

If your application retains a standalone `store *memorytelemetry.Driver`,
publish its bounded history as MCP tools:

```go
tools := store.MCPTools("memory")
```

These definitions expose traces, a trace tree with logs, cursor-based logs, and
metric aggregates; they do not open a listener. Start and stop a standalone
store yourself, or let application telemetry own its lifecycle as above. Check
each definition's `Err()` and select and register it on a protected endpoint
following [Exposing Diagnostics](exposing-diagnostics.md). For application-owned
capture, prefer `composer.MCPTools` inside a composer, as in that guide. Read-only telemetry can
still disclose application logs and attributes, so authenticate operator access.

### Running locally

No external service is needed. Start the application, send a request through
the [tracing middleware](request-tracing.md), then inspect `driver.Spans()` with
the checked `DriverAs` fragment. A retained span and response `traceresponse`
header confirm capture and correlation. Stop normally; process exit loses history.

### API references

For every setting, see the
[`memorytelemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry/memorytelemetry).

## OpenTelemetry

Built on the OpenTelemetry Go SDK, this driver exports traces, metrics, and logs
over OTLP (gRPC or HTTP) and reports Go runtime metrics by default. Choose it
for a collector or vendor such as Grafana, Honeycomb, Datadog, or Jaeger.

### Installation

```sh
go get arkoren.dev/driver/otel
```

### Connecting

To export through your deployment's OTLP configuration, construct the driver:

```go
import "arkoren.dev/driver/otel/oteltelemetry"

app := application.New(func(app *application.Builder) {
	app.Telemetry(telemetry.New(oteltelemetry.New()))
})
```

Without callbacks, standard `OTEL_*` environment variables configure the driver,
letting one binary target different collectors. It does not replace global trace, meter, or logger
providers. It does install the global SDK error handler by default; disable
`GlobalErrorHandler` if another component owns that process-wide setting.

### Configuration

You can configure the driver with environment variables, Builder callbacks, or
both. Explicit nonempty settings win over the environment; empty settings retain
the SDK fallback chain. The most used environment variables are:

| Variable | Effect |
| --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | The collector address. |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` (the default, port 4318) or `grpc` (port 4317). |
| `OTEL_EXPORTER_OTLP_HEADERS` | Extra headers, such as an API key for a vendor. |
| `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` | The resource, when the application does not set it. |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | How many traces are kept. |
| `OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER`, `OTEL_LOGS_EXPORTER` | `otlp` (the default) or `none` to switch a signal off. |
| `OTEL_SDK_DISABLED=true` | Disables recording and export for all signals; configured context propagation remains active. |

Use plaintext only for a collector on a trusted local network; omit
`Insecure(true)` and use HTTPS/TLS for remote or untrusted connections. To use
a trusted internal gRPC collector, configure:

```go
driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
	instrumentation.Endpoint("collector:4317")
	instrumentation.Protocol(oteltelemetry.ProtocolGRPC)
	instrumentation.Insecure(true)
})
```

`Builder.Endpoint` takes `host[:port]` or an HTTP(S) URL. Explicit endpoints for
driver-owned exporters are validated at Start: malformed addresses, unsupported
schemes, URL credentials, nonempty query strings, and fragments are rejected.
The driver's endpoint-validation error does not echo the supplied address; use
`Headers` for authentication instead of URL user information. Empty endpoint
settings leave parsing and defaults to the native SDK environment chain.

An `http` URL or `Builder.Insecure(true)` disables TLS; `Insecure(false)` does not
override an HTTP URL or the native environment chain to force TLS.

To reduce trace volume, import `sdktrace` from
`go.opentelemetry.io/otel/sdk/trace` and add this setting to the same driver callback:

```go
instrumentation.Sampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.1)))
```

This sampler keeps one in ten new traces but follows the caller's sampling
decision for child spans. Sampling reduces volume, not the sensitivity of retained
data; see [Telemetry Privacy](telemetry-privacy.md#reducing-what-leaves-the-process).

If your service already has `tracerProvider *sdktrace.TracerProvider`, pass that
existing provider instead of letting the driver build its own:

```go
driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
	instrumentation.TracerProvider(tracerProvider)
})
```

A provider you pass in belongs to you: at application shutdown the driver calls
its `ForceFlush(context.Context) error` method if it has one, but never shuts
it down. Arrange the provider's shutdown yourself. Runtime-metric callbacks
installed by this driver are still its responsibility: Stop and failed-start
rollback unregister them, including on a borrowed meter provider, without
removing your other callbacks or shutting that provider down.
The per-signal `OTEL_*_EXPORTER=none` settings do not disable caller-supplied
providers; configure those providers yourself. `OTEL_SDK_DISABLED=true` disables
recording and export for all signals in this driver, including supplied providers.
Neither setting is a propagation policy: use the HTTP trust and propagation
controls in [Request Tracing](request-tracing.md) to control trace headers.

### Exporting request traces

With the application wiring above, add the
[request-tracing middleware](request-tracing.md#tracing-incoming-requests) to
your existing router. After startup, send a request and find its span by service
name in your collector's backend. For application-specific work, use the
[span example](telemetry.md#starting-spans). Successful startup validates
configuration; it does not prove a collector accepted a later export.

### Behavior and limits

`Start` builds the resource, exporters, and providers. Invalid configuration for
driver-owned signals fails startup, including `OTEL_TRACES_EXPORTER` other than
`otlp` or `none`, or unsupported `http/json`. Failed or canceled native startup
releases acquired resources before returning. A facade whose Start fails is
terminal; see [lifecycle](telemetry.md#lifecycle) before retrying with a new handle.

For each driver-owned pipeline, Stop calls ForceFlush with its supplied context
and preserves flush errors, then completes that pipeline's SDK shutdown on an
uncanceled context even if flushing was canceled. Cleanup runs in reverse
acquisition order. Native shutdown can perform final exports, so the whole Stop
can outlast that deadline; native exporter timeouts still apply. Teardown is not
abandoned in a background goroutine. Borrowed-provider flushing and native hooks remain synchronous and
must cooperate: cancellation cannot forcibly interrupt them or an earlier
serialized lifecycle call. Drain application work and end spans before Stop;
previously obtained native spans retain their native lifetime.

Default batch export separates normal request work from network export, but
supplied providers, processors, and exporters can block or impose their own
behavior. SDK export failures normally reach the application's unwrapped logger
through the default error handler; they are not universally redacted.

The application supplies the resource: `application.Builder.Name` becomes `service.name`,
`application.Builder.Version` becomes `service.version`, and environment becomes
`deployment.environment.name`. Empty fields fall back to environment variables;
without any service name, it uses `unknown_service:<executable>`.

A valid native OpenTelemetry parent takes precedence over a conflicting facade
identity, including when the native parent is non-recording or remote. Otherwise
the driver adopts a valid facade parent, such as a memory-driver span. It
snapshots the selected parent's trace and span IDs, sampled flag, remote bit,
and tracestate before SDK Start. The recorder's `Parent()` and facade observers
report that snapshot, or zero for a root, including an invalid incoming identity
with other metadata. The returned context still carries the native child for
native descendants and injection. Observation captures facade calls, not the
entire native SDK export stream; see [observing telemetry](telemetry.md#observing-telemetry).

Metric label values are strings; empty values are omitted.
Logs are converted from `slog` records, with the severity mapped from the level,
groups flattened into dotted keys, and the span of the context attached.

`New` enables runtime metrics; `Builder.RuntimeMetrics(false)` disables them.
`Builder.GlobalErrorHandler`, also enabled by default, routes SDK errors to the
application logger through process-wide `otel.SetErrorHandler`.

Explicit false keeps those defaults off. Headers are copied; native providers
and propagators remain shared caller-owned handles. The installed process-wide
error handler is not restored on Stop or failed startup. If your application
owns that policy, set `GlobalErrorHandler(false)` and configure it yourself.

### Using native providers

Optionally supply native providers to share existing SDK setup with Arkoren.
Alongside `TracerProvider` above, `MeterProvider`, `LoggerProvider`, and
`Propagator` accept native handles. Retain them for native SDK calls: the driver
has no provider getters and does not install global providers. Supplied providers
own exporter configuration; the flush and shutdown responsibilities above apply.

Connection settings and `Sampler` do not reconfigure a supplied tracer provider;
configure its resources, sampling, and exporters yourself. The same connection
ownership applies to supplied meter and logger providers. A nil interface, such
as `TracerProvider(nil)`, restores the driver's fallback. A **typed-nil pointer**
is a nil pointer stored inside a non-nil interface; it is invalid, not a fallback.
Start rejects typed-nil provider and propagator pointers, and a typed-nil sampler
when no tracer provider is supplied. An unused sampler remains ignored when a
tracer provider is supplied.

### Running locally

Use an OTLP receiver configured for your selected protocol and signals. For
local traces, Jaeger accepts OTLP over HTTP and provides a web interface:

```sh
docker run --rm --name arkoren-jaeger -p 127.0.0.1:16686:16686 \
  -p 127.0.0.1:4318:4318 jaegertracing/all-in-one
```

Jaeger stores traces only, so switch the other two signals off, and let the
driver use its default endpoint, `localhost:4318`:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
  OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
  OTEL_METRICS_EXPORTER=none OTEL_LOGS_EXPORTER=none go run .
```

Open `http://localhost:16686` and select your service to see request spans. For
all three signals, use the OpenTelemetry Collector with your chosen backends.

Run these commands from your application's module directory; the application
must already use the driver and tracing middleware above. Stop the application
through its normal lifecycle to flush the final batch, then stop Jaeger with
`docker stop arkoren-jaeger`. The container is removed and its in-memory trace
history is lost.

### API references

For every option, see the
[`oteltelemetry` API reference](https://pkg.go.dev/arkoren.dev/driver/otel/oteltelemetry).

## Prometheus

The Prometheus driver records metrics in a registry for scraping. It supports
metrics only; application spans need a separate trace driver.

### Installation

```sh
go get arkoren.dev/driver/prometheus
```

### Connecting

Keep a reference to the driver, because you mount its handler on a route that
Prometheus scrapes. Here `r` is your existing restricted internal router;
[operator endpoints](../application/health-and-readiness.md) shows how to give it
a separate listener. Do not mount this unauthenticated handler on your public router:

```go
import "arkoren.dev/driver/prometheus/prometheustelemetry"

prom := prometheustelemetry.New()

app := application.New(func(app *application.Builder) {
	app.Telemetry(telemetry.New(prom))
})

r.Get("/metrics", router.HTTP(prom.Handler()))
```

The facade uses the driver's metric signal; traces and exported logs stay off.
To add another signal, see [Combining drivers](#combining-drivers).
The handler works before startup and serves Prometheus text or, when requested,
OpenMetrics with exemplars.

> [!WARNING]
> Nothing protects `/metrics`. Metric names and label values describe your
> routes and traffic. Serve it on an internal port, as shown for the other
> [operator endpoints](../application/health-and-readiness.md), or restrict it
> in your gateway.

### Configuration

For native registry configuration, import `prometheus` from
`github.com/prometheus/client_golang/prometheus`.
The driver creates an isolated registry by default. To share the process-wide
default with other libraries, pass its registerer and gatherer. Add a namespace
to prefix instrument names:

```go
prom := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
	metrics.Namespace("shop")
	metrics.Registerer(prometheus.DefaultRegisterer)
	metrics.Gatherer(prometheus.DefaultGatherer)
})
```

`Builder.Registry` sets both from one `*prometheus.Registry`. The namespace turns
`http_server_request_duration_seconds` into `shop_http_server_request_duration_seconds`;
it is not applied to the Go and process metrics or to `target_info`.

Omitted settings and nil `Registerer`/`Gatherer` interfaces retain registry
inference. A registerer-only configuration that cannot supply a gatherer makes
Handler answer 501 Not Implemented. Typed-nil native pointers instead make Start
return an error and Handler answer a fixed 503 Service Unavailable, even before
Start. In particular, `Registry(nil)` supplies typed-nil handles; it does not
select the default private registry.

The default retains at most 1000 label combinations per instrument. To use a smaller
budget, set `Builder.MaxSeries`:

```go
prom := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
	metrics.MaxSeries(200)
})
```

Each instrument now keeps at most 200 actual Prometheus children. New combinations
at capacity are dropped before child, histogram bucket, or exemplar allocation;
already admitted combinations keep recording with their original labels and values.
Admission is atomic under concurrent recording, and nothing is evicted or reset.
There is no overflow label or per-drop log or metric. Zero selects the finite default
of 1000 combinations per instrument; a negative value keeps no metric combinations.
Neither means unlimited.

`Builder.MaxInstruments` separately bounds instrument definitions per driver (default
1000). New definitions beyond capacity fail; admitted definitions remain usable.
This includes empty collectors created with variable names or keys. Zero selects
1000 definitions; negative admits none. Configuring only a native registry
preserves both finite defaults. `RuntimeMetrics` and `TargetInfo` default to true;
explicit false disables them. Native registries remain shared caller-owned handles.
These limits do not cover runtime collectors, `target_info`, or native collectors
you register yourself. They bound counts, not bytes: keep instrument names, keys,
and bucket lists fixed, and avoid unbounded label-value sizes. Recording retains
children without tracing or scraping; scrape-side limits cannot protect process memory.
The telemetry facade separately caches supplied definitions, including rejected ones;
do not derive instrument names or keys from untrusted input.

### Reading application metrics

After application startup, send a request through the
[request-tracing middleware](request-tracing.md#tracing-incoming-requests), then
read `/metrics` on the internal listener established above. A
`http_server_request_duration_seconds_count` series confirms that a request was
recorded. For your own counters, use the
[metric example](telemetry.md#recording-metrics) with this same telemetry handle.

### Behavior and limits

Arkoren names metrics the way OpenTelemetry does, with dots, and the driver
translates them to Prometheus names. Dots become underscores, a unit becomes a
suffix, and counters gain `_total`. So `http.server.request.duration` (seconds)
is exposed as `http_server_request_duration_seconds` with its `_bucket`, `_sum`,
and `_count` series, and `arkoren.cache.operations` becomes
`arkoren_cache_operations_total`. Label keys are translated the same way.
Up-down counters and gauges are Prometheus gauges. The instrumentation scope is
dropped: no series carries a scope label.

`Start` adds Go and process collectors and `target_info` with resource attributes
(`service_name`, `service_version`, and so on). Join with `target_info` in PromQL;
attributes are not copied onto every series. The target metric is validated
before registration, so invalid final label values fail Start rather than panic
during collection. Failed startup rolls back collectors newly registered by that
attempt. Resource values remain visible in the metric; validation is not redaction.

`Stop` unregisters only what the driver registered, allowing registry reuse.
Equivalent preexisting runtime collectors, such as the default registry's Go
collector, remain. Stop waits for native Unregister calls even with an expired
context; these methods have no cancellation parameter. The handler remains
usable before Start and after Stop, exposing whatever caller-owned collectors
the gatherer still holds.

Two instruments that translate to the same Prometheus name, colliding translated
label keys, a clash with another registered descriptor, or invalid bucket bounds
make `Instrument` fail. Managed histogram `_count`, `_sum`, and `_bucket` sample
names also cannot overlap another managed instrument, regardless of registration
order. The facade disables that one instrument and logs a warning; the rest keep
working. The driver copies retained `InstrumentSpec.Keys` and `Buckets`, so later
caller slice edits cannot change duplicate-definition checks or emitted labels
and boundaries. Label values that are not valid UTF-8 and negative counter
increments are dropped.

A recording made inside a sampled span carries `trace_id` and `span_id` as an
**exemplar** on counters and histograms, which lets a dashboard jump from a slow
bucket to a trace. The sampled span context can come from a trace driver or a
trace-capable observer. Exemplars also need the OpenMetrics format and a
Prometheus server started with exemplar storage enabled.

### Sharing a native registry

Native Prometheus collectors can share the registry configured in
[Configuration](#configuration-2), so one scrape handler serves both them and
Arkoren's instruments. Keep your registry handle for native registration and
gathering; the driver exposes `Handler()`, not a registry getter. Register and
remove your own collectors yourself. The driver's admission limits do not
cover them, and `Stop` leaves caller-owned collectors registered.

You must prevent generated sample-name conflicts in your native collectors;
the Registerer interface cannot reveal every emitted name or unchecked collector
behavior. Custom collector panics are not contained by the driver's managed
instrument validation. Native gathering errors can reach application logs or
the HTTP error response when no metrics can be served. Protect both the endpoint
and logs; label and resource values are not automatically scrubbed.

### Running locally

Prometheus needs only a configuration that scrapes your service. This Docker
example assumes `/metrics` is on a port reachable from the container at
`host.docker.internal:8080`; a host-loopback-only listener may not be reachable
on every Docker platform. Use a dedicated private scrape listener reachable
from the container and restrict it with your firewall. Do not expose your
diagnostic MCP endpoint on that listener merely to make metrics reachable:

```yaml
scrape_configs:
  - job_name: billing
    scrape_interval: 5s
    static_configs:
      - targets: ["host.docker.internal:8080"]
```

Save it as `prometheus.yml` and run Prometheus next to your service:

```sh
docker run --rm --name arkoren-prometheus -p 127.0.0.1:9090:9090 \
  -v "$PWD/prometheus.yml:/etc/prometheus/prometheus.yml:ro" \
  prom/prometheus
```

On Linux, add `--add-host=host.docker.internal:host-gateway`. Open
`http://localhost:9090` and query `http_server_request_duration_seconds_count`
after sending a few requests.

Check Prometheus's Targets page if the query is empty; the `billing` target must
be up before scrape results appear. Stop the container with
`docker stop arkoren-prometheus`; its local time-series history is discarded.
Remove your application's temporary scrape listener if it is no longer needed.

### API references

For every option, see the
[`prometheustelemetry` API reference](https://pkg.go.dev/arkoren.dev/driver/prometheus/prometheustelemetry).

## Combining drivers

Use `telemetry.Compose` to assign drivers per signal: send traces and logs over
OTLP while exposing metrics for scraping:

```go
otel := oteltelemetry.New()
prom := prometheustelemetry.New()

driver := telemetry.Compose(
	telemetry.Traces(otel),
	telemetry.Metrics(prom),
	telemetry.Logs(otel),
)
```

Wrap this `driver` with `telemetry.New` using the [application wiring](#wiring-a-driver)
above. Keep `prom` to mount its handler on the restricted metrics router.

A driver selected for multiple signals starts once. Distinct drivers start in
first-selection order and stop in reverse: last started, first stopped. If a
`Start` fails, already-started drivers stop and errors are joined. The last
selection for a signal wins; drivers left serving no signal never start.

Use shared pointer handles when one driver serves multiple roles. Noncomparable
value drivers have no shared value identity, so separate selections are distinct.
Lifecycle calls stay serialized across backend work; backend lifecycle callbacks
must return and must not synchronously reenter the same composite's Start or
Stop. Rollback forwards the startup context, including cancellation, attempts
every acquired driver's Stop, and joins errors. The driver whose Start failed
owns cleanup of its partial acquisition; context expiry does not force it to stop.

A common development setup keeps the trace IDs without a collector and still
exposes metrics:

```go
driver := telemetry.Compose(
	telemetry.Traces(memorytelemetry.New()),
	telemetry.Metrics(prometheustelemetry.New()),
)
```

A signal that neither a selected driver nor an attached observer covers stays a
no-op. Without a trace driver or trace-capable observer, this composition creates
no trace IDs for logs, responses, or exemplars. If you wrap a composed driver you can still find a part with
`DriverAs`, for example `DriverAs[*prometheustelemetry.Driver]()`.

## Writing a driver

A driver is a type that implements `Start` and `Stop` plus any of the three
signal interfaces. The `telemetrytest.Run` conformance suite checks the
lifecycle rules, span parenting, propagation, and instrument behavior, so a new
driver proves itself the same way the three above do. See
[Conformance suites](../testing/conformance-suites.md).

Every custom recorder and recorder decorator must now implement or forward the
mandatory `Parent() telemetry.SpanContext` method. This is a source-compatibility
change, not an optional capability. Capture the effective selected parent before
creating the child and keep that value immutable, including for unsampled spans.
Preserve its IDs, sampled flag, remote bit, and tracestate; return zero for roots
and inactive recorders. Do not infer it later from the child's identity or a
possibly conflicting facade context. The observer's existing `FinishedSpan.Parent`
field now uses this backend result; diagnostic JSON field names do not change.

For an existing decorator with an `inner telemetry.SpanRecorder` field, forwarding
keeps the backend authoritative:

```go
func (recorder *spanDecorator) Parent() telemetry.SpanContext {
	return recorder.inner.Parent()
}
```

The facade waits for admitted recorder calls before backend End. Recorder
callbacks, including caller error formatting, must return and must not
synchronously end their own span. Span and instrument attribute/key/bucket
containers passed to a backend are owned independently of observer metadata.
Observers must treat their shared snapshots as read-only, copy retained label
values, and clone retained log records. Stop is not a drain for already-admitted
observer callbacks; see [lifecycle](telemetry.md#lifecycle).

For the signal contracts and composition options, see the
[`telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
