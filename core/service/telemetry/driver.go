package telemetry

import (
	"context"
	"errors"
	"log/slog"
)

var (
	// ErrStarted is returned by Start when the driver or [Telemetry] was
	// already started.
	//
	//	if err := service.Start(ctx, telemetry.Setup{}); err != nil {
	//		if errors.Is(err, telemetry.ErrStarted) {
	//			fmt.Println("telemetry was started already")
	//		}
	//		return err
	//	}
	ErrStarted = errors.New("telemetry already started")

	// ErrStopped is returned by Start when the driver or [Telemetry] was
	// already shut down. A shut-down telemetry cannot be restarted.
	//
	//	if err := service.Start(ctx, telemetry.Setup{}); err != nil {
	//		if errors.Is(err, telemetry.ErrStopped) {
	//			fmt.Println("construct a new telemetry service")
	//		}
	//		return err
	//	}
	ErrStopped = errors.New("telemetry already shut down")
)

// Resource describes the process that emits telemetry. Drivers map it onto
// their backend's resource model.
// Resource values describe one process, not an individual request or user.
//
//	resource := telemetry.Resource{Service: "billing", Version: "1.2.3", Environment: "production"}
//	setup := telemetry.Setup{Resource: resource}
//	fmt.Println(setup.Resource.Service)
type Resource struct {
	// Service is the logical service name. When empty a driver falls back to
	// its native configuration, such as OTEL_SERVICE_NAME.
	//
	//	setup := telemetry.Setup{Resource: telemetry.Resource{Service: "billing"}}
	//	fmt.Println(setup.Resource.Service)
	Service string

	// Version is the service version.
	// Use release metadata rather than request-specific values.
	//
	//	resource := telemetry.Resource{Service: "billing", Version: "1.2.3"}
	//	fmt.Println(resource.Version)
	Version string

	// Environment is the deployment environment, such as production.
	// It describes the emitting deployment, not the caller's environment.
	//
	//	resource := telemetry.Resource{Service: "billing", Environment: "staging"}
	//	fmt.Println(resource.Environment)
	Environment string

	// Attrs are extra resource attributes.
	// Keep them process-scoped and free of secrets or personal identifiers.
	//
	//	resource := telemetry.Resource{Attrs: []telemetry.Attr{telemetry.String("region", "eu")}}
	//	fmt.Println(resource.Attrs[0].Text())
	Attrs []Attr
}

// Setup is everything a driver receives in [Lifecycle.Start]. It is a struct
// so fields can be added without breaking drivers.
// Supply it at startup; changing a caller's copy does not reconfigure a driver.
//
//	setup := telemetry.Setup{
//		Resource: telemetry.Resource{Service: "billing"}, Logger: slog.New(slog.DiscardHandler),
//	}
//	if err := service.Start(ctx, setup); err != nil {
//		return err
//	}
type Setup struct {
	// Resource describes the emitting process.
	//
	//	setup := telemetry.Setup{Resource: telemetry.Resource{Service: "billing"}}
	//	fmt.Println(setup.Resource.Service)
	Resource Resource

	// Logger is the application's unwrapped logger, for reporting exporter
	// failures. It is not wrapped by [Telemetry.Handler], so errors logged
	// through it are never exported again. Nil means "discard".
	//
	//	setup := telemetry.Setup{Logger: slog.New(slog.DiscardHandler)}
	//	setup.Logger.ErrorContext(ctx, "exporter unavailable")
	Logger *slog.Logger
}

// Lifecycle is the part of a driver that every signal driver shares. It has
// two phases: a pure constructor that only stores configuration, then Start,
// which validates it and performs I/O.
//
// Every implementation behaves as follows, and telemetrytest.Run verifies it:
//
//   - Before Start and after Stop, every other method of the driver is a
//     harmless no-op: spans are non-recording, recordings are discarded.
//   - A second Start returns an error wrapping [ErrStarted], and Start after
//     Stop returns an error wrapping [ErrStopped].
//   - Stop without Start is safe and returns nil. Stop flushes what
//     was recorded, honors ctx, and is idempotent.
//   - Methods are safe for concurrent use, including concurrently with
//     Stop.
//
// Typical explicit lifecycle ownership with an in-process driver:
//
//	var driver telemetry.Lifecycle = memorytelemetry.New()
//	if err := driver.Start(ctx, telemetry.Setup{}); err != nil {
//		return err
//	}
//	if err := driver.Stop(ctx); err != nil {
//		return err
//	}
type Lifecycle interface {
	// Start validates configuration and opens connections. The ctx bounds
	// startup only; the driver must not store it.
	//
	//	driver := memorytelemetry.New()
	//	if err := driver.Start(ctx, telemetry.Setup{Resource: telemetry.Resource{Service: "billing"}}); err != nil {
	//		return err
	//	}
	Start(ctx context.Context, setup Setup) error

	// Stop flushes buffered data and releases resources. After it
	// returns the driver is a no-op.
	//
	//	if err := driver.Stop(shutdownContext); err != nil {
	//		return err
	//	}
	Stop(ctx context.Context) error
}

// TraceDriver is the driver capability for traces and propagation.
//
// The facade ([Tracer], [Span]) validates attributes and keeps the fast path
// for disabled telemetry, so a driver only sees valid, owned data.
// Parent selection belongs to the backend; the returned recorder reports the
// actual decision through [SpanRecorder.Parent].
//
//	var traces telemetry.TraceDriver = memorytelemetry.New()
//	service := telemetry.New(traces)
//	fmt.Println(service.Enabled()) // false until Start
type TraceDriver interface {
	// Lifecycle is the one shared lifecycle for this driver's signals.
	// Passing a trace driver to New does not create a second lifecycle owner.
	//
	//	var traces telemetry.TraceDriver = memorytelemetry.New()
	//	var lifecycle telemetry.Lifecycle = traces
	//	service := telemetry.New(lifecycle)
	//	fmt.Println(service.Enabled())
	Lifecycle

	// StartSpan starts a span using the backend's parent-selection rules,
	// which may prefer native context over [SpanFromContext](ctx).Context().
	// Its recorder reports the actual decision through Parent. A driver
	// that wraps a native tracer returns a context
	// carrying the native span so native instrumentation parents correctly;
	// otherwise it returns ctx unchanged. The facade stores the returned
	// recorder's identity in the context itself.
	//
	// Before Start and after Stop it returns ctx and a non-recording
	// recorder with an invalid [SpanContext].
	//
	//	ctx, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get", Kind: telemetry.Client})
	//	fmt.Println(recorder.Context().IsValid(), recorder.Parent().IsValid())
	//	recorder.End()
	//	_ = ctx
	StartSpan(ctx context.Context, start SpanStart) (context.Context, SpanRecorder)

	// Inject writes the propagation headers of the current span of ctx into
	// carrier. It writes nothing when ctx has no valid span.
	//
	//	carrier := telemetry.MapCarrier{}
	//	driver.Inject(ctx, carrier)
	//	_ = carrier.Get("traceparent")
	Inject(ctx context.Context, carrier Carrier)

	// Extract reads propagation headers from carrier. It returns the context
	// to continue with (a driver with native context state puts it there;
	// otherwise ctx unchanged) and the extracted remote [SpanContext], which
	// has Remote set. Absent or malformed headers yield ctx and the zero
	// [SpanContext]; Extract never fails.
	//
	//	ctx, parent := driver.Extract(ctx, carrier)
	//	if parent.IsValid() {
	//		fmt.Println(parent.TraceID.String())
	//	}
	//	_ = ctx
	Extract(ctx context.Context, carrier Carrier) (context.Context, SpanContext)
}

// SpanStart describes a span to start.
// Drivers receive normalized kind and independently owned attribute containers
// from the facade; raw driver callers supply their own valid metadata.
//
//	start := telemetry.SpanStart{Scope: "example.com/cache", Name: "cache get", Kind: telemetry.Client}
//	_, recorder := driver.StartSpan(ctx, start)
//	recorder.End()
type SpanStart struct {
	// Scope is the instrumentation scope of the tracer, such as
	// "arkoren.dev/core/service/cache".
	//
	//	start := telemetry.SpanStart{Scope: "example.com/cache", Name: "cache get"}
	//	fmt.Println(start.Scope)
	Scope string

	// Name is the span name. It must have low cardinality.
	// Put request-specific values in attributes rather than the name.
	//
	//	start := telemetry.SpanStart{Name: "cache get"}
	//	fmt.Println(start.Name)
	Name string

	// Kind is the span kind. An empty value selects [Internal]; the facade
	// supplies that named default before calling the driver.
	//
	//	start := telemetry.SpanStart{Name: "cache get", Kind: telemetry.Client}
	Kind SpanKind

	// Attrs are the initial attributes. They are valid and owned by the
	// driver.
	//
	//	start := telemetry.SpanStart{Name: "cache get", Attrs: []telemetry.Attr{telemetry.Bool("cached", true)}}
	//	fmt.Println(start.Attrs[0].AsBool())
	Attrs []Attr
}

// SpanRecorder is the driver side of one span. The facade calls End at most
// once and no other method after End. Implementations must be safe for
// concurrent use. Context and Parent are immutable metadata captured when the
// backend starts the span, including for an unsampled span that keeps only IDs.
//
// Custom recorder migration: implement Parent() SpanContext on every recorder
// and decorator. Capture the effective parent before the backend creates its
// child; return zero for a root or inactive recorder. Forward the method through
// wrappers. Do not infer it later from caller context or from the child's ID.
// This is a required method, not an optional capability; JSON field names do not
// change.
//
//	_, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
//	parent := recorder.Parent()
//	_ = parent.IsValid()
//	recorder.End()
type SpanRecorder interface {
	// Context returns the identity of the span. It is invalid for a
	// non-recording span that was started before Start.
	//
	//	_, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
	//	fmt.Println(recorder.Context().IsValid())
	//	recorder.End()
	Context() SpanContext

	// Parent returns the actual parent selected by the backend at StartSpan,
	// not a guess from the facade's incoming context. Root and inactive spans
	// return the zero SpanContext. The result is fixed for the recorder's
	// lifetime and preserves the selected parent's flags and TraceState.
	//
	//	_, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
	//	if parent := recorder.Parent(); parent.IsValid() {
	//		fmt.Println(parent.SpanID.String())
	//	}
	//	recorder.End()
	Parent() SpanContext

	// Recording reports whether attributes and events are being kept. It is
	// false for spans that are only used for their IDs.
	//
	//	_, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
	//	if recorder.Recording() {
	//		recorder.AddEvent("hit", nil)
	//	}
	//	recorder.End()
	Recording() bool

	// SetAttributes adds attributes. A later attribute with the same key
	// replaces the earlier value.
	//
	//	_, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
	//	recorder.SetAttributes([]telemetry.Attr{telemetry.Bool("cached", true)})
	//	recorder.End()
	SetAttributes(attrs []Attr)

	// AddEvent records a timestamped event.
	// The facade supplies validated, owned attributes when it calls this method.
	//
	//	_, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
	//	recorder.AddEvent("retry", []telemetry.Attr{telemetry.Int("attempt", 2)})
	//	recorder.End()
	AddEvent(name string, attrs []Attr)

	// RecordError records err as an event named "exception" with the
	// attributes exception.type (the Go type of err) and exception.message.
	// It does not change the status.
	//
	//	_, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
	//	recorder.RecordError(errors.New("backend unavailable"))
	//	recorder.SetStatus(telemetry.Error, "operation failed")
	//	recorder.End()
	RecordError(err error)

	// SetStatus sets the status. [Unset] is ignored. [OK] is final: later
	// calls are ignored. [Error] replaces [Unset] and an earlier [Error]
	// status and its description. The description is kept only for [Error].
	//
	//	_, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
	//	recorder.SetStatus(telemetry.Error, "backend unavailable")
	//	recorder.End()
	SetStatus(code StatusCode, description string)

	// End finishes the span and hands it to the backend.
	// The facade waits for admitted recorder operations before calling it once.
	//
	//	_, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
	//	recorder.End()
	End()
}

// MetricDriver is the driver capability for metrics. The facade binds declared
// instruments after startup and owns portable validation and label filtering;
// implementations supply recorders and backend-specific aggregation.
//
//	var driver telemetry.MetricDriver = memorytelemetry.New()
//	service := telemetry.New(driver)
//	meter := service.Meter("example.com/cache") // Start service before recording.
//	_ = meter
type MetricDriver interface {
	// Lifecycle is shared with any trace or log capabilities on this driver.
	// Start and stop the owner once rather than once per signal.
	//
	//	var metrics telemetry.MetricDriver = memorytelemetry.New()
	//	var lifecycle telemetry.Lifecycle = metrics
	//	service := telemetry.New(lifecycle)
	//	fmt.Println(service.Enabled())
	Lifecycle

	// Instrument creates the recorder for one instrument. The facade calls
	// it once per scope and name for the current binding, after Start, and
	// binds again when an observer-supplied backend changes. An error disables that
	// instrument and is reported to [Setup.Logger]; the other instruments are
	// unaffected. Before Start and after Stop the returned recorder
	// discards recordings.
	//
	// The facade supplies independently owned Keys and Buckets containers;
	// the driver may retain or normalize them without changing facade
	// filtering or observer metadata. Construction must return before the
	// instrument can record; it may use other instrument identities, but must
	// not recursively record the instrument currently being constructed.
	//
	//	recorder, err := driver.Instrument(telemetry.InstrumentSpec{
	//		Scope: "example.com/cache", Name: "requests", Kind: telemetry.CounterKind,
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	recorder.Record(ctx, 1, nil)
	Instrument(spec InstrumentSpec) (InstrumentRecorder, error)
}

// InstrumentSpec describes an instrument. Backends that need label names at
// creation, such as Prometheus, use Keys; others may ignore it.
// Kind and declared Keys form the facade's schema identity within Scope/Name.
// Descriptions, units and optional buckets are backend metadata.
//
//	spec := telemetry.InstrumentSpec{
//		Scope: "example.com/cache", Name: "requests", Kind: telemetry.CounterKind, Keys: []string{"result"},
//	}
//	recorder, err := driver.Instrument(spec)
//	if err != nil {
//		return err
//	}
//	recorder.Record(ctx, 1, []string{"hit"})
type InstrumentSpec struct {
	// Scope is the instrumentation scope of the meter.
	//
	//	spec := telemetry.InstrumentSpec{Scope: "example.com/cache", Name: "requests", Kind: telemetry.CounterKind}
	//	fmt.Println(spec.Scope)
	Scope string

	// Name is the instrument name, written as an OpenTelemetry semantic
	// convention (dots, no unit suffix). Drivers translate it if their
	// backend needs another form.
	//
	//	spec := telemetry.InstrumentSpec{Name: "http.server.requests", Kind: telemetry.CounterKind}
	//	fmt.Println(spec.Name)
	Name string

	// Description is the human-readable help text. It may be empty.
	//
	//	spec := telemetry.InstrumentSpec{
	//		Name: "requests", Kind: telemetry.CounterKind, Description: "Completed cache operations.",
	//	}
	//	fmt.Println(spec.Description)
	Description string

	// Unit is the UCUM unit, such as "s" or "{request}". It may be empty.
	//
	//	spec := telemetry.InstrumentSpec{Name: "duration", Kind: telemetry.HistogramKind, Unit: "s"}
	//	fmt.Println(spec.Unit)
	Unit string

	// Kind is the instrument kind.
	//
	//	spec := telemetry.InstrumentSpec{Name: "inflight", Kind: telemetry.UpDownCounterKind}
	//	fmt.Println(spec.Kind == telemetry.UpDownCounterKind)
	Kind InstrumentKind

	// Keys are the declared label keys, without duplicates or empty names,
	// in declaration order.
	//
	//	spec := telemetry.InstrumentSpec{Name: "requests", Kind: telemetry.CounterKind, Keys: []string{"result"}}
	//	fmt.Println(spec.Keys[0]) // First recording value corresponds to result.
	Keys []string

	// Buckets are the optional explicit histogram bucket upper bounds in
	// ascending order. A driver may ignore the hint; it is only set for
	// histograms.
	//
	//	spec := telemetry.InstrumentSpec{Name: "duration", Kind: telemetry.HistogramKind, Buckets: []float64{0.1, 1}}
	//	fmt.Println(spec.Buckets) // Raw callers supply ascending bounds.
	Buckets []float64
}

// InstrumentRecorder is the driver side of one instrument. Implementations
// must be safe for concurrent use.
// Values are per-recording input, not persistent labels owned by the recorder.
//
//	recorder, err := driver.Instrument(telemetry.InstrumentSpec{Name: "requests", Kind: telemetry.CounterKind})
//	if err != nil {
//		return err
//	}
//	recorder.Record(ctx, 1, nil)
type InstrumentRecorder interface {
	// Record records one measurement. For counters and up-down counters it
	// is the increment, for histograms the observed value, and for gauges
	// the new value. Counter increments are never negative. values holds one
	// label value per declared key, in declared order, already normalised by
	// the facade; non-string attributes arrive in their [Attr.Text] form.
	// values is only valid during the call, so a driver that keeps it must
	// copy it. ctx lets a driver attach exemplars linked to the current
	// trace.
	//
	//	recorder, err := driver.Instrument(telemetry.InstrumentSpec{
	//		Name: "requests", Kind: telemetry.CounterKind, Keys: []string{"result"},
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	recorder.Record(ctx, 1, []string{"hit"})
	Record(ctx context.Context, value float64, values []string)
}

// LogDriver is the driver capability for exporting log records.
// The facade folds logger groups and attributes before Export; the driver does
// not need the slog handler's construction state to interpret a record.
//
//	var logs telemetry.LogDriver = memorytelemetry.New()
//	service := telemetry.New(logs)
//	logger := slog.New(service.Handler(slog.DiscardHandler))
//	_ = logger // Start service before exporting records.
type LogDriver interface {
	// Lifecycle owns startup and cleanup for all signals supplied by this driver.
	// A log-only driver still participates in normal application lifecycle.
	//
	//	var logs telemetry.LogDriver = memorytelemetry.New()
	//	var lifecycle telemetry.Lifecycle = logs
	//	service := telemetry.New(lifecycle)
	//	fmt.Println(service.Enabled())
	Lifecycle

	// Enabled reports whether Export would accept a record at level. The
	// handler returned by [Telemetry.Handler] uses it to skip work.
	//
	//	if driver.Enabled(ctx, slog.LevelInfo) {
	//		fmt.Println("exporter accepts info records")
	//	}
	Enabled(ctx context.Context, level slog.Level) bool

	// Export sends record to the backend. The record is self-contained:
	// attributes and groups added through WithAttrs and WithGroup are
	// already part of it. The record carries no trace_id or span_id
	// attributes; use [SpanFromContext] on ctx to correlate. The driver must
	// not retain the record without cloning it.
	//
	//	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "operation completed", 0)
	//	if err := driver.Export(ctx, record); err != nil {
	//		return err
	//	}
	Export(ctx context.Context, record slog.Record) error
}
