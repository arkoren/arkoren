package sdk

import (
	"errors"
	"reflect"

	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Config is the completed pipeline input. Containers are driver-owned snapshots;
// native providers, the sampler, and the propagator remain shared handles.
type Config struct {
	// Endpoint overrides the native collector environment chain when nonempty.
	Endpoint string

	// Protocol selects an OTLP transport, or defers to the environment when empty.
	Protocol string

	// Insecure disables collector TLS when true.
	Insecure bool

	// Headers overrides native exporter headers when nonempty.
	Headers map[string]string

	// Sampler selects sampling for owned traces only.
	Sampler sdktrace.Sampler

	// TracerProvider is borrowed and is never shut down by the pipeline.
	TracerProvider trace.TracerProvider

	// MeterProvider is borrowed and is never shut down by the pipeline.
	MeterProvider metric.MeterProvider

	// LoggerProvider is borrowed and is never shut down by the pipeline.
	LoggerProvider log.LoggerProvider

	// Propagator replaces the default W3C trace context and baggage propagator.
	Propagator propagation.TextMapPropagator

	// RuntimeMetrics enables driver-owned runtime callback registrations.
	RuntimeMetrics bool

	// GlobalErrorHandler installs process-wide SDK error routing at startup.
	GlobalErrorHandler bool
}

// Validate rejects typed-nil native pointers before a provider, sampler, or
// propagator can be used. A borrowed tracer ignores Sampler, including invalid
// unused values. Nil interfaces keep their documented fallback; live native
// handles are retained unchanged, not cloned or recursively inspected.
func (config Config) Validate() error {
	handles := []any{
		config.TracerProvider, config.MeterProvider, config.LoggerProvider,
		config.Propagator,
	}

	if config.TracerProvider == nil {
		handles = append(handles, config.Sampler)
	}

	for _, handle := range handles {
		if handle == nil {
			continue
		}

		value := reflect.ValueOf(handle)

		if value.Kind() == reflect.Pointer && value.IsNil() {
			return errors.New("otel telemetry: typed-nil native provider, sampler, or propagator")
		}
	}

	return nil
}
