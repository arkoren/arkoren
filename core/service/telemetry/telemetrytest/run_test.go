package telemetrytest_test

import (
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestRunMemory runs the conformance suite against the memory driver.
func TestRunMemory(t *testing.T) {
	t.Parallel()

	telemetrytest.Run(t, func(*testing.T) telemetry.Lifecycle {
		return memorytelemetry.New()
	})
}

// TestRunFake runs the conformance suite against the fake, which adds a log
// driver.
func TestRunFake(t *testing.T) {
	t.Parallel()

	telemetrytest.Run(t, func(*testing.T) telemetry.Lifecycle {
		return telemetrytest.New()
	})
}

// TestRunWithReader runs the suite with the reader supplied by option.
func TestRunWithReader(t *testing.T) {
	t.Parallel()

	telemetrytest.Run(t,
		func(*testing.T) telemetry.Lifecycle {
			return memorytelemetry.New()
		},
		telemetrytest.WithReader(func(driver telemetry.Lifecycle) telemetrytest.Reader {
			return driver.(*memorytelemetry.Driver)
		}),
	)
}
