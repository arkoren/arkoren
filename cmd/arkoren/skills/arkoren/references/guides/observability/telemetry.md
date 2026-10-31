---
title: Telemetry
description: How Arkoren reports traces, metrics, and log correlation, how to wire a driver into the application, and how to add your own.
order: 10
---

# Telemetry

## Introduction

When a request is slow or a job keeps failing, logs alone rarely tell you why.
**Telemetry** adds two more views of the same system. A **trace** follows one
unit of work, such as a request, through every step it takes, and shows how
long each step lasted. A **metric** counts or measures something over time,
such as requests per second or the 99th percentile of query duration. Logs stay
what they are, `log/slog` records, but telemetry stamps them with the identity
of the trace they belong to, so you can jump from a log line to the whole
request and back.

Arkoren treats telemetry like any other service: a facade, `telemetry.Telemetry`,
sits in front of a **driver** that decides where the data goes. Arkoren's own
components use the facade to describe themselves, and your code uses the same
facade to describe yours. You do not need an OpenTelemetry or Prometheus
dependency unless you choose a driver that uses it. You
choose the driver once, when you build the application: the in-process
[memory driver](telemetry-drivers.md#memory) for trace IDs and tests, the
[OpenTelemetry driver](telemetry-drivers.md#opentelemetry) to export traces, metrics, and
logs over OTLP, or the [Prometheus driver](telemetry-drivers.md#prometheus) for a scrape
endpoint.

Until telemetry is started with a driver, spans are no-ops: they generate no
IDs and allocate nothing, so instrumented code is safe to leave in place.
An observer can also supply a missing signal, as described under
[Observing telemetry](#observing-telemetry).

Telemetry samples and aggregates, so it is not a record of individual actions.
For a durable record of who did what, see [Auditing with Events](../events/auditing.md).

## Turning telemetry on

Build a `telemetry.Telemetry` from a driver and give it to the application.
This is the application used by the later examples:

```go
import (
	"arkoren.dev/core/application"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

app := application.New(func(app *application.Builder) {
	app.Telemetry(telemetry.New(memorytelemetry.New()))
})
```

`telemetry.New` never fails and does no I/O; it only wraps the driver. The
application does the rest. It starts the driver before every other start hook,
so spans created while the other services start are already recorded. It shuts
the driver down last, after the final `app stopped` log, so shutdown logs and
spans are flushed. It also passes the driver the service name and version you
set with `application.Builder.Name` and `application.Builder.Version`, and the environment, which
exporters attach to everything they send.

`app.Telemetry()` returns the handle. It is never nil: without
`application.Builder.Telemetry` it is a no-op, and the logger still passes records straight to its handler.

Focused examples keep error results visible but omit routine error handling.
Handle those errors before using results in your application.

### Giving components the handle

The application does not reach into your services; you pass the handle to each
one that should report. Every instrumented component takes it as an option:

Here `driver` is your configured database driver. Import `database` from
`arkoren.dev/core/service/database`:

```go
db := database.New(driver, func(connection *database.Builder) {
	connection.Telemetry(app.Telemetry())
})
```

Other instrumented components accept the handle through their own builders.
[Instrumented components](instrumented-components.md) lists what each one
records. A component built without the option follows the process default,
described next.

### The process default

Wiring the handle into every component is explicit, but sometimes a package
deep in your code needs telemetry and has no handle. For that,
`telemetry.Default()` returns a process-wide handle, in the manner of
`slog.Default()`. It is a no-op until you call `telemetry.SetDefault`:

```go
app.SetDefaults() // slog.SetDefault and telemetry.SetDefault
```

`SetDefaults` is an explicit method rather than an option so that the global
change is visible at the call site. Components resolve the default when they
use it, not when they are built, so calling it after you construct your
services still takes effect. You can also call `telemetry.SetDefault` yourself;
passing `nil` restores the no-op.

## Starting spans

A **span** records one operation: its name, how long it took, whether it
failed, and a few attributes. You get a tracer from the handle, named by an
**instrumentation scope**, which identifies the code that produces the spans.
Use your package's import path. In this fragment, `ctx` is the caller's request
or command context, `gateway` is your existing payment client, and `order` is
the order being charged:

```go
tracer := app.Telemetry().Tracer("example.com/billing")

ctx, span := tracer.Start(ctx, "charge",
	telemetry.Kind(telemetry.Client),
	telemetry.Attrs(telemetry.String("currency", order.Currency)),
)
defer span.End()

err := gateway.Charge(ctx, order)
if err != nil {
	span.SetStatus(telemetry.Error, "charge failed")
}
```

`Start` returns a new context that carries the span. Pass that context on to
everything the operation calls: a database query or a cache read started with
it becomes a child of your span, which is what turns separate spans into a
trace. Always call `End`, usually with `defer`. The first call closes admission
to new recorder calls and waits for already-admitted calls before ending the
backend span. Later calls to `End` have no effect; attribute, event, and status
updates after admission closes are no-ops. Recorder callbacks must return and
must not synchronously call `End` on their own span, including from error
formatting: that would wait for the callback itself.

The kind says how the operation relates to its neighbors. `Internal` is the
default, `Client` and `Producer` describe calls out of the process, and
`Server` and `Consumer` describe work that arrived from outside. Backends use
the kind to draw service maps.

Give spans a short name with a bounded set of values, such as `charge` or
`GET /orders/{id}`, never one that includes an ID or a user input. Put safe
descriptive values, such as currency, into attributes instead; do not move
sensitive identifiers from names into attributes. If building an attribute is expensive,
check `span.IsRecording()` first; it is false when telemetry is off.

Empty span kind selects `Internal`, but it is not equal to that named constant.
Recorded status begins at `Unset`; setting an empty, unknown, or `Unset` status
does not change it. `OK` is final, whereas `Error` can replace an earlier error.

### Recording failures

Set the status to `telemetry.Error` when the operation failed, with a short
description. Call `span.RecordError(err)` only when its text is safe to export: it
attaches the error's type and its full text to the span as an event, and the
text is exported. Arkoren's own components never call it, because error text can
contain values such as keys or addresses; see [Privacy](telemetry-privacy.md).

## Recording metrics

A **metric** is created once, with a name, a unit, and the attribute keys you
will use to slice it, and then recorded to as often as you like. Create
instruments in a constructor, not in a hot path:

For a count of charges by currency, create a counter once, then record each
charge with its operation context `ctx`:

```go
meter := app.Telemetry().Meter("example.com/billing")

charges := meter.Counter("billing.charges",
	telemetry.Unit("{charge}"),
	telemetry.Keys("currency"),
)

charges.Add(ctx, 1, telemetry.String("currency", "EUR"))
```

To measure duration as well, create a histogram on the same `meter`. Import
standard-library `time`; `start` is the `time.Time` captured before the operation:

```go
duration := meter.Histogram("billing.charge.duration",
	telemetry.Unit("s"),
	telemetry.Keys("currency"),
)

duration.Record(ctx, time.Since(start).Seconds(), telemetry.String("currency", "EUR"))
```

A counter only goes up, an up-down counter also goes down (in-flight work), a
histogram records the distribution of a value (durations, sizes), and a gauge
holds a value that you set (a queue depth).

Declaring `Keys` is deliberate. Prometheus needs label names up front, and
every metric system is hurt by unbounded label values, so a recording keeps only
the declared keys and drops the rest. Declare only low-cardinality keys, such
as a currency or a status class, never an ID or an email address. A key you
declare but omit in a recording is reported as an empty string.

The same scope and name share one backend instrument. A later declaration with
a different kind or keys is disabled and logs a warning through `Setup.Logger`.
Instruments bind on first use after startup; creating one before startup is safe.
Histogram buckets are sorted and deduplicated. Negative counter increments and
NaN histogram recordings are ignored. An empty instrument kind is invalid, not
a default.

Instrument options publish independent normalized key and bucket slices.
Retaining an option's draft cannot change an existing instrument. Binding gives
the backend its own copies, so backend normalization does not alter facade label
filtering or observer metadata.

Metric names follow the OpenTelemetry semantic conventions as you write them,
with dots. The Prometheus driver translates them to its own spelling.

## Attributes

Spans, events, and metric recordings carry **attributes**, built with
`telemetry.String`, `Int`, `Int64`, `Float64`, `Bool`, `Strings`, and `Duration`
(which stores float seconds). An attribute with an empty key is invalid and is
dropped without an error, so a mistake in instrumentation never breaks the
operation it observes. When a metric receives a non-string attribute it is
converted to its text form, because label values are strings.

`telemetry.Attrs` snapshots its input slice. Span creation, `SetAttributes`, and
`AddEvent` give the backend owned attribute containers; passive observation keeps
separate containers before forwarding them. This protects metadata from later
slice edits, not from sensitive values you deliberately record. Database SQL
text is [off by default](telemetry-privacy.md#query-text); explicitly enabling
`QueryText(true)` exports compiled SQL verbatim, not sanitized SQL.

## Propagating a trace between services

A trace is only useful if it can cross a process boundary. The driver owns the
wire format, W3C Trace Context by default, and the facade offers two calls:
`Inject` writes the current span into a carrier, and `Extract` reads a remote
span out of one and returns a context that continues it. A carrier is any
key-value container; `telemetry.HeaderCarrier` adapts `http.Header` and
`telemetry.MapCarrier` adapts a map, for example message metadata.

The HTTP middleware and client transport, described on the [HTTP page](request-tracing.md),
do this for you, and the [queue and broadcast components](instrumented-components.md)
carry trace context in message metadata. You need the calls yourself when you
cross a boundary Arkoren does not know about:

```go
metadata := telemetry.MapCarrier{}
app.Telemetry().Inject(ctx, metadata)
// ... send metadata with the message ...

ctx = app.Telemetry().Extract(context.Background(), metadata)
```

Malformed input leaves the context unchanged, so a bad header never fails a
request. Raw `Extract` does not establish trust: call it only for metadata from
a source your application trusts. HTTP propagation across a trust boundary is gated; the
[HTTP page](request-tracing.md) explains which callers are
believed.

## Trace IDs in logs

Log correlation does not redact the record. Before enabling log export, decide
which attributes may leave the process and who may read them; see
[Telemetry Privacy](telemetry-privacy.md#logs).

The application's logger is always wrapped, so that, once telemetry runs, a record
logged with a context that holds a span gets `trace_id` and `span_id`
attributes. If the driver also exports logs, as the OpenTelemetry driver does,
those records are sent to the backend with the same IDs:

```go
logger := app.Logger()

ctx, span := tracer.Start(ctx, "charge")
defer span.End()

logger.InfoContext(ctx, "charging order", "order", order.ID)
```

With a JSON log handler the record looks like this:

```json
{
  "time": "2026-10-12T09:30:00Z",
  "level": "INFO",
  "msg": "charging order",
  "order": "ord_123",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id": "00f067aa0ba902b7"
}
```

Correlation needs the context-aware methods: `InfoContext`, `ErrorContext`, and
so on. `logger.Info` carries no context, so it carries no IDs. With the HTTP
[middleware](request-tracing.md) registered, every record you log with the request's
context is correlated with that request, and the trace ID is also returned to
the client so it can be quoted in a support ticket.

To read the IDs yourself, `telemetry.SpanFromContext(ctx)` never returns nil,
and `Context()` on the span gives its `TraceID` and `SpanID`, both with a
`String()` method that returns hex.

## Combining drivers

A driver supplies any of the three signals. The OpenTelemetry driver supplies
all three; the Prometheus driver supplies metrics only. `telemetry.Compose`
picks one driver per signal, which is how you send traces and logs over OTLP
and keep metrics on a Prometheus endpoint. Import `oteltelemetry` from
`arkoren.dev/driver/otel/oteltelemetry` and `prometheustelemetry` from
`arkoren.dev/driver/prometheus/prometheustelemetry` after installing those
[drivers](telemetry-drivers.md):

```go
otel := oteltelemetry.New()
prom := prometheustelemetry.New()

driver := telemetry.Compose(
	telemetry.Traces(otel),
	telemetry.Logs(otel),
	telemetry.Metrics(prom),
)
```

Give `telemetry.New(driver)` to the application as in
[Turning telemetry on](#turning-telemetry-on), and mount `prom.Handler()` on a
restricted metrics route as shown in [Prometheus setup](telemetry-drivers.md#prometheus).
A signal with no driver is a no-op. Each distinct driver is started once, in
order, and stopped in reverse, and if one fails to start the ones already
started are shut down. [Drivers](telemetry-drivers.md) covers each driver and its
configuration.

## Observing telemetry

Sometimes a second component needs its own view of everything the application
reports, without changing where the data goes. Application diagnostics are an example:
`composer.MCPTools` attaches capture during composition, before startup, so its tools show recent
spans, logs, and metrics even when the application exports them to
OpenTelemetry. `Telemetry.Observe` attaches an `Observer` for that. It
is a runtime subscription, like `event.Bus.Listen`: you can attach and detach
observers while the application serves traffic, and `Observe` returns the
function that detaches the observer.

```go
mirror := memorytelemetry.New()
err := mirror.Start(ctx, telemetry.Setup{})
defer mirror.Stop(context.WithoutCancel(ctx))

unsubscribe := app.Telemetry().Observe(mirror)
defer unsubscribe()

// later: mirror.Spans(), mirror.Logs(), mirror.Metrics()
```

An observer receives each finished span synchronously after the backend recorder
ends. Its `FinishedSpan.Context` and `FinishedSpan.Parent` come from the recorder:
the parent is the backend's actual selection, not a guess from the incoming
facade context. With OpenTelemetry, a valid native parent wins even if a different
facade identity is present. Roots have a zero parent; selected parent IDs,
sampled flag, remote bit, and tracestate are preserved. Custom recorders and
decorators need the mandatory `SpanRecorder.Parent()` method; see
[writing a driver](telemetry-drivers.md#writing-a-driver).

Attributes, events, and status reflect calls through the facade, even when the
backend span was not recording. Facade timestamps and backend retention can
differ. Observation is not a native SDK export stream: spans and attributes
created only through a native SDK are not automatically mirrored.

Log records arrive with the context they were logged with, so
`telemetry.SpanFromContext(ctx).Context()` gives their IDs.
Records arrive when the application logger emits them, with or without a log
driver. Observers run on the goroutine that produced the signal, so keep them
fast, safe for concurrent use, and free of blocking or panics. They must return
and must not recursively produce the same signal on the observed telemetry.
A span is captured from its start, so spans that began before you attached are
not delivered.

Observer span snapshots and their containers are shared read-only; copy before
editing. Clone a log record before retaining it, and copy measurement label
values if you need them after the callback returns. `Stop` prevents notifications
not yet admitted; it neither waits for nor cancels already-admitted observer
callbacks. If your observer needs a completion barrier before releasing its own
resources, coordinate that in your application.

If the telemetry has no trace driver and the observer is also a
`telemetry.TraceDriver`, the observer fills in: it creates spans and IDs and
propagates `traceparent` while it stays attached. The same holds for a metric
driver. This is how trace IDs in logs and in `traceresponse` headers work in an
application that configured no telemetry. A configured driver is never
replaced, and if several observers could fill in, the first attached does.
The observer that fills in is not also notified through `Observer`, so it
records each span once. When no qualifying observer remains, the signal returns
to a no-op. Already-started spans still end on their original recorder; passive
completion is delivered only if telemetry is still enabled when that callback
is admitted, even if the observer detached meanwhile.

Two things stay your job. `Observe` does not start the observer, so start a
memory driver before you attach it or it records nothing. Stop that driver
when you finish using it; detaching does not stop it. And the telemetry
must be started: an application built without `application.Builder.Telemetry` holds an
unstarted no-op telemetry until something starts it, and
`telemetry.Default()` before `SetDefault` is shared and never started.

## Inspecting captured snapshots

To summarize local capture without changing it, inspect the started memory
driver `mirror` from the observer example. Import standard-library `log/slog`:

```go
failed := mirror.Spans().Filter(func(_ int, span memorytelemetry.Span) bool {
	return span.Status == telemetry.Error
})
warnings := mirror.Logs().Filter(func(_ int, entry memorytelemetry.Log) bool {
	return entry.Level >= slog.LevelWarn
})
byScope := mirror.Metrics().GroupBy(func(_ int, metric memorytelemetry.Metric) string {
	return metric.Scope
})
```

These methods return eager `collection.Slice` snapshots. Spans and logs are
oldest first; metrics follow instrument creation order, and grouping preserves
order within each group. Only retained finished spans and exported logs appear.
`LogsAfter(seq)` also returns a collection, containing retained records whose
`Seq` is greater than `seq`; continue from the last observed sequence, not the
number remaining after a filter.

These are not live queries: later recordings, ring eviction, and `Reset` do not
change a snapshot. The methods copy owned attributes, events, keys, points, and
label values before wrapping them. The collection wrapper itself does not copy
or acquire ownership. Empty logs remain nil; empty span and metric snapshots
remain allocated empty slices. `Reset` keeps instrument registrations but clears
their points; it does not restart log sequence numbers. For bounded capture and
backend differences, see [telemetry drivers](telemetry-drivers.md).

## Lifecycle

A `Telemetry` has a single lifecycle. Before `Start` and after `Stop` it is a
no-op, so you can create tracers and instruments before the application starts
and they begin recording when it does. A second `Start` returns an error
wrapping `telemetry.ErrStarted`, and `Start` after `Stop` wraps
`telemetry.ErrStopped`. A failed facade `Start` is terminal too: construct a new
`Telemetry` for another attempt. `Stop` without `Start` is safe and returns nil.

Facade and composite lifecycle calls stay serialized across backend startup,
rollback, and shutdown work. A canceled context does not interrupt waiting for
an earlier lifecycle call, forcibly terminate backend work, or make synchronous
reentry safe. Backend lifecycle callbacks must return and must not synchronously
call `Start` or `Stop` on the same facade or composite.

When you use `application.Builder.Telemetry`, the application manages startup
and shutdown. Startup receives the `Start` context without
retaining it. `Stop` disables new recordings and notifications before calling
the backend with the supplied context, including its cancellation. It is not an
observer drain or a replacement for draining application work and ending spans.
Already-admitted callbacks must return on their own.

Give flushing its own shutdown budget. The application gives it what remains
of the shutdown timeout, and at least 10 seconds. The
[OpenTelemetry driver](telemetry-drivers.md#behavior-and-limits-1) uses that
budget for cancelable flushing, then completes owned native teardown on an
uncanceled context; the whole `Stop` can outlast the deadline. Native hooks
must cooperate, and no forced cancellation is promised. If you build telemetry
outside an application, such as in a command-line tool, start and stop it
yourself. Inside a function with named return `err error`, use a bounded fresh shutdown context
and preserve flush failures (standard imports `context`, `errors`, and `time`):

```go
t := telemetry.New(oteltelemetry.New())

if err := t.Start(ctx, telemetry.Setup{
	Resource: telemetry.Resource{Service: "billing-cli", Version: "1.4.0"},
}); err != nil {
	return err
}
defer func() {
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err = errors.Join(err, t.Stop(shutdown))
}()
```

A short-lived process that exits without calling `Stop` can lose the last
batch of data, since exporters send in the background.
The deferred cleanup joins a flush failure with any operation error returned by
the enclosing function.

## Testing

`telemetrytest.New()` returns a fake that records spans, metrics, and logs in
memory so a test can assert on what your code reported. Here `NewService` and
`Order` are your application's constructor and order type, and the service
accepts a telemetry handle:

Import `telemetrytest` from `arkoren.dev/core/service/telemetry/telemetrytest`
and `testing` from the standard library:

```go
func TestChargeIsTraced(t *testing.T) {
	fake := telemetrytest.New()
	service := NewService(fake.Telemetry(t))

	if err := service.Charge(t.Context(), Order{Currency: "EUR"}); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "charge", telemetry.String("currency", "EUR"))
}
```

[Fakes](../testing/fakes.md) shows the available assertions.

## Where to go next

- [Instrumented components](instrumented-components.md) lists the spans and
  metrics that the database, cache, queue, storage, mail, broadcast, and
  scheduling components record on their own.
- [HTTP](request-tracing.md) covers request tracing, the trace ID in responses, and trust
  of incoming trace context.
- [Privacy](telemetry-privacy.md) explains what telemetry can contain and how to keep
  sensitive data out.
- [Drivers](telemetry-drivers.md) covers the memory, OpenTelemetry, and Prometheus
  drivers.
- [Health and readiness](../application/health-and-readiness.md) cover the liveness and readiness
  endpoints that sit beside telemetry.

For every function and option, see [the `telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
