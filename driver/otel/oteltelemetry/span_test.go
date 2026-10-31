package oteltelemetry_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/driver/otel/oteltelemetry"

	"go.opentelemetry.io/otel/attribute"
	lognoop "go.opentelemetry.io/otel/log/noop"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// ExampleDriver_StartSpan demonstrates the mandatory Parent result with a
// non-recording remote parent and an unsampled child. The native SDK owns child
// creation; the recorder reports the incoming parent's metadata independently.
// No exporter, network access, clock assertions, or random output is required.
func ExampleDriver_StartSpan() {
	ctx := context.Background()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
		instrumentation.TracerProvider(provider)
		instrumentation.MeterProvider(metricnoop.NewMeterProvider())
		instrumentation.LoggerProvider(lognoop.NewLoggerProvider())
		instrumentation.RuntimeMetrics(false)
		instrumentation.GlobalErrorHandler(false)
	})

	if err := driver.Start(ctx, telemetry.Setup{}); err != nil {
		panic(err)
	}

	state, err := trace.ParseTraceState("vendor=upstream")

	if err != nil {
		panic(err)
	}

	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
		Remote: true, TraceState: state,
	})

	ctx = trace.ContextWithSpanContext(ctx, parent)
	ctx, span := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})
	fmt.Println(span.Parent().TraceID == telemetry.TraceID(parent.TraceID()),
		span.Parent().SpanID == telemetry.SpanID(parent.SpanID()))
	fmt.Println(span.Parent().Sampled, span.Parent().Remote, span.Parent().TraceState)
	fmt.Println(span.Recording(), trace.SpanContextFromContext(ctx).TraceID() == parent.TraceID())
	span.End()

	if err := driver.Stop(ctx); err != nil {
		panic(err)
	}

	if err := provider.Shutdown(ctx); err != nil {
		panic(err)
	}
	// Output:
	// true true
	// true true vendor=upstream
	// false true
}

// TestSpanKeepsNativeParentAndExplicitError exercises native interoperation and
// the intentional privacy boundary: only the caller's explicit RecordError adds
// exception text; the adapter does not replace the parent or invent payload data.
func TestSpanKeepsNativeParentAndExplicitError(t *testing.T) {
	t.Parallel()

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.WithoutCancel(t.Context()))
	})

	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
		instrumentation.TracerProvider(provider)
		instrumentation.MeterProvider(sdkmetric.NewMeterProvider())
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

	ctx, parent := provider.Tracer("native").Start(t.Context(), "native operation")
	_, span := service.Tracer("example.com/billing").Start(ctx, "cache get")
	span.RecordError(errors.New("approved failure"))
	span.End()
	parent.End()
	spans := exporter.GetSpans()

	if len(spans) != 2 {
		t.Fatalf("exported spans = %d, want child and parent", len(spans))
	}

	child := spans[0]

	if child.Name != "cache get" || !child.Parent.Equal(parent.SpanContext()) {
		t.Fatalf("native parent or registered name changed: %+v", child)
	}

	if len(child.Events) != 1 || len(child.Attributes) != 0 {
		t.Fatalf("unexpected automatic payload attributes or events: %+v", child)
	}

	attributes := attribute.NewSet(child.Events[0].Attributes...)
	message, _ := attributes.Value("exception.message")

	if message.AsString() != "approved failure" {
		t.Fatalf("explicit exception text was lost: %+v", child.Events)
	}
}
