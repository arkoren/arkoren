package clientop_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/errtype"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// errRejected contains personal data to detect accidental error-text capture.
// Its registered class name is the only error detail allowed in telemetry.
var errRejected = errors.New("rejected: alice@example.com")

// newInstruments builds isolated test instruments with explicit label keys.
// Each test gets a fresh resolver so cached telemetry cannot cross tests.
func newInstruments() *telemetry.Instruments[clientop.Instruments] {
	return telemetry.NewInstruments(clientop.Build(clientop.Names{
		Scope:    "arkoren.dev/test",
		Noun:     "test operations",
		Count:    "arkoren.test.operations",
		Duration: "arkoren.test.operation.duration",
		Keys:     []string{"arkoren.test.operation", "arkoren.test.outcome"},
		Buckets:  []float64{0.1, 1},
	}))
}

// TestOpRecordsSpanAndMetrics checks successful operation instrumentation.
// Late labels and span-only details must reach their respective destinations.
func TestOpRecordsSpanAndMetrics(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	in := newInstruments()
	operation := telemetry.String("arkoren.test.operation", "put")

	_, op := clientop.Begin(t.Context(), fake.Telemetry(t), in, telemetry.Client, "test", "put", operation)
	op.Label(telemetry.String("arkoren.test.outcome", "ok"))
	op.SetAttrs(telemetry.Int("arkoren.test.size", 7))
	op.End(t.Context(), nil)

	fake.AssertSpan(t, "test put", operation, telemetry.Int("arkoren.test.size", 7))
	fake.AssertCounter(t, "arkoren.test.operations", 1, operation, telemetry.String("arkoren.test.outcome", "ok"))
	fake.AssertHistogram(t, "arkoren.test.operation.duration", 1, operation)

	span, _ := fake.Span("test put")

	if span.Status != telemetry.Unset {
		t.Errorf("status = %v, want unset", span.Status)
	}
}

// TestEndClassifiesErrorWithoutRecordingText checks safe failure recording.
// A stable error class must replace personal error text and exception events.
func TestEndClassifiesErrorWithoutRecordingText(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	known := errtype.Sentinel{Err: errRejected, Name: "test.ErrRejected"}

	_, op := clientop.Begin(t.Context(), fake.Telemetry(t), newInstruments(), telemetry.Client, "test", "put")
	op.End(t.Context(), errRejected, known)

	fake.AssertSpan(t, "test put", telemetry.String("error.type", "test.ErrRejected"))
	fake.AssertCounter(t, "arkoren.test.operations", 1, telemetry.String("error.type", "test.ErrRejected"))

	span, _ := fake.Span("test put")

	if span.Status != telemetry.Error || len(span.Events) != 0 {
		t.Errorf("status = %v, events = %d, want error and no events", span.Status, len(span.Events))
	}

	for _, attr := range span.Attrs {
		if attr.Text() == errRejected.Error() {
			t.Errorf("span attribute %q carries the error text", attr.Key())
		}
	}
}

// TestNilOpDoesNothing checks the disabled operation's nil-safe methods.
// Callers must be able to label and finish without their own enabled guard.
func TestNilOpDoesNothing(t *testing.T) {
	t.Parallel()

	var op *clientop.Op

	op.Label(telemetry.String("a", "b"))
	op.SetAttrs(telemetry.String("a", "b"))
	op.End(t.Context(), errRejected)
}

// TestDisabledTelemetryAllocatesNothing measures the complete disabled path.
// Beginning, labeling, and finishing must not allocate instrumentation state.
func TestDisabledTelemetryAllocatesNothing(t *testing.T) {
	disabled := telemetry.New(nil)
	in := newInstruments()
	ctx := context.Background()
	operation := telemetry.String("arkoren.test.operation", "put")

	allocations := testing.AllocsPerRun(100, func() {
		_, op := clientop.Begin(ctx, disabled, in, telemetry.Client, "test", "put", operation)
		op.Label(telemetry.String("arkoren.test.outcome", "ok"))
		op.End(ctx, nil)
	})

	if allocations != 0 {
		t.Errorf("disabled telemetry allocated %v times per operation", allocations)
	}
}
