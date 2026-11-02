---
title: Telemetry
description: Share a telemetry handle, record application work, and manage propagation, observation, and shutdown.
order: 10
summary:
  - Share a started telemetry handle through explicit service options or the process default.
  - Record bounded spans and metrics, propagate trusted context, and correlate logs.
  - Respect observer ownership and shutdown limits without treating telemetry as an audit ledger.
---

# Telemetry

Use telemetry to connect a slow request to the operations it called and to
measure recurring behavior across requests. A **span** describes one operation;
related spans form a **trace**. **Metrics** aggregate measurements over time.
Context-aware logs can carry the same trace identity.

`telemetry.Telemetry` is the shared handle for these signals. Its driver decides
where they go; choose and configure one on [Drivers](telemetry-drivers.md).
Telemetry can be sampled, aggregated, or dropped. It is not an audit ledger or
proof that a business action completed; use [auditing with events](../events/auditing.md)
when individual actions need a durable record.

## Shared Handle

With your selected `driver`, give the handle to the application. Import
`application` from `arkoren.dev/core/application` and `telemetry` from
`arkoren.dev/core/service/telemetry`:

```go
app := application.New(func(app *application.Builder) {
	app.Telemetry(telemetry.New(driver))
})
```

Construction performs no I/O. After composition succeeds, the application starts
telemetry before user startup callbacks and stops it after cleanup and the final
`app stopped` log. It supplies the service name, version, and environment as the
telemetry resource. Without configured telemetry or diagnostic capture,
`app.Telemetry()` remains a nonnil, unstarted no-op handle.

Pass that handle explicitly when constructing an instrumented service. With an
existing database driver `backend` and `database` imported from
`arkoren.dev/core/service/database`:

```go
db := database.New(backend, func(connection *database.Builder) {
	connection.Telemetry(app.Telemetry())
})
```

The [component catalog](instrumented-components.md) identifies each builder and
its observations. An omitted or nil handle follows `telemetry.Default()` at
operation time. To publish the application's logger and telemetry together:

```go
app.SetDefaults()
```

This changes process-wide state; prefer explicit handles when applications or
tests share a process. `telemetry.SetDefault(nil)` restores the unstarted no-op
default. Publishing a handle does not start it.

Focused fragments keep error results visible and omit routine propagation.
Complete application code must handle those errors before using results.

## Recording Spans

Suppose your report renderer accepts a caller's `ctx` and a validated format
restricted to `pdf` or `csv`. Name the tracer's **instrumentation scope** after
your application's package, and keep the operation name fixed:

```go
tracer := app.Telemetry().Tracer("example.com/reports")
ctx, span := tracer.Start(ctx, "report render",
	telemetry.Attrs(telemetry.String("format", format)),
)
defer span.End()
```

Pass the returned context to the renderer's dependencies. Their spans can then
be children of this span. The default kind is `Internal`; use `Client` for a
remote call, `Producer` for sending a message, and `Server` or `Consumer` for
receiving work. Without a started trace driver or a trace-capable observer,
`Start` returns the original context and a non-recording span without new IDs.

Record a failure with a fixed description rather than raw diagnostics. Here
`render` is your existing `func(context.Context, string) error`:

```go
err := render(ctx, format)
if err != nil {
	span.SetStatus(telemetry.Error, "render failed")
	span.SetAttributes(telemetry.String("error.type", "render_failed"))
}
```

`RecordError(err)` also records `err.Error()` as an exception event. Use it only
when that text is approved for capture and export. Built-in components classify
failures without recording their text; see [Telemetry Privacy](telemetry-privacy.md).
`Unset` is the initial status, not a failure. `OK` is final; setting `Unset`, an
empty status, or an unknown status does not clear an existing verdict.

Call `End` on every span, normally with `defer`. Its first call closes admission
to updates and waits for already-admitted recorder calls; subsequent calls and
late updates do nothing. Recorder callbacks must return and must not
synchronously end their own span, including from error formatting.

Attributes use `String`, `Int`, `Int64`, `Float64`, `Bool`, `Strings`, or
`Duration` (float seconds). Empty keys are dropped. Attribute containers are
copied at publication and backend boundaries, but copying does not sanitize
their contents. Check `span.IsRecording()` before expensive attribute work.

## Recording Metrics

Create instruments once, for example in your service constructor. For the same
report renderer, count completions by the bounded format value:

```go
meter := app.Telemetry().Meter("example.com/reports")
renders := meter.Counter("reports.renders",
	telemetry.Unit("{render}"), telemetry.Keys("format"),
)
```

At the operation boundary, record one observation with its context:

```go
renders.Add(ctx, 1, telemetry.String("format", format))
```

Use a histogram for distributions, an up-down counter for in-flight work, and a
gauge for a current value. To record rendering duration, import standard-library
`time`; `startedAt` is captured before rendering:

```go
duration := meter.Histogram("reports.render.duration",
	telemetry.Unit("s"), telemetry.Keys("format"),
)
duration.Record(ctx, time.Since(startedAt).Seconds(), telemetry.String("format", format))
```

Keep the histogram declaration in the constructor too. Instruments can be
declared before startup and bind on first use afterward. Matching scope/name
pairs share a backend instrument; a later declaration with different kind or
keys is disabled with a warning through `Setup.Logger`. Driver rejection also
disables that instrument, not the observed application operation.

`Keys` filters recordings to the declared schema. Missing values become empty
strings; undeclared keys disappear and non-string values become text. This does
not bound the number or size of label values: never use request IDs, arbitrary
paths, or user input. Negative counter additions and NaN histogram values are
ignored. Bucket hints are sorted, deduplicated, and stripped of non-finite bounds.
Published key and bucket containers are detached from option drafts, and backends
receive independent copies; retaining or normalizing a draft cannot change the
facade's label schema.

For reusable package instrumentation that follows changing handles,
`telemetry.NewInstruments` lazily builds and caches instruments for the most
recent handle; `For(handle)` rebuilds when the handle changes. Do not keep one
global instrument bound to an unrelated application's handle. `Enabled()` is a
startup-state check, not proof of a configured signal: a started `New(nil)` also
returns true so observers can participate.

## Trace Propagation

A process boundary needs serialized trace context, not a transported Go context.
HTTP, queues, broadcast, and workflows provide their own integration. For an
application-specific transport, initialize a carrier and inject the sending
context:

```go
metadata := telemetry.MapCarrier{}
app.Telemetry().Inject(ctx, metadata)
```

Send this carrier with the message. Only after authenticating or otherwise
trusting its source should the receiver extract it into its local operation
context:

```go
ctx = app.Telemetry().Extract(ctx, metadata)
```

`HeaderCarrier` adapts `http.Header`; `MapCarrier` adapts string metadata. The
driver owns the wire format. Missing or malformed context leaves the context
unchanged. Raw extraction does not authenticate the sender or scrub baggage.
[Request Tracing](request-tracing.md#incoming-trust) explains the separate HTTP
defaults: reject incoming headers, but allow outgoing injection to internal
destinations.

## Correlated Logs

The application wraps its logger. Use context-aware methods while the context
contains a valid span:

```go
app.Logger().InfoContext(ctx, "report rendered", "format", format)
```

The normal handler receives `trace_id` and `span_id`; plain `Info` has no
context and adds neither. A log-capable driver can also export the complete
record. Correlation is not redaction: review log messages and attributes before
enabling [log export](telemetry-privacy.md#logs).

`telemetry.SpanFromContext(ctx)` never returns nil. Its `Context()` exposes
`TraceID` and `SpanID`, whose `String()` methods return hex. A valid identity
does not guarantee that a backend retained the trace.

## Passive Observation

Use `Observe` when another in-process component needs facade observations
without replacing the configured backend. With an existing started
`observer` implementing `telemetry.Observer`:

```go
unsubscribe := app.Telemetry().Observe(observer)
defer unsubscribe()
```

Observation neither starts nor stops the observer, and the facade must also be
started. For application-owned bounded history and protected access, use
[Capture Setup](../diagnostics/capture-setup.md#attach-capture), not an ad hoc
public debug endpoint.

Callbacks are synchronous on producing goroutines and can run concurrently.
Keep them fast, nonblocking, panic-free, and nonrecursive for the same signal.
Finished spans report the backend-selected context and parent; attributes,
events, status, and timestamps reflect facade calls, not the whole native SDK
stream. Native OpenTelemetry parent selection takes precedence when valid.

Span snapshots are shared read-only. Copy containers before editing, clone log
records before retaining them, and copy measurement label values before the
callback returns. Only spans started while subscribed are captured; they can
finish after detachment if notification is admitted while telemetry remains
enabled. `Stop` suppresses new notifications but neither drains nor cancels
already-admitted callbacks. Coordinate your own observer completion barrier.

An observer implementing `TraceDriver` or `MetricDriver` can supply a missing
signal; the first eligible observer wins and a configured driver is never
replaced. That observer records through its driver role, without duplicate
passive notification for the same signal. Detaching restores the next eligible
observer or the no-op; existing spans retain their original recorder.

## Lifecycle Rules

Before `Start` and after `Stop`, the facade records nothing. A second `Start`
wraps `ErrStarted`; a start after stop or failed startup wraps `ErrStopped`.
Construct a new handle to retry a failed startup. `Stop` is safe before startup
and on repeated calls. Lifecycle calls serialize across backend work; their
callbacks must return and must not synchronously reenter the same lifecycle.
Cancellation cannot forcibly interrupt backend work or waiting for an earlier
lifecycle call.
Startup receives its context without retaining it; stop forwards the supplied
context, including cancellation, after disabling new recordings and notifications.

The application manages this lifecycle for a configured handle. It gives
telemetry the remaining shutdown budget, with at least ten seconds. Drain
application work and end spans before telemetry stops. Background exporters can
lose the final batch if the process exits without stopping them.

Outside an application, start the handle yourself. Here `handle` is your
configured `*telemetry.Telemetry`:

```go
err := handle.Start(ctx, telemetry.Setup{
	Resource: telemetry.Resource{Service: "reports-cli"},
})
```

After successful startup and completed work, use a fresh bounded shutdown context
instead of reusing a canceled operation context. Import `context`, `errors`, and
`time`; `err` is the operation error you want to preserve:

```go
shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
defer cancel()
err = errors.Join(err, handle.Stop(shutdown))
```

Preserve shutdown errors. OpenTelemetry flushes with this budget but completes
owned native teardown on an uncanceled context, so `Stop` can outlast the
deadline; see [export lifecycle](telemetry-drivers.md#export-lifecycle).

For assertions on reported work, use the [telemetry fake](../testing/fakes.md).
For all declarations, see the
[`telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
