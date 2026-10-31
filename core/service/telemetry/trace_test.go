package telemetry_test

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// callbackError exercises caller-controlled error formatting. Its callback
// records harmless additional span metadata, rather than ending the same span
// from inside its recorder operation.
type callbackError struct {
	// format runs each time a backend requests the error's public text.
	format func()
}

// Error records the controlled callback and returns deterministic safe text.
// It deliberately does not acquire a test lock or perform any I/O.
func (err callbackError) Error() string {
	err.format()

	return "operation failed"
}

// TestAttrsOptionSnapshotsCallerSlice checks publication through span output.
// Reusing the caller's slice after constructing an option must not change the
// attributes of a later span that uses the published option.
func TestAttrsOptionSnapshotsCallerSlice(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	service := fake.Telemetry(t)
	attrs := []telemetry.Attr{telemetry.String("region", "original"), telemetry.String("unit", "ms")}
	single := telemetry.Attrs(attrs[:1]...)
	multiple := telemetry.Attrs(attrs...)
	attrs[0] = telemetry.String("region", "changed")
	_, span := service.Tracer("test").Start(t.Context(), "single", single)
	span.End()

	_, span = service.Tracer("test").Start(t.Context(), "multiple", multiple)
	span.End()

	spans := fake.Spans()

	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(spans))
	}

	for _, recorded := range spans {
		if attr, ok := recorded.Attr("region"); !ok || attr.AsString() != "original" {
			t.Fatalf("span attribute = %v, found %v; want region=original", attr, ok)
		}
	}
}

// TestSpanStopsRecordingAndObservingAfterTelemetryStop checks an already-open
// public span. Its identity and End remain usable, but disabled telemetry must
// neither report it as recording nor deliver a finished-span notification.
func TestSpanStopsRecordingAndObservingAfterTelemetryStop(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	service := fake.Telemetry(t)
	observer := &countingObserver{}
	service.Observe(observer)

	_, span := service.Tracer("test").Start(t.Context(), "operation")

	if err := service.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	recording := span.IsRecording()
	span.SetAttributes(telemetry.String("late", "ignored"))
	span.End()

	if recording || len(observer.spans) != 0 {
		t.Fatalf("recording = %v, observed spans = %d; want false, 0", recording, len(observer.spans))
	}
}

// blockingSpanDriver replaces only StartSpan's recorder. Lifecycle and
// propagation remain those of the embedded memory driver.
type blockingSpanDriver struct {
	// Driver supplies the remaining public TraceDriver contract.
	*memorytelemetry.Driver

	// recorder exposes a controlled in-progress Recording call to the test.
	recorder *blockingSpanRecorder
}

// StartSpan returns the test recorder without inspecting facade internals.
// The context is unchanged because this driver has no native context state.
func (driver *blockingSpanDriver) StartSpan(ctx context.Context, _ telemetry.SpanStart) (context.Context, telemetry.SpanRecorder) {
	return ctx, driver.recorder
}

// blockingSpanRecorder exposes the ordering of public SpanRecorder calls.
// Only its first Recording call blocks; all shared flags are atomic.
type blockingSpanRecorder struct {
	// SpanRecorder supplies unused methods for this ordering test. Only the
	// explicitly implemented Context, Parent, Recording, SetAttributes and End run.
	telemetry.SpanRecorder

	// entered closes when the first Recording call has started.
	entered chan struct{}

	// release lets that Recording call finish after End has been requested.
	release chan struct{}

	// blocked claims the single controlled Recording call.
	blocked atomic.Bool

	// ended records whether the facade has called the driver's End.
	ended atomic.Bool

	// late records an attribute callback initiated after the driver's End.
	late atomic.Bool
}

// Context returns an invalid identity; identity generation is irrelevant to
// the contract that facade callbacks must not cross the driver's End.
func (*blockingSpanRecorder) Context() telemetry.SpanContext {
	return telemetry.SpanContext{}
}

// Parent reports a root for the ordering fixture. It is explicit rather than
// promoted through the unused embedded recorder so observation can inspect the
// full mandatory metadata protocol without dereferencing a nil test delegate.
func (*blockingSpanRecorder) Parent() telemetry.SpanContext {
	return telemetry.SpanContext{}
}

// Recording blocks its first caller at a deterministic public-driver seam.
// The recorder otherwise always accepts attributes.
func (recorder *blockingSpanRecorder) Recording() bool {
	if recorder.blocked.CompareAndSwap(false, true) {
		close(recorder.entered)
		<-recorder.release
	}

	return true
}

// SetAttributes detects invocation after End without relying on a backend's
// own late-call filtering, which would hide a facade contract violation.
func (recorder *blockingSpanRecorder) SetAttributes([]telemetry.Attr) {
	recorder.late.Store(recorder.ended.Load())
}

// End marks the public driver's final callback. It does not block or acquire
// any application locks, so ordering is controlled only by the facade.
func (recorder *blockingSpanRecorder) End() {
	recorder.ended.Store(true)
}

// TestSpanEndWaitsForAdmittedRecorderCalls starts End while a Recording
// callback is in flight. End must wait before ending the driver, and the
// previously admitted operation must never initiate a callback after it.
func TestSpanEndWaitsForAdmittedRecorderCalls(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		recorder := &blockingSpanRecorder{entered: make(chan struct{}), release: make(chan struct{})}
		driver := &blockingSpanDriver{Driver: memorytelemetry.New(), recorder: recorder}
		service := startTelemetry(t, driver)
		_, span := service.Tracer("test").Start(t.Context(), "operation")
		recorded := make(chan struct{})

		go func() {
			span.SetAttributes(telemetry.String("region", "eu"))
			close(recorded)
		}()

		<-recorder.entered

		ended := make(chan struct{})

		go func() {
			span.End()
			close(ended)
		}()

		synctest.Wait()

		early := recorder.ended.Load()
		close(recorder.release)
		<-recorded
		<-ended

		if early || recorder.late.Load() {
			t.Fatalf("driver ended before callback completion = %v; callback after End = %v", early, recorder.late.Load())
		}
	})
}

// TestSpanAttributeValidation checks that invalid attributes never reach the
// driver.
func TestSpanAttributeValidation(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	facade := fake.Telemetry(t)

	_, span := facade.Tracer("t").Start(t.Context(), "s",
		telemetry.Attrs(telemetry.String("", "no key"), telemetry.Attr{}, telemetry.String("ok", "v")))
	span.SetAttributes(telemetry.Int("", 1))
	span.AddEvent("e", telemetry.Attr{})
	span.End()

	recorded, _ := fake.Span("s")

	if len(recorded.Attrs) != 1 || recorded.Attrs[0].Key() != "ok" {
		t.Fatalf("attributes = %v, want only ok", recorded.Attrs)
	}

	if len(recorded.Events[0].Attrs) != 0 {
		t.Fatalf("event attributes = %v", recorded.Events[0].Attrs)
	}
}
