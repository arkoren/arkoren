package telemetry_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

// traceStub is a trace driver stub that records lifecycle calls.
type traceStub struct {
	// Driver supplies tracing while lifecycle overrides record composite ordering.
	*memorytelemetry.Driver

	// name and calls log the lifecycle.
	name string
	// calls records lifecycle ordering across all drivers in the composite.
	calls *callLog

	// failStart injects a Start error.
	failStart bool
}

// Start logs the attempt, optionally fails, then starts the wrapped memory driver.
func (driver *traceStub) Start(ctx context.Context, setup telemetry.Setup) error {
	driver.calls.add("start " + driver.name)

	if driver.failStart {
		return errBoom
	}

	return driver.Driver.Start(ctx, setup)
}

// Stop logs the shutdown order before stopping the wrapped memory driver.
func (driver *traceStub) Stop(ctx context.Context) error {
	driver.calls.add("stop " + driver.name)

	return driver.Driver.Stop(ctx)
}

// TestComposeOrder checks distinct starts in order and reverse shutdown.
func TestComposeOrder(t *testing.T) {
	t.Parallel()

	calls := &callLog{}
	shared := &traceStub{
		Driver: memorytelemetry.New(),
		name:   "shared",
		calls:  calls,
	}
	metrics := newStub("metrics", calls)

	composite := telemetry.Compose(
		telemetry.Traces(shared),
		telemetry.Metrics(metrics),
		telemetry.Metrics(shared),
	)

	if err := composite.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	if err := composite.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	// The metrics stub lost the metrics signal to shared, so it never starts.
	want := []string{"start shared", "stop shared"}

	if got := calls.list(); !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

// TestComposeDistinctInOrder checks ordering across different drivers.
func TestComposeDistinctInOrder(t *testing.T) {
	t.Parallel()

	calls := &callLog{}
	first := &traceStub{
		Driver: memorytelemetry.New(),
		name:   "first",
		calls:  calls,
	}
	second := newStub("second", calls)

	composite := telemetry.Compose(telemetry.Traces(first), telemetry.Metrics(second), telemetry.Traces(first))
	facade := telemetry.New(composite)

	if err := facade.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	if err := facade.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	want := []string{"start first", "start second", "stop second", "stop first"}

	if got := calls.list(); !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

// TestComposeRoutesSignals checks that each signal reaches its own driver.
func TestComposeRoutesSignals(t *testing.T) {
	t.Parallel()

	traces := memorytelemetry.New()
	metrics := newStub("metrics", &callLog{})

	facade := telemetry.New(telemetry.Compose(telemetry.Traces(traces), telemetry.Metrics(metrics)))

	if err := facade.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	ctx, span := facade.Tracer("t").Start(t.Context(), "routed")
	facade.Meter("t").Counter("c").Add(ctx, 1)
	span.End()

	if len(traces.Spans()) != 1 {
		t.Errorf("trace driver spans = %d, want 1", len(traces.Spans()))
	}

	if len(metrics.records) != 1 {
		t.Errorf("metric driver records = %d, want 1", len(metrics.records))
	}
}

// TestComposeMissingSignalsAreNoop checks signals without a driver.
func TestComposeMissingSignalsAreNoop(t *testing.T) {
	t.Parallel()

	facade := telemetry.New(telemetry.Compose(telemetry.Metrics(newStub("m", &callLog{}))))

	if err := facade.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	_, span := facade.Tracer("t").Start(t.Context(), "x")

	if span.IsRecording() {
		t.Fatal("span records without a trace driver")
	}

	empty := telemetry.Compose(telemetry.Traces(nil), telemetry.Metrics(nil), telemetry.Logs(nil))

	if err := empty.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}
}

// TestComposeStartFailureRollsBack checks that started drivers are shut down.
func TestComposeStartFailureRollsBack(t *testing.T) {
	t.Parallel()

	calls := &callLog{}
	first := &traceStub{
		Driver: memorytelemetry.New(),
		name:   "first",
		calls:  calls,
	}
	second := newStub("second", calls)
	second.failStart = true

	composite := telemetry.Compose(telemetry.Traces(first), telemetry.Metrics(second))

	err := composite.Start(t.Context(), telemetry.Setup{})

	if !errors.Is(err, errBoom) {
		t.Fatalf("Start = %v, want errBoom", err)
	}

	want := []string{"start first", "start second", "stop first"}

	if got := calls.list(); !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}

	if err := composite.Start(t.Context(), telemetry.Setup{}); !errors.Is(err, telemetry.ErrStopped) {
		t.Fatalf("Start after failure = %v, want ErrStopped", err)
	}

	if err := composite.Stop(t.Context()); err != nil {
		t.Fatalf("Stop after failure = %v", err)
	}

	if got := calls.list(); len(got) != len(want) {
		t.Fatalf("Stop after rollback called drivers again: %v", got)
	}
}

// TestComposeLifecycleRules checks the Lifecycle rules on the composite.
func TestComposeLifecycleRules(t *testing.T) {
	t.Parallel()

	composite := telemetry.Compose(telemetry.Traces(memorytelemetry.New()))

	if err := composite.Stop(t.Context()); err != nil {
		t.Fatalf("Stop without Start = %v", err)
	}

	if err := composite.Start(t.Context(), telemetry.Setup{}); !errors.Is(err, telemetry.ErrStopped) {
		t.Fatalf("Start after Stop = %v, want ErrStopped", err)
	}

	started := telemetry.Compose(telemetry.Traces(memorytelemetry.New()))

	if err := started.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	if err := started.Start(t.Context(), telemetry.Setup{}); !errors.Is(err, telemetry.ErrStarted) {
		t.Fatalf("second Start = %v, want ErrStarted", err)
	}
}

// TestComposeStopJoinsErrors checks that every driver is shut down.
func TestComposeStopJoinsErrors(t *testing.T) {
	t.Parallel()

	calls := &callLog{}
	first, second := newStub("first", calls), newStub("second", calls)
	first.failStop, second.failStop = true, true

	composite := telemetry.Compose(telemetry.Metrics(first), telemetry.Logs(logStub{second}))

	if err := composite.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	if err := composite.Stop(t.Context()); !errors.Is(err, errBoom) {
		t.Fatalf("Stop = %v, want errBoom", err)
	}

	want := []string{"start first", "start second", "stop second", "stop first"}

	if got := calls.list(); !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

// rollbackContextDriver observes the cancellation contract at the public Stop
// seam while preserving real memory-driver acquisition and release.
type rollbackContextDriver struct {
	// traceStub records successful acquisition and rollback ordering.
	*traceStub

	// stopErr is the context error received by rollback's Stop call.
	stopErr error
}

// Stop always releases the memory driver, including with an expired context,
// then reports the received cancellation alongside any release error.
func (driver *rollbackContextDriver) Stop(ctx context.Context) error {
	driver.stopErr = ctx.Err()

	return errors.Join(driver.traceStub.Stop(ctx), driver.stopErr)
}

// cancelingStartDriver cancels the startup context immediately before a
// configured startup failure, after preceding drivers have acquired resources.
type cancelingStartDriver struct {
	// stubDriver records the failing startup attempt.
	*stubDriver

	// cancel cancels the context shared with the composite's startup.
	cancel context.CancelFunc
}

// Start cancels startup before returning the wrapped driver's configured
// failure. This makes rollback cancellation deterministic without real time.
func (driver *cancelingStartDriver) Start(ctx context.Context, setup telemetry.Setup) error {
	driver.cancel()

	return driver.stubDriver.Start(ctx, setup)
}

// TestComposeStartsNoncomparableDriver exercises selected driver lifecycle.
// Lack of Go equality must not silently remove the final metric selection.
func TestComposeStartsNoncomparableDriver(t *testing.T) {
	t.Parallel()

	calls := &callLog{}
	driver := noncomparableMetricDriver{stubDriver: newStub("metrics", calls), names: []string{"requests"}}

	composite := telemetry.Compose(telemetry.Metrics(driver))

	if err := composite.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	if err := composite.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got := calls.list(); !slices.Equal(got, []string{"start metrics", "stop metrics"}) {
		t.Fatalf("lifecycle calls = %v, want one start and stop", got)
	}
}

// TestComposeRollbackPreservesStartupCancellation observes cancellation at
// each owned Stop boundary. Rollback must not replace an expired startup budget
// with an unbounded context, and must retain both startup and cleanup errors.
func TestComposeRollbackPreservesStartupCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	calls := &callLog{}
	first := &rollbackContextDriver{traceStub: &traceStub{
		Driver: memorytelemetry.New(), name: "first", calls: calls,
	}}
	second := &cancelingStartDriver{stubDriver: newStub("second", calls), cancel: cancel}
	second.failStart = true

	composite := telemetry.Compose(telemetry.Traces(first), telemetry.Metrics(second))

	err := composite.Start(ctx, telemetry.Setup{})

	if !errors.Is(first.stopErr, context.Canceled) {
		t.Fatalf("rollback context error = %v, want context.Canceled", first.stopErr)
	}

	if !errors.Is(err, errBoom) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want startup and cleanup causes", err)
	}

	if got := calls.list(); !slices.Equal(got, []string{"start first", "start second", "stop first"}) {
		t.Fatalf("lifecycle calls = %v", got)
	}
}
