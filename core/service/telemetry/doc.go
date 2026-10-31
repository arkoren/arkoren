// Package telemetry provides a vendor-neutral observability contract: traces,
// metrics, and logs, for Arkoren packages and for application code.
// Core never imports a vendor SDK;
// drivers adapt OpenTelemetry, Prometheus, or the in-process
// memory driver (arkoren.dev/core/service/telemetry/memorytelemetry) to it.
//
// # Model
//
// A [Telemetry] wraps a driver. Each signal is a capability interface that a
// driver implements or not: [TraceDriver], [MetricDriver], and [LogDriver],
// all built on [Lifecycle]. [Compose] picks a driver per signal, for example
// OpenTelemetry for traces and Prometheus for metrics. A signal without a
// driver is a no-op.
//
//	t := telemetry.New(memorytelemetry.New())
//	if err := t.Start(ctx, telemetry.Setup{}); err != nil {
//		return err
//	}
//	tracer := t.Tracer("example.com/billing")
//	meter := t.Meter("example.com/billing")
//
//	charges := meter.Counter("billing.charges",
//		telemetry.Unit("{charge}"),
//		telemetry.Keys("currency"),
//	)
//
//	ctx, span := tracer.Start(ctx, "charge", telemetry.Attrs(telemetry.String("currency", "EUR")))
//	charges.Add(ctx, 1, telemetry.String("currency", "EUR"))
//	span.End()
//	if err := t.Stop(ctx); err != nil {
//		return err
//	}
//
// The scope given to Tracer and Meter names the instrumentation, such as
// "arkoren.dev/core/service/cache" or the importing package's path.
//
// [Telemetry] owns the behavior that must be identical for every driver:
// the lifecycle, attribute validation, declared-key filtering, and the
// non-recording fast path. Drivers only record.
// Start and Stop are serialized and backend callbacks must return; cancellation
// is cooperative, not forced. Composite startup rollback preserves the original
// startup context, including an expired deadline. Give normal exporter shutdown
// a separate application-owned budget.
//
// # Attributes
//
// [Attr] is the attribute type: [String], [Int], [Int64], [Float64], [Bool],
// [Strings], and [Duration] (float seconds). It maps one to one onto
// OpenTelemetry attributes. Invalid attributes (empty key) are dropped.
// Span [Attrs] options snapshot their input containers. A single attribute is
// stored inline; constructing an option with multiple attributes copies its
// slice even if a later span start is disabled.
//
// # Metrics
//
// Instruments are created once with their name, [Unit], [Description],
// declared [Keys], and optional [Buckets]: [Meter.Counter],
// [Meter.UpDownCounter], [Meter.Histogram], and [Meter.Gauge]. Prometheus
// needs label names at creation and OpenTelemetry does not; declaring keys
// works on both and guards against cardinality mistakes. Recordings keep
// only declared keys, in declaration order, and missing keys become "".
// Names follow OpenTelemetry semantic conventions exactly as written;
// drivers translate them.
//
// # Propagation
//
// The driver owns the wire format (W3C Trace Context, B3, ...). Core calls
// [Telemetry.Inject] and [Telemetry.Extract] at process boundaries with a
// [Carrier]: [HeaderCarrier] for HTTP headers and [MapCarrier] for message or
// workflow metadata.
//
// Propagated context is only as trustworthy as its sender, so both HTTP
// boundaries are gated. [Middleware] extracts a caller's trace context only
// when [MiddlewareBuilder.TrustRemote] allows it. The default, including nil,
// is [TrustNone]: incoming headers never supply the server span's parent.
// This replaces the former [TrustInternal] default; trusted services must
// explicitly opt in with [TrustInternal], [MiddlewareBuilder.TrustedNetworks],
// or [TrustAll]. A private proxy address does not authenticate browser-supplied
// headers passing through it. Keep [TrustNone] at a public edge unless the
// proxy strips untrusted context. X-Forwarded-For is never consulted.
// [Transport] injects headers, baggage included, only where
// [TransportBuilder.Propagation] allows; the default, [PropagateInternal], covers
// private and loopback IPs, single-label names, and the suffixes .internal,
// .local, .svc, and .cluster.local. Add partner hosts with
// [TransportBuilder.PropagationHosts] or use [PropagateAll]. Spans and metrics are recorded
// either way.
//
// # Logs
//
// Logs stay [log/slog]. [Telemetry.Handler] wraps a handler so that records
// logged with a context holding a span get trace_id and span_id attributes,
// and, if the driver implements [LogDriver], are exported. Correlation works
// only with the context-aware methods (InfoContext, ErrorContext, ...), which
// the shared lint configuration enforces in Arkoren's own code.
//
// # HTTP
//
// [Telemetry.Middleware] traces and measures incoming requests, and
// [Telemetry.Transport] traces, measures, and propagates outgoing ones.
// [Middleware] and [Transport] do the same for [Default], resolved at request
// time. The middleware also exposes the trace ID on the W3C traceresponse
// header (optionally another header) and as a problem response member, so
// clients and support can quote it. Register it before other middleware.
//
//	r.Use(app.Telemetry().Middleware(func(middleware *telemetry.MiddlewareBuilder) {
//		middleware.TraceHeader("X-Trace-ID")
//	}))
//
//	client := &http.Client{Transport: app.Telemetry().Transport(nil)}
//
// # Lifecycle
//
// Construction never fails and does no I/O. [Telemetry.Start] validates and
// connects once; [Telemetry.Stop] flushes. Before Start and after
// Stop everything is a no-op; a second Start returns [ErrStarted]; Start
// after Stop returns [ErrStopped]; Stop without Start is safe. The
// application starts telemetry first and shuts it down last, so startup and
// shutdown logs and spans are still exported.
//
// # Defaults
//
// [Default] and [SetDefault] mirror [log/slog]. The default is a no-op that
// generates no IDs and does not allocate. Components resolve [Default] when
// they use it, not when they are built, so [SetDefault] may come late.
//
// # Observing
//
// [Telemetry.Observe] attaches an [Observer] at runtime. It receives every
// span (with the driver's IDs), log record, and measurement the telemetry
// produces, whatever the configured driver does with them, and an observer
// that is also a [TraceDriver] or [MetricDriver] fills in for a signal the
// telemetry has no driver for. See [Telemetry.Observe] for the rules.
// Capture stops admitting new notifications when telemetry stops, but Stop
// neither waits for nor interrupts a notification already admitted. [FinishedSpan]
// describes facade timing and backend-selected parent metadata; observers are not a
// native SDK export stream.
// Custom span recorders must implement [SpanRecorder.Parent]. Capture the
// effective parent before creating the child, return zero for roots and inactive
// spans, and forward Parent through decorators. Native parent precedence is
// unchanged. This required-method migration changes no JSON field names.
//
// # Instrumented components
//
// The core database, cache, feature, storage, mail, queue, and broadcast facades
// take a Telemetry setting on their role-specific construction builder, as does
// the HTTP client, schedule, search and LLM components. LLM exposes separate
// ModelBuilder and EmbedderBuilder roles. Nil telemetry follows [Default] per operation. They
// keep their instruments in [Instruments] and check [Telemetry.Enabled] first,
// so a disabled telemetry costs no allocations. Each records under its own
// package path as scope, such as "arkoren.dev/core/service/database"; the HTTP
// [Middleware] and [Transport] record under "arkoren.dev/core/service/telemetry".
//
// # Privacy
//
// Instrumentation records low-cardinality facts, never content. HTTP spans
// carry no URLs, query strings, headers, or bodies, and only 5xx responses
// are errors; error.type is a status code or Go type name, never error text.
// Database spans carry parameterised query text only. Cache spans omit keys
// and values. Storage spans carry the operation, store name, and object size,
// never paths or contents. Mail spans carry recipient and attachment counts,
// never addresses, subjects, or bodies. Schedule spans carry the entry name,
// kind, and outcome. Queue and broadcast spans carry the messaging system,
// destination (queue or channel), message ID, job, and attempt, never
// payloads. Errors of these components set error.type and the span status
// but are never passed to [Span.RecordError], because their text can carry
// values.
// [Span.RecordError] is for unexpected errors only; error text is exported,
// so keep secrets and personal data out of it.
package telemetry
