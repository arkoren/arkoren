// Package sdk owns native pipeline acquisition, configuration, and cleanup.
package sdk

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"arkoren.dev/core/service/telemetry"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
)

// instrumentationScope is the scope of the logger the driver emits log
// records through.
const instrumentationScope = "arkoren.dev/core/service/telemetry"

// State holds a completed pipeline. Disabled signals have nil native handles.
// Cleanup and the tracer cache belong to this pipeline, not its caller.
type State struct {
	// Propagator reads and writes trace context.
	Propagator propagation.TextMapPropagator

	// TracerProvider creates the tracers.
	TracerProvider trace.TracerProvider

	// tracers caches a trace.Tracer per scope.
	tracers sync.Map

	// MeterProvider creates the instruments.
	MeterProvider metric.MeterProvider

	// Logger emits log records.
	Logger log.Logger

	// stops flush or shut down the providers in creation order.
	stops []func(ctx context.Context) error
}

// Build creates unpublished signals in trace, metric, then log order. A native
// setup failure or cancellation rolls back every acquired release in reverse
// order; the caller publishes running state only after the whole build succeeds.
func Build(ctx context.Context, config Config, setup telemetry.Setup) (*State, error) {
	run := &State{
		Propagator: config.Propagator,
	}

	run.Propagator = cmp.Or[propagation.TextMapPropagator](run.Propagator, defaultPropagator())

	if sdkDisabled() {
		return run, nil
	}

	if config.GlobalErrorHandler {
		installErrorHandler(setup.Logger)
	}

	res, err := NewResource(ctx, setup.Resource)

	if err != nil {
		return nil, err
	}

	steps := []func(context.Context, Config, *resource.Resource) error{run.startTraces, run.startMetrics, run.startLogs}

	for _, step := range steps {
		if err := errors.Join(step(ctx, config, res), ctx.Err()); err != nil {
			return nil, errors.Join(err, run.Shutdown(ctx))
		}
	}

	return run, nil
}

// defaultPropagator returns W3C Trace Context together with W3C Baggage.
func defaultPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
}

// installErrorHandler routes SDK errors to logger, or discards them when
// logger is nil.
func installErrorHandler(logger *slog.Logger) {
	logger = cmp.Or(logger, slog.New(slog.DiscardHandler))

	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.ErrorContext(context.Background(), "OpenTelemetry error", slog.Any("error", err))
	}))
}

// startTraces borrows a supplied provider or creates an owned OTLP pipeline.
// Environment exporter/protocol validation applies only to the owned path.
// Every successful acquisition records its matching flush or teardown action.
func (state *State) startTraces(ctx context.Context, config Config, res *resource.Resource) error {
	if config.TracerProvider != nil {
		state.TracerProvider = config.TracerProvider
		state.addFlush(config.TracerProvider)

		return nil
	}

	enabled, err := exporterEnabled(envTracesExporter)

	if err != nil || !enabled {
		return err
	}

	protocol, err := ResolveProtocol(config, envTracesProtocol)

	if err != nil {
		return err
	}

	exporter, err := newSpanExporter(ctx, config, protocol)

	if err != nil {
		return err
	}

	opts := []sdktrace.TracerProviderOption{sdktrace.WithBatcher(exporter), sdktrace.WithResource(res)}

	if config.Sampler != nil {
		opts = append(opts, sdktrace.WithSampler(config.Sampler))
	}

	provider := sdktrace.NewTracerProvider(opts...)
	state.TracerProvider = provider
	state.addOwnedStop(provider.ForceFlush, provider.Shutdown)

	return nil
}

// startMetrics acquires the selected native meter and optional runtime metrics.
// Runtime callback releases are retained independently of provider ownership,
// including when native instrumentation fails after partial registration.
func (state *State) startMetrics(ctx context.Context, config Config, res *resource.Resource) error {
	if err := state.setupMeterProvider(ctx, config, res); err != nil || state.MeterProvider == nil {
		return err
	}

	if !config.RuntimeMetrics {
		return nil
	}

	provider := runtimeMeterProvider{MeterProvider: state.MeterProvider, running: state}

	if err := runtime.Start(runtime.WithMeterProvider(provider)); err != nil {
		return fmt.Errorf("start runtime metrics: %w", err)
	}

	return nil
}

// setupMeterProvider borrows the caller's meter provider or builds an owned
// periodic OTLP pipeline. Native connection settings apply only to owned
// exporters; cleanup records preserve that distinction without cloning handles.
func (state *State) setupMeterProvider(ctx context.Context, config Config, res *resource.Resource) error {
	if config.MeterProvider != nil {
		state.MeterProvider = config.MeterProvider
		state.addFlush(config.MeterProvider)

		return nil
	}

	enabled, err := exporterEnabled(envMetricsExporter)

	if err != nil || !enabled {
		return err
	}

	protocol, err := ResolveProtocol(config, envMetricsProtocol)

	if err != nil {
		return err
	}

	exporter, err := newMetricExporter(ctx, config, protocol)

	if err != nil {
		return err
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
	)
	state.MeterProvider = provider
	state.addOwnedStop(provider.ForceFlush, provider.Shutdown)

	return nil
}

// startLogs borrows the supplied logger provider or creates an owned OTLP batch
// pipeline. Logger creation remains a native hook; no log records are generated
// here. Cleanup flushes borrowed providers without taking their ownership.
func (state *State) startLogs(ctx context.Context, config Config, res *resource.Resource) error {
	if config.LoggerProvider != nil {
		state.Logger = config.LoggerProvider.Logger(instrumentationScope)
		state.addFlush(config.LoggerProvider)

		return nil
	}

	enabled, err := exporterEnabled(envLogsExporter)

	if err != nil || !enabled {
		return err
	}

	protocol, err := ResolveProtocol(config, envLogsProtocol)

	if err != nil {
		return err
	}

	exporter, err := newLogExporter(ctx, config, protocol)

	if err != nil {
		return err
	}

	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	)
	state.Logger = provider.Logger(instrumentationScope)
	state.addOwnedStop(provider.ForceFlush, provider.Shutdown)

	return nil
}

// addFlush retains the native SDK's optional ForceFlush operation for a borrowed
// provider. Providers without it need no driver cleanup; their final shutdown
// remains with the caller, and their flush errors retain their original causes.
func (state *State) addFlush(provider any) {
	if flusher, ok := provider.(interface{ ForceFlush(context.Context) error }); ok {
		state.stops = append(state.stops, flusher.ForceFlush)
	}
}

// addOwnedStop separates the caller's flush budget from mandatory native
// teardown. SDK provider Shutdown can permanently skip processor cleanup when
// called with a canceled context, so it receives an uncanceled context after
// ForceFlush. Native shutdown may perform another final export and must finish;
// no goroutine is abandoned when the caller's deadline expires.
func (state *State) addOwnedStop(flush, stop func(context.Context) error) {
	state.stops = append(state.stops, func(ctx context.Context) error {
		return errors.Join(flush(ctx), stop(context.WithoutCancel(ctx)))
	})
}

// Shutdown executes every acquired release in reverse order, even after an
// earlier release fails or ctx expires. Individual owned actions separate flush
// cancellation from mandatory teardown; returned errors preserve every cause.
func (state *State) Shutdown(ctx context.Context) error {
	var errs []error

	for _, stop := range slices.Backward(state.stops) {
		errs = append(errs, stop(ctx))
	}

	return errors.Join(errs...)
}

// Tracer shares one native tracer per scope across concurrent driver calls.
// The running state owns this cache, so it cannot reuse providers across driver
// lifetimes. Native tracer creation remains synchronous and follows provider
// ownership.
func (state *State) Tracer(scope string) trace.Tracer {
	if tracer, ok := state.tracers.Load(scope); ok {
		return tracer.(trace.Tracer)
	}

	tracer, _ := state.tracers.LoadOrStore(scope, state.TracerProvider.Tracer(scope))

	return tracer.(trace.Tracer)
}
