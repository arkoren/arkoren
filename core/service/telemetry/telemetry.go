package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"

	"arkoren.dev/core/internal/unwrap"
)

// lifecycleState is the position of a [Telemetry] in its single lifecycle. The
// zero value means Start has not been called. It stays numeric because it is
// private process state, never an API, configuration, or storage value.
type lifecycleState uint8

const (
	// stateStarted means the driver is running.
	stateStarted lifecycleState = iota + 1

	// stateStopped means Stop was called or Start failed.
	stateStopped
)

// Telemetry is the facade over a telemetry driver. It owns the behavior that
// must be identical for every driver: the single lifecycle, attribute
// validation, declared-key filtering for metrics, and a cheap non-recording
// fast path. Drivers stay small.
//
// Signals the driver does not implement fall back to a no-op, unless an
// observer fills in (see [Telemetry.Observe]). Until Start
// succeeds, and after Stop, everything is a no-op, so instrumentation can
// be written unconditionally and components can be built before the
// application starts.
//
// A Telemetry is safe for concurrent use and must not be copied after first
// use. Obtain the application's already-started service for instrumentation:
//
//	service := app.Telemetry()
//	_, span := service.Tracer("example.com/billing").Start(ctx, "charge")
//	span.End()
type Telemetry struct {
	// driver is the driver given to New. Nil for a no-op Telemetry.
	driver Lifecycle

	// traces, metrics, and logs are the capabilities of driver, nil when
	// absent.
	traces TraceDriver
	// metrics is the configured metric driver, before observer routing is applied.
	metrics MetricDriver
	// logs is the configured log exporter, independent of passive observers.
	logs LogDriver

	// active is true between a successful Start and Stop. It is the only
	// state the hot path reads.
	active atomic.Bool

	// logger reports driver failures. It is set by Start before active is
	// published and never nil.
	logger *slog.Logger

	// lifecycle serializes Start and Stop and guards state.
	lifecycle sync.Mutex

	// state is the lifecycle position.
	state lifecycleState

	// instrumentsMu guards instruments.
	instrumentsMu sync.Mutex

	// instruments holds one binding per exact scope/name pair. A structured
	// key keeps embedded NUL bytes from changing the pair's identity.
	instruments map[instrumentIdentity]*boundInstrument

	// signals is the routing table of drivers in effect and observers. The
	// hot paths read it with a single atomic load; [Telemetry.Observe]
	// replaces it. It is never nil after [New].
	signals atomic.Pointer[signals]

	// observeMu guards subscriptions and serializes publishing signals.
	observeMu sync.Mutex

	// subscriptions are the attached observers in attach order.
	subscriptions []*subscription
}

// instrumentIdentity identifies a binding without interpreting scope or name
// bytes. Keeping both strings separate prevents delimiter collisions.
type instrumentIdentity struct {
	// scope is the instrumentation scope exactly as supplied by the meter.
	scope string

	// name is the instrument name within scope, without escaping or joining.
	name string
}

// boundInstrument is an instrument's binding to the metric driver in effect.
type boundInstrument struct {
	// spec is what the binding was created from.
	spec InstrumentSpec

	// source identifies the subscription supplying metrics, or nil for the
	// immutable configured driver (including its absence). Pointer identity
	// works even when the selected driver's value is not comparable.
	source *subscription

	// once publishes recorder and disabled before bind returns. Backend
	// construction runs outside instrumentsMu, so it may use other instruments.
	once sync.Once

	// recorder is the driver recorder, nil without a driver.
	recorder InstrumentRecorder

	// disabled is true when the driver rejected the instrument or its scope
	// and name were redefined with another kind or keys.
	disabled bool
}

// New returns a Telemetry over driver. The driver's capabilities are found by
// type assertion: implement [TraceDriver], [MetricDriver], and [LogDriver] as
// needed, or pass the result of [Compose] to mix drivers per signal. A nil
// driver gives a no-op Telemetry.
//
// New performs no I/O; call [Telemetry.Start] before use. The application
// does it for you.
//
//	service := telemetry.New(memorytelemetry.New())
//	if err := service.Start(ctx, telemetry.Setup{}); err != nil {
//		return err
//	}
//	_, span := service.Tracer("example.com/billing").Start(ctx, "charge")
//	span.End()
//	if err := service.Stop(ctx); err != nil {
//		return err
//	}
func New(driver Lifecycle) *Telemetry {
	t := &Telemetry{
		driver: driver,
		logger: slog.New(slog.DiscardHandler),
	}

	if composite, ok := driver.(*Composite); ok {
		t.traces, t.metrics, t.logs = composite.traces, composite.metrics, composite.logs
	} else {
		t.traces, _ = driver.(TraceDriver)
		t.metrics, _ = driver.(MetricDriver)
		t.logs, _ = driver.(LogDriver)
	}

	t.publishSignals()

	return t
}

// Driver returns the driver given to [New], or nil for a no-op Telemetry.
// Direct driver calls bypass facade validation and observation. Prefer facade
// operations unless native integration specifically needs the lifecycle value.
//
//	store := memorytelemetry.New()
//	service := telemetry.New(store)
//	if driver, ok := service.Driver().(*memorytelemetry.Driver); ok {
//		fmt.Println(len(driver.Spans()))
//	}
func (telemetry *Telemetry) Driver() Lifecycle {
	return telemetry.driver
}

// DriverAs returns the first driver of type T behind telemetry. A driver that wraps
// another exposes it with an Unwrap() Lifecycle method, and a [Compose]d
// driver is searched part by part.
//
//	if memory, ok := t.DriverAs[*memorytelemetry.Driver](); ok {
//		_ = memory.Spans()
//	}
func (telemetry *Telemetry) DriverAs[T any]() (T, bool) {
	parts := []Lifecycle{telemetry.driver}

	if composite, ok := telemetry.driver.(*Composite); ok {
		parts = composite.drivers
	}

	for _, part := range parts {
		if found, ok := unwrap.As[T](part); ok {
			return found, true
		}
	}

	var zero T

	return zero, false
}

// Start starts the driver with setup. It returns an error wrapping
// [ErrStarted] when already started and [ErrStopped] after Stop or a
// failed Start. ctx is passed to backend startup only and is not retained.
// Starting a Telemetry with no driver succeeds and makes Enabled true, allowing
// attached observers to receive signals; absent signals still do nothing.
// Lifecycle calls are serialized. A backend must return from its callbacks and
// must not synchronously reenter Start or Stop on the same facade; ctx does not
// interrupt waiting for an earlier lifecycle call to finish.
//
//	service := telemetry.New(nil)
//	if err := service.Start(ctx, telemetry.Setup{}); err != nil {
//		return err
//	}
//	fmt.Println(service.Enabled()) // true, even without a backend
func (telemetry *Telemetry) Start(ctx context.Context, setup Setup) error {
	telemetry.lifecycle.Lock()
	defer telemetry.lifecycle.Unlock()

	switch telemetry.state {
	case stateStarted:
		return fmt.Errorf("start telemetry: %w", ErrStarted)
	case stateStopped:
		return fmt.Errorf("start telemetry: %w", ErrStopped)
	}

	if setup.Logger != nil {
		telemetry.logger = setup.Logger
	}

	if telemetry.driver != nil {
		if err := telemetry.driver.Start(ctx, setup); err != nil {
			telemetry.state = stateStopped

			return fmt.Errorf("start telemetry: %w", err)
		}
	}

	telemetry.state = stateStarted
	telemetry.active.Store(true)

	return nil
}

// Stop stops recording, then shuts the driver down so it can flush. It is
// safe without Start, safe to call twice, and returns nil in both cases. After
// it returns, Telemetry is a no-op for good. ctx is forwarded unchanged to the
// backend, including cancellation. Stop does not forcibly interrupt backend or
// observer callbacks, wait for observer notifications already admitted, or
// interrupt an earlier serialized lifecycle call. Drivers own
// resource release and must return from Stop; applications should give exporter
// flushing its own shutdown budget rather than reuse an expired startup budget.
//
//	if err := service.Stop(shutdownContext); err != nil {
//		return err
//	}
func (telemetry *Telemetry) Stop(ctx context.Context) error {
	telemetry.lifecycle.Lock()
	defer telemetry.lifecycle.Unlock()

	wasStarted := telemetry.state == stateStarted
	telemetry.state = stateStopped
	telemetry.active.Store(false)

	telemetry.instrumentsMu.Lock()

	telemetry.instruments = nil
	telemetry.instrumentsMu.Unlock()

	if !wasStarted || telemetry.driver == nil {
		return nil
	}

	if err := telemetry.driver.Stop(ctx); err != nil {
		return fmt.Errorf("shut down telemetry: %w", err)
	}

	return nil
}

// Enabled reports whether telemetry is started and so records signals. It is false
// for the no-op default, before Start, and after Stop. Instrumented
// components check it first to skip all work, including building attributes,
// when nothing would be recorded. A nil receiver is not enabled.
//
//	if !telemetry.Default().Enabled() {
//		return run()
//	}
func (telemetry *Telemetry) Enabled() bool {
	return telemetry != nil && telemetry.active.Load()
}

// Tracer returns a Tracer for the instrumentation scope, such as
// "arkoren.dev/core/service/cache" or the importing package's path. It is cheap; resolve
// it where it is used.
//
//	tracer := service.Tracer("example.com/billing")
//	_, span := tracer.Start(ctx, "charge")
//	span.End()
func (telemetry *Telemetry) Tracer(scope string) Tracer {
	return Tracer{
		telemetry: telemetry,
		scope:     scope,
	}
}

// Meter returns a Meter for the instrumentation scope.
// Instrument declarations are cheap and bind to the active backend on use.
//
//	meter := service.Meter("example.com/billing")
//	charges := meter.Counter("charges", telemetry.Keys("currency"))
//	charges.Add(ctx, 1, telemetry.String("currency", "EUR"))
func (telemetry *Telemetry) Meter(scope string) Meter {
	return Meter{
		telemetry: telemetry,
		scope:     scope,
	}
}

// Inject writes the propagation headers of the current span of ctx into
// carrier, in the driver's format (W3C Trace Context for the memory driver).
// It does nothing without a started trace driver (or filling-in observer, see
// [Telemetry.Observe]) or a valid span.
// Initialize mutable carriers before use; propagation alone does not authenticate
// a destination or scrub baggage. Apply the application's boundary policy first.
//
//	ctx, span := service.Tracer("example.com/cache").Start(ctx, "cache get")
//	carrier := telemetry.MapCarrier{}
//	service.Inject(ctx, carrier)
//	span.End()
//	fmt.Println(carrier.Get("traceparent"))
func (telemetry *Telemetry) Inject(ctx context.Context, carrier Carrier) {
	traces := telemetry.signals.Load().traces

	if traces == nil || carrier == nil || !telemetry.active.Load() {
		return
	}

	traces.Inject(ctx, carrier)
}

// Extract reads propagation headers from carrier and returns a context whose
// current span is the remote parent, so the next span started from it
// continues the remote trace. Absent or malformed headers return ctx
// unchanged.
// This low-level operation performs no peer authentication; HTTP middleware
// applies its trust predicate before invoking it for an inbound request.
//
//	ctx := service.Extract(ctx, carrier)
//	_, span := service.Tracer("example.com/worker").Start(ctx, "process")
//	span.End()
func (telemetry *Telemetry) Extract(ctx context.Context, carrier Carrier) context.Context {
	traces := telemetry.signals.Load().traces

	if traces == nil || carrier == nil || !telemetry.active.Load() {
		return ctx
	}

	ctx, spanContext := traces.Extract(ctx, carrier)

	if !spanContext.IsValid() {
		return ctx
	}

	spanContext.Remote = true

	return ContextWithSpanContext(ctx, spanContext)
}

// bind returns the binding of spec to table's metric driver in effect
// (nil when none), creating it on first use and again when the driver in
// effect changed. A second spec with the same scope and name must have the
// same kind and keys; otherwise its binding is disabled and a warning is
// logged. Scope and name are compared byte-for-byte as separate strings,
// including embedded NUL bytes. A driver error disables the binding the same way.
func (telemetry *Telemetry) bind(ctx context.Context, spec InstrumentSpec, table *signals) *boundInstrument {
	bound, conflict := telemetry.reserveBinding(spec, table.metricSource)

	if conflict {
		telemetry.logger.WarnContext(ctx, "telemetry instrument redefined with another kind or keys; disabled",
			"scope", spec.Scope, "name", spec.Name)

		return bound
	}

	var err error
	bound.once.Do(func() {
		if table.metrics == nil {
			bound.disabled = false

			return
		}

		// The backend owns its metadata containers; neither its normalization
		// nor retained edits may change facade filtering or observer metadata.
		definition := bound.spec
		definition.Keys = slices.Clone(definition.Keys)
		definition.Buckets = slices.Clone(definition.Buckets)

		bound.recorder, err = table.metrics.Instrument(definition)
		bound.disabled = err != nil
	})

	if err != nil {
		telemetry.logger.WarnContext(ctx, "telemetry instrument rejected by driver; disabled",
			"scope", spec.Scope, "name", spec.Name, "err", err)
	}

	return bound
}

// reserveBinding returns the cached binding or reserves an uninitialized one.
// It compares schema and registration identity while holding instrumentsMu, but
// never invokes drivers or log handlers. A schema conflict returns a disabled
// uncached binding; the first published schema remains authoritative.
func (telemetry *Telemetry) reserveBinding(spec InstrumentSpec, source *subscription) (*boundInstrument, bool) {
	telemetry.instrumentsMu.Lock()
	defer telemetry.instrumentsMu.Unlock()

	key := instrumentIdentity{scope: spec.Scope, name: spec.Name}

	if bound, ok := telemetry.instruments[key]; ok {
		if bound.spec.Kind != spec.Kind || !slices.Equal(bound.spec.Keys, spec.Keys) {
			return &boundInstrument{
				source:   source,
				disabled: true,
			}, true
		}

		if bound.source == source {
			return bound, false
		}
	}

	bound := &boundInstrument{
		spec:     spec,
		source:   source,
		disabled: true,
	}

	if telemetry.instruments == nil {
		telemetry.instruments = make(map[instrumentIdentity]*boundInstrument)
	}

	telemetry.instruments[key] = bound

	return bound, false
}
