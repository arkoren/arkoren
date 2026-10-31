package telemetrytest

import (
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest/internal/conformance"
)

// Run is the conformance suite every telemetry driver must pass. newDriver
// returns a fresh, unstarted driver for each subtest. The suite looks at the
// capabilities the driver implements ([telemetry.TraceDriver],
// [telemetry.MetricDriver], [telemetry.LogDriver]) and only runs the subtests
// of those, so a metrics-only driver such as Prometheus passes by being
// correct at what it does.
//
// It verifies the lifecycle rules of [telemetry.Lifecycle] on every driver;
// for traces, parenting, recorded span data, status rules, and propagation
// round trips; for metrics, declared keys and every instrument kind; and for
// logs, correlation and export. Subtests that inspect recorded content need a
// [Reader] ([WithReader], or the driver itself) and are skipped without one.
// Recorder Parent metadata is checked directly even without a Reader: actual
// selected parent identity, flags and trace state must be stable; roots and
// inactive recorders must report zero.
//
// A [telemetry.Composite] is not supported: run the suite on its parts.
//
//	func TestConformance(t *testing.T) {
//		telemetrytest.Run(t, func(t *testing.T) telemetry.Lifecycle {
//			return memorytelemetry.New()
//		})
//	}
func Run(t *testing.T, newDriver func(t *testing.T) telemetry.Lifecycle, opts ...Option) {
	t.Helper()

	var config suiteConfig

	for _, opt := range opts {
		opt(&config)
	}

	var reader func(telemetry.Lifecycle) conformance.Reader

	if config.reader != nil {
		reader = func(driver telemetry.Lifecycle) conformance.Reader {
			return config.reader(driver)
		}
	}

	conformance.Run(t, newDriver, reader)
}
