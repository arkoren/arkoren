package sdk

import (
	"context"

	"go.opentelemetry.io/otel/metric"
)

// runtimeMeterProvider intercepts only callback registration performed by the
// native runtime instrumentation during Start. All instruments and provider
// ownership remain native; registrations belong to this driver's running state.
type runtimeMeterProvider struct {
	// MeterProvider supplies the caller's or driver's native instruments.
	metric.MeterProvider

	// running records callback releases synchronously during construction only.
	running *State
}

// Meter wraps the native meter solely to retain runtime callback releases.
// The wrapper is private to runtime.Start and is never given to application code.
func (runtimeMeterProvider runtimeMeterProvider) Meter(name string, options ...metric.MeterOption) metric.Meter {
	return runtimeMeter{
		Meter:   runtimeMeterProvider.MeterProvider.Meter(name, options...),
		running: runtimeMeterProvider.running,
	}
}

// runtimeMeter retains the registration handles discarded by runtime.Start.
// Embedding the native meter forwards the rest of the SDK without redefining
// capabilities or changing instruments returned to the runtime package.
type runtimeMeter struct {
	// Meter forwards native instrument creation unchanged.
	metric.Meter

	// running owns the registered callbacks, not the native meter provider.
	running *State
}

// RegisterCallback registers through the SDK and acquires its matching release.
// Stop and failed-start rollback unregister it before flushing the provider;
// cancellation never prevents release of this driver-owned registration.
func (runtimeMeter runtimeMeter) RegisterCallback(
	callback metric.Callback, instruments ...metric.Observable,
) (metric.Registration, error) {
	registration, err := runtimeMeter.Meter.RegisterCallback(callback, instruments...)

	if err != nil {
		return nil, err
	}

	runtimeMeter.running.stops = append(runtimeMeter.running.stops, func(context.Context) error {
		return registration.Unregister()
	})

	return registration, nil
}
