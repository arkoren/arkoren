package memorytelemetry_test

import (
	"log/slog"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

// TestObserverMethodsKeepNothingWhenDisabled checks zero-capacity retention.
// Passive delivery must respect the same disabled storage settings as drivers.
func TestObserverMethodsKeepNothingWhenDisabled(t *testing.T) {
	t.Parallel()

	driver := memorytelemetry.New(func(store *memorytelemetry.Builder) {
		store.MaxSpans(0)
		store.MaxLogs(0)
	})

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	driver.SpanEnded(telemetry.FinishedSpan{Name: "x"})
	driver.Logged(t.Context(), slog.Record{})

	if len(driver.Spans()) != 0 || len(driver.Logs()) != 0 {
		t.Fatal("driver kept data although its rings are disabled")
	}
}

// TestObserverMethodsDiscardBeforeStart checks the observer lifecycle boundary.
// Signals delivered before startup must not populate memory snapshots.
func TestObserverMethodsDiscardBeforeStart(t *testing.T) {
	t.Parallel()

	driver := memorytelemetry.New()
	driver.SpanEnded(telemetry.FinishedSpan{Name: "x"})
	driver.Measured(telemetry.InstrumentSpec{
		Name: "m",
		Kind: telemetry.CounterKind,
	}, 1, nil)

	if len(driver.Spans()) != 0 || len(driver.Metrics()) != 0 {
		t.Fatal("driver kept data before Start")
	}
}
