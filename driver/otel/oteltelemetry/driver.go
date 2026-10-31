package oteltelemetry

import (
	"sync"
	"sync/atomic"

	"arkoren.dev/driver/otel/oteltelemetry/internal/sdk"
)

// Driver is the OpenTelemetry implementation of the telemetry TraceDriver,
// MetricDriver, and LogDriver. Create it with [New]; it does nothing until the
// application starts it.
//
// Without construction callbacks, Start uses the standard variables:
//
//   - OTEL_SDK_DISABLED=true turns every signal into a no-op.
//   - OTEL_EXPORTER_OTLP_PROTOCOL (and the _TRACES_, _METRICS_, _LOGS_
//     variants) selects "grpc" or "http/protobuf", the default.
//   - OTEL_EXPORTER_OTLP_ENDPOINT, _HEADERS, _INSECURE, _TIMEOUT, _CERTIFICATE
//     and their per-signal variants are read by the OTLP exporters.
//   - OTEL_TRACES_EXPORTER, OTEL_METRICS_EXPORTER, and OTEL_LOGS_EXPORTER
//     accept "otlp" (default) or "none". Other values, such as "console",
//     fail Start.
//   - OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES describe the process.
//     Values in the telemetry Setup, when non-empty, win over them.
//   - OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG choose the sampler.
//
// The driver shuts down only what it created. Providers passed through
// [Builder.TracerProvider], [Builder.MeterProvider], and [Builder.LoggerProvider] belong to
// the caller: Stop flushes them when they support it and leaves them
// running. The driver never calls [go.opentelemetry.io/otel.SetTracerProvider]
// or the other global setters. The one global it touches is the SDK error handler, see
// [Builder.GlobalErrorHandler].
//
// A Driver is safe for concurrent use.
//
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.GlobalErrorHandler(false)
//	})
//	service := telemetry.New(driver)
//	_ = service
type Driver struct {
	// config is the configuration given to New.
	config settings

	// mutex serializes Start and Stop.
	mutex sync.Mutex

	// phase is the lifecycle phase, guarded by mutex.
	phase phase

	// state holds the running signals, or nil while the driver is inactive.
	state atomic.Pointer[sdk.State]
}

// New returns a Driver that applies configure. It only stores the configuration; no
// exporter is created and nothing is validated until Start.
// Callbacks run directly once, synchronously in order on one fresh draft. Nil
// entries panic; nil or empty expansions supply no edits. Recommended runtime
// metrics and global error handling are enabled before callbacks, so false wins.
// Headers are copied on input and publication; native providers remain shared.
//
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.Endpoint("collector:4317")
//		instrumentation.Protocol(oteltelemetry.ProtocolGRPC)
//		instrumentation.Insecure(true)
//	})
//	service := telemetry.New(driver)
//	_ = service
func New(configure ...BuilderFunc) *Driver {
	return &Driver{
		config: newConfig(configure...),
	}
}
