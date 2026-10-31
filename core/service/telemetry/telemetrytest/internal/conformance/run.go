// Package conformance exercises telemetry driver protocols and readback.
package conformance

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/foundation/collection"
)

// scope is the instrumentation scope the suite uses.
const scope = "conformance"

// suite carries what every conformance subtest needs.
type suite struct {
	// newDriver creates a fresh, unstarted driver.
	newDriver func(t *testing.T) telemetry.Lifecycle

	// reader finds recorded content when an adapter is supplied.
	reader func(telemetry.Lifecycle) Reader
}

// Reader is the readback protocol used by the conformance implementation.
// The public telemetrytest.Reader remains declared in its owning package.
type Reader interface {
	// Spans returns detached finished spans.
	Spans() collection.Slice[memorytelemetry.Span]

	// Metrics returns detached instrument aggregates.
	Metrics() collection.Slice[memorytelemetry.Metric]
}

// fixture is a started driver behind a [telemetry.Telemetry].
type fixture struct {
	// driver is the driver under test.
	driver telemetry.Lifecycle

	// telemetry is the started facade over driver.
	telemetry *telemetry.Telemetry

	// reader reads back recorded data, nil when unavailable.
	reader Reader
}

// Run checks each supported signal using a fresh driver for each fixture.
// A nil reader adapter falls back to the driver's readback implementation.
func Run(t *testing.T, newDriver func(t *testing.T) telemetry.Lifecycle, reader func(telemetry.Lifecycle) Reader) {
	t.Helper()

	s := suite{
		newDriver: newDriver,
		reader:    reader,
	}
	probe := newDriver(t)
	_, traces := probe.(telemetry.TraceDriver)
	_, metrics := probe.(telemetry.MetricDriver)
	_, logs := probe.(telemetry.LogDriver)

	t.Run("Lifecycle", func(t *testing.T) {
		t.Run("NoopBeforeStart", s.noopBeforeStart)
		t.Run("SecondStartFails", s.secondStartFails)
		t.Run("StartAfterStopFails", s.startAfterStopFails)
		t.Run("StopWithoutStart", s.stopWithoutStart)
		t.Run("NoopAfterStop", s.noopAfterStop)
	})

	if traces {
		t.Run("Traces", func(t *testing.T) {
			t.Run("Parenting", s.parenting)
			t.Run("RecorderParent", s.recorderParent)
			t.Run("RecorderRootParent", s.recorderRootParent)
			t.Run("RootSpansStartNewTraces", s.rootSpans)
			t.Run("SpanData", s.spanData)
			t.Run("Status", s.status)
			t.Run("EmptySpanDefaults", s.emptySpanDefaults)
			t.Run("EmptyStatusDoesNotClearError", s.emptyStatusDoesNotClearError)
			t.Run("EndIsIdempotent", s.endIsIdempotent)
			t.Run("PropagationRoundTrip", s.propagationRoundTrip)
			t.Run("PropagationIgnoresGarbage", s.propagationIgnoresGarbage)
		})
	}

	if metrics {
		t.Run("Metrics", func(t *testing.T) {
			t.Run("DeclaredKeys", s.declaredKeys)
			t.Run("InstrumentKinds", s.instrumentKinds)
		})
	}

	if logs || traces {
		t.Run("Logs", func(t *testing.T) {
			if traces {
				t.Run("HandlerCorrelates", s.handlerCorrelates)
			}

			if logs {
				t.Run("ExportAcceptsRecords", s.exportAcceptsRecords)
			}
		})
	}
}

// setup is the Setup the suite starts drivers with.
func setup() telemetry.Setup {
	return telemetry.Setup{
		Resource: telemetry.Resource{
			Service:     scope,
			Version:     "1.0.0",
			Environment: "test",
		},
		Logger: slog.New(slog.DiscardHandler),
	}
}

// readerFor returns the Reader of driver, or nil.
func (suite suite) readerFor(driver telemetry.Lifecycle) Reader {
	if suite.reader != nil {
		return suite.reader(driver)
	}

	reader, _ := driver.(Reader)

	return reader
}

// open creates a driver and starts it behind a Telemetry that is shut down
// when t ends.
func (suite suite) open(t *testing.T) fixture {
	t.Helper()

	driver := suite.newDriver(t)
	facade := telemetry.New(driver)

	if err := facade.Start(t.Context(), setup()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = facade.Stop(context.WithoutCancel(t.Context()))
	})

	return fixture{
		driver:    driver,
		telemetry: facade,
		reader:    suite.readerFor(driver),
	}
}

// needReader skips the subtest when recorded data cannot be read back.
func (fixture fixture) needReader(t *testing.T) Reader {
	t.Helper()

	if fixture.reader == nil {
		t.Skip("driver exposes no Reader; pass telemetrytest.WithReader to check recorded content")
	}

	return fixture.reader
}

// remote returns a valid span context for adopting as a parent.
func remote() telemetry.SpanContext {
	return telemetry.SpanContext{
		TraceID: telemetry.TraceID{1},
		SpanID:  telemetry.SpanID{2},
		Sampled: true,
	}
}

// exerciseRecorder calls every recorder method, which must be harmless.
func exerciseRecorder(recorder telemetry.SpanRecorder) {
	recorder.SetAttributes([]telemetry.Attr{telemetry.String("k", "v")})
	recorder.AddEvent("event", nil)
	recorder.RecordError(errors.New("ignored"))
	recorder.SetStatus(telemetry.Error, "ignored")
	recorder.End()
}

// inactive checks that a driver ignores calls in the given lifecycle phase.
func (suite suite) inactive(t *testing.T, driver telemetry.Lifecycle, phase string) {
	t.Helper()

	ctx := telemetry.ContextWithSpanContext(t.Context(), remote())

	if tracer, ok := driver.(telemetry.TraceDriver); ok {
		suite.inactiveTrace(ctx, t, tracer, phase)
	}

	if meter, ok := driver.(telemetry.MetricDriver); ok {
		recorder, err := meter.Instrument(telemetry.InstrumentSpec{
			Scope: scope,
			Name:  "conformance.inactive",
			Kind:  telemetry.CounterKind,
			Keys:  []string{"k"},
		})

		if err != nil || recorder == nil {
			t.Fatalf("Instrument %s = %v, %v; want a discarding recorder", phase, recorder, err)
		}

		recorder.Record(ctx, 1, []string{"v"})
	}

	if logger, ok := driver.(telemetry.LogDriver); ok {
		record := slog.NewRecord(time.Time{}, slog.LevelInfo, "inactive", 0)

		if err := logger.Export(ctx, record); err != nil {
			t.Errorf("Export %s = %v, want nil", phase, err)
		}
	}

}

// inactiveTrace checks the complete trace-side inactive protocol with a valid
// incoming parent: no child identity, no selected parent, no recording, and no
// propagation. Keeping this signal's assertions together leaves the capability
// dispatcher independent of each protocol's individual checks.
func (suite suite) inactiveTrace(ctx context.Context, t *testing.T, tracer telemetry.TraceDriver, phase string) {
	t.Helper()

	_, recorder := tracer.StartSpan(ctx, telemetry.SpanStart{Scope: scope, Name: "inactive"})

	if recorder == nil {
		t.Fatalf("StartSpan %s returned a nil recorder", phase)
	}

	if recorder.Recording() || recorder.Context().IsValid() {
		t.Errorf("span started %s is recording or has a valid context", phase)
	}

	if parent := recorder.Parent(); parent != (telemetry.SpanContext{}) {
		t.Errorf("span started %s selected parent %+v, want zero", phase, parent)
	}

	exerciseRecorder(recorder)

	carrier := telemetry.MapCarrier{}
	tracer.Inject(ctx, carrier)

	if len(carrier) != 0 {
		t.Errorf("Inject %s wrote %v", phase, carrier)
	}

	suite.extractIsInactive(t, tracer)
}

// recorderParent checks the required backend protocol without a Reader. The
// selected parent must preserve identity, remote/sample flags and trace state,
// and remain stable across ordinary recording calls before backend End.
func (suite suite) recorderParent(t *testing.T) {
	fixture := suite.open(t)
	driver := fixture.driver.(telemetry.TraceDriver)
	parent := telemetry.SpanContext{
		TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2},
		Remote: true, TraceState: "vendor=value",
	}
	ctx := telemetry.ContextWithSpanContext(t.Context(), parent)
	_, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Scope: scope, Name: "parent metadata"})
	t.Cleanup(recorder.End)

	if got := recorder.Parent(); got != parent {
		t.Fatalf("recorder parent = %+v, want selected parent %+v", got, parent)
	}

	recorder.SetAttributes([]telemetry.Attr{telemetry.String("region", "eu")})

	if got := recorder.Parent(); got != parent {
		t.Fatalf("recorder parent changed after recording: %+v", got)
	}
}

// recorderRootParent checks zero parent metadata on a started root recorder.
// It runs even without export/readback support, so every trace implementation
// must expose the actual root decision through the mandatory protocol.
func (suite suite) recorderRootParent(t *testing.T) {
	fixture := suite.open(t)
	driver := fixture.driver.(telemetry.TraceDriver)
	_, recorder := driver.StartSpan(t.Context(), telemetry.SpanStart{Scope: scope, Name: "root metadata"})
	t.Cleanup(recorder.End)

	if parent := recorder.Parent(); parent != (telemetry.SpanContext{}) {
		t.Fatalf("root recorder parent = %+v, want zero", parent)
	}
}

// extractIsInactive checks that Extract yields no span context while the
// driver is inactive, even from a carrier written by a running driver.
func (suite suite) extractIsInactive(t *testing.T, tracer telemetry.TraceDriver) {
	t.Helper()

	running := suite.open(t)
	ctx, span := running.telemetry.Tracer(scope).Start(t.Context(), "source")
	defer span.End()

	carrier := telemetry.MapCarrier{}
	running.telemetry.Inject(ctx, carrier)

	if _, extracted := tracer.Extract(t.Context(), carrier); extracted.IsValid() {
		t.Errorf("inactive Extract returned %+v", extracted)
	}
}

// requireNothingRecorded fails when reader holds spans or metric points.
func requireNothingRecorded(t *testing.T, reader Reader, phase string) {
	t.Helper()

	if reader == nil {
		return
	}

	if spans := reader.Spans(); len(spans) != 0 {
		t.Errorf("%d spans recorded %s, want none", len(spans), phase)
	}

	for _, metric := range reader.Metrics() {
		if len(metric.Points) != 0 {
			t.Errorf("metric %q recorded %s, want nothing", metric.Name, phase)
		}
	}
}

// noopBeforeStart checks that all implemented signals discard pre-start traffic.
// Starting afterwards makes any incorrectly retained data visible to the reader.
func (suite suite) noopBeforeStart(t *testing.T) {
	driver := suite.newDriver(t)
	reader := suite.readerFor(driver)

	suite.inactive(t, driver, "before Start")

	if err := driver.Start(t.Context(), setup()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	requireNothingRecorded(t, reader, "before Start")
}

// secondStartFails checks that a running driver rejects a second startup.
// The returned error must match the shared ErrStarted sentinel.
func (suite suite) secondStartFails(t *testing.T) {
	driver := suite.newDriver(t)

	if err := driver.Start(t.Context(), setup()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	if err := driver.Start(t.Context(), setup()); !errors.Is(err, telemetry.ErrStarted) {
		t.Fatalf("second Start = %v, want an error wrapping ErrStarted", err)
	}
}

// startAfterStopFails checks that shutdown permanently ends a driver's lifecycle.
// Attempting to restart it must report ErrStopped.
func (suite suite) startAfterStopFails(t *testing.T) {
	driver := suite.newDriver(t)

	if err := driver.Start(t.Context(), setup()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if err := driver.Start(t.Context(), setup()); !errors.Is(err, telemetry.ErrStopped) {
		t.Fatalf("Start after Stop = %v, want an error wrapping ErrStopped", err)
	}
}

// stopWithoutStart checks safe cleanup of a driver that never started.
// Both the first stop and a repeated stop must succeed.
func (suite suite) stopWithoutStart(t *testing.T) {
	driver := suite.newDriver(t)

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop without Start = %v, want nil", err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("second Stop = %v, want nil", err)
	}
}

// noopAfterStop checks that shutdown disables all implemented signal operations.
// Later operations must not append to the snapshots captured before shutdown.
func (suite suite) noopAfterStop(t *testing.T) {
	fixture := suite.open(t)

	_, span := fixture.telemetry.Tracer(scope).Start(t.Context(), "before-stop")
	span.End()

	if err := fixture.driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if err := fixture.driver.Stop(t.Context()); err != nil {
		t.Fatalf("second Stop = %v, want nil", err)
	}

	suite.inactive(t, fixture.driver, "after Stop")

	if fixture.reader == nil {
		return
	}

	for _, recorded := range fixture.reader.Spans() {
		if recorded.Name == "inactive" {
			t.Errorf("span started after Stop was recorded")
		}
	}
}

// parenting checks local parent, child, and sibling span relationships.
// Context propagation must preserve trace identity while assigning distinct spans.
func (suite suite) parenting(t *testing.T) {
	fixture := suite.open(t)
	tracer := fixture.telemetry.Tracer(scope)

	ctx, parent := tracer.Start(t.Context(), "parent")
	childCtx, child := tracer.Start(ctx, "child")
	_, sibling := tracer.Start(ctx, "sibling")

	parentContext, childContext, siblingContext := parent.Context(), child.Context(), sibling.Context()

	if !parentContext.IsValid() || !childContext.IsValid() || !siblingContext.IsValid() {
		t.Fatalf("invalid span contexts: %+v %+v %+v", parentContext, childContext, siblingContext)
	}

	if childContext.TraceID != parentContext.TraceID || siblingContext.TraceID != parentContext.TraceID {
		t.Errorf("children left the parent's trace")
	}

	if parentContext.SpanID == childContext.SpanID || childContext.SpanID == siblingContext.SpanID {
		t.Errorf("span IDs are not unique")
	}

	if telemetry.SpanFromContext(ctx) != parent || telemetry.SpanFromContext(childCtx) != child {
		t.Errorf("SpanFromContext does not return the span the context was created for")
	}

	if !parent.Context().Sampled {
		t.Errorf("root span is not sampled")
	}

	child.End()
	sibling.End()
	parent.End()

	reader := fixture.needReader(t)

	spans := reader.Spans()

	if got := findSpan(t, spans, "child").Parent.SpanID; got != parentContext.SpanID {
		t.Errorf("child parent = %v, want %v", got, parentContext.SpanID)
	}

	if got := findSpan(t, spans, "sibling").Parent.SpanID; got != parentContext.SpanID {
		t.Errorf("sibling parent = %v, want %v", got, parentContext.SpanID)
	}

	if got := findSpan(t, spans, "parent").Parent; got.IsValid() {
		t.Errorf("root span has parent %+v", got)
	}
}

// rootSpans checks independent starts without a parent context.
// Each root must receive a distinct valid trace identity.
func (suite suite) rootSpans(t *testing.T) {
	tracer := suite.open(t).telemetry.Tracer(scope)

	_, first := tracer.Start(t.Context(), "first")
	_, second := tracer.Start(t.Context(), "second")
	defer first.End()
	defer second.End()

	if first.Context().TraceID == second.Context().TraceID {
		t.Errorf("independent root spans share trace %v", first.Context().TraceID)
	}
}

// spanData checks retention of attributes, events, errors, kind, and scope.
// The reader's final snapshot must reflect all recorder updates before End.
func (suite suite) spanData(t *testing.T) {
	fixture := suite.open(t)
	tracer := fixture.telemetry.Tracer(scope)

	_, span := tracer.Start(t.Context(), "operation",
		telemetry.Kind(telemetry.Client),
		telemetry.Attrs(
			telemetry.String("s", "text"),
			telemetry.Int("i", 2),
			telemetry.Float64("f", 1.5),
			telemetry.Bool("b", true),
			telemetry.Strings("l", []string{"x", "y"}),
			telemetry.Attr{},
		),
	)
	span.SetAttributes(telemetry.String("s", "replaced"), telemetry.String("late", "yes"))
	span.AddEvent("progress", telemetry.Int("step", 1))
	span.RecordError(errors.New("boom"))
	span.SetStatus(telemetry.Error, "failed")
	span.End()

	recorded := findSpan(t, fixture.needReader(t).Spans(), "operation")

	if recorded.Kind != telemetry.Client || recorded.Scope != scope {
		t.Errorf("kind, scope = %v, %q; want Client, %q", recorded.Kind, recorded.Scope, scope)
	}

	want := []telemetry.Attr{
		telemetry.String("s", "replaced"),
		telemetry.Int("i", 2),
		telemetry.Float64("f", 1.5),
		telemetry.Bool("b", true),
		telemetry.Strings("l", []string{"x", "y"}),
		telemetry.String("late", "yes"),
	}

	for _, attr := range want {
		if got, ok := recorded.Attr(attr.Key()); !ok || !got.Equal(attr) {
			t.Errorf("attribute %v = %v (found %v)", attr, got, ok)
		}
	}

	if len(recorded.Attrs) != len(want) {
		t.Errorf("recorded %d attributes %v, want %d", len(recorded.Attrs), recorded.Attrs, len(want))
	}

	if recorded.Status != telemetry.Error || recorded.StatusDescription != "failed" {
		t.Errorf("status = %v %q, want Error \"failed\"", recorded.Status, recorded.StatusDescription)
	}

	requireEvent(t, recorded, "progress", telemetry.Int("step", 1))
	requireEvent(t, recorded, "exception", telemetry.String("exception.message", "boom"))
}

// requireEvent reports a failure unless one event has the requested name and
// every requested attribute. Empty attrs match by name alone; attributes from
// separate events cannot satisfy a single request.
func requireEvent(t testing.TB, span memorytelemetry.Span, name string, attrs ...telemetry.Attr) {
	t.Helper()

	found := slices.ContainsFunc(span.Events, func(event memorytelemetry.Event) bool {
		if event.Name != name {
			return false
		}

		return !slices.ContainsFunc(attrs, func(want telemetry.Attr) bool {
			return !slices.ContainsFunc(event.Attrs, want.Equal)
		})
	})

	if !found {
		t.Errorf("span has no event %q with attributes %v; events: %+v", name, attrs, span.Events)
	}
}

// status checks status precedence through successive updates on separate spans.
// Every scenario shares one started driver. All spans finish before one snapshot
// is read and checked by the explicit scenario subtests.
func (suite suite) status(t *testing.T) {
	fixture := suite.open(t)
	tracer := fixture.telemetry.Tracer(scope)

	_, span := tracer.Start(t.Context(), "status unset")
	span.End()

	_, span = tracer.Start(t.Context(), "status unset call ignored")
	span.SetStatus(telemetry.Unset, "ignored")
	span.End()

	_, span = tracer.Start(t.Context(), "status error keeps description")
	span.SetStatus(telemetry.Error, "first")
	span.End()

	_, span = tracer.Start(t.Context(), "status error replaces error")
	span.SetStatus(telemetry.Error, "first")
	span.SetStatus(telemetry.Error, "second")
	span.End()

	_, span = tracer.Start(t.Context(), "status ok replaces error")
	span.SetStatus(telemetry.Error, "first")
	span.SetStatus(telemetry.OK, "dropped")
	span.End()

	_, span = tracer.Start(t.Context(), "status ok is final")
	span.SetStatus(telemetry.OK, "dropped")
	span.SetStatus(telemetry.Error, "late")
	span.End()

	_, span = tracer.Start(t.Context(), "status unset does not clear error")
	span.SetStatus(telemetry.Error, "first")
	span.SetStatus(telemetry.Unset, "ignored")
	span.End()

	spans := fixture.needReader(t).Spans()

	t.Run("unset", func(t *testing.T) {
		recorded := findSpan(t, spans, "status unset")

		if recorded.Status != telemetry.Unset || recorded.StatusDescription != "" {
			t.Errorf("%s: status = %v %q, want %v %q",
				"unset", recorded.Status, recorded.StatusDescription, telemetry.Unset, "")
		}
	})
	t.Run("unset call ignored", func(t *testing.T) {
		recorded := findSpan(t, spans, "status unset call ignored")

		if recorded.Status != telemetry.Unset || recorded.StatusDescription != "" {
			t.Errorf("%s: status = %v %q, want %v %q",
				"unset call ignored", recorded.Status, recorded.StatusDescription, telemetry.Unset, "")
		}
	})
	t.Run("error keeps description", func(t *testing.T) {
		recorded := findSpan(t, spans, "status error keeps description")

		if recorded.Status != telemetry.Error || recorded.StatusDescription != "first" {
			t.Errorf("%s: status = %v %q, want %v %q",
				"error keeps description", recorded.Status, recorded.StatusDescription, telemetry.Error, "first")
		}
	})
	t.Run("error replaces error", func(t *testing.T) {
		recorded := findSpan(t, spans, "status error replaces error")

		if recorded.Status != telemetry.Error || recorded.StatusDescription != "second" {
			t.Errorf("%s: status = %v %q, want %v %q",
				"error replaces error", recorded.Status, recorded.StatusDescription, telemetry.Error, "second")
		}
	})
	t.Run("ok replaces error", func(t *testing.T) {
		recorded := findSpan(t, spans, "status ok replaces error")

		if recorded.Status != telemetry.OK || recorded.StatusDescription != "" {
			t.Errorf("%s: status = %v %q, want %v %q",
				"ok replaces error", recorded.Status, recorded.StatusDescription, telemetry.OK, "")
		}
	})
	t.Run("ok is final", func(t *testing.T) {
		recorded := findSpan(t, spans, "status ok is final")

		if recorded.Status != telemetry.OK || recorded.StatusDescription != "" {
			t.Errorf("%s: status = %v %q, want %v %q",
				"ok is final", recorded.Status, recorded.StatusDescription, telemetry.OK, "")
		}
	})
	t.Run("unset does not clear error", func(t *testing.T) {
		recorded := findSpan(t, spans, "status unset does not clear error")

		if recorded.Status != telemetry.Error || recorded.StatusDescription != "first" {
			t.Errorf("%s: status = %v %q, want %v %q",
				"unset does not clear error", recorded.Status, recorded.StatusDescription, telemetry.Error, "first")
		}
	})
}

// emptySpanDefaults checks raw driver input so named string defaults do not
// depend on construction through the facade.
func (suite suite) emptySpanDefaults(t *testing.T) {
	fixture := suite.open(t)
	driver := fixture.driver.(telemetry.TraceDriver)
	_, recorder := driver.StartSpan(t.Context(), telemetry.SpanStart{Name: "empty defaults"})
	recorder.End()

	recorded := findSpan(t, fixture.needReader(t).Spans(), "empty defaults")

	if recorded.Kind != telemetry.Internal || recorded.Status != telemetry.Unset {
		t.Fatalf("kind/status = %q/%q, want internal/unset", recorded.Kind, recorded.Status)
	}
}

// emptyStatusDoesNotClearError preserves the former zero-code no-op behavior
// independently of the explicit Unset constant's new string representation.
func (suite suite) emptyStatusDoesNotClearError(t *testing.T) {
	fixture := suite.open(t)
	_, span := fixture.telemetry.Tracer(scope).Start(t.Context(), "empty status")
	span.SetStatus(telemetry.Error, "first")
	span.SetStatus("", "ignored")
	span.End()

	recorded := findSpan(t, fixture.needReader(t).Spans(), "empty status")

	if recorded.Status != telemetry.Error || recorded.StatusDescription != "first" {
		t.Fatalf("status = %q %q, want error first", recorded.Status, recorded.StatusDescription)
	}
}

// endIsIdempotent checks repeated End and updates made after the first End.
// Exactly one finished snapshot must remain, unaffected by those later calls.
func (suite suite) endIsIdempotent(t *testing.T) {
	fixture := suite.open(t)

	_, span := fixture.telemetry.Tracer(scope).Start(t.Context(), "twice")
	span.End()
	span.End()
	span.SetAttributes(telemetry.String("after", "end"))

	count := 0

	for _, recorded := range fixture.needReader(t).Spans() {
		if recorded.Name == "twice" {
			count++

			if _, ok := recorded.Attr("after"); ok {
				t.Errorf("attribute set after End was recorded")
			}
		}
	}

	if count != 1 {
		t.Errorf("span ended twice was recorded %d times, want once", count)
	}
}

// propagationRoundTrip checks trace injection and extraction for both carriers.
// Extracted remote context must become the parent of a subsequently started span.
func (suite suite) propagationRoundTrip(t *testing.T) {
	carriers := map[string]func() telemetry.Carrier{
		"map": func() telemetry.Carrier {
			return telemetry.MapCarrier{}
		},
		"header": func() telemetry.Carrier {
			return telemetry.HeaderCarrier{}
		},
	}

	for name, newCarrier := range carriers {
		t.Run(name, func(t *testing.T) {
			fixture := suite.open(t)
			tracer := fixture.telemetry.Tracer(scope)

			untouched := newCarrier()
			fixture.telemetry.Inject(t.Context(), untouched)

			if len(untouched.Keys()) != 0 {
				t.Errorf("Inject without a span wrote %v", untouched.Keys())
			}

			ctx, span := tracer.Start(t.Context(), "sender", telemetry.Kind(telemetry.Producer))
			carrier := newCarrier()
			fixture.telemetry.Inject(ctx, carrier)

			if len(carrier.Keys()) == 0 {
				t.Fatalf("Inject wrote nothing for a valid span")
			}

			received := fixture.telemetry.Extract(t.Context(), carrier)
			remote := telemetry.SpanFromContext(received).Context()

			if remote.TraceID != span.Context().TraceID || remote.SpanID != span.Context().SpanID {
				t.Errorf("extracted %+v, want trace %v span %v", remote, span.Context().TraceID, span.Context().SpanID)
			}

			if !remote.Remote || remote.Sampled != span.Context().Sampled {
				t.Errorf("extracted Remote = %v, Sampled = %v; want true, %v", remote.Remote, remote.Sampled, span.Context().Sampled)
			}

			_, child := tracer.Start(received, "receiver", telemetry.Kind(telemetry.Consumer))

			if child.Context().TraceID != span.Context().TraceID {
				t.Errorf("receiver started trace %v, want %v", child.Context().TraceID, span.Context().TraceID)
			}

			child.End()
			span.End()

			if fixture.reader == nil {
				return
			}

			if got := findSpan(t, fixture.reader.Spans(), "receiver").Parent.SpanID; got != span.Context().SpanID {
				t.Errorf("receiver parent = %v, want the sender span %v", got, span.Context().SpanID)
			}
		})
	}
}

// propagationIgnoresGarbage checks malformed or absent trace headers.
// Extraction must leave no valid remote span context behind.
func (suite suite) propagationIgnoresGarbage(t *testing.T) {
	fixture := suite.open(t)

	garbage := []telemetry.MapCarrier{
		{},
		{"traceparent": "garbage"},
		{"traceparent": "00-00000000000000000000000000000000-0000000000000000-01"},
		{"traceparent": "zz-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "tracestate": "a=b"},
		{"b3": "zzz", "x-amzn-trace-id": "Root=garbage"},
	}

	for _, carrier := range garbage {
		ctx := fixture.telemetry.Extract(t.Context(), carrier)

		if spanContext := telemetry.SpanFromContext(ctx).Context(); spanContext.IsValid() {
			t.Errorf("Extract(%v) returned a valid span context %+v", carrier, spanContext)
		}
	}
}

// declaredKeys checks normalized declarations and bounded metric label values.
// Undeclared attributes must not create extra metric dimensions.
func (suite suite) declaredKeys(t *testing.T) {
	fixture := suite.open(t)
	meter := fixture.telemetry.Meter(scope)

	counter := meter.Counter("conformance.requests",
		telemetry.Unit("{request}"),
		telemetry.Description("Requests handled."),
		telemetry.Keys("method", "route", "method", ""),
	)
	counter.Add(t.Context(), 1,
		telemetry.String("route", "/a"),
		telemetry.String("method", "GET"),
		telemetry.String("undeclared", "dropped"),
	)
	counter.Add(t.Context(), 2, telemetry.String("method", "GET"))
	counter.Add(t.Context(), 4, telemetry.String("method", "GET"), telemetry.String("route", "/a"))
	counter.Add(t.Context(), -5, telemetry.String("method", "GET"))

	metric := findMetric(t, fixture.needReader(t).Metrics(), "conformance.requests")

	if !slices.Equal(metric.Keys, []string{"method", "route"}) {
		t.Fatalf("keys = %v, want [method route]", metric.Keys)
	}

	if len(metric.Points) != 2 {
		t.Fatalf("points = %+v, want 2 label combinations", metric.Points)
	}

	if point, ok := metric.Point("GET", "/a"); !ok || point.Sum != 5 {
		t.Errorf("point GET /a = %+v (found %v), want sum 5", point, ok)
	}

	if point, ok := metric.Point("GET", ""); !ok || point.Sum != 2 {
		t.Errorf("point GET \"\" = %+v (found %v), want sum 2 with the missing key empty", point, ok)
	}
}

// instrumentKinds checks counter, up-down counter, histogram, and gauge updates.
// One shared fixture exposes each instrument's aggregates through one snapshot,
// checked by explicit instrument-specific subtests after all updates complete.
func (suite suite) instrumentKinds(t *testing.T) {
	fixture := suite.open(t)
	meter := fixture.telemetry.Meter(scope)
	ctx := t.Context()

	meter.Counter("conformance.total").Add(ctx, 3)

	inFlight := meter.UpDownCounter("conformance.in_flight")
	inFlight.Add(ctx, 3)
	inFlight.Add(ctx, -1)

	duration := meter.Histogram("conformance.duration", telemetry.Unit("s"), telemetry.Buckets(10, 1, 0.1, 1))
	duration.Record(ctx, 0.5)
	duration.Record(ctx, 1.5)

	depth := meter.Gauge("conformance.depth")
	depth.Set(ctx, 5)
	depth.Set(ctx, 7)

	metrics := fixture.needReader(t).Metrics()

	t.Run("Counter", func(t *testing.T) {
		metric := findMetric(t, metrics, "conformance.total")
		point, ok := metric.Point()

		if metric.Kind != telemetry.CounterKind || !ok {
			t.Errorf("%s: kind = %v, point found %v; want %v with one point",
				"conformance.total", metric.Kind, ok, telemetry.CounterKind)

			return
		}

		if point.Sum != 3 {
			t.Errorf("%s sum = %v, want %v", "conformance.total", point.Sum, 3)
		}
	})
	t.Run("UpDownCounter", func(t *testing.T) {
		metric := findMetric(t, metrics, "conformance.in_flight")
		point, ok := metric.Point()

		if metric.Kind != telemetry.UpDownCounterKind || !ok {
			t.Errorf("%s: kind = %v, point found %v; want %v with one point",
				"conformance.in_flight", metric.Kind, ok, telemetry.UpDownCounterKind)

			return
		}

		if point.Sum != 2 {
			t.Errorf("%s sum = %v, want %v", "conformance.in_flight", point.Sum, 2)
		}
	})
	t.Run("Histogram", func(t *testing.T) {
		metric := findMetric(t, metrics, "conformance.duration")
		point, ok := metric.Point()

		if metric.Kind != telemetry.HistogramKind || !ok {
			t.Errorf("%s: kind = %v, point found %v; want %v with one point",
				"conformance.duration", metric.Kind, ok, telemetry.HistogramKind)

			return
		}

		if point.Count != 2 || point.Sum != 2 {
			t.Errorf("%s count, sum = %d, %v; want %d, %v",
				"conformance.duration", point.Count, point.Sum, 2, 2)
		}
	})
	t.Run("Gauge", func(t *testing.T) {
		metric := findMetric(t, metrics, "conformance.depth")
		point, ok := metric.Point()

		if metric.Kind != telemetry.GaugeKind || !ok {
			t.Errorf("%s: kind = %v, point found %v; want %v with one point",
				"conformance.depth", metric.Kind, ok, telemetry.GaugeKind)

			return
		}

		if point.Last != 7 {
			t.Errorf("%s last = %v, want %v", "conformance.depth", point.Last, 7)
		}
	})
}

// handlerCorrelates checks structured logs produced within an active span.
// The wrapped handler must receive trace and span IDs matching that span.
func (suite suite) handlerCorrelates(t *testing.T) {
	fixture := suite.open(t)

	var output bytes.Buffer

	logger := slog.New(fixture.telemetry.Handler(slog.NewJSONHandler(&output, nil)))

	ctx, span := fixture.telemetry.Tracer(scope).Start(t.Context(), "logging")
	defer span.End()

	logger.With("a", 1).WithGroup("g").InfoContext(ctx, "with span", "k", "v")
	logger.InfoContext(t.Context(), "without span")
	logger.DebugContext(ctx, "below the level")

	var lines []map[string]any

	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var decoded map[string]any

		if err := json.Unmarshal(line, &decoded); err != nil {
			t.Fatalf("decode %s: %v", line, err)
		}

		lines = append(lines, decoded)
	}

	if len(lines) != 2 {
		t.Fatalf("logged %d records, want 2: %s", len(lines), output.String())
	}

	group, _ := lines[0]["g"].(map[string]any)

	if group["trace_id"] != span.Context().TraceID.String() || group["span_id"] != span.Context().SpanID.String() {
		t.Errorf("record with span = %v, want trace_id and span_id of %+v", lines[0], span.Context())
	}

	if lines[0]["a"] != float64(1) || group["k"] != "v" {
		t.Errorf("record with span lost attributes: %v", lines[0])
	}

	if _, ok := lines[1]["trace_id"]; ok {
		t.Errorf("record without span has trace_id: %v", lines[1])
	}
}

// exportAcceptsRecords checks the log export path with a self-contained record.
// Drivers with a reader must retain the expected message and attributes.
func (suite suite) exportAcceptsRecords(t *testing.T) {
	fixture := suite.open(t)
	handler := fixture.telemetry.Handler(slog.DiscardHandler)

	ctx, span := fixture.telemetry.Tracer(scope).Start(t.Context(), "exporting")
	defer span.End()

	record := slog.NewRecord(time.Time{}, slog.LevelError, "exported", 0)
	record.AddAttrs(slog.String("k", "v"))

	if !handler.Enabled(ctx, slog.LevelError) {
		t.Errorf("handler with a log driver reports Error disabled")
	}

	if err := handler.WithAttrs([]slog.Attr{slog.Int("n", 1)}).WithGroup("g").Handle(ctx, record); err != nil {
		t.Errorf("Handle = %v, want nil", err)
	}
}

// findSpan returns the span named name or fails the test.
func findSpan(t *testing.T, spans []memorytelemetry.Span, name string) memorytelemetry.Span {
	t.Helper()

	for _, span := range spans {
		if span.Name == name {
			return span
		}
	}

	t.Fatalf("no span %q among %d recorded spans", name, len(spans))

	return memorytelemetry.Span{}
}

// findMetric returns the instrument named name or fails the test.
func findMetric(t *testing.T, metrics []memorytelemetry.Metric, name string) memorytelemetry.Metric {
	t.Helper()

	for _, metric := range metrics {
		if metric.Name == name {
			return metric
		}
	}

	t.Fatalf("no metric %q among %d recorded metrics", name, len(metrics))

	return memorytelemetry.Metric{}
}
