package oteltelemetry_test

import (
	"context"
	"reflect"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/driver/otel/oteltelemetry"

	"go.opentelemetry.io/otel/attribute"
	lognoop "go.opentelemetry.io/otel/log/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// TestMeterPreservesNULTuples protects the landed tuple-identity fix through a
// public Meter and actual native aggregation. No cache encoding is inspected.
func TestMeterPreservesNULTuples(t *testing.T) {
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
		instrumentation.RuntimeMetrics(false)
		instrumentation.GlobalErrorHandler(false)
	})

	service := telemetry.New(driver)

	if err := service.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = service.Stop(context.WithoutCancel(t.Context()))
	})

	counter := service.Meter("example.com/billing").Counter("tuple", telemetry.Keys("first", "second"))
	counter.Add(t.Context(), 7, telemetry.String("first", "a\x00b"), telemetry.String("second", "c"))
	counter.Add(t.Context(), 11, telemetry.String("first", "a"), telemetry.String("second", "b\x00c"))
	var collected metricdata.ResourceMetrics

	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}

	if len(collected.ScopeMetrics) != 1 {
		t.Fatalf("collected scopes = %+v", collected.ScopeMetrics)
	}

	sum := collected.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[float64])
	recorded := make(map[string]float64)

	for _, point := range sum.DataPoints {
		first, _ := point.Attributes.Value("first")
		recorded[first.AsString()] = point.Value
	}

	if recorded["a\x00b"] != 7 || recorded["a"] != 11 {
		t.Fatalf("distinct label tuples were merged: %v", recorded)
	}
}

// TestTupleIdentityOTELLabelsRemainDistinct observes native SDK collection,
// not the driver's attribute cache. NUL-bearing tuples must retain independent
// totals on cache hits, including tuples whose other label is empty and omitted
// from the native attribute set.
func TestTupleIdentityOTELLabelsRemainDistinct(t *testing.T) {
	t.Parallel()

	reader, service := startTupleIdentityMetrics(t)
	counter := service.Meter("checkout").Counter("requests", telemetry.Keys("region", "result"))
	counter.Add(t.Context(), 2, telemetry.String("region", "west\x00api"), telemetry.String("result", "ok"))
	counter.Add(t.Context(), 3, telemetry.String("region", "west"), telemetry.String("result", "api\x00ok"))
	counter.Add(t.Context(), 5, telemetry.String("region", "west\x00api"), telemetry.String("result", "ok"))
	counter.Add(t.Context(), 7, telemetry.String("region", "west"), telemetry.String("result", "api\x00ok"))
	counter.Add(t.Context(), 11, telemetry.String("region", "\x00"))
	counter.Add(t.Context(), 13, telemetry.String("result", "\x00"))
	counter.Add(t.Context(), 17, telemetry.String("region", "\x00"))

	first := attribute.NewSet(attribute.String("region", "west\x00api"), attribute.String("result", "ok"))
	second := attribute.NewSet(attribute.String("region", "west"), attribute.String("result", "api\x00ok"))
	regionOnly := attribute.NewSet(attribute.String("region", "\x00"))
	resultOnly := attribute.NewSet(attribute.String("result", "\x00"))
	want := map[attribute.Distinct]float64{
		first.Equivalent():      7,
		second.Equivalent():     10,
		regionOnly.Equivalent(): 28,
		resultOnly.Equivalent(): 13,
	}

	if got := collectTupleIdentitySums(t, reader); !reflect.DeepEqual(got, want) {
		t.Fatalf("exported counter sums = %v, want %v", got, want)
	}
}

// startTupleIdentityMetrics supplies only in-process SDK providers through the
// public Builder seam. Disabling runtime metrics isolates test recordings and
// prevents exporter network I/O. Cleanup stops the facade before shutting down
// the caller-owned meter provider; neither changes process-global providers.
func startTupleIdentityMetrics(t *testing.T) (*sdkmetric.ManualReader, *telemetry.Telemetry) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})

	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
		instrumentation.TracerProvider(tracenoop.NewTracerProvider())
		instrumentation.MeterProvider(provider)
		instrumentation.LoggerProvider(lognoop.NewLoggerProvider())
		instrumentation.RuntimeMetrics(false)
		instrumentation.GlobalErrorHandler(false)
	})

	service := telemetry.New(driver)

	if err := service.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := service.Stop(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})

	return reader, service
}

// collectTupleIdentitySums reads the sole test counter through the SDK reader.
// Attribute-set identities ignore SDK point ordering and timestamps without
// joining label bytes or depending on the driver's private tuple encoding.
func collectTupleIdentitySums(t *testing.T, reader *sdkmetric.ManualReader) map[attribute.Distinct]float64 {
	t.Helper()

	var collected metricdata.ResourceMetrics

	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}

	if len(collected.ScopeMetrics) != 1 {
		t.Fatalf("exported scopes = %d, want 1", len(collected.ScopeMetrics))
	}

	metrics := collected.ScopeMetrics[0].Metrics

	if len(metrics) != 1 {
		t.Fatalf("exported instruments = %d, want 1", len(metrics))
	}

	sum, ok := metrics[0].Data.(metricdata.Sum[float64])

	if !ok {
		t.Fatalf("aggregation = %T, want a float64 sum", metrics[0].Data)
	}

	values := make(map[attribute.Distinct]float64, len(sum.DataPoints))

	for _, point := range sum.DataPoints {
		values[point.Attributes.Equivalent()] = point.Value
	}

	return values
}
