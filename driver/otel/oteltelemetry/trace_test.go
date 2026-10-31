package oteltelemetry_test

import (
	"context"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/driver/otel/oteltelemetry"

	lognoop "go.opentelemetry.io/otel/log/noop"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// startObservedTelemetry starts an OTEL-backed facade and an independently
// owned passive memory observer. Synchronous native export makes observations
// comparable after End without polling, sleeps, or private driver access.
func startObservedTelemetry(t *testing.T) (
	*telemetry.Telemetry, *sdktrace.TracerProvider, *tracetest.InMemoryExporter, *memorytelemetry.Driver,
) {
	t.Helper()

	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("native provider Shutdown: %v", err)
		}
	})

	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
		instrumentation.TracerProvider(provider)
		instrumentation.MeterProvider(metricnoop.NewMeterProvider())
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
			t.Errorf("telemetry Stop: %v", err)
		}
	})

	capture := memorytelemetry.New()

	if err := capture.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := capture.Stop(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("observer Stop: %v", err)
		}
	})

	t.Cleanup(service.Observe(capture))

	return service, provider, exporter, capture
}

// requireObservedParent compares both public outputs with the chosen native
// parent, including metadata and child identity. Each test ends only its facade
// child before calling this assertion, so exactly one span must be present.
// SDK exports can retain invalid input metadata for roots; validity determines
// parent absence there, while the facade contract requires a completely zero
// parent. Valid parent metadata must match exactly in both outputs.
func requireObservedParent(
	t *testing.T, capture *memorytelemetry.Driver, exporter *tracetest.InMemoryExporter, parent trace.SpanContext,
) {
	t.Helper()

	observed := capture.Spans()
	exported := exporter.GetSpans()

	if len(observed) != 1 || len(exported) != 1 {
		t.Fatalf("observed spans = %d, exported spans = %d; want one each", len(observed), len(exported))
	}

	if exported[0].Parent.IsValid() != parent.IsValid() {
		t.Fatalf("SDK parent validity = %v, want %v", exported[0].Parent.IsValid(), parent.IsValid())
	}

	if parent.IsValid() && !exported[0].Parent.Equal(parent) {
		t.Fatalf("SDK parent = %v, want %v", exported[0].Parent, parent)
	}

	child := exported[0].SpanContext
	wantChild := telemetry.SpanContext{
		TraceID: telemetry.TraceID(child.TraceID()), SpanID: telemetry.SpanID(child.SpanID()),
		Sampled: child.IsSampled(), Remote: child.IsRemote(), TraceState: child.TraceState().String(),
	}

	if observed[0].Context != wantChild {
		t.Fatalf("diagnostic child = %+v, SDK child = %+v", observed[0].Context, wantChild)
	}

	wantParent := telemetry.SpanContext{
		TraceID: telemetry.TraceID(parent.TraceID()), SpanID: telemetry.SpanID(parent.SpanID()),
		Sampled: parent.IsSampled(), Remote: parent.IsRemote(), TraceState: parent.TraceState().String(),
	}

	if observed[0].Parent != wantParent {
		t.Fatalf("diagnostic parent = %+v, SDK parent = %+v", observed[0].Parent, wantParent)
	}
}

// TestObserveReportsNativeOnlyParent reproduces the case where SDK parentage
// exists without any facade span in the incoming context. Diagnostics must not
// misreport the real child as a root or replace its SDK-selected identity.
func TestObserveReportsNativeOnlyParent(t *testing.T) {
	t.Parallel()

	service, provider, exporter, capture := startObservedTelemetry(t)
	ctx, parent := provider.Tracer("native").Start(t.Context(), "native operation")
	t.Cleanup(func() {
		parent.End()
	})

	_, span := service.Tracer("example.com/billing").Start(ctx, "cache get")
	span.End()

	requireObservedParent(t, capture, exporter, parent.SpanContext())
}

// TestObservePrefersNativeParentOverConflictingFacadeParent checks that adding
// an unrelated adopted facade identity cannot replace the native SDK parent,
// either in backend export or in the passive diagnostic observer.
func TestObservePrefersNativeParentOverConflictingFacadeParent(t *testing.T) {
	t.Parallel()

	service, provider, exporter, capture := startObservedTelemetry(t)
	ctx, parent := provider.Tracer("native").Start(t.Context(), "native operation")
	t.Cleanup(func() {
		parent.End()
	})

	ctx = telemetry.ContextWithSpanContext(ctx, telemetry.SpanContext{
		TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2},
		Sampled: false, Remote: true, TraceState: "facade=conflicting",
	})

	_, span := service.Tracer("example.com/billing").Start(ctx, "cache get")
	span.End()

	requireObservedParent(t, capture, exporter, parent.SpanContext())
}

// TestObserveReportsRootWithoutParent distinguishes an absent parent from the
// newly generated child identity. Both diagnostics and the SDK export must keep
// the parent completely zero while still agreeing on the real child.
func TestObserveReportsRootWithoutParent(t *testing.T) {
	t.Parallel()

	service, _, exporter, capture := startObservedTelemetry(t)

	_, span := service.Tracer("example.com/billing").Start(t.Context(), "cache get")
	span.End()

	requireObservedParent(t, capture, exporter, trace.SpanContext{})
}

// TestObserveTreatsInvalidNativeParentAsRoot ensures invalid incoming IDs do
// not leak remote, sampling, or tracestate metadata into a diagnostic root.
// Native SDK root selection ignores that unusable identity for child creation,
// even though its exported raw Parent can retain the invalid input metadata.
func TestObserveTreatsInvalidNativeParentAsRoot(t *testing.T) {
	t.Parallel()

	service, _, exporter, capture := startObservedTelemetry(t)
	state, err := trace.ParseTraceState("vendor=invalid")

	if err != nil {
		t.Fatal(err)
	}

	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, TraceFlags: trace.FlagsSampled,
		Remote: true, TraceState: state,
	}))

	_, span := service.Tracer("example.com/billing").Start(ctx, "cache get")
	span.End()

	requireObservedParent(t, capture, exporter, trace.SpanContext{})
}

// TestObserveAdoptsMemoryFacadeParent exercises cross-driver parenting using an
// actual memory-backed facade span, not a native span or a recorder substitute.
// The OTEL child and passive observer must adopt that facade-only identity.
func TestObserveAdoptsMemoryFacadeParent(t *testing.T) {
	t.Parallel()

	service, _, exporter, capture := startObservedTelemetry(t)
	memory := telemetry.New(memorytelemetry.New())

	if err := memory.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := memory.Stop(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("memory telemetry Stop: %v", err)
		}
	})

	ctx, parent := memory.Tracer("example.com/upstream").Start(t.Context(), "queue process")
	t.Cleanup(func() {
		parent.End()
	})

	_, span := service.Tracer("example.com/billing").Start(ctx, "cache get")
	span.End()

	requireObservedParent(t, capture, exporter, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID(parent.Context().TraceID), SpanID: trace.SpanID(parent.Context().SpanID),
		TraceFlags: trace.FlagsSampled,
	}))
}

// TestObserveAdoptsFacadeMetadataWhenNativeParentIsInvalid checks the fallback
// preserves the valid facade identity and its metadata instead of treating an
// invalid native context as higher priority or retaining its unrelated state.
func TestObserveAdoptsFacadeMetadataWhenNativeParentIsInvalid(t *testing.T) {
	t.Parallel()

	service, _, exporter, capture := startObservedTelemetry(t)
	state, err := trace.ParseTraceState("facade=selected")

	if err != nil {
		t.Fatal(err)
	}

	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		Remote: true, TraceFlags: trace.FlagsSampled,
	}))
	ctx = telemetry.ContextWithSpanContext(ctx, telemetry.SpanContext{
		TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2},
		Sampled: true, Remote: true, TraceState: "facade=selected",
	})

	_, span := service.Tracer("example.com/billing").Start(ctx, "cache get")
	span.End()

	requireObservedParent(t, capture, exporter, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2},
		TraceFlags: trace.FlagsSampled, Remote: true, TraceState: state,
	}))
}

// TestObservePreservesNonRecordingRemoteParent checks a native propagated
// identity needs no recording span to win over a conflicting facade parent.
// Its sampled flag, remote bit, and tracestate must survive into diagnostics.
func TestObservePreservesNonRecordingRemoteParent(t *testing.T) {
	t.Parallel()

	service, _, exporter, capture := startObservedTelemetry(t)
	state, err := trace.ParseTraceState("vendor=remote,other=retained")

	if err != nil {
		t.Fatal(err)
	}

	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{3}, SpanID: trace.SpanID{4},
		TraceFlags: trace.FlagsSampled, Remote: true, TraceState: state,
	})

	ctx := trace.ContextWithSpanContext(t.Context(), parent)
	ctx = telemetry.ContextWithSpanContext(ctx, telemetry.SpanContext{
		TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2}, TraceState: "facade=ignored",
	})

	_, span := service.Tracer("example.com/billing").Start(ctx, "cache get")
	span.End()

	requireObservedParent(t, capture, exporter, parent)
}

// TestObservePreservesUnsampledNativeParent keeps the incoming unsampled flag
// separate from the child's AlwaysSample decision. The non-recording local
// parent remains unsampled and retains its own tracestate in both outputs.
func TestObservePreservesUnsampledNativeParent(t *testing.T) {
	t.Parallel()

	service, _, exporter, capture := startObservedTelemetry(t)
	state, err := trace.ParseTraceState("vendor=unsampled")

	if err != nil {
		t.Fatal(err)
	}

	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{5}, SpanID: trace.SpanID{6}, TraceState: state,
	})

	ctx := trace.ContextWithSpanContext(t.Context(), parent)

	_, span := service.Tracer("example.com/billing").Start(ctx, "cache get")
	span.End()

	requireObservedParent(t, capture, exporter, parent)
}

// TestFacadeSpanRetainsNativeChildAndPropagation checks that reading the chosen
// parent does not replace the SDK's returned child context. Native descendant
// instrumentation and injected headers must still use that child, not its parent
// or an unrelated facade identity carried into Start.
func TestFacadeSpanRetainsNativeChildAndPropagation(t *testing.T) {
	t.Parallel()

	service, provider, exporter, capture := startObservedTelemetry(t)
	ctx, parent := provider.Tracer("native").Start(t.Context(), "native operation")
	t.Cleanup(func() {
		parent.End()
	})

	ctx = telemetry.ContextWithSpanContext(ctx, telemetry.SpanContext{
		TraceID: telemetry.TraceID{1}, SpanID: telemetry.SpanID{2},
	})

	ctx, span := service.Tracer("example.com/billing").Start(ctx, "cache get")
	span.End()

	requireObservedParent(t, capture, exporter, parent.SpanContext())

	child := exporter.GetSpans()[0].SpanContext

	if !trace.SpanContextFromContext(ctx).Equal(child) {
		t.Fatal("facade did not retain the native child in the returned context")
	}

	carrier := telemetry.MapCarrier{}
	service.Inject(ctx, carrier)
	propagated := (propagation.TraceContext{}).Extract(t.Context(), propagation.MapCarrier(carrier))

	if !trace.SpanContextFromContext(propagated).WithRemote(false).Equal(child) {
		t.Fatalf("injected context differs from the child: %v", carrier)
	}

	_, descendant := provider.Tracer("native").Start(ctx, "native descendant")
	descendant.End()

	exported := exporter.GetSpans()

	if len(exported) != 2 || !exported[1].Parent.Equal(child) {
		t.Fatalf("native descendant did not use the facade's native child: %+v", exported)
	}
}

// TestSpanRecorderBeforeStartHasNoParent checks the mandatory recorder contract
// at the raw driver seam: an inactive operation leaves ctx unchanged and reports
// no new child or parent, even when a valid native parent was supplied.
func TestSpanRecorderBeforeStartHasNoParent(t *testing.T) {
	t.Parallel()

	driver := oteltelemetry.New()
	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
	}))

	returned, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})

	if returned != ctx || recorder.Context() != (telemetry.SpanContext{}) {
		t.Fatal("inactive driver changed the context or created a child")
	}

	if recorder.Parent() != (telemetry.SpanContext{}) {
		t.Fatalf("inactive parent = %+v, want zero", recorder.Parent())
	}

	recorder.End()
}

// TestSpanRecorderAfterStopHasNoParent checks the same no-op contract after
// lifecycle termination without creating exporters or opening a connection.
// Stop before Start is supported and must not adopt an incoming native parent.
func TestSpanRecorderAfterStopHasNoParent(t *testing.T) {
	t.Parallel()

	driver := oteltelemetry.New()

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled,
	}))

	returned, recorder := driver.StartSpan(ctx, telemetry.SpanStart{Name: "cache get"})

	if returned != ctx || recorder.Context() != (telemetry.SpanContext{}) {
		t.Fatal("stopped driver changed the context or created a child")
	}

	if recorder.Parent() != (telemetry.SpanContext{}) {
		t.Fatalf("stopped parent = %+v, want zero", recorder.Parent())
	}

	recorder.End()
}
