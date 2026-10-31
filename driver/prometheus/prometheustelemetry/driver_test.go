package prometheustelemetry

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
	"arkoren.dev/driver/prometheus/prometheustelemetry/internal/collector"
	"arkoren.dev/foundation/collection"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	dto "github.com/prometheus/client_model/go"
)

// harness is a Driver registering into a registry it owns. It is also the
// telemetrytest.Reader that converts what the registry holds into memory
// snapshots.
type harness struct {
	// Driver provides the production lifecycle and metric recording behavior.
	// The harness adds registry inspection without replacing those operations.
	*Driver

	// registry holds the collectors of the driver.
	registry *prometheus.Registry

	// mutex guards specs.
	mutex sync.Mutex

	// specs are the instruments created, which Prometheus data cannot map
	// back to the names they were created with.
	specs []telemetry.InstrumentSpec
}

// newHarness returns an unstarted harness. Runtime metrics and target_info are
// off so the reader only sees what a test records; callbacks can turn them on.
func newHarness(configure ...func(*Builder)) *harness {
	registry := prometheus.NewRegistry()
	defaults := []func(*Builder){func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	}}

	return &harness{
		Driver:   New(append(defaults, configure...)...),
		registry: registry,
	}
}

// Instrument remembers spec and creates the instrument.
func (harness *harness) Instrument(spec telemetry.InstrumentSpec) (telemetry.InstrumentRecorder, error) {
	harness.mutex.Lock()
	harness.specs = append(harness.specs, spec)
	harness.mutex.Unlock()

	return harness.Driver.Instrument(spec)
}

// Spans returns nothing: the driver has no traces.
func (*harness) Spans() collection.Slice[memorytelemetry.Span] {
	return nil
}

// Metrics gathers the registry and returns each created instrument as a memory
// snapshot under the name it was created with.
func (harness *harness) Metrics() collection.Slice[memorytelemetry.Metric] {
	families, err := harness.registry.Gather()

	if err != nil {
		return nil
	}

	byName := map[string]*dto.MetricFamily{}

	for _, family := range families {
		byName[family.GetName()] = family
	}

	harness.mutex.Lock()
	specs := slices.Clone(harness.specs)
	harness.mutex.Unlock()

	var metrics []memorytelemetry.Metric

	for _, spec := range specs {
		family, ok := byName[collector.MetricName(harness.config.Namespace, spec)]

		if !ok {
			continue
		}

		metrics = append(metrics, memoryMetric(spec, family))
	}

	return metrics
}

// memoryMetric converts family, the Prometheus form of spec.
func memoryMetric(spec telemetry.InstrumentSpec, family *dto.MetricFamily) memorytelemetry.Metric {
	metric := memorytelemetry.Metric{
		Scope:       spec.Scope,
		Name:        spec.Name,
		Description: spec.Description,
		Unit:        spec.Unit,
		Kind:        spec.Kind,
		Keys:        spec.Keys,
	}

	for _, sample := range family.GetMetric() {
		values := labelValues(spec.Keys, sample)

		switch spec.Kind {
		case telemetry.CounterKind:
			metric.Points = append(metric.Points, memorytelemetry.Point{
				Values: values,
				Sum:    sample.GetCounter().GetValue(),
				Last:   sample.GetCounter().GetValue(),
			})
		case telemetry.UpDownCounterKind, telemetry.GaugeKind:
			metric.Points = append(metric.Points, memorytelemetry.Point{
				Values: values,
				Sum:    sample.GetGauge().GetValue(),
				Last:   sample.GetGauge().GetValue(),
			})
		case telemetry.HistogramKind:
			metric.Points = append(metric.Points, memorytelemetry.Point{
				Values: values,
				Count:  sample.GetHistogram().GetSampleCount(),
				Sum:    sample.GetHistogram().GetSampleSum(),
			})
		}
	}

	return metric
}

// labelValues returns the value of each key in sample, "" when absent.
func labelValues(keys []string, sample *dto.Metric) []string {
	values := make([]string, len(keys))

	for index, key := range keys {
		for _, pair := range sample.GetLabel() {
			if pair.GetName() == collector.LabelName(key) {
				values[index] = pair.GetValue()
			}
		}
	}

	return values
}

// TestConformance runs the shared telemetry driver suite against a registry.
func TestConformance(t *testing.T) {
	telemetrytest.Run(t,
		func(*testing.T) telemetry.Lifecycle {
			return newHarness()
		},
		telemetrytest.WithReader(func(driver telemetry.Lifecycle) telemetrytest.Reader {
			reader, _ := driver.(*harness)

			return reader
		}),
	)
}

// durationSpec is the HTTP server duration instrument core emits.
var durationSpec = telemetry.InstrumentSpec{
	Scope:       "arkoren.dev/core/service/telemetry",
	Name:        "http.server.request.duration",
	Description: "Request duration.",
	Unit:        "s",
	Kind:        telemetry.HistogramKind,
	Keys:        []string{"http.request.method", "http.route"},
	Buckets:     []float64{0.1, 1},
}

// startDriver starts driver and shuts it down when t ends.
func startDriver(t *testing.T, driver *Driver, setup telemetry.Setup) {
	t.Helper()

	if err := driver.Start(t.Context(), setup); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(context.WithoutCancel(t.Context()))
	})
}

// scrape serves GET /metrics through handler and returns the body.
func scrape(t *testing.T, handler http.Handler, accept string) string {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)

	if accept != "" {
		request.Header.Set("Accept", accept)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	body, err := io.ReadAll(recorder.Result().Body)

	if err != nil {
		t.Fatalf("read scrape: %v", err)
	}

	return string(body)
}

// family returns the gathered family called name, or nil.
func family(t *testing.T, gatherer prometheus.Gatherer, name string) *dto.MetricFamily {
	t.Helper()

	families, err := gatherer.Gather()

	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	for _, found := range families {
		if found.GetName() == name {
			return found
		}
	}

	return nil
}

// TestHandlerServesTranslatedSeries verifies recorded values appear under their
// translated names and labels.
func TestHandlerServesTranslatedSeries(t *testing.T) {
	t.Parallel()

	driver := New(func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	})
	startDriver(t, driver, telemetry.Setup{})

	recorder, err := driver.Instrument(durationSpec)

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	recorder.Record(t.Context(), 0.5, []string{"GET", "/users/{id}"})
	recorder.Record(t.Context(), 2, []string{"GET", ""})

	body := scrape(t, driver.Handler(), "")

	for _, want := range []string{
		"# HELP http_server_request_duration_seconds Request duration.",
		`http_server_request_duration_seconds_bucket{http_request_method="GET",http_route="/users/{id}",le="1"} 1`,
		`http_server_request_duration_seconds_count{http_request_method="GET",http_route=""} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape misses %q:\n%s", want, body)
		}
	}
}

// TestHandlerWorksBeforeStart verifies the handler answers before Start.
func TestHandlerWorksBeforeStart(t *testing.T) {
	t.Parallel()

	if body := scrape(t, New().Handler(), ""); body != "" {
		t.Errorf("scrape before Start = %q, want empty", body)
	}
}

// TestHandlerWithoutGatherer verifies a registerer-only configuration answers
// 501.
func TestHandlerWithoutGatherer(t *testing.T) {
	t.Parallel()

	only := struct {
		// Registerer deliberately exposes registration without Gatherer methods.
		// The handler must not assume every registerer supports scraping.
		prometheus.Registerer
	}{
		Registerer: prometheus.NewRegistry(),
	}
	recorder := httptest.NewRecorder()
	New(func(metrics *Builder) {
		metrics.Registerer(only)
	}).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if recorder.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusNotImplemented)
	}
}

// TestNamespace verifies the namespace prefixes instruments.
func TestNamespace(t *testing.T) {
	t.Parallel()

	driver := New(func(metrics *Builder) {
		metrics.Namespace("shop")
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	})
	startDriver(t, driver, telemetry.Setup{})

	recorder, err := driver.Instrument(telemetry.InstrumentSpec{
		Name: "orders",
		Kind: telemetry.CounterKind,
	})

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	recorder.Record(t.Context(), 1, nil)

	if !strings.Contains(scrape(t, driver.Handler(), ""), "shop_orders_total 1") {
		t.Error("scrape misses shop_orders_total 1")
	}
}

// TestTargetInfo verifies the resource becomes target_info labels.
func TestTargetInfo(t *testing.T) {
	t.Parallel()

	driver := New(func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	})
	startDriver(t, driver, telemetry.Setup{
		Resource: telemetry.Resource{
			Service:     "shop",
			Version:     "1.2.3",
			Environment: "production",
			Attrs:       []telemetry.Attr{telemetry.String("cloud.region", "eu-west-1"), telemetry.Int64("replica", 3)},
		},
	})

	want := `target_info{cloud_region="eu-west-1",deployment_environment_name="production",replica="3",service_name="shop",service_version="1.2.3"} 1`

	if body := scrape(t, driver.Handler(), ""); !strings.Contains(body, want) {
		t.Errorf("scrape misses %q:\n%s", want, body)
	}
}

// TestTargetInfoOff verifies target_info can be disabled and is skipped for an
// empty resource.
func TestTargetInfoOff(t *testing.T) {
	t.Parallel()

	off := New(func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	})
	startDriver(t, off, telemetry.Setup{
		Resource: telemetry.Resource{
			Service: "shop",
		},
	})

	empty := New(func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	})
	startDriver(t, empty, telemetry.Setup{})

	for name, driver := range map[string]*Driver{"disabled": off, "empty resource": empty} {
		if body := scrape(t, driver.Handler(), ""); strings.Contains(body, "target_info") {
			t.Errorf("%s: scrape has target_info:\n%s", name, body)
		}
	}
}

// TestRuntimeMetricsLifecycle verifies the runtime collectors are registered on
// Start and removed on Stop.
func TestRuntimeMetricsLifecycle(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	})

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if family(t, registry, "go_goroutines") == nil {
		t.Error("go_goroutines missing after Start")
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if family(t, registry, "go_goroutines") != nil {
		t.Error("go_goroutines still registered after Stop")
	}
}

// TestRuntimeMetricsToleratesExistingCollector verifies Start succeeds when the
// registry already holds the Go collector, and Stop leaves it registered.
func TestRuntimeMetricsToleratesExistingCollector(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector())

	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	})

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatalf("Start with existing Go collector: %v", err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if family(t, registry, "go_goroutines") == nil {
		t.Error("the caller's Go collector was unregistered")
	}
}

// TestShutdownAllowsRegistryReuse verifies a second driver can use the registry
// after the first shut down.
func TestShutdownAllowsRegistryReuse(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()

	for range 2 {
		driver := New(func(metrics *Builder) {
			metrics.Registry(registry)
		}, func(metrics *Builder) {
			metrics.RuntimeMetrics(false)
		})

		if err := driver.Start(t.Context(), telemetry.Setup{
			Resource: telemetry.Resource{
				Service: "shop",
			},
		}); err != nil {
			t.Fatalf("Start: %v", err)
		}

		if _, err := driver.Instrument(durationSpec); err != nil {
			t.Fatalf("Instrument: %v", err)
		}

		if err := driver.Stop(t.Context()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}
}

// TestInstrumentCollisions verifies instruments that cannot coexist return
// errors instead of panicking.
func TestInstrumentCollisions(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "taken",
		Help: "taken",
	}))

	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	})
	startDriver(t, driver, telemetry.Setup{})

	if _, err := driver.Instrument(telemetry.InstrumentSpec{
		Scope: "a",
		Name:  "same.name",
		Kind:  telemetry.GaugeKind,
	}); err != nil {
		t.Fatalf("first Instrument: %v", err)
	}

	tests := map[string]telemetry.InstrumentSpec{
		"other scope": {
			Scope: "b",
			Name:  "same.name",
			Kind:  telemetry.GaugeKind,
		},
		"other spelling": {
			Scope: "a",
			Name:  "same-name",
			Kind:  telemetry.GaugeKind,
		},
		"registry collision": {
			Name: "taken",
			Kind: telemetry.GaugeKind,
		},
		"label collision": {
			Name: "labels",
			Kind: telemetry.GaugeKind,
			Keys: []string{"a.b", "a_b"},
		},
		"reserved le": {
			Name: "bucketed",
			Kind: telemetry.HistogramKind,
			Keys: []string{"le"},
		},
		"bad buckets": {
			Name:    "unordered",
			Kind:    telemetry.HistogramKind,
			Buckets: []float64{2, 1},
		},
		"unknown kind": {
			Name: "mystery",
			Kind: telemetry.InstrumentKind("unknown"),
		},
	}

	for name, spec := range tests {
		if recorder, err := driver.Instrument(spec); err == nil {
			t.Errorf("%s: Instrument = %v, nil; want an error", name, recorder)
		}
	}
}

// TestInstrumentRepeatReturnsSameRecorder verifies creating an identical
// instrument twice is not a collision.
func TestInstrumentRepeatReturnsSameRecorder(t *testing.T) {
	t.Parallel()

	driver := New(func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	})
	startDriver(t, driver, telemetry.Setup{})

	first, err := driver.Instrument(durationSpec)

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	second, err := driver.Instrument(durationSpec)

	if err != nil || first != second {
		t.Errorf("second Instrument = %v, %v; want the first recorder", second, err)
	}
}

// TestDefaultBuckets verifies histograms without declared buckets use the
// Prometheus defaults.
func TestDefaultBuckets(t *testing.T) {
	t.Parallel()

	driver := New(func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	})
	startDriver(t, driver, telemetry.Setup{})

	recorder, err := driver.Instrument(telemetry.InstrumentSpec{
		Name: "work",
		Kind: telemetry.HistogramKind,
	})

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	recorder.Record(t.Context(), 1, nil)

	if body := scrape(t, driver.Handler(), ""); !strings.Contains(body, `work_bucket{le="0.005"}`) {
		t.Errorf("scrape misses the default 0.005 bucket:\n%s", body)
	}
}

// TestRecordIgnoredOutsideLifecycle verifies recordings before Start and after
// Stop change nothing, including through a recorder created while running.
func TestRecordIgnoredOutsideLifecycle(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	})
	spec := telemetry.InstrumentSpec{
		Name: "events",
		Kind: telemetry.CounterKind,
	}

	before, err := driver.Instrument(spec)

	if err != nil {
		t.Fatalf("Instrument before Start: %v", err)
	}

	before.Record(t.Context(), 1, nil)

	startDriver(t, driver, telemetry.Setup{})

	running, err := driver.Instrument(spec)

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	before.Record(t.Context(), 1, nil)
	running.Record(t.Context(), 2, nil)

	counter := family(t, registry, "events_total")

	if counter == nil || counter.GetMetric()[0].GetCounter().GetValue() != 2 {
		t.Fatalf("events_total = %v, want 2 from the running recorder only", counter)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	running.Record(t.Context(), 5, nil)

	if family(t, registry, "events_total") != nil {
		t.Error("events_total still exposed after Stop")
	}
}

// TestRecordIgnoresInvalidInput verifies negative counter increments, wrong
// value counts, and invalid UTF-8 never panic and never record.
func TestRecordIgnoresInvalidInput(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	})
	startDriver(t, driver, telemetry.Setup{})

	recorder, err := driver.Instrument(telemetry.InstrumentSpec{
		Name: "events",
		Kind: telemetry.CounterKind,
		Keys: []string{"k"},
	})

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	recorder.Record(t.Context(), -1, []string{"v"})
	recorder.Record(t.Context(), 1, nil)
	recorder.Record(t.Context(), 1, []string{"a", "b"})
	recorder.Record(t.Context(), 1, []string{"\xff"})

	if found := family(t, registry, "events_total"); found != nil {
		t.Errorf("events_total = %v, want nothing recorded", found)
	}
}

// TestStartFailureRollsBack verifies a failed Start leaves the registry clean.
func TestStartFailureRollsBack(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	registry.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "target_info",
		Help:        "mine",
		ConstLabels: prometheus.Labels{"other": "x"},
	}))

	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	})

	if err := driver.Start(t.Context(), telemetry.Setup{
		Resource: telemetry.Resource{
			Service: "shop",
		},
	}); err == nil {
		t.Fatal("Start = nil, want a target_info conflict")
	}
}

// TestExemplars verifies a sampled span in the context becomes an exemplar.
func TestExemplars(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	prom := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	})
	facade := telemetry.New(telemetry.Compose(telemetry.Traces(memorytelemetry.New()), telemetry.Metrics(prom)))

	if err := facade.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = facade.Stop(context.WithoutCancel(t.Context()))
	})

	ctx, span := facade.Tracer("test").Start(t.Context(), "work")
	defer span.End()

	meter := facade.Meter("test")
	meter.Counter("jobs").Add(ctx, 1)
	meter.Histogram("job.duration", telemetry.Unit("s")).Record(ctx, 0.2)

	wantTrace := span.Context().TraceID.String()

	counter := family(t, registry, "jobs_total").GetMetric()[0].GetCounter().GetExemplar()

	if got := exemplarValue(counter, "trace_id"); got != wantTrace {
		t.Errorf("counter exemplar trace_id = %q, want %q", got, wantTrace)
	}

	found := false

	for _, bucket := range family(t, registry, "job_duration_seconds").GetMetric()[0].GetHistogram().GetBucket() {
		if exemplarValue(bucket.GetExemplar(), "span_id") == span.Context().SpanID.String() {
			found = true
		}
	}

	if !found {
		t.Error("no histogram bucket carries the span_id exemplar")
	}

	if body := scrape(t, prom.Handler(), "application/openmetrics-text"); !strings.Contains(body, "trace_id=\""+wantTrace+"\"") {
		t.Errorf("OpenMetrics scrape misses the exemplar:\n%s", body)
	}
}

// TestNoExemplarWithoutSpan verifies recordings outside a span carry none.
func TestNoExemplarWithoutSpan(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	})
	startDriver(t, driver, telemetry.Setup{})

	recorder, err := driver.Instrument(telemetry.InstrumentSpec{
		Name: "jobs",
		Kind: telemetry.CounterKind,
	})

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	recorder.Record(t.Context(), 1, nil)

	if exemplar := family(t, registry, "jobs_total").GetMetric()[0].GetCounter().GetExemplar(); exemplar != nil {
		t.Errorf("exemplar = %v, want none", exemplar)
	}
}

// exemplarValue returns the value of label name in exemplar, or "".
func exemplarValue(exemplar *dto.Exemplar, name string) string {
	for _, pair := range exemplar.GetLabel() {
		if pair.GetName() == name {
			return pair.GetValue()
		}
	}

	return ""
}

// TestHandlerLogsGatherErrors verifies gathering failures reach the
// application logger while the rest is still served.
func TestHandlerLogsGatherErrors(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	registry.MustRegister(failingCollector{})

	var logs bytes.Buffer

	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	})
	startDriver(t, driver, telemetry.Setup{
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})

	if _, err := driver.Instrument(telemetry.InstrumentSpec{
		Name: "fine",
		Kind: telemetry.GaugeKind,
	}); err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	scrape(t, driver.Handler(), "")

	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("logs = %q, want an error entry", logs.String())
	}
}

// failingCollector fails every collection.
type failingCollector struct{}

// Describe sends nothing, making the collector unchecked.
func (failingCollector) Describe(chan<- *prometheus.Desc) {
}

// Collect reports an error metric.
func (failingCollector) Collect(metrics chan<- prometheus.Metric) {
	metrics <- prometheus.NewInvalidMetric(prometheus.NewDesc("broken", "broken", nil, nil), io.ErrUnexpectedEOF)
}

// TestConcurrentRecordAndShutdown verifies recording, instrument creation, and
// scraping are race free while the driver shuts down.
func TestConcurrentRecordAndShutdown(t *testing.T) {
	t.Parallel()

	driver := New(func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	})

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	recorder, err := driver.Instrument(durationSpec)

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	var group sync.WaitGroup

	for range 8 {
		group.Go(func() {
			for range 200 {
				recorder.Record(t.Context(), 0.5, []string{"GET", "/a"})
				_, _ = driver.Instrument(durationSpec)
				scrape(t, driver.Handler(), "")
			}
		})
	}

	group.Go(func() {
		_ = driver.Stop(t.Context())
	})
	group.Wait()
}

// TestEndToEnd verifies the middleware's metrics are scraped from a router
// with the driver composed beside a trace driver.
func TestEndToEnd(t *testing.T) {
	t.Parallel()

	prom := New(func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	})
	facade := telemetry.New(telemetry.Compose(telemetry.Traces(memorytelemetry.New()), telemetry.Metrics(prom)))

	if err := facade.Start(t.Context(), telemetry.Setup{
		Resource: telemetry.Resource{
			Service: "shop",
		},
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = facade.Stop(context.WithoutCancel(t.Context()))
	})

	if found, ok := facade.DriverAs[*Driver](); !ok || found != prom {
		t.Fatalf("DriverAs[*Driver] = %v, %v; want the composed driver", found, ok)
	}

	app := router.New()
	app.Use(facade.Middleware())
	app.Get("/users/{id}", func(writer http.ResponseWriter, _ *http.Request) error {
		writer.WriteHeader(http.StatusNoContent)

		return nil
	})
	app.Get("/metrics", router.HTTP(prom.Handler()))

	app.Record(httptest.NewRequest(http.MethodGet, "/users/42", nil))

	response := app.Record(httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body, err := io.ReadAll(response.Body)

	if err != nil {
		t.Fatalf("read /metrics: %v", err)
	}

	for _, want := range []string{
		`http_server_request_duration_seconds_bucket{`,
		`http_route="/users/{id}"`,
		`http_response_status_code="204"`,
		`target_info{service_name="shop"} 1`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics misses %q:\n%s", want, body)
		}
	}
}
