package sdk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/driver/otel/oteltelemetry"

	lognoop "go.opentelemetry.io/otel/log/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// TestMalformedEndpointDoesNotDiscloseCredentials checks validation errors do
// not echo a malformed URL, even when it contains user-supplied secret material.
func TestMalformedEndpointDoesNotDiscloseCredentials(t *testing.T) {
	t.Parallel()

	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
		instrumentation.Endpoint("https://user:private-token@[malformed")
		instrumentation.Protocol(oteltelemetry.ProtocolHTTP)
		instrumentation.MeterProvider(sdkmetric.NewMeterProvider())
		instrumentation.LoggerProvider(lognoop.NewLoggerProvider())
		instrumentation.RuntimeMetrics(false)
		instrumentation.GlobalErrorHandler(false)
	})

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	err := driver.Start(t.Context(), telemetry.Setup{})

	if err == nil {
		t.Fatal("Start accepted a malformed endpoint")
	}

	if strings.Contains(err.Error(), "private-token") {
		t.Fatalf("endpoint credentials disclosed: %v", err)
	}
}

// TestConstructionSnapshotsExporterHeadersAndDraft observes input copying,
// publication, and once-only callback timing at the actual exporter HTTP seam.
func TestConstructionSnapshotsExporterHeadersAndDraft(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_SDK_DISABLED", "false")
	var header atomic.Value
	collector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		header.Store(request.Header.Get("X-Tenant"))
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	headers := map[string]string{"X-Tenant": "original"}
	var retained *oteltelemetry.Builder
	calls := 0
	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
		calls++
		retained = instrumentation
		instrumentation.Headers(headers)
		headers["X-Tenant"] = "input-mutated"
	}, func(instrumentation *oteltelemetry.Builder) {
		if calls != 1 || instrumentation != retained {
			t.Fatal("construction callbacks did not share one ordered draft")
		}

		calls++
		instrumentation.Endpoint(collector.URL)
		instrumentation.Protocol(oteltelemetry.ProtocolHTTP)
		instrumentation.MeterProvider(sdkmetric.NewMeterProvider())
		instrumentation.LoggerProvider(lognoop.NewLoggerProvider())
		instrumentation.RuntimeMetrics(false)
		instrumentation.GlobalErrorHandler(false)
	})

	retained.Headers(map[string]string{"X-Tenant": "draft-mutated"})
	retained.Endpoint("not/a/collector")

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	_, span := driver.StartSpan(t.Context(), telemetry.SpanStart{Name: "work run"})
	span.End()

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if calls != 2 || header.Load() != "original" {
		t.Fatalf("callbacks = %d, exported header = %v", calls, header.Load())
	}
}

// TestUnsupportedEndpointPartsAreRejected rejects unsupported URL meanings
// instead of silently dropping authentication, query, fragment, or scheme data.
func TestUnsupportedEndpointPartsAreRejected(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{
		"ftp://collector.example.com", "https://user:secret@collector.example.com",
		"https://collector.example.com?token=secret", "https://collector.example.com#secret",
	} {
		driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
			instrumentation.Endpoint(endpoint)
			instrumentation.MeterProvider(sdkmetric.NewMeterProvider())
			instrumentation.LoggerProvider(lognoop.NewLoggerProvider())
			instrumentation.GlobalErrorHandler(false)
		})

		if err := driver.Start(t.Context(), telemetry.Setup{}); err == nil {
			t.Fatal("Start accepted unsupported endpoint URL parts")
		}
	}
}
