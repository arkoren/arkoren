package telemetry_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// startMemory returns a started memory driver.
func startMemory(t *testing.T) *memorytelemetry.Driver {
	t.Helper()

	driver := memorytelemetry.New()

	if err := driver.Start(context.Background(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	return driver
}

// startTelemetry returns a started Telemetry over driver, which may be nil.
func startTelemetry(t *testing.T, driver telemetry.Lifecycle) *telemetry.Telemetry {
	t.Helper()

	tel := telemetry.New(driver)

	if err := tel.Start(context.Background(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = tel.Stop(context.Background())
	})

	return tel
}

// countingObserver is an Observer that is not a driver. It counts what it
// receives.
type countingObserver struct {
	// mu protects snapshots and counts while traffic runs concurrently.
	mu sync.Mutex
	// spans retains each completed span delivered to this observer.
	spans []telemetry.FinishedSpan
	// logs counts log notifications without retaining their records.
	logs int
	// measures counts metric notifications without retaining their labels.
	measures int
}

// SpanEnded retains the delivered snapshot under the observer lock.
func (observer *countingObserver) SpanEnded(span telemetry.FinishedSpan) {
	observer.mu.Lock()
	defer observer.mu.Unlock()

	observer.spans = append(observer.spans, span)
}

// Logged counts a delivered log notification under the observer lock.
func (observer *countingObserver) Logged(context.Context, slog.Record) {
	observer.mu.Lock()
	defer observer.mu.Unlock()

	observer.logs++
}

// Measured counts a delivered measurement under the observer lock.
func (observer *countingObserver) Measured(telemetry.InstrumentSpec, float64, []string) {
	observer.mu.Lock()
	defer observer.mu.Unlock()

	observer.measures++
}

// TestObserveCopiesSpansWithDriverIDs checks the observer's detached span view.
// Its context must retain the trace and span IDs assigned by the active driver.
func TestObserveCopiesSpansWithDriverIDs(t *testing.T) {
	t.Parallel()

	primary, copied := memorytelemetry.New(), startMemory(t)
	tel := startTelemetry(t, primary)
	tel.Observe(copied)

	tracer := tel.Tracer("test")
	ctx, parent := tracer.Start(context.Background(), "parent", telemetry.Kind(telemetry.Server),
		telemetry.Attrs(telemetry.String("a", "1")))
	_, child := tracer.Start(ctx, "child")
	child.SetAttributes(telemetry.String("b", "2"), telemetry.String("a", "replaced"))
	child.AddEvent("note", telemetry.Int("n", 3))
	child.RecordError(errBoom)
	child.SetStatus(telemetry.Error, "failed")
	child.End()
	parent.SetStatus(telemetry.OK, "")
	parent.End()

	want, got := primary.Spans(), copied.Spans()

	if len(want) != 2 || len(got) != 2 {
		t.Fatalf("spans primary=%d observer=%d, want 2 each", len(want), len(got))
	}

	for index := range want {
		if want[index].Context != got[index].Context || want[index].Parent != got[index].Parent {
			t.Fatalf("span %d identity differs: %+v vs %+v", index, want[index], got[index])
		}

		if want[index].Name != got[index].Name || want[index].Scope != got[index].Scope ||
			want[index].Kind != got[index].Kind || want[index].Status != got[index].Status ||
			want[index].StatusDescription != got[index].StatusDescription {
			t.Fatalf("span %d fields differ: %+v vs %+v", index, want[index], got[index])
		}

		if !slices.EqualFunc(want[index].Attrs, got[index].Attrs, telemetry.Attr.Equal) {
			t.Fatalf("span %d attrs differ: %v vs %v", index, want[index].Attrs, got[index].Attrs)
		}

		if len(want[index].Events) != len(got[index].Events) {
			t.Fatalf("span %d events differ: %v vs %v", index, want[index].Events, got[index].Events)
		}

		for number := range want[index].Events {
			wantEvent, gotEvent := want[index].Events[number], got[index].Events[number]

			if wantEvent.Name != gotEvent.Name || !slices.EqualFunc(wantEvent.Attrs, gotEvent.Attrs, telemetry.Attr.Equal) {
				t.Fatalf("span %d event %d differs: %v vs %v", index, number, wantEvent, gotEvent)
			}
		}
	}

	if got[0].Name == "child" && !got[0].Parent.IsValid() {
		t.Fatal("child lost its parent")
	}

	if len(got[0].Events) != 2 || got[0].Events[1].Name != "exception" {
		t.Fatalf("child events = %v, want note and exception", got[0].Events)
	}
}

// TestObserveDeliversLogsAndMeasurements checks passive delivery of both signals.
// Observers must see traffic already sent through the configured drivers.
func TestObserveDeliversLogsAndMeasurements(t *testing.T) {
	t.Parallel()

	primary, copied := memorytelemetry.New(), startMemory(t)
	tel := startTelemetry(t, primary)
	tel.Observe(copied)

	counter := tel.Meter("test").Counter("things", telemetry.Keys("k"))
	counter.Add(context.Background(), 2, telemetry.String("k", "v"))

	ctx, span := tel.Tracer("test").Start(context.Background(), "op")
	logger := slog.New(tel.Handler(slog.DiscardHandler))
	logger.InfoContext(ctx, "hello", "x", 1)
	span.End()

	metrics := copied.Metrics()

	if len(metrics) != 1 {
		t.Fatalf("observer metrics = %v", metrics)
	}

	if point, ok := metrics[0].Point("v"); !ok || point.Sum != 2 || point.Count != 1 {
		t.Fatalf("observer point = %+v, %v", point, ok)
	}

	logs := copied.Logs()

	if len(logs) != 1 || logs[0].Message != "hello" || logs[0].Context != span.Context() {
		t.Fatalf("observer logs = %+v, want one record in span %v", logs, span.Context())
	}

	if want := primary.Logs(); len(want) != 1 || want[0].Seq != logs[0].Seq || want[0].Context != logs[0].Context {
		t.Fatalf("primary logs = %+v, observer logs = %+v", want, logs)
	}
}

// TestObserveLogsWithoutLogDriver checks log observation without an exporter.
// The handler must still notify subscribers when only a trace driver exists.
func TestObserveLogsWithoutLogDriver(t *testing.T) {
	t.Parallel()

	tel := startTelemetry(t, nil)
	observer := &countingObserver{}
	unsubscribe := tel.Observe(observer)

	logger := slog.New(tel.Handler(slog.DiscardHandler))
	logger.ErrorContext(t.Context(), "nobody wants this level")

	if observer.logs != 0 {
		t.Fatalf("logs = %d, want 0 when no handler wants the level", observer.logs)
	}

	logger = slog.New(tel.Handler(slog.NewTextHandler(&discardWriter{}, nil)))
	logger.InfoContext(t.Context(), "one")
	logger.With("k", "v").WithGroup("g").InfoContext(t.Context(), "two")
	unsubscribe()
	logger.InfoContext(t.Context(), "three")

	if observer.logs != 2 {
		t.Fatalf("logs = %d, want 2", observer.logs)
	}
}

// discardWriter is an io.Writer that drops everything.
type discardWriter struct{}

// Write accepts every byte without retaining output so logging exercises only telemetry.
func (*discardWriter) Write(data []byte) (int, error) {
	return len(data), nil
}

// TestObserveUnsubscribeStopsDeliveryAndIsIdempotent checks subscription removal.
// Repeated cancellation must be harmless and later traffic must not be delivered.
func TestObserveUnsubscribeStopsDeliveryAndIsIdempotent(t *testing.T) {
	t.Parallel()

	tel := startTelemetry(t, memorytelemetry.New())
	observer := &countingObserver{}
	unsubscribe := tel.Observe(observer)
	counter := tel.Meter("test").Counter("things")

	_, span := tel.Tracer("test").Start(context.Background(), "one")
	span.End()
	counter.Add(context.Background(), 1)

	unsubscribe()
	unsubscribe()

	_, span = tel.Tracer("test").Start(context.Background(), "two")
	span.End()
	counter.Add(context.Background(), 1)

	if len(observer.spans) != 1 || observer.measures != 1 {
		t.Fatalf("spans=%d measures=%d, want 1 each", len(observer.spans), observer.measures)
	}
}

// TestObserveSameObserverTwiceUnsubscribesIndependently checks distinct handles.
// Removing one registration must leave the other registration active.
func TestObserveSameObserverTwiceUnsubscribesIndependently(t *testing.T) {
	t.Parallel()

	tel := startTelemetry(t, memorytelemetry.New())
	observer := &countingObserver{}
	first := tel.Observe(observer)
	tel.Observe(observer)
	first()

	_, span := tel.Tracer("test").Start(context.Background(), "x")
	span.End()

	if len(observer.spans) != 1 {
		t.Fatalf("spans = %d, want 1 from the remaining subscription", len(observer.spans))
	}
}

// TestObserveNilObserverAttachesNothing covers an omitted passive receiver.
// Its returned cancellation function must remain safe to call.
func TestObserveNilObserverAttachesNothing(t *testing.T) {
	t.Parallel()

	tel := startTelemetry(t, nil)
	tel.Observe(nil)()
}

// TestObserveSeesNothingBeforeStart checks delivery while telemetry is inactive.
// Registering an observer must not implicitly activate recording.
func TestObserveSeesNothingBeforeStart(t *testing.T) {
	t.Parallel()

	tel := telemetry.New(memorytelemetry.New())
	observer := &countingObserver{}
	tel.Observe(observer)

	_, span := tel.Tracer("test").Start(context.Background(), "x")
	span.End()

	if len(observer.spans) != 0 {
		t.Fatalf("spans = %d before Start, want 0", len(observer.spans))
	}
}

// TestObserveNoObserverAddsNoAllocationsToSpanStart checks the unsubscribed path.
// Supporting passive observation must not add allocations to ordinary span start.
func TestObserveNoObserverAddsNoAllocationsToSpanStart(t *testing.T) {
	tel := startTelemetry(t, memorytelemetry.New())
	tracer := tel.Tracer("test")
	ctx := context.Background()

	run := func() {
		_, span := tracer.Start(ctx, "op", telemetry.Attrs(telemetry.String("a", "b")))
		span.End()
	}

	before := testing.AllocsPerRun(100, run)

	unsubscribe := tel.Observe(&countingObserver{})
	observed := testing.AllocsPerRun(100, run)
	unsubscribe()

	after := testing.AllocsPerRun(100, run)

	if after != before {
		t.Fatalf("allocations after unsubscribe = %v, before observing = %v", after, before)
	}

	if observed <= before {
		t.Fatalf("observed allocations = %v, expected more than %v", observed, before)
	}
}

// TestObserveFillsInTraceDriver checks an observer that also implements tracing.
// It may supply IDs and propagation when no trace driver was configured.
func TestObserveFillsInTraceDriver(t *testing.T) {
	t.Parallel()

	tel := startTelemetry(t, nil)
	memory := startMemory(t)

	if _, span := tel.Tracer("test").Start(context.Background(), "before"); span.Context().IsValid() {
		t.Fatal("span has an ID before any observer attached")
	}

	unsubscribe := tel.Observe(memory)

	ctx, span := tel.Tracer("test").Start(context.Background(), "filled")

	if !span.Context().IsValid() {
		t.Fatal("filled-in span has no ID")
	}

	carrier := telemetry.MapCarrier{}
	tel.Inject(ctx, carrier)

	if carrier.Get("traceparent") == "" {
		t.Fatal("Inject wrote no traceparent")
	}

	remote := tel.Extract(context.Background(), carrier)

	if got := telemetry.SpanFromContext(remote).Context(); got.TraceID != span.Context().TraceID || !got.Remote {
		t.Fatalf("Extract = %+v, want remote span of the trace", got)
	}

	span.End()

	if spans := memory.Spans(); len(spans) != 1 || spans[0].Context != span.Context() {
		t.Fatalf("spans = %+v, want the one filled-in span recorded once", spans)
	}

	unsubscribe()

	ctx, span = tel.Tracer("test").Start(context.Background(), "after")
	tel.Inject(ctx, carrier)

	if span.Context().IsValid() || len(memory.Spans()) != 1 {
		t.Fatal("telemetry did not revert to no-op after unsubscribe")
	}
}

// TestObserveFillsInMetricDriver checks an observer that also records metrics.
// New instruments may bind to it when no metric driver was configured.
func TestObserveFillsInMetricDriver(t *testing.T) {
	t.Parallel()

	tel := startTelemetry(t, nil)
	memory := startMemory(t)
	counter := tel.Meter("test").Counter("things", telemetry.Keys("k"))

	counter.Add(context.Background(), 5, telemetry.String("k", "ignored"))

	unsubscribe := tel.Observe(memory)
	counter.Add(context.Background(), 1, telemetry.String("k", "a"))
	counter.Add(context.Background(), 2, telemetry.String("k", "a"))

	metrics := memory.Metrics()

	if len(metrics) != 1 {
		t.Fatalf("metrics = %v, want one instrument", metrics)
	}

	if point, ok := metrics[0].Point("a"); !ok || point.Sum != 3 || point.Count != 2 {
		t.Fatalf("point = %+v, %v, want sum 3 recorded once each", point, ok)
	}

	unsubscribe()
	counter.Add(context.Background(), 9, telemetry.String("k", "a"))

	if point, _ := memory.Metrics()[0].Point("a"); point.Sum != 3 {
		t.Fatalf("sum = %v after unsubscribe, want 3", point.Sum)
	}
}

// TestObserveDoesNotReplaceConfiguredDriver protects explicit signal selection.
// A capable observer must remain passive when a driver already owns the signal.
func TestObserveDoesNotReplaceConfiguredDriver(t *testing.T) {
	t.Parallel()

	primary, observer := memorytelemetry.New(), startMemory(t)
	tel := startTelemetry(t, primary)
	tel.Observe(observer)

	_, span := tel.Tracer("test").Start(context.Background(), "x")
	span.End()
	tel.Meter("test").Counter("c").Add(context.Background(), 1)

	if len(primary.Spans()) != 1 || len(observer.Spans()) != 1 {
		t.Fatalf("spans primary=%d observer=%d, want 1 each", len(primary.Spans()), len(observer.Spans()))
	}

	if primary.Spans()[0].Context != observer.Spans()[0].Context {
		t.Fatal("observer span has another identity than the configured driver's")
	}

	if len(primary.Metrics()) != 1 || len(observer.Metrics()) != 1 {
		t.Fatal("each memory driver should have recorded the counter once")
	}
}

// TestObserveFirstDriverObserverFillsIn checks ordering among capable observers.
// The earliest eligible registration supplies a missing signal driver.
func TestObserveFirstDriverObserverFillsIn(t *testing.T) {
	t.Parallel()

	tel := startTelemetry(t, nil)
	first, second := startMemory(t), startMemory(t)
	tel.Observe(first)
	tel.Observe(second)

	_, span := tel.Tracer("test").Start(context.Background(), "x")
	span.End()

	if len(first.Spans()) != 1 || len(second.Spans()) != 1 {
		t.Fatalf("spans first=%d second=%d, want 1 each", len(first.Spans()), len(second.Spans()))
	}

	if first.Spans()[0].Context != second.Spans()[0].Context || first.Spans()[0].Context != span.Context() {
		t.Fatal("second observer should copy the first one's identity")
	}
}

// TestObserveDriverAlsoObserverRecordsOnce prevents duplicate signal delivery.
// A driver registered as an observer must not record its own signal twice.
func TestObserveDriverAlsoObserverRecordsOnce(t *testing.T) {
	t.Parallel()

	memory := memorytelemetry.New()
	tel := startTelemetry(t, memory)
	tel.Observe(memory)

	_, span := tel.Tracer("test").Start(context.Background(), "x")
	span.End()
	tel.Meter("test").Counter("c").Add(context.Background(), 1)
	slog.New(tel.Handler(slog.NewTextHandler(&discardWriter{}, nil))).InfoContext(t.Context(), "once")

	if len(memory.Spans()) != 1 || len(memory.Logs()) != 1 {
		t.Fatalf("spans=%d logs=%d, want 1 each", len(memory.Spans()), len(memory.Logs()))
	}

	if point, _ := memory.Metrics()[0].Point(); point.Count != 1 {
		t.Fatalf("count = %d, want 1", point.Count)
	}
}

// TestObserveNonDriverObserverDoesNotFillIn checks a purely passive receiver.
// Subscription must not invent driver capabilities the observer does not provide.
func TestObserveNonDriverObserverDoesNotFillIn(t *testing.T) {
	t.Parallel()

	tel := startTelemetry(t, nil)
	observer := &countingObserver{}
	tel.Observe(observer)

	_, span := tel.Tracer("test").Start(context.Background(), "x")
	span.End()

	if span.Context().IsValid() || len(observer.spans) != 0 {
		t.Fatal("an observer without TraceDriver must not create spans")
	}
}

// TestObserveInFlightSpanFinishesAfterUnsubscribe checks captured routing state.
// A span already recording must still notify its original observers at End.
func TestObserveInFlightSpanFinishesAfterUnsubscribe(t *testing.T) {
	t.Parallel()

	tel := startTelemetry(t, nil)
	memory := startMemory(t)
	unsubscribe := tel.Observe(memory)

	_, span := tel.Tracer("test").Start(context.Background(), "inflight")
	unsubscribe()
	span.SetAttributes(telemetry.String("late", "yes"))
	span.RecordError(errors.New("late"))
	span.End()

	spans := memory.Spans()

	if len(spans) != 1 {
		t.Fatalf("spans = %d, want the in-flight span to finish on its own recorder", len(spans))
	}

	if _, ok := spans[0].Attr("late"); !ok || len(spans[0].Events) != 1 {
		t.Fatalf("span = %+v, want late attribute and error event", spans[0])
	}
}

// TestObserveWorksWithTelemetryBuiltLikeTheApplicationDefault covers app wiring.
// Passive inspection must coexist with a configured IDs-only memory driver.
func TestObserveWorksWithTelemetryBuiltLikeTheApplicationDefault(t *testing.T) {
	t.Parallel()

	// The application builds telemetry.New(nil) when Builder.Telemetry is not
	// used; once started it has no driver, so an observer fills in.
	tel := telemetry.New(nil)

	if err := tel.Start(context.Background(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = tel.Stop(context.Background())
	})

	memory := startMemory(t)
	tel.Observe(memory)

	_, span := tel.Tracer("test").Start(context.Background(), "x")
	span.End()

	if len(memory.Spans()) != 1 {
		t.Fatalf("spans = %d, want 1", len(memory.Spans()))
	}
}

// TestObserveNeverStartedTelemetryStaysSilent checks inactive observer delivery.
// A capable subscriber must not bypass the facade's startup requirement.
func TestObserveNeverStartedTelemetryStaysSilent(t *testing.T) {
	t.Parallel()

	tel := telemetry.New(nil)
	memory := startMemory(t)
	tel.Observe(memory)

	if _, span := tel.Tracer("test").Start(context.Background(), "x"); span.Context().IsValid() {
		t.Fatal("an unstarted telemetry must not record, even with a filling-in observer")
	}
}

// TestObserveConcurrentWithTraffic exercises immutable routing publication.
// Subscribing and unsubscribing must remain safe while signals are produced.
func TestObserveConcurrentWithTraffic(t *testing.T) {
	t.Parallel()

	tel := startTelemetry(t, memorytelemetry.New())
	counter := tel.Meter("test").Counter("c")
	logger := slog.New(tel.Handler(slog.NewTextHandler(&discardWriter{}, nil)))

	var group sync.WaitGroup

	for range 4 {
		group.Go(func() {
			for range 200 {
				ctx, span := tel.Tracer("test").Start(context.Background(), "op")
				span.SetAttributes(telemetry.Int("n", 1))
				counter.Add(ctx, 1)
				logger.InfoContext(ctx, "x")
				span.End()
			}
		})
	}

	for range 4 {
		group.Go(func() {
			for range 100 {
				tel.Observe(&countingObserver{})()

				memory := startMemory(t)
				tel.Observe(memory)()
			}
		})
	}

	group.Wait()
}

// TestEmptySpanKindDefaultsForObservers checks the facade's independently
// captured snapshot as well as the trace driver's snapshot.
func TestEmptySpanKindDefaultsForObservers(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	service := fake.Telemetry(t)
	observer := memorytelemetry.New()
	t.Cleanup(service.Observe(observer))

	_, span := service.Tracer("defaults").Start(t.Context(), "empty kind", telemetry.Kind(""))
	span.End()

	for _, recorded := range append(fake.Spans(), observer.Spans()...) {
		if recorded.Kind != telemetry.Internal || recorded.Status != telemetry.Unset {
			t.Fatalf("kind/status = %q/%q, want internal/unset", recorded.Kind, recorded.Status)
		}
	}
}

// selectedParentDriver models a native backend that selects a parent distinct
// from the facade context. Recorded backend spans provide independent evidence
// of the actual parent; the input and returned facade contexts are not rewritten.
type selectedParentDriver struct {
	// Driver supplies lifecycle, propagation, and recorded backend output.
	*memorytelemetry.Driver

	// parent is the effective parent chosen by the backend at StartSpan.
	parent telemetry.SpanContext
}

// StartSpan starts the backend recorder with its selected parent and exposes
// that immutable parent through the recorder protocol. The caller's context is
// returned unchanged so the facade cannot guess the answer from a context slot.
func (driver *selectedParentDriver) StartSpan(ctx context.Context, start telemetry.SpanStart) (context.Context, telemetry.SpanRecorder) {
	_, recorder := driver.Driver.StartSpan(telemetry.ContextWithSpanContext(ctx, driver.parent), start)

	return ctx, &selectedParentRecorder{SpanRecorder: recorder, parent: driver.parent}
}

// selectedParentRecorder reports the backend's actual parent, including zero
// for a root. Other recorder methods retain real memory-backend behavior.
type selectedParentRecorder struct {
	// SpanRecorder implements ordinary backend recording and span identity.
	telemetry.SpanRecorder

	// parent is fixed when the backend selects its parent at span creation.
	parent telemetry.SpanContext
}

// Parent returns the actual immutable backend parent without changing context
// state or choosing another parent when the facade asks for it.
func (recorder *selectedParentRecorder) Parent() telemetry.SpanContext {
	return recorder.parent
}

// TestObserverUsesActualBackendParent compares a public observer notification
// with independently recorded backend output. A valid but stale facade parent
// must not replace the different parent actually selected by the backend.
func TestObserverUsesActualBackendParent(t *testing.T) {
	t.Parallel()

	driver := &selectedParentDriver{
		Driver: memorytelemetry.New(),
		parent: telemetry.SpanContext{TraceID: telemetry.TraceID{2}, SpanID: telemetry.SpanID{2}, Sampled: true},
	}
	service := startTelemetry(t, driver)
	observer := &countingObserver{}
	service.Observe(observer)

	ctx := telemetry.ContextWithSpanContext(t.Context(), telemetry.SpanContext{
		TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{1},
	})
	_, span := service.Tracer("test").Start(ctx, "operation")
	span.End()

	backend := driver.Spans()

	if len(backend) != 1 || len(observer.spans) != 1 {
		t.Fatalf("backend spans = %d, observer spans = %d; want 1 each", len(backend), len(observer.spans))
	}

	if backend[0].Parent != driver.parent {
		t.Fatalf("backend parent = %+v, want selected parent %+v", backend[0].Parent, driver.parent)
	}

	if got := observer.spans[0].Parent; got != backend[0].Parent {
		t.Fatalf("observer parent = %+v, actual backend parent = %+v", got, backend[0].Parent)
	}
}

// TestObserverPreservesBackendSelectedRoot verifies that a zero actual parent
// is authoritative, not an invitation to fall back to a stale facade parent.
// The backend and observer must both report a root without rewriting inputs.
func TestObserverPreservesBackendSelectedRoot(t *testing.T) {
	t.Parallel()

	driver := &selectedParentDriver{Driver: memorytelemetry.New()}
	service := startTelemetry(t, driver)
	observer := &countingObserver{}
	service.Observe(observer)

	ctx := telemetry.ContextWithSpanContext(t.Context(), telemetry.SpanContext{
		TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{1},
	})
	_, span := service.Tracer("test").Start(ctx, "root")
	span.End()

	backend := driver.Spans()

	if len(backend) != 1 || len(observer.spans) != 1 {
		t.Fatalf("backend spans = %d, observer spans = %d; want 1 each", len(backend), len(observer.spans))
	}

	if backend[0].Parent != (telemetry.SpanContext{}) || observer.spans[0].Parent != backend[0].Parent {
		t.Fatalf("backend parent = %+v, observer parent = %+v; want roots", backend[0].Parent, observer.spans[0].Parent)
	}
}

// TestObservedErrorFormattingCanSetAttributes verifies reentrant formatting
// through Span.RecordError with both a real backend and a passive observer.
// Formatting must not run while the observer snapshot mutex is held.
func TestObservedErrorFormattingCanSetAttributes(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	service := fake.Telemetry(t)
	observer := &countingObserver{}
	service.Observe(observer)

	_, span := service.Tracer("test").Start(t.Context(), "operation")
	span.RecordError(callbackError{format: func() {
		span.SetAttributes(telemetry.String("error.format", "called"))
	}})
	span.End()

	if len(observer.spans) != 1 {
		t.Fatalf("observed spans = %d, want 1", len(observer.spans))
	}

	if attr, ok := observer.spans[0].Attr("error.format"); !ok || attr.AsString() != "called" {
		t.Fatalf("observed attribute = %v, found %v", attr, ok)
	}

	if len(observer.spans[0].Events) != 1 {
		t.Fatalf("observed events = %d, want one exception", len(observer.spans[0].Events))
	}
}
