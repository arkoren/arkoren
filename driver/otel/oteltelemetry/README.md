# OpenTelemetry telemetry driver

Use this driver to export Arkoren traces, metrics, and logs over OTLP. Import
`arkoren.dev/driver/otel/oteltelemetry` alongside `arkoren.dev/core/service/telemetry`.

```go
driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
	instrumentation.Endpoint("https://collector.example.com:4318")
})
service := telemetry.New(driver)
```

Construction performs no network I/O. Start `service` through your application's
startup lifecycle and stop it after application work has drained. The default
transport is HTTP/protobuf; omitted connection settings retain the native
`OTEL_*` environment chain. Explicit URLs must use HTTP(S) without credentials,
nonempty query strings, or fragments. Set authentication through `Headers`, not
URL user information. Protect credentials in environment variables and headers.

Construction callbacks run once, synchronously in order. Headers are copied on
input and publication; retained drafts cannot reconfigure a driver. Runtime
metrics and the process-wide SDK error handler start enabled. Explicit
`RuntimeMetrics(false)` and `GlobalErrorHandler(false)` remain off.
The installed SDK error handler is not restored on Stop or failed startup. If
your application owns that process-wide policy, explicitly disable installation.

## Native providers and shutdown

If you supply native providers through the builder, you own their final
shutdown. They remain shared, not cloned. Connection settings are ignored for
those signals. Nil interfaces restore native defaults; typed-nil native pointers
fail at Start. Driver-installed runtime callbacks are removed on Stop and failed
startup, including on a borrowed meter provider.

Stop flushes with the supplied context, retains flush errors, and then completes
owned SDK teardown using an uncanceled context. Native shutdown may perform a
final export, so Stop can outlast the flush deadline; native exporter timeouts
still apply. Native hooks and borrowed-provider flushes must cooperate with
cancellation. The driver does not abandon teardown in a background goroutine.
Native spans obtained before Stop retain their native lifetime; drain application
work first. A canceled or failed Start remains retryable; a stopped driver does
not restart.

## Native and custom trace-driver interoperability

When native OpenTelemetry and Arkoren instrumentation share a context, a valid
native parent wins, even if it is non-recording or a different facade parent is
present. Otherwise the driver adopts a valid facade identity, including one from
the memory driver. The returned context still carries the native child for
native descendants and header injection.

To include facade-created spans in diagnostics, attach an observer through
`service.Observe`. With `service` and a `memorytelemetry` observer named `capture`
already started, and `provider` supplied through `TracerProvider`:

```go
unsubscribe := service.Observe(capture)
ctx, native := provider.Tracer("example.com/native").Start(ctx, "native operation")
_, span := service.Tracer("example.com/billing").Start(ctx, "cache get")
span.End()
native.End()
unsubscribe()
```

The captured `cache get` span's `Parent` contains the native parent's trace and
span IDs, sampled flag, remote bit, and tracestate, matching the native SDK
parent. Observation covers facade-created spans; it does not capture every
native SDK span.

If you implement a custom trace driver or wrap its recorders, every
`telemetry.SpanRecorder` must now implement or forward `Parent() telemetry.SpanContext`.
This is a required interface method, so existing custom implementations need a
source update. Capture the effective selected parent before the backend starts
its child and keep that snapshot immutable, including for unsampled children.
Return zero for roots and inactive recorders, even if an invalid incoming
identity carries flags or tracestate. Do not infer the parent later from the
child or the caller's facade context. No additional context key is needed, and
the diagnostic JSON field names are unchanged.

## Data handling

This is a transport adapter, not a redaction layer. Explicit log records,
`Span.RecordError` text, baggage, resource attributes, and metric labels can carry
sensitive data. Select approved values before recording them and protect the
collector and SDK-error logs. Low-level Extract does not authenticate remote
trace headers; use the application's inbound trust policy.

See the [package API](https://pkg.go.dev/arkoren.dev/driver/otel/oteltelemetry) for
configuration and lifecycle examples.
