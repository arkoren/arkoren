package telemetry_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// errBoom is the error injected by test drivers.
var errBoom = errors.New("boom")

// stubDriver is a metric driver that records lifecycle calls and
// recordings.
type stubDriver struct {
	// name identifies the driver in the shared call log.
	name string

	// calls is the shared, ordered log of lifecycle calls.
	calls *callLog

	// failStart and failStop inject errors.
	failStart, failStop bool

	// rejectInstrument makes Instrument fail.
	rejectInstrument bool

	// mu guards the fields below.
	mu sync.Mutex

	// specs holds the created instrument specs and records the recordings.
	specs []telemetry.InstrumentSpec
	// records captures every accepted measurement for later assertions.
	records []record
}

// record is one measurement received by a stubDriver.
type record struct {
	// value is the numeric measurement passed to the recorder.
	value float64
	// values holds the label values aligned with the instrument's declared keys.
	values []string
}

// callLog is a goroutine-safe ordered list of strings.
type callLog struct {
	// mu protects lifecycle calls made by concurrent test operations.
	mu sync.Mutex
	// items preserves the observed order of lifecycle actions.
	items []string
}

// add appends one lifecycle action while excluding concurrent readers and writers.
func (log *callLog) add(item string) {
	log.mu.Lock()
	defer log.mu.Unlock()

	log.items = append(log.items, item)
}

// list returns a detached snapshot so assertions do not retain mutable storage.
func (log *callLog) list() []string {
	log.mu.Lock()
	defer log.mu.Unlock()

	return slices.Clone(log.items)
}

// Start records the attempt and returns the configured startup outcome.
func (driver *stubDriver) Start(context.Context, telemetry.Setup) error {
	driver.calls.add("start " + driver.name)

	if driver.failStart {
		return errBoom
	}

	return nil
}

// Stop records the attempt and returns the configured shutdown outcome.
func (driver *stubDriver) Stop(context.Context) error {
	driver.calls.add("stop " + driver.name)

	if driver.failStop {
		return errBoom
	}

	return nil
}

// Instrument captures accepted declarations and records subsequent measurements.
// A configured rejection fails before adding the declaration to the snapshot.
func (driver *stubDriver) Instrument(spec telemetry.InstrumentSpec) (telemetry.InstrumentRecorder, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.rejectInstrument {
		return nil, errBoom
	}

	driver.specs = append(driver.specs, spec)

	return recorderFunc(func(_ context.Context, value float64, values []string) {
		driver.mu.Lock()
		defer driver.mu.Unlock()

		driver.records = append(driver.records, record{
			value:  value,
			values: slices.Clone(values),
		})
	}), nil
}

// recorderFunc adapts a function to telemetry.InstrumentRecorder.
type recorderFunc func(context.Context, float64, []string)

// Record invokes the callback that captures measurements for the test driver.
func (record recorderFunc) Record(ctx context.Context, value float64, values []string) {
	record(ctx, value, values)
}

// newStub returns a stub driver sharing calls.
func newStub(name string, calls *callLog) *stubDriver {
	return &stubDriver{
		name:  name,
		calls: calls,
	}
}

// TestLifecycleRules checks the single lifecycle enforced by Telemetry.
func TestLifecycleRules(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	facade := telemetry.New(fake)
	tracer := facade.Tracer("test")

	_, early := tracer.Start(t.Context(), "early")
	early.End()

	if early.IsRecording() || early.Context().IsValid() {
		t.Fatal("span before Start is recording")
	}

	if err := facade.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	if err := facade.Start(t.Context(), telemetry.Setup{}); !errors.Is(err, telemetry.ErrStarted) {
		t.Fatalf("second Start = %v, want ErrStarted", err)
	}

	_, span := tracer.Start(t.Context(), "running")
	span.End()

	if err := facade.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := facade.Stop(t.Context()); err != nil {
		t.Fatalf("second Stop = %v, want nil", err)
	}

	if err := facade.Start(t.Context(), telemetry.Setup{}); !errors.Is(err, telemetry.ErrStopped) {
		t.Fatalf("Start after Stop = %v, want ErrStopped", err)
	}

	_, late := tracer.Start(t.Context(), "late")
	late.End()

	fake.AssertSpanCount(t, 1)
	fake.AssertSpan(t, "running")
}

// TestStopWithoutStart checks that a never-started telemetry shuts down
// without touching the driver.
func TestStopWithoutStart(t *testing.T) {
	t.Parallel()

	calls := &callLog{}
	facade := telemetry.New(newStub("a", calls))

	if err := facade.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := calls.list(); len(got) != 0 {
		t.Fatalf("driver calls = %v, want none", got)
	}
}

// TestStartFailure checks error wrapping and that a failed Start ends the
// lifecycle.
func TestStartFailure(t *testing.T) {
	t.Parallel()

	calls := &callLog{}
	driver := newStub("a", calls)
	driver.failStart = true
	facade := telemetry.New(driver)

	if err := facade.Start(t.Context(), telemetry.Setup{}); !errors.Is(err, errBoom) {
		t.Fatalf("Start = %v, want errBoom", err)
	}

	if err := facade.Start(t.Context(), telemetry.Setup{}); !errors.Is(err, telemetry.ErrStopped) {
		t.Fatalf("Start after failure = %v, want ErrStopped", err)
	}

	if err := facade.Stop(t.Context()); err != nil {
		t.Fatalf("Stop after failed Start = %v", err)
	}

	if got := calls.list(); !slices.Equal(got, []string{"start a"}) {
		t.Fatalf("driver calls = %v", got)
	}
}

// TestStopError checks that the driver's error is wrapped.
func TestStopError(t *testing.T) {
	t.Parallel()

	driver := newStub("a", &callLog{})
	driver.failStop = true
	facade := telemetry.New(driver)

	if err := facade.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	if err := facade.Stop(t.Context()); !errors.Is(err, errBoom) {
		t.Fatalf("Stop = %v, want errBoom", err)
	}
}

// TestNilDriver checks that New(nil) is a working no-op.
func TestNilDriver(t *testing.T) {
	t.Parallel()

	facade := telemetry.New(nil)

	if err := facade.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	ctx, span := facade.Tracer("t").Start(t.Context(), "x")

	if span.IsRecording() || span.Context().IsValid() {
		t.Fatal("no-op span records")
	}

	facade.Meter("t").Counter("c").Add(ctx, 1)
	facade.Inject(ctx, telemetry.MapCarrier{})

	if facade.Extract(ctx, telemetry.MapCarrier{"traceparent": "x"}) != ctx {
		t.Fatal("Extract changed the context of a no-op telemetry")
	}

	if _, ok := facade.DriverAs[*memorytelemetry.Driver](); ok {
		t.Fatal("DriverAs found a driver in a no-op telemetry")
	}

	if err := facade.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestNoopDoesNotAllocate checks the fast path of the default telemetry.
func TestNoopDoesNotAllocate(t *testing.T) {
	facade := telemetry.Default()
	tracer := facade.Tracer("test")
	counter := facade.Meter("test").Counter("requests", telemetry.Keys("route"))
	histogram := facade.Meter("test").Histogram("duration")
	ctx := t.Context()
	attr := telemetry.String("route", "/a")

	allocs := testing.AllocsPerRun(100, func() {
		ctx, span := tracer.Start(ctx, "operation", telemetry.Kind(telemetry.Server), telemetry.Attrs(attr))
		span.SetAttributes(attr)
		span.AddEvent("event", attr)
		span.SetStatus(telemetry.Error, "failed")
		span.End()
		counter.Add(ctx, 1, attr)
		histogram.Record(ctx, 0.5, attr)

		_ = telemetry.SpanFromContext(ctx).Context()
	})

	if allocs != 0 {
		t.Fatalf("allocations per run = %v, want 0", allocs)
	}
}

// TestDriverAs checks lookup through a Composite.
func TestDriverAs(t *testing.T) {
	t.Parallel()

	spans, metrics := memorytelemetry.New(), newStub("m", &callLog{})

	facade := telemetry.New(telemetry.Compose(telemetry.Traces(spans), telemetry.Metrics(metrics)))

	if got, ok := facade.DriverAs[*memorytelemetry.Driver](); !ok || got != spans {
		t.Fatalf("DriverAs(*memorytelemetry.Driver) = %v, %v", got, ok)
	}

	if got, ok := facade.DriverAs[*stubDriver](); !ok || got != metrics {
		t.Fatalf("DriverAs(*stubDriver) = %v, %v", got, ok)
	}

	if _, ok := facade.DriverAs[*telemetrytest.Fake](); ok {
		t.Fatal("DriverAs found a driver that is not there")
	}

	if facade.Driver() == nil {
		t.Fatal("Driver returned nil")
	}
}

// TestInjectExtractDisabled checks that propagation is inert until started.
func TestInjectExtractDisabled(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	facade := telemetry.New(fake)
	ctx := telemetry.ContextWithSpanContext(t.Context(),
		telemetry.SpanContext{
			TraceID: telemetry.TraceID{1},
			SpanID:  telemetry.SpanID{1},
		})

	carrier := telemetry.MapCarrier{}
	facade.Inject(ctx, carrier)
	facade.Inject(ctx, nil)

	if len(carrier) != 0 {
		t.Fatalf("Inject before Start wrote %v", carrier)
	}

	if facade.Extract(ctx, telemetry.MapCarrier{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}) != ctx {
		t.Fatal("Extract before Start changed the context")
	}
}

// logStub makes a stubDriver a log driver that discards records.
type logStub struct {
	// stubDriver supplies lifecycle and metrics for log-routing tests.
	*stubDriver
}

// Enabled rejects all levels so tests can distinguish routing from lifecycle state.
func (logStub) Enabled(context.Context, slog.Level) bool {
	return false
}

// Export accepts records without retaining them for the lifecycle-only log stub.
func (logStub) Export(context.Context, slog.Record) error {
	return nil
}

// TestEnabledFollowsLifecycle verifies that Enabled is true only while started.
func TestEnabledFollowsLifecycle(t *testing.T) {
	t.Parallel()

	var missing *telemetry.Telemetry

	if missing.Enabled() || telemetry.New(nil).Enabled() {
		t.Fatal("nil and unstarted telemetry must not be enabled")
	}

	started := telemetry.New(nil)

	if err := started.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	if !started.Enabled() {
		t.Fatal("started telemetry must be enabled")
	}

	if err := started.Stop(t.Context()); err != nil || started.Enabled() {
		t.Fatalf("stop telemetry must not be enabled (err %v)", err)
	}
}

// Example demonstrates the package overview with an explicitly started,
// in-process backend. Ending the span before Stop makes its output observable.
func Example() {
	ctx := context.Background()
	driver := memorytelemetry.New()
	service := telemetry.New(driver)

	if err := service.Start(ctx, telemetry.Setup{}); err != nil {
		fmt.Println(err)

		return
	}

	charges := service.Meter("example.com/billing").Counter("billing.charges", telemetry.Keys("currency"))
	ctx, span := service.Tracer("example.com/billing").Start(ctx, "charge",
		telemetry.Attrs(telemetry.String("currency", "EUR")))
	charges.Add(ctx, 1, telemetry.String("currency", "EUR"))
	span.End()

	if err := service.Stop(ctx); err != nil {
		fmt.Println(err)

		return
	}

	point, ok := driver.Metrics()[0].Point("EUR")
	fmt.Println("spans:", len(driver.Spans()))
	fmt.Println("charges:", point.Sum, "found:", ok)
	// Output:
	// spans: 1
	// charges: 1 found: true
}

// ExampleTelemetry_Start demonstrates the observable enabled state of a
// facade with no backend. Such a facade can later accept passive observers.
func ExampleTelemetry_Start() {
	ctx := context.Background()
	service := telemetry.New(nil)

	if err := service.Start(ctx, telemetry.Setup{}); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(service.Enabled())

	if err := service.Stop(ctx); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(service.Enabled())
	// Output:
	// true
	// false
}
