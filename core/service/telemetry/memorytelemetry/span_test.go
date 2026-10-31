package memorytelemetry_test

import (
	"fmt"
	"sync"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

// TestRingKeepsNewestSpans checks the bounded ring and its ordering.
func TestRingKeepsNewestSpans(t *testing.T) {
	t.Parallel()

	driver, facade := start(t, func(store *memorytelemetry.Builder) {
		store.MaxSpans(3)
	})

	tracer := facade.Tracer("t")

	for index := range 5 {
		_, span := tracer.Start(t.Context(), fmt.Sprintf("span-%d", index))
		span.End()
	}

	spans := driver.Spans()

	if len(spans) != 3 {
		t.Fatalf("kept %d spans, want 3", len(spans))
	}

	for index, want := range []string{"span-2", "span-3", "span-4"} {
		if spans[index].Name != want {
			t.Errorf("span %d = %s, want %s", index, spans[index].Name, want)
		}
	}
}

// TestIDsOnly checks that MaxSpans zero generates IDs but records nothing.
func TestIDsOnly(t *testing.T) {
	t.Parallel()

	driver, facade := start(t, func(store *memorytelemetry.Builder) {
		store.MaxSpans(0)
	})

	_, span := facade.Tracer("t").Start(t.Context(), "ids", telemetry.Attrs(telemetry.String("k", "v")))
	span.SetAttributes(telemetry.String("late", "v"))
	span.AddEvent("e")
	span.End()

	if !span.Context().IsValid() || span.IsRecording() {
		t.Fatalf("span valid = %v, recording = %v; want valid IDs and no recording", span.Context().IsValid(), span.IsRecording())
	}

	if len(driver.Spans()) != 0 {
		t.Fatal("spans were kept")
	}

	carrier := telemetry.MapCarrier{}
	ctx := telemetry.ContextWithSpanContext(t.Context(), span.Context())
	facade.Inject(ctx, carrier)

	if carrier.Get("traceparent") == "" {
		t.Fatal("IDs-only driver does not propagate")
	}
}

// TestSpanSnapshotOperationsSurviveConcurrentReset verifies eager native
// filtering and nested ownership while the bounded span ring advances.
func TestSpanSnapshotOperationsSurviveConcurrentReset(t *testing.T) {
	t.Parallel()

	driver, facade := start(t, func(store *memorytelemetry.Builder) {
		store.MaxSpans(1)
	})

	tracer := facade.Tracer("snapshot")
	_, span := tracer.Start(t.Context(), "original", telemetry.Attrs(telemetry.String("key", "value")))
	span.AddEvent("event", telemetry.String("key", "value"))
	span.End()

	snapshot := driver.Spans()
	var workers sync.WaitGroup
	workers.Go(func() {
		for range 30 {
			_, next := tracer.Start(t.Context(), "later")
			next.End()
			driver.Reset()
		}
	})

	for range 30 {
		selected := snapshot.Filter(func(_ int, span memorytelemetry.Span) bool {
			return span.Name == "original"
		})

		if selected.Len() != 1 || selected[0].Events[0].Attrs[0].AsString() != "value" {
			t.Error("span snapshot changed while ring advanced")
		}
	}

	workers.Wait()
}
