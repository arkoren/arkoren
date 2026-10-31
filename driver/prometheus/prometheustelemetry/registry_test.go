package prometheustelemetry_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/driver/prometheus/prometheustelemetry"
)

// Example demonstrates the README's construction and HTTP seam using an
// in-process request, so the example needs no listener, service, or credentials.
func Example() {
	policy := prometheustelemetry.BuilderFunc(func(metrics *prometheustelemetry.Builder) {
		metrics.Namespace("billing")
		metrics.RuntimeMetrics(false)
	})
	driver := prometheustelemetry.New(policy)
	service := telemetry.New(driver)
	ctx := context.Background()

	if err := service.Start(ctx, telemetry.Setup{Resource: telemetry.Resource{Service: "billing"}}); err != nil {
		panic(err)
	}

	service.Meter("example.com/billing").Counter("jobs").Add(ctx, 3)

	mux := http.NewServeMux()
	mux.Handle("/metrics", driver.Handler())
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	fmt.Println(strings.Contains(response.Body.String(), "billing_jobs_total 3"))

	if err := service.Stop(ctx); err != nil {
		panic(err)
	}
	// Output: true
}

// TestStartRejectsInvalidResourceLabels prevents malformed resource values from
// reaching a collector goroutine, where a native metric panic would be fatal.
func TestStartRejectsInvalidResourceLabels(t *testing.T) {
	t.Parallel()

	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
		metrics.RuntimeMetrics(false)
	})
	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	err := driver.Start(t.Context(), telemetry.Setup{
		Resource: telemetry.Resource{Service: "\xff"},
	})

	if err == nil {
		t.Fatal("Start accepted an invalid UTF-8 target label")
	}

	response := httptest.NewRecorder()
	driver.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if response.Body.Len() != 0 {
		t.Fatalf("failed Start retained metrics: %s", response.Body)
	}
}

// TestStartRejectsTypedNilRegistry requires bad native handles to fail through
// Start's error result rather than a nil receiver panic in registration.
func TestStartRejectsTypedNilRegistry(t *testing.T) {
	t.Parallel()

	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
		metrics.Registry(nil)
	})

	if err := driver.Start(t.Context(), telemetry.Setup{}); err == nil {
		t.Fatal("Start accepted a typed-nil registry")
	}
}

// TestHandlerRejectsTypedNilRegistry exercises the independently usable HTTP
// seam before Start, without allowing native collection to dereference nil.
func TestHandlerRejectsTypedNilRegistry(t *testing.T) {
	t.Parallel()

	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
		metrics.Registry(nil)
	})
	response := httptest.NewRecorder()
	driver.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

// startMetricsDriver supplies an isolated, started public driver without
// built-in collectors so each test observes only its own instruments.
func startMetricsDriver(t *testing.T) *prometheustelemetry.Driver {
	t.Helper()

	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
		metrics.RuntimeMetrics(false)
		metrics.TargetInfo(false)
	})

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	return driver
}

// TestHistogramRejectsSampleNameCollisions checks every generated histogram
// sample name is unavailable to later instruments, before anything is recorded.
func TestHistogramRejectsSampleNameCollisions(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"work_count", "work_sum", "work_bucket"} {
		driver := startMetricsDriver(t)

		if _, err := driver.Instrument(telemetry.InstrumentSpec{Name: "work", Kind: telemetry.HistogramKind}); err != nil {
			t.Fatal(err)
		}

		if _, err := driver.Instrument(telemetry.InstrumentSpec{Name: name, Kind: telemetry.GaugeKind}); err == nil {
			t.Fatalf("Instrument accepted histogram sample name %q", name)
		}
	}
}

// TestHistogramRejectsPreviouslyRegisteredSampleNames exercises the reverse
// registration order and confirms rejected histograms leave the gauge usable.
func TestHistogramRejectsPreviouslyRegisteredSampleNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"work_count", "work_sum", "work_bucket"} {
		driver := startMetricsDriver(t)
		recorder, err := driver.Instrument(telemetry.InstrumentSpec{Name: name, Kind: telemetry.GaugeKind})

		if err != nil {
			t.Fatal(err)
		}

		if _, err := driver.Instrument(telemetry.InstrumentSpec{Name: "work", Kind: telemetry.HistogramKind}); err == nil {
			t.Fatalf("histogram overlapped %q", name)
		}

		recorder.Record(t.Context(), 7, nil)

		response := httptest.NewRecorder()
		driver.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

		if !strings.Contains(response.Body.String(), name+" 7") {
			t.Fatalf("existing gauge disappeared: %s", response.Body)
		}
	}
}

// TestInstrumentOwnsRetainedSpecification checks caller edits cannot change
// duplicate-definition identity or the histogram's declared labels and buckets.
func TestInstrumentOwnsRetainedSpecification(t *testing.T) {
	t.Parallel()

	driver := startMetricsDriver(t)
	spec := telemetry.InstrumentSpec{
		Name: "work", Kind: telemetry.HistogramKind,
		Keys: []string{"queue"}, Buckets: []float64{1, 2},
	}
	first, err := driver.Instrument(spec)

	if err != nil {
		t.Fatal(err)
	}

	spec.Keys[0], spec.Buckets[0] = "changed", 0.5

	second, err := driver.Instrument(telemetry.InstrumentSpec{
		Name: "work", Kind: telemetry.HistogramKind,
		Keys: []string{"queue"}, Buckets: []float64{1, 2},
	})

	if err != nil {
		t.Fatalf("caller mutation changed retained identity: %v", err)
	}

	if first != second {
		t.Fatal("unchanged definition did not reuse its recorder")
	}

	second.Record(t.Context(), 0.75, []string{"billing"})

	response := httptest.NewRecorder()
	driver.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if !strings.Contains(response.Body.String(), `work_bucket{queue="billing",le="1"} 1`) {
		t.Fatalf("caller mutation changed emitted buckets or labels: %s", response.Body)
	}
}

// TestCanceledStartDoesNotRegisterCollectors checks cancellation before any
// registry mutation, without preventing a later startup attempt.
func TestCanceledStartDoesNotRegisterCollectors(t *testing.T) {
	t.Parallel()

	driver := prometheustelemetry.New()
	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Start(ctx, telemetry.Setup{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want context.Canceled", err)
	}

	response := httptest.NewRecorder()
	driver.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if response.Body.Len() != 0 {
		t.Fatal("canceled Start registered collectors")
	}

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatalf("retry after cancellation: %v", err)
	}
}

// TestMeterLabelCollisionDoesNotDisableOtherMetrics verifies lazy facade binding
// rejects colliding translated labels and leaves unrelated metrics scrapeable.
func TestMeterLabelCollisionDoesNotDisableOtherMetrics(t *testing.T) {
	t.Parallel()

	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
		metrics.RuntimeMetrics(false)
		metrics.TargetInfo(false)
	})
	service := telemetry.New(driver)

	if err := service.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = service.Stop(context.WithoutCancel(t.Context()))
	})

	meter := service.Meter("example.com/billing")
	meter.Gauge("rejected", telemetry.Keys("a.b", "a_b")).Set(t.Context(), 1,
		telemetry.String("a.b", "one"), telemetry.String("a_b", "two"))
	meter.Counter("accepted").Add(t.Context(), 3)

	response := httptest.NewRecorder()
	driver.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if strings.Contains(response.Body.String(), "rejected") {
		t.Fatalf("colliding labels were advertised: %s", response.Body)
	}

	if !strings.Contains(response.Body.String(), "accepted_total 3") {
		t.Fatalf("unrelated metric was disabled: %s", response.Body)
	}
}

// TestConstructionKeepsPublishedSettingsAndExplicitOff observes publication
// through the HTTP result: retained drafts cannot change names or reenable
// recommended built-ins that the construction callbacks explicitly disabled.
func TestConstructionKeepsPublishedSettingsAndExplicitOff(t *testing.T) {
	t.Parallel()

	var retained *prometheustelemetry.Builder
	calls := 0
	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
		retained = metrics
		calls++
		metrics.Namespace("first")
	}, func(metrics *prometheustelemetry.Builder) {
		if metrics != retained || calls != 1 {
			t.Fatal("callbacks did not share one ordered draft")
		}

		calls++
		metrics.RuntimeMetrics(false)
		metrics.TargetInfo(false)
	})

	retained.Namespace("changed")
	retained.RuntimeMetrics(true)
	retained.TargetInfo(true)

	if err := driver.Start(t.Context(), telemetry.Setup{Resource: telemetry.Resource{Service: "billing"}}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})

	recorder, err := driver.Instrument(telemetry.InstrumentSpec{Name: "jobs", Kind: telemetry.CounterKind})

	if err != nil {
		t.Fatal(err)
	}

	recorder.Record(t.Context(), 3, nil)

	response := httptest.NewRecorder()
	driver.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if calls != 2 || !strings.Contains(response.Body.String(), "first_jobs_total 3") {
		t.Fatalf("published settings changed or callbacks replayed: %s", response.Body)
	}

	if strings.Contains(response.Body.String(), "target_info") || strings.Contains(response.Body.String(), "go_goroutines") {
		t.Fatalf("explicitly disabled defaults were restored: %s", response.Body)
	}
}
