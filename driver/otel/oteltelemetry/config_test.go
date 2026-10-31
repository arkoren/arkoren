package oteltelemetry

import (
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestBuilderCallbacksShareOneDraft verifies ordered, once-only execution and
// replacement without replaying policies during publication.
func TestBuilderCallbacksShareOneDraft(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	driver := New(func(instrumentation *Builder) {
		calls++
		retained = instrumentation
		instrumentation.Endpoint("first")
	}, func(instrumentation *Builder) {
		calls++

		if instrumentation != retained || instrumentation.config.Endpoint != "first" {
			t.Fatal("callbacks did not share the ordered draft")
		}

		instrumentation.Endpoint("final")
	})

	if calls != 2 || driver.config.Endpoint != "final" {
		t.Fatalf("calls = %d, endpoint = %q", calls, driver.config.Endpoint)
	}

	retained.Endpoint("later")

	if driver.config.Endpoint != "final" {
		t.Fatal("retained root changed driver")
	}
}

// TestBuilderCopiesHeadersAtBothBoundaries checks input capture separately
// from publication, including direct mutation of the retained draft's map.
func TestBuilderCopiesHeadersAtBothBoundaries(t *testing.T) {
	t.Parallel()

	headers := map[string]string{"X-Tenant": "original"}
	var retained *Builder
	driver := New(func(instrumentation *Builder) {
		retained = instrumentation
		instrumentation.Headers(headers)
		headers["X-Tenant"] = "input-mutated"
	})

	if driver.config.Headers["X-Tenant"] != "original" {
		t.Fatal("input headers were retained")
	}

	retained.config.Headers["X-Tenant"] = "draft-mutated"

	if driver.config.Headers["X-Tenant"] != "original" {
		t.Fatal("draft headers were published without copying")
	}
}

// TestBuilderDefaultsAndExplicitOff separates recommended initial booleans
// from caller edits; publication must not restore disabled instrumentation.
func TestBuilderDefaultsAndExplicitOff(t *testing.T) {
	t.Parallel()

	defaults := New()

	if !defaults.config.RuntimeMetrics || !defaults.config.GlobalErrorHandler {
		t.Fatal("recommended defaults are disabled")
	}

	driver := New(func(instrumentation *Builder) {
		instrumentation.RuntimeMetrics(false)
		instrumentation.GlobalErrorHandler(false)
	})

	if driver.config.RuntimeMetrics || driver.config.GlobalErrorHandler {
		t.Fatal("explicit false was overwritten")
	}
}

// TestBuilderEmptyExpansionsUseFreshDefaults verifies zero edits for expanded
// nil and empty callback slices without sharing a mutable root across calls.
func TestBuilderEmptyExpansionsUseFreshDefaults(t *testing.T) {
	t.Parallel()

	var callbacks []func(*Builder)
	first := New(callbacks...)
	second := New([]func(*Builder){}...)
	first.config.Endpoint = "changed"

	if second.config.Endpoint != "" || !second.config.RuntimeMetrics {
		t.Fatal("zero-callback constructors did not publish fresh defaults")
	}
}

// TestBuilderNilCallbackPanics distinguishes misuse from a zero-callback
// expansion; callback invocation must not filter nil entries.
func TestBuilderNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()

	New(nil)
}

// TestBuilderCallbackPanicPropagates checks application failures are not caught
// or converted into configuration errors during direct callback execution.
func TestBuilderCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if recovered := recover(); recovered != "policy failed" {
			t.Fatalf("panic = %v, want policy failed", recovered)
		}
	}()

	New(func(instrumentation *Builder) {
		instrumentation.Endpoint("unused")
		panic("policy failed")
	})
}

// TestBuilderNilNativeSettingsRestoreFallbacks verifies nil SDK settings clear
// earlier edits without being treated as nil construction callbacks.
func TestBuilderNilNativeSettingsRestoreFallbacks(t *testing.T) {
	t.Parallel()

	driver := New(func(instrumentation *Builder) {
		instrumentation.TracerProvider(sdktrace.NewTracerProvider())
		instrumentation.Headers(map[string]string{"X-Tenant": "temporary"})
	}, func(instrumentation *Builder) {
		instrumentation.TracerProvider(nil)
		instrumentation.Headers(nil)
	})

	if driver.config.TracerProvider != nil || driver.config.Headers != nil {
		t.Fatal("nil native settings did not clear previous edits")
	}
}

// TestBuilderNativeHandlesRemainShared verifies native providers and hooks are
// retained, not invoked or recursively cloned during construction.
func TestBuilderNativeHandlesRemainShared(t *testing.T) {
	t.Parallel()

	traces := sdktrace.NewTracerProvider()
	metrics := sdkmetric.NewMeterProvider()
	logs := sdklog.NewLoggerProvider()
	propagator := &propagation.TraceContext{}
	sampler := sdktrace.AlwaysSample()
	driver := New(func(instrumentation *Builder) {
		instrumentation.TracerProvider(traces)
		instrumentation.MeterProvider(metrics)
		instrumentation.LoggerProvider(logs)
		instrumentation.Propagator(propagator)
		instrumentation.Sampler(sampler)
	})

	if driver.config.TracerProvider != traces || driver.config.MeterProvider != metrics {
		t.Fatal("native trace or meter provider was replaced")
	}

	if driver.config.LoggerProvider != logs || driver.config.Propagator != propagator {
		t.Fatal("native logger provider or propagator was replaced")
	}

	if driver.config.Sampler != sampler {
		t.Fatal("sampler was replaced")
	}
}

// TestBuilderSettingsReturnReceiver checks fluent edits stay on one draft.
// Methods neither validate early nor return a published driver.
func TestBuilderSettingsReturnReceiver(t *testing.T) {
	t.Parallel()

	telemetry := &Builder{}
	results := []*Builder{
		telemetry.Endpoint(""), telemetry.Protocol(""), telemetry.Insecure(false),
		telemetry.Headers(nil), telemetry.Sampler(nil), telemetry.TracerProvider(nil),
		telemetry.MeterProvider(nil), telemetry.LoggerProvider(nil), telemetry.Propagator(nil),
		telemetry.RuntimeMetrics(false), telemetry.GlobalErrorHandler(false),
	}

	for _, result := range results {
		if result != telemetry {
			t.Fatal("setting did not return the same receiver")
		}
	}
}
