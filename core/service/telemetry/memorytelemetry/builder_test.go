package memorytelemetry_test

import (
	"log/slog"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

// TestConstructionCallbacksDetachCapacities checks ordered, once-only edits
// on one root and live span/log/series limits after the draft is mutated.
func TestConstructionCallbacksDetachCapacities(t *testing.T) {
	t.Parallel()

	var retained *memorytelemetry.Builder
	calls := 0
	driver, facade := start(t, func(store *memorytelemetry.Builder) {
		calls++
		retained = store
		store.MaxSpans(9)
		store.MaxLogs(9)
		store.MaxSeries(9)
	}, func(store *memorytelemetry.Builder) {
		calls++

		if store != retained {
			t.Fatal("callbacks received different roots")
		}

		store.MaxSpans(calls)
		store.MaxLogs(calls)
		store.MaxSeries(calls)
	})
	retained.MaxSpans(0).MaxLogs(0)
	retained.MaxSeries(0)

	logger := slog.New(facade.Handler(slog.DiscardHandler))
	counter := facade.Meter("test").Counter("count", telemetry.Keys("key"))

	for _, value := range []string{"a", "b", "c"} {
		_, span := facade.Tracer("test").Start(t.Context(), "work")
		span.End()
		logger.InfoContext(t.Context(), "record")
		counter.Add(t.Context(), 1, telemetry.String("key", value))
	}

	if calls != 2 || len(driver.Spans()) != 2 || len(driver.Logs()) != 2 {
		t.Fatalf("calls=%d, spans=%d, logs=%d", calls, len(driver.Spans()), len(driver.Logs()))
	}

	if len(driver.Metrics()[0].Points) != 2 {
		t.Fatalf("metrics=%v", driver.Metrics())
	}
}

// TestConstructionSettingsReturnReceiver ensures capacity setters mutate the
// same root, including nonpositive values which must not restore defaults.
func TestConstructionSettingsReturnReceiver(t *testing.T) {
	t.Parallel()

	store := &memorytelemetry.Builder{}

	if store.MaxSpans(0) != store || store.MaxLogs(-1) != store || store.MaxSeries(2) != store {
		t.Fatal("setting returned another receiver")
	}
}

// TestConstructionEmptyCallbackExpansions preserves default retention for
// omission and empty policies without accepting a supplied nil entry.
func TestConstructionEmptyCallbackExpansions(t *testing.T) {
	t.Parallel()

	var omitted []func(*memorytelemetry.Builder)

	for _, configure := range [][]func(*memorytelemetry.Builder){nil, omitted, {}, {func(store *memorytelemetry.Builder) {
	}}} {
		driver, facade := start(t, configure...)
		_, span := facade.Tracer("test").Start(t.Context(), "work")
		span.End()

		if len(driver.Spans()) != 1 {
			t.Fatal("empty policy disabled default span retention")
		}
	}
}

// TestConstructionNilCallbackPanics verifies direct invocation in order;
// recording-limit validation must not silently filter nil policies.
func TestConstructionNilCallbackPanics(t *testing.T) {
	t.Parallel()

	calls := 0
	defer func() {
		if recover() == nil || calls != 1 {
			t.Fatalf("nil callback did not panic in order: calls=%d", calls)
		}
	}()
	memorytelemetry.New(func(store *memorytelemetry.Builder) {
		calls++
	}, nil, func(store *memorytelemetry.Builder) {
		calls++
	})
}

// TestConstructionCallbackPanicPropagates preserves the policy panic value
// rather than swallowing it or publishing a partially configured store.
func TestConstructionCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "policy failed" {
			t.Fatalf("panic=%v", got)
		}
	}()
	memorytelemetry.New(func(store *memorytelemetry.Builder) {
		panic("policy failed")
	})
}
