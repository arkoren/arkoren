package oteltelemetry_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/driver/otel/oteltelemetry"

	"go.opentelemetry.io/otel/log"
	lognoop "go.opentelemetry.io/otel/log/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Example demonstrates native ownership and the exported driver operations
// using in-process/no-op SDK providers rather than a network collector.
func Example() {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	policy := oteltelemetry.BuilderFunc(func(instrumentation *oteltelemetry.Builder) {
		instrumentation.MeterProvider(provider)
		instrumentation.TracerProvider(tracenoop.NewTracerProvider())
		instrumentation.LoggerProvider(lognoop.NewLoggerProvider())
		instrumentation.RuntimeMetrics(false)
		instrumentation.GlobalErrorHandler(false)
	})

	driver := oteltelemetry.New(policy)

	if err := driver.Start(ctx, telemetry.Setup{}); err != nil {
		panic(err)
	}

	ctx, span := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
	carrier := telemetry.MapCarrier{}
	driver.Inject(ctx, carrier)
	_, parent := driver.Extract(ctx, carrier)
	fmt.Println(parent.IsValid(), driver.Enabled(ctx, slog.LevelInfo))
	span.End()

	if err := driver.Export(ctx, slog.NewRecord(time.Unix(1, 0), slog.LevelInfo, "completed", 0)); err != nil {
		panic(err)
	}

	recorder, err := driver.Instrument(telemetry.InstrumentSpec{Name: "jobs", Kind: telemetry.CounterKind})

	if err != nil {
		panic(err)
	}

	recorder.Record(ctx, 1, nil)

	if err := driver.Stop(ctx); err != nil {
		panic(err)
	}

	var collected metricdata.ResourceMetrics

	if err := reader.Collect(ctx, &collected); err != nil {
		panic(err)
	}

	fmt.Println(len(collected.ScopeMetrics)) // The borrowed provider still works.

	if err := provider.Shutdown(ctx); err != nil {
		panic(err)
	}
	// Output:
	// false false
	// 1
}

// TestStopCanceledContextReleasesOwnedPipelines observes final export through
// the public OTLP HTTP seam, not private provider state. Both native batch
// processors must be shut down even when the caller's flush budget is exhausted.
func TestStopCanceledContextReleasesOwnedPipelines(t *testing.T) {
	for _, name := range []string{"OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER", "OTEL_LOGS_EXPORTER"} {
		t.Setenv(name, "otlp")
	}

	t.Setenv("OTEL_SDK_DISABLED", "false")
	var paths sync.Map
	collector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths.Store(request.URL.Path, true)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
		instrumentation.Endpoint(collector.URL)
		instrumentation.Protocol(oteltelemetry.ProtocolHTTP)
		instrumentation.RuntimeMetrics(false)
		instrumentation.GlobalErrorHandler(false)
	})

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	_, span := driver.StartSpan(t.Context(), telemetry.SpanStart{Name: "work run"})
	span.End()

	if err := driver.Export(t.Context(), slog.NewRecord(time.Unix(1, 0), slog.LevelInfo, "completed", 0)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop = %v, want cancellation identity", err)
	}

	for _, path := range []string{"/v1/traces", "/v1/logs"} {
		if _, ok := paths.Load(path); !ok {
			t.Fatalf("owned pipeline did not complete teardown export to %s", path)
		}
	}
}

// TestStopUnregistersRuntimeMetricsFromBorrowedProvider checks default runtime
// instrumentation stops while the caller's native meter remains usable.
func TestStopUnregistersRuntimeMetricsFromBorrowedProvider(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.WithoutCancel(t.Context()))
	})

	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
		instrumentation.MeterProvider(provider)
		instrumentation.TracerProvider(tracenoop.NewTracerProvider())
		instrumentation.LoggerProvider(lognoop.NewLoggerProvider())
		instrumentation.GlobalErrorHandler(false)
	})

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	var before metricdata.ResourceMetrics

	if err := reader.Collect(t.Context(), &before); err != nil {
		t.Fatal(err)
	}

	if len(before.ScopeMetrics) == 0 {
		t.Fatal("recommended runtime metrics were not registered")
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	var after metricdata.ResourceMetrics

	if err := reader.Collect(t.Context(), &after); err != nil {
		t.Fatalf("borrowed provider was stopped: %v", err)
	}

	if len(after.ScopeMetrics) != 0 {
		t.Fatalf("runtime metrics still collected after Stop: %+v", after.ScopeMetrics)
	}
}

// TestStartCanceledContextLeavesDriverRetryable requires native-provider-only
// startup to honor cancellation too, without consuming the driver's lifecycle.
func TestStartCanceledContextLeavesDriverRetryable(t *testing.T) {
	t.Parallel()

	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
		instrumentation.MeterProvider(sdkmetric.NewMeterProvider())
		instrumentation.TracerProvider(tracenoop.NewTracerProvider())
		instrumentation.LoggerProvider(lognoop.NewLoggerProvider())
		instrumentation.RuntimeMetrics(false)
		instrumentation.GlobalErrorHandler(false)
	})

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Start(ctx, telemetry.Setup{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want context.Canceled", err)
	}

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatalf("retry after canceled startup: %v", err)
	}
}

// TestStartRejectsTypedNilNativeProvider rejects a malformed borrowed pointer
// before it can be published and later dereferenced by tracing or shutdown.
func TestStartRejectsTypedNilNativeProvider(t *testing.T) {
	t.Parallel()

	var provider *sdktrace.TracerProvider
	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
		instrumentation.TracerProvider(provider)
		instrumentation.MeterProvider(sdkmetric.NewMeterProvider())
		instrumentation.LoggerProvider(lognoop.NewLoggerProvider())
		instrumentation.RuntimeMetrics(false)
		instrumentation.GlobalErrorHandler(false)
	})

	if err := driver.Start(t.Context(), telemetry.Setup{}); err == nil {
		t.Fatal("Start accepted a typed-nil tracer provider")
	}
}

// cancelingLoggerProvider is a native SDK boundary substitute that cancels the
// startup context after meter instrumentation has already acquired callbacks.
type cancelingLoggerProvider struct {
	// LoggerProvider supplies an otherwise ordinary no-op native logger.
	log.LoggerProvider
	// cancel ends only the test's first startup attempt.
	cancel context.CancelFunc
}

// Logger introduces deterministic cancellation at the final native setup step;
// it has no clock, network dependency, or access to driver internals.
func (cancelingLoggerProvider cancelingLoggerProvider) Logger(name string, options ...log.LoggerOption) log.Logger {
	cancelingLoggerProvider.cancel()

	return cancelingLoggerProvider.LoggerProvider.Logger(name, options...)
}

// TestCanceledStartupUnregistersAcquiredRuntimeCallbacks checks rollback at a
// native hook boundary, including retryability and borrowed-provider ownership.
func TestCanceledStartupUnregistersAcquiredRuntimeCallbacks(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.WithoutCancel(t.Context()))
	})

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
		instrumentation.MeterProvider(provider)
		instrumentation.TracerProvider(tracenoop.NewTracerProvider())
		instrumentation.LoggerProvider(cancelingLoggerProvider{LoggerProvider: lognoop.NewLoggerProvider(), cancel: cancel})
		instrumentation.GlobalErrorHandler(false)
	})

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	if err := driver.Start(ctx, telemetry.Setup{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want cancellation from native setup", err)
	}

	var collected metricdata.ResourceMetrics

	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("rollback stopped the borrowed provider: %v", err)
	}

	if len(collected.ScopeMetrics) != 0 {
		t.Fatal("failed startup retained runtime callbacks")
	}

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

// TestBorrowedTracerIgnoresUnusedSamplerAndConnectionSettings preserves native
// applicability: connection settings and the sampler cannot affect supplied
// providers, even when those unused settings would be invalid for owned ones.
func TestBorrowedTracerIgnoresUnusedSamplerAndConnectionSettings(t *testing.T) {
	t.Parallel()

	var unused *struct {
		// Sampler is deliberately unavailable and must never be used.
		sdktrace.Sampler
	}

	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
		instrumentation.TracerProvider(tracenoop.NewTracerProvider())
		instrumentation.MeterProvider(sdkmetric.NewMeterProvider())
		instrumentation.LoggerProvider(lognoop.NewLoggerProvider())
		instrumentation.Endpoint("not/a/collector")
		instrumentation.Protocol("unsupported")
		instrumentation.Sampler(unused)
		instrumentation.RuntimeMetrics(false)
		instrumentation.GlobalErrorHandler(false)
	})

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatalf("unused owned-provider settings were applied: %v", err)
	}
}
