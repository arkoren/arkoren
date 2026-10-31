package telemetry_test

import (
	"context"
	"fmt"
	"testing"

	"arkoren.dev/core/service/telemetry"
)

// TestSpanContextHex checks the hex rendering and validity of IDs.
func TestSpanContextHex(t *testing.T) {
	t.Parallel()

	spanContext := telemetry.SpanContext{
		TraceID: telemetry.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
		SpanID:  telemetry.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
	}

	if got := spanContext.TraceID.String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("TraceID = %s", got)
	}

	if got := spanContext.SpanID.String(); got != "00f067aa0ba902b7" {
		t.Errorf("SpanID = %s", got)
	}

	if !spanContext.IsValid() {
		t.Error("context with both IDs is invalid")
	}

	if (telemetry.SpanContext{TraceID: spanContext.TraceID}).IsValid() {
		t.Error("context without span ID is valid")
	}
}

// TestSpanFromContextDefault checks the no-op span for plain contexts.
func TestSpanFromContextDefault(t *testing.T) {
	t.Parallel()

	span := telemetry.SpanFromContext(t.Context())

	if span == nil || span.Context().IsValid() || span.IsRecording() {
		t.Fatalf("span = %+v, want a non-recording span with invalid context", span)
	}

	span.SetAttributes(telemetry.String("k", "v"))
	span.AddEvent("e")
	span.RecordError(nil)
	span.SetStatus(telemetry.Error, "x")
	span.End()
}

// TestContextWithSpanContext checks adopting an identity.
func TestContextWithSpanContext(t *testing.T) {
	t.Parallel()

	want := telemetry.SpanContext{
		TraceID: telemetry.TraceID{1},
		SpanID:  telemetry.SpanID{2},
		Sampled: true,
		Remote:  true,
	}
	ctx := telemetry.ContextWithSpanContext(t.Context(), want)

	if got := telemetry.SpanFromContext(ctx).Context(); got != want {
		t.Fatalf("span context = %+v, want %+v", got, want)
	}

	if telemetry.SpanFromContext(ctx).IsRecording() {
		t.Fatal("adopted span is recording")
	}
}

// TestNilSpan checks that a nil span is usable.
func TestNilSpan(t *testing.T) {
	t.Parallel()

	var span *telemetry.Span
	span.SetAttributes(telemetry.String("k", "v"))
	span.End()

	if span.IsRecording() || span.Context().IsValid() {
		t.Fatal("nil span is recording or valid")
	}
}

// ExampleContextWithSpanContext demonstrates identity adoption without
// constructing a recording span or changing the supplied remote flag.
func ExampleContextWithSpanContext() {
	identity := telemetry.SpanContext{
		TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}, Remote: true,
	}
	ctx := telemetry.ContextWithSpanContext(context.Background(), identity)
	span := telemetry.SpanFromContext(ctx)
	fmt.Println(span.Context().IsValid(), span.Context().Remote, span.IsRecording())
	span.End()
	// Output:
	// true true false
}
