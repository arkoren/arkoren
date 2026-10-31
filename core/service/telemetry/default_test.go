package telemetry_test

import (
	"sync"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestDefault checks the no-op default, SetDefault, and nil restore.
func TestDefault(t *testing.T) {
	noop := telemetry.Default()
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	if _, span := noop.Tracer("t").Start(t.Context(), "x"); span.IsRecording() {
		t.Fatal("default records")
	}

	fake := telemetrytest.New()
	custom := fake.Telemetry(t)
	telemetry.SetDefault(custom)

	if telemetry.Default() != custom {
		t.Fatal("SetDefault did not take effect")
	}

	_, span := telemetry.Default().Tracer("t").Start(t.Context(), "via default")
	span.End()

	fake.AssertSpan(t, "via default")

	telemetry.SetDefault(nil)

	if telemetry.Default() != noop {
		t.Fatal("SetDefault(nil) did not restore the no-op")
	}
}

// TestDefaultConcurrent exercises SetDefault under the race detector.
func TestDefaultConcurrent(t *testing.T) {
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	custom := telemetry.New(nil)

	var group sync.WaitGroup

	for range 8 {
		group.Go(func() {
			for range 100 {
				telemetry.SetDefault(custom)

				_ = telemetry.Default().Tracer("t")
				telemetry.SetDefault(nil)
			}
		})
	}

	group.Wait()
}
