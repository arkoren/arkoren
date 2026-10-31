package oteltelemetry

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
	"arkoren.dev/driver/otel/oteltelemetry/internal/sdk"
	"arkoren.dev/foundation/collection"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const (
	// collectorEndpoint is the OTLP gRPC address of the Compose collector.
	collectorEndpoint = "127.0.0.1:4317"

	// collectorLog is the default host-side file written through the source
	// mount in driver/otel/compose.yaml. Tests can select an isolated host file
	// with ARKOREN_OTEL_COLLECTOR_LOG without changing collector configuration.
	collectorLog = "testdata/collector.log"

	// collectorTimeout bounds the whole collector test.
	collectorTimeout = 30 * time.Second

	// collectorPollInterval is how often the collector log is re-read.
	collectorPollInterval = 50 * time.Millisecond
)

// collectorLogPath selects the host-side log read by the integration test.
// ARKOREN_OTEL_COLLECTOR_LOG overrides the path; unset or empty keeps collectorLog.
// Relative paths use the test package's working directory. An absolute path can
// point to an external fixture mount. This lookup never creates or changes files.
func collectorLogPath() string {
	if path := os.Getenv("ARKOREN_OTEL_COLLECTOR_LOG"); path != "" {
		return path
	}

	return collectorLog
}

// TestCollectorLogPathDefaults checks empty configuration preserves the existing
// relative path without opening or changing the source-mounted collector log.
func TestCollectorLogPathDefaults(t *testing.T) {
	t.Setenv("ARKOREN_OTEL_COLLECTOR_LOG", "")

	if path := collectorLogPath(); path != "testdata/collector.log" {
		t.Fatalf("collector log path = %q, want testdata/collector.log", path)
	}
}

// TestCollectorLogPathUnsetUsesDefault checks an absent override has the same
// default as an empty value. Setenv owns restoration of any original setting;
// the test resolves the path only and never reads the default file.
func TestCollectorLogPathUnsetUsesDefault(t *testing.T) {
	t.Setenv("ARKOREN_OTEL_COLLECTOR_LOG", "")

	if err := os.Unsetenv("ARKOREN_OTEL_COLLECTOR_LOG"); err != nil {
		t.Fatal(err)
	}

	if path := collectorLogPath(); path != "testdata/collector.log" {
		t.Fatalf("collector log path = %q, want testdata/collector.log", path)
	}
}

// TestCollectorLogPathOverrideReadsIsolatedFile exercises the override and
// existing same-line matching against a newly created temporary fixture only.
// It needs no collector and never accesses the source-mounted collector log.
func TestCollectorLogPathOverrideReadsIsolatedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.log")

	if err := os.WriteFile(path, []byte("fixture-service expected-fragment\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ARKOREN_OTEL_COLLECTOR_LOG", path)
	selected := collectorLogPath()

	if selected != path {
		t.Fatalf("collector log path = %q, want %q", selected, path)
	}

	if !collectorLogHas(selected, "fixture-service", "expected-fragment") {
		t.Fatal("overridden collector log did not contain the expected line")
	}

	if collectorLogHas(selected, "another-service", "expected-fragment") {
		t.Fatal("collector log matched a different service")
	}
}

// TestCollectorReceivesFacadeTelemetry exports a request span and a correlated
// log record through the facade, the HTTP middleware, and OTLP gRPC to a real
// collector, and waits until the collector reports both.
func TestCollectorReceivesFacadeTelemetry(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	ctx, cancel := context.WithTimeout(t.Context(), collectorTimeout)
	defer cancel()

	// A unique service name tells this run's data from earlier runs in the log.
	service := "collector-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	facade := telemetry.New(New(
		func(instrumentation *Builder) {
			instrumentation.Endpoint(collectorEndpoint)
		},
		func(instrumentation *Builder) {
			instrumentation.Protocol(ProtocolGRPC)
		},
		func(instrumentation *Builder) {
			instrumentation.Insecure(true)
		},
		func(instrumentation *Builder) {
			instrumentation.RuntimeMetrics(false)
		},
	))

	if err := facade.Start(ctx, telemetry.Setup{
		Resource: telemetry.Resource{
			Service: service,
		},
		Logger: slog.New(slog.DiscardHandler),
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = facade.Stop(context.WithoutCancel(t.Context()))
	})

	logger := slog.New(facade.Handler(slog.DiscardHandler))

	app := router.New()
	app.Use(facade.Middleware())
	app.Get("/shipments/{id}", func(w http.ResponseWriter, r *http.Request) error {
		logger.ErrorContext(r.Context(), "shipment created", "shipment", "123")
		w.WriteHeader(http.StatusCreated)

		return nil
	})

	response := app.Record(httptest.NewRequest(http.MethodGet, "/shipments/123", nil))

	if response.StatusCode != http.StatusCreated {
		t.Fatalf("response status = %d, want %d", response.StatusCode, http.StatusCreated)
	}

	if err := facade.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	wanted := map[string]string{
		"span":   `"key":"http.route","value":{"stringValue":"/shipments/{id}"}`,
		"status": fmt.Sprintf(`"key":"http.response.status_code","value":{"intValue":"%d"}`, http.StatusCreated),
		"log":    `"stringValue":"shipment created"`,
	}

	for name, fragment := range wanted {
		if err := waitForCollectorLine(ctx, service, fragment); err != nil {
			t.Errorf("collector never reported the %s: %v", name, err)
		}
	}
}

// waitForCollectorLine resolves the test-only log override once, then polls
// that file until one line mentions both service and fragment, or ctx ends.
// The file is read only; collector lifecycle and its output mount remain owned
// by the integration runner.
func waitForCollectorLine(ctx context.Context, service, fragment string) error {
	path := collectorLogPath()
	ticker := time.NewTicker(collectorPollInterval)
	defer ticker.Stop()

	for {
		if collectorLogHas(path, service, fragment) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// collectorLogHas reports whether one line of the host file at path contains
// both service and fragment. It opens the file read-only and treats an unreadable
// or missing log as empty, preserving the integration test's polling behavior.
func collectorLogHas(path, service, fragment string) bool {
	file, err := os.Open(path)

	if err != nil {
		return false
	}

	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, 16<<20)

	for scanner.Scan() {
		line := scanner.Text()

		if strings.Contains(line, service) && strings.Contains(line, fragment) {
			return true
		}
	}

	return false
}

// Environment names used by public driver behavior tests. Production lookup
// belongs to internal/sdk; these literals also guard the external OTEL names.
const (
	// envSDKDisabled turns every signal into a no-op when "true".
	envSDKDisabled = "OTEL_SDK_DISABLED"
	// envProtocol selects the OTLP transport of every signal.
	envProtocol = "OTEL_EXPORTER_OTLP_PROTOCOL"
	// envTracesProtocol selects the trace transport.
	envTracesProtocol = "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"
	// envLogsProtocol selects the log transport.
	envLogsProtocol = "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"
	// envTracesExporter selects the trace exporter.
	envTracesExporter = "OTEL_TRACES_EXPORTER"
	// envMetricsExporter selects the metric exporter.
	envMetricsExporter = "OTEL_METRICS_EXPORTER"
	// envLogsExporter selects the log exporter.
	envLogsExporter = "OTEL_LOGS_EXPORTER"
)

// harness is a Driver backed by in-memory OpenTelemetry exporters. It is also
// the telemetrytest.Reader that converts what they hold into memory snapshots.
type harness struct {
	// Driver supplies the lifecycle and signal methods exercised by the suite.
	// The harness replaces only collection with inspectable in-memory snapshots.
	*Driver

	// tracerProvider exports finished spans synchronously to spans.
	tracerProvider *sdktrace.TracerProvider

	// spans holds finished spans.
	spans *tracetest.InMemoryExporter

	// reader collects metrics on demand.
	reader *sdkmetric.ManualReader

	// logs holds emitted log records.
	logs *logRecorder

	// mutex guards specs.
	mutex sync.Mutex

	// specs are the instruments created, which OpenTelemetry data cannot
	// reproduce (declared key order).
	specs map[string]telemetry.InstrumentSpec
}

// newHarness returns an unstarted harness. Runtime metrics are off so the
// readers only see what a test records; callbacks can turn them on again.
func newHarness(configure ...func(*Builder)) *harness {
	spans := tracetest.NewInMemoryExporter()
	reader := sdkmetric.NewManualReader()
	logs := &logRecorder{}

	harness := &harness{
		tracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans)),
		spans:          spans,
		reader:         reader,
		logs:           logs,
		specs:          map[string]telemetry.InstrumentSpec{},
	}

	defaults := []func(*Builder){
		func(instrumentation *Builder) {
			instrumentation.TracerProvider(harness.tracerProvider)
		},
		func(instrumentation *Builder) {
			instrumentation.MeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
		},
		func(instrumentation *Builder) {
			instrumentation.LoggerProvider(sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(logs))))
		},
		func(instrumentation *Builder) {
			instrumentation.RuntimeMetrics(false)
		},
	}

	harness.Driver = New(append(defaults, configure...)...)

	return harness
}

// Instrument remembers spec and creates the instrument.
func (harness *harness) Instrument(spec telemetry.InstrumentSpec) (telemetry.InstrumentRecorder, error) {
	harness.mutex.Lock()
	harness.specs[spec.Scope+"\x00"+spec.Name] = spec
	harness.mutex.Unlock()

	return harness.Driver.Instrument(spec)
}

// Spans returns the finished spans as memory snapshots.
func (harness *harness) Spans() collection.Slice[memorytelemetry.Span] {
	var spans []memorytelemetry.Span

	for _, stub := range harness.spans.GetSpans() {
		spans = append(spans, memorySpan(stub))
	}

	return spans
}

// Metrics collects the instruments and returns them as memory snapshots.
func (harness *harness) Metrics() collection.Slice[memorytelemetry.Metric] {
	var collected metricdata.ResourceMetrics

	if err := harness.reader.Collect(context.Background(), &collected); err != nil {
		return nil
	}

	var metrics []memorytelemetry.Metric

	for _, scope := range collected.ScopeMetrics {
		for _, data := range scope.Metrics {
			harness.mutex.Lock()
			spec := harness.specs[scope.Scope.Name+"\x00"+data.Name]
			harness.mutex.Unlock()

			metrics = append(metrics, memoryMetric(scope.Scope.Name, spec.Keys, data))
		}
	}

	return metrics
}

// memorySpan converts a finished OpenTelemetry span.
func memorySpan(stub tracetest.SpanStub) memorytelemetry.Span {
	span := memorytelemetry.Span{
		Scope:   stub.InstrumentationScope.Name,
		Name:    stub.Name,
		Kind:    telemetryKind(stub.SpanKind),
		Status:  telemetry.Unset,
		Context: fromOTelSpanContext(stub.SpanContext),
		Parent:  fromOTelSpanContext(stub.Parent),
		Start:   stub.StartTime,
		End:     stub.EndTime,
		Attrs:   telemetryAttrs(stub.Attributes),
	}

	for _, event := range stub.Events {
		span.Events = append(span.Events, memorytelemetry.Event{
			Name:  event.Name,
			Time:  event.Time,
			Attrs: telemetryAttrs(event.Attributes),
		})
	}

	switch stub.Status.Code {
	case codes.Ok:
		span.Status = telemetry.OK
	case codes.Error:
		span.Status = telemetry.Error
		span.StatusDescription = stub.Status.Description
	}

	return span
}

// telemetryKind converts a span kind back.
func telemetryKind(kind trace.SpanKind) telemetry.SpanKind {
	for name, candidate := range spanKinds {
		if candidate == kind {
			return name
		}
	}

	return telemetry.Internal
}

// telemetryAttrs converts attributes back.
func telemetryAttrs(attrs []attribute.KeyValue) []telemetry.Attr {
	converted := make([]telemetry.Attr, 0, len(attrs))

	for _, attr := range attrs {
		key := string(attr.Key)

		switch attr.Value.Type() {
		case attribute.STRING:
			converted = append(converted, telemetry.String(key, attr.Value.AsString()))
		case attribute.INT64:
			converted = append(converted, telemetry.Int64(key, attr.Value.AsInt64()))
		case attribute.FLOAT64:
			converted = append(converted, telemetry.Float64(key, attr.Value.AsFloat64()))
		case attribute.BOOL:
			converted = append(converted, telemetry.Bool(key, attr.Value.AsBool()))
		case attribute.STRINGSLICE:
			converted = append(converted, telemetry.Strings(key, attr.Value.AsStringSlice()))
		}
	}

	return converted
}

// memoryMetric converts collected metric data. keys are the declared label
// keys in order.
func memoryMetric(scope string, keys []string, data metricdata.Metrics) memorytelemetry.Metric {
	metric := memorytelemetry.Metric{
		Scope:       scope,
		Name:        data.Name,
		Description: data.Description,
		Unit:        data.Unit,
		Keys:        keys,
	}

	switch aggregation := data.Data.(type) {
	case metricdata.Sum[float64]:
		metric.Kind = telemetry.UpDownCounterKind

		if aggregation.IsMonotonic {
			metric.Kind = telemetry.CounterKind
		}

		for _, point := range aggregation.DataPoints {
			metric.Points = append(metric.Points, memorytelemetry.Point{
				Values: labelValues(keys, point.Attributes),
				Sum:    point.Value,
				Last:   point.Value,
			})
		}
	case metricdata.Histogram[float64]:
		metric.Kind = telemetry.HistogramKind

		for _, point := range aggregation.DataPoints {
			metric.Points = append(metric.Points, memorytelemetry.Point{
				Values: labelValues(keys, point.Attributes),
				Count:  point.Count,
				Sum:    point.Sum,
			})
		}
	case metricdata.Gauge[float64]:
		metric.Kind = telemetry.GaugeKind

		for _, point := range aggregation.DataPoints {
			metric.Points = append(metric.Points, memorytelemetry.Point{
				Values: labelValues(keys, point.Attributes),
				Last:   point.Value,
			})
		}
	}

	return metric
}

// labelValues returns the value of each key in set, "" when absent.
func labelValues(keys []string, set attribute.Set) []string {
	values := make([]string, len(keys))

	for index, key := range keys {
		if value, ok := set.Value(attribute.Key(key)); ok {
			values[index] = value.AsString()
		}
	}

	return values
}

// logRecorder is a log exporter that keeps the records it receives.
type logRecorder struct {
	// mutex guards records.
	mutex sync.Mutex

	// records are the exported records.
	records []sdklog.Record
}

// Export keeps clones of records.
func (recorder *logRecorder) Export(_ context.Context, records []sdklog.Record) error {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	for _, record := range records {
		recorder.records = append(recorder.records, record.Clone())
	}

	return nil
}

// Shutdown does nothing.
func (*logRecorder) Shutdown(context.Context) error {
	return nil
}

// ForceFlush does nothing.
func (*logRecorder) ForceFlush(context.Context) error {
	return nil
}

// Records returns the exported records.
func (recorder *logRecorder) Records() []sdklog.Record {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	return append([]sdklog.Record(nil), recorder.records...)
}

// startTimeout bounds waits in tests.
const startTimeout = 10 * time.Second

// testTime is a fixed timestamp for log records.
var testTime = time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)

// TestConformance runs the shared telemetry driver suite against in-memory
// OpenTelemetry exporters.
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

// TestEmptySpanKindUsesInternal verifies raw driver callers retain the former
// zero-kind default and expose an explicit unset status in conformance snapshots.
func TestEmptySpanKindUsesInternal(t *testing.T) {
	harness := startHarness(t)

	_, span := harness.StartSpan(t.Context(), telemetry.SpanStart{Name: "cache get"})
	span.SetStatus("", "ignored")
	span.End()

	spans := harness.Spans()

	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}

	if spans[0].Kind != telemetry.Internal || spans[0].Status != telemetry.Unset {
		t.Fatalf("kind = %q, status = %q", spans[0].Kind, spans[0].Status)
	}
}

// testSetup is the Setup tests start drivers with.
func testSetup() telemetry.Setup {
	return telemetry.Setup{
		Resource: telemetry.Resource{
			Service:     "billing",
			Version:     "1.2.3",
			Environment: "test",
		},
		Logger: slog.New(slog.DiscardHandler),
	}
}

// startHarness starts a harness and shuts it down when the test ends.
func startHarness(t *testing.T, configure ...func(*Builder)) *harness {
	t.Helper()

	harness := newHarness(configure...)

	if err := harness.Start(t.Context(), testSetup()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = harness.Stop(context.WithoutCancel(t.Context()))
	})

	return harness
}

// resourceAttr returns the value of key in the resource built for described.
func resourceAttr(t *testing.T, described telemetry.Resource, key string) (string, bool) {
	t.Helper()

	res, err := sdk.NewResource(t.Context(), described)

	if err != nil {
		t.Fatalf("newResource: %v", err)
	}

	value, ok := res.Set().Value(attribute.Key(key))

	return value.String(), ok
}

// TestResourceUsesSetupOverEnvironment checks explicit service metadata takes
// precedence while unrelated environment resource attributes remain present.
func TestResourceUsesSetupOverEnvironment(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "from-env")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=staging,team=core")

	described := telemetry.Resource{
		Service: "billing",
		Version: "1.2.3",
		Attrs:   []telemetry.Attr{telemetry.String("region", "eu"), {}},
	}

	want := map[string]string{
		"service.name":                "billing",
		"service.version":             "1.2.3",
		"deployment.environment.name": "staging",
		"team":                        "core",
		"region":                      "eu",
		"telemetry.sdk.language":      "go",
	}

	for key, value := range want {
		if got, ok := resourceAttr(t, described, key); !ok || got != value {
			t.Errorf("%s = %q (found %v), want %q", key, got, ok, value)
		}
	}
}

// TestResourceFallsBackToEnvironmentAndDefault exercises service-name fallback
// from setup to the environment and then to the SDK's unknown-service name.
func TestResourceFallsBackToEnvironmentAndDefault(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "from-env")

	if got, _ := resourceAttr(t, telemetry.Resource{}, "service.name"); got != "from-env" {
		t.Errorf("service.name = %q, want from-env", got)
	}

	t.Setenv("OTEL_SERVICE_NAME", "")

	if got, _ := resourceAttr(t, telemetry.Resource{}, "service.name"); !strings.HasPrefix(got, "unknown_service:") {
		t.Errorf("service.name = %q, want an unknown_service default", got)
	}
}

// requireProtocol sets env, resolves the traces protocol for config and
// verifies the result and whether it failed.
func requireProtocol(t *testing.T, config settings, env map[string]string, want Protocol, wantErr bool) {
	t.Helper()

	for key, value := range env {
		t.Setenv(key, value)
	}

	value, err := sdk.ResolveProtocol(config.native(), envTracesProtocol)
	got := Protocol(value)

	if (err != nil) != wantErr || got != want {
		t.Errorf("resolveProtocol = %q, %v; want %q, error %v", got, err, want, wantErr)
	}
}

// TestResolveProtocolDefault protects the HTTP/protobuf fallback when neither
// construction settings nor protocol environment variables select a transport.
func TestResolveProtocolDefault(t *testing.T) {
	requireProtocol(t, settings{}, nil, ProtocolHTTP, false)
}

// TestResolveProtocolConfigWins gives the explicit transport precedence over
// the generic OTLP environment variable, keeping construction deterministic.
func TestResolveProtocolConfigWins(t *testing.T) {
	requireProtocol(t, settings{
		Protocol: ProtocolGRPC,
	}, map[string]string{envProtocol: "http/protobuf"}, ProtocolGRPC, false)
}

// TestResolveProtocolSignalEnv checks a signal-specific protocol overrides the
// generic OTLP protocol without requiring an exporter connection.
func TestResolveProtocolSignalEnv(t *testing.T) {
	requireProtocol(t, settings{}, map[string]string{envTracesProtocol: "grpc", envProtocol: "http/protobuf"}, ProtocolGRPC, false)
}

// TestResolveProtocolGenericEnv uses the generic OTLP setting when no explicit
// or signal-specific protocol has been supplied.
func TestResolveProtocolGenericEnv(t *testing.T) {
	requireProtocol(t, settings{}, map[string]string{envProtocol: "grpc"}, ProtocolGRPC, false)
}

// TestResolveProtocolUnsupported rejects an unsupported encoding instead of
// silently choosing another transport for the caller's telemetry.
func TestResolveProtocolUnsupported(t *testing.T) {
	requireProtocol(t, settings{}, map[string]string{envProtocol: "http/json"}, "", true)
}

// requireStartRejected sets env, starts a driver built with callbacks and verifies
// Start fails and leaves the driver inactive.
func requireStartRejected(t *testing.T, configure []func(*Builder), env map[string]string) {
	t.Helper()

	for key, value := range env {
		t.Setenv(key, value)
	}

	driver := New(configure...)

	if err := driver.Start(t.Context(), testSetup()); err == nil {
		_ = driver.Stop(t.Context())
		t.Fatalf("Start succeeded, want an error")
	}

	// A failed Start leaves the driver unstarted and inactive.
	if _, recorder := driver.StartSpan(t.Context(), telemetry.SpanStart{
		Name: "x",
	}); recorder.Recording() {
		t.Errorf("driver records after a failed Start")
	}
}

// TestStartRejectsUnsupportedProtocol reports invalid explicit configuration at
// startup, before any usable exporter state is published.
func TestStartRejectsUnsupportedProtocol(t *testing.T) {
	requireStartRejected(t, []func(*Builder){func(instrumentation *Builder) {
		instrumentation.Protocol("carrier-pigeon")
	}}, nil)
}

// TestStartRejectsConsoleExporter rejects a non-OTLP trace exporter requested
// through the environment rather than silently disabling trace delivery.
func TestStartRejectsConsoleExporter(t *testing.T) {
	requireStartRejected(t, nil, map[string]string{envTracesExporter: "console"})
}

// TestStartRejectsPrometheusMetricsExporter protects the OTLP driver's transport
// boundary; Prometheus collection belongs to its separate driver.
func TestStartRejectsPrometheusMetricsExporter(t *testing.T) {
	requireStartRejected(t, nil, map[string]string{envMetricsExporter: "prometheus"})
}

// TestStartRejectsUnsupportedLogsProtocol checks per-signal validation even when
// other exporters could otherwise start with their defaults.
func TestStartRejectsUnsupportedLogsProtocol(t *testing.T) {
	requireStartRejected(t, nil, map[string]string{envLogsProtocol: "nope"})
}

// TestSDKDisabledIsNoop checks all three signals honor the SDK disable switch.
// Spans and instruments remain callable but must not record or enable logs.
func TestSDKDisabledIsNoop(t *testing.T) {
	t.Setenv(envSDKDisabled, "true")

	driver := New()

	if err := driver.Start(t.Context(), testSetup()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, recorder := driver.StartSpan(t.Context(), telemetry.SpanStart{
		Name: "x",
	})

	if recorder.Recording() || recorder.Context().IsValid() {
		t.Errorf("disabled driver recorded a span")
	}

	instrument, err := driver.Instrument(telemetry.InstrumentSpec{
		Name: "x",
		Kind: telemetry.CounterKind,
	})

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	instrument.Record(t.Context(), 1, nil)

	if driver.Enabled(t.Context(), slog.LevelError) {
		t.Errorf("disabled driver reports logs enabled")
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// TestExportersCanBeTurnedOffByEnvironment disables each OTLP signal separately.
// Successful startup must not imply spans or logs are enabled in that state.
func TestExportersCanBeTurnedOffByEnvironment(t *testing.T) {
	t.Setenv(envTracesExporter, "none")
	t.Setenv(envMetricsExporter, "none")
	t.Setenv(envLogsExporter, "none")

	driver := New()

	if err := driver.Start(t.Context(), testSetup()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(t.Context())
	})

	if _, recorder := driver.StartSpan(t.Context(), telemetry.SpanStart{
		Name: "x",
	}); recorder.Recording() {
		t.Errorf("span recorded with OTEL_TRACES_EXPORTER=none")
	}

	if driver.Enabled(t.Context(), slog.LevelError) {
		t.Errorf("logs enabled with OTEL_LOGS_EXPORTER=none")
	}
}

// TestEmptyLabelValuesAreOmitted checks sparse metric attributes without
// coalescing samples whose nonempty route labels differ.
func TestEmptyLabelValuesAreOmitted(t *testing.T) {
	harness := startHarness(t)

	instrument, err := harness.Instrument(telemetry.InstrumentSpec{
		Scope: "test",
		Name:  "test.requests",
		Kind:  telemetry.CounterKind,
		Keys:  []string{"method", "route"},
	})

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	instrument.Record(t.Context(), 1, []string{"GET", ""})
	instrument.Record(t.Context(), 1, []string{"GET", ""})
	instrument.Record(t.Context(), 1, []string{"GET", "/a"})

	var collected metricdata.ResourceMetrics

	if err := harness.reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	sum, _ := collected.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[float64])

	if len(sum.DataPoints) != 2 {
		t.Fatalf("data points = %+v, want 2", sum.DataPoints)
	}

	for _, point := range sum.DataPoints {
		_, hasRoute := point.Attributes.Value("route")

		if hasRoute != (point.Value == 1) {
			t.Errorf("point %v value %v: route attribute present = %v", point.Attributes.ToSlice(), point.Value, hasRoute)
		}

		if _, ok := point.Attributes.Value("method"); !ok {
			t.Errorf("point %v lost the method attribute", point.Attributes.ToSlice())
		}
	}
}

// TestHistogramUsesBucketHint reads the SDK's collected histogram to verify
// contract bucket hints reach the native aggregation configuration.
func TestHistogramUsesBucketHint(t *testing.T) {
	harness := startHarness(t)

	instrument, err := harness.Instrument(telemetry.InstrumentSpec{
		Scope:   "test",
		Name:    "test.duration",
		Kind:    telemetry.HistogramKind,
		Unit:    "s",
		Buckets: []float64{0.1, 1, 10},
	})

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	instrument.Record(t.Context(), 0.5, nil)

	var collected metricdata.ResourceMetrics
	_ = harness.reader.Collect(t.Context(), &collected)

	histogram, _ := collected.ScopeMetrics[0].Metrics[0].Data.(metricdata.Histogram[float64])

	if got := histogram.DataPoints[0].Bounds; !slices.Equal(got, []float64{0.1, 1, 10}) {
		t.Errorf("bounds = %v, want [0.1 1 10]", got)
	}
}

// TestInstrumentRejectsUnknownKind prevents creation of an undefined native
// instrument when the contract kind is outside the supported set.
func TestInstrumentRejectsUnknownKind(t *testing.T) {
	harness := startHarness(t)

	if _, err := harness.Instrument(telemetry.InstrumentSpec{
		Name: "x",
		Kind: "",
	}); err == nil {
		t.Errorf("Instrument with an unknown kind succeeded")
	}
}

// TestRuntimeMetricsAreOnByDefault checks both recommended configuration and
// actual SDK collection, so a default flag alone cannot satisfy the test.
func TestRuntimeMetricsAreOnByDefault(t *testing.T) {
	harness := newHarness(func(instrumentation *Builder) {
		instrumentation.RuntimeMetrics(true)
	})

	if err := harness.Start(t.Context(), testSetup()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = harness.Stop(t.Context())
	})

	if !newConfig().RuntimeMetrics {
		t.Errorf("newConfig().RuntimeMetrics = false, want true")
	}

	var collected metricdata.ResourceMetrics

	if err := harness.reader.Collect(t.Context(), &collected); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(collected.ScopeMetrics) == 0 {
		t.Errorf("no runtime metrics collected")
	}
}

// TestNativeSpanParentsBeforeTelemetrySpan gives a valid native context priority
// over facade metadata, preserving parentage for mixed instrumentation.
func TestNativeSpanParentsBeforeTelemetrySpan(t *testing.T) {
	harness := startHarness(t)

	nativeCtx, native := harness.tracerProvider.Tracer("native").Start(t.Context(), "native")
	facadeCtx := telemetry.ContextWithSpanContext(nativeCtx, telemetry.SpanContext{
		TraceID: telemetry.TraceID{9},
		SpanID:  telemetry.SpanID{9},
		Sampled: true,
	})

	_, recorder := harness.StartSpan(facadeCtx, telemetry.SpanStart{
		Scope: "test",
		Name:  "child",
	})

	recorder.End()
	native.End()

	for _, span := range harness.Spans() {
		if span.Name == "child" && span.Parent.SpanID != fromOTelSpanContext(native.SpanContext()).SpanID {
			t.Errorf("child parent = %v, want the native span", span.Parent.SpanID)
		}
	}
}

// TestTelemetrySpanParentsWhenNoNativeSpan restores facade parent metadata when
// no native parent exists, including remote state and vendor trace state.
func TestTelemetrySpanParentsWhenNoNativeSpan(t *testing.T) {
	harness := startHarness(t)

	parent := telemetry.SpanContext{
		TraceID:    telemetry.TraceID{1},
		SpanID:     telemetry.SpanID{2},
		Sampled:    true,
		Remote:     true,
		TraceState: "vendor=1",
	}

	ctx := telemetry.ContextWithSpanContext(t.Context(), parent)

	_, recorder := harness.StartSpan(ctx, telemetry.SpanStart{
		Scope: "test",
		Name:  "child",
	})

	recorder.End()

	got := harness.Spans()[0]

	if got.Parent.TraceID != parent.TraceID || got.Parent.SpanID != parent.SpanID || !got.Parent.Remote {
		t.Errorf("parent = %+v, want %+v", got.Parent, parent)
	}

	if got.Context.TraceState != "vendor=1" {
		t.Errorf("trace state = %q, want it inherited from the parent", got.Context.TraceState)
	}
}

// TestInjectWritesTraceparentAndExtractRestoresIt round-trips W3C propagation.
// Extracted metadata must retain span identity and mark the parent as remote.
func TestInjectWritesTraceparentAndExtractRestoresIt(t *testing.T) {
	harness := startHarness(t)

	ctx, recorder := harness.StartSpan(t.Context(), telemetry.SpanStart{
		Scope: "test",
		Name:  "client",
		Kind:  telemetry.Client,
	})

	defer recorder.End()

	carrier := telemetry.MapCarrier{}
	harness.Inject(ctx, carrier)

	if !strings.Contains(carrier["traceparent"], recorder.Context().TraceID.String()) {
		t.Fatalf("traceparent = %q, want trace %v", carrier["traceparent"], recorder.Context().TraceID)
	}

	_, extracted := harness.Extract(t.Context(), carrier)

	if !extracted.Remote || extracted.SpanID != recorder.Context().SpanID {
		t.Errorf("extracted %+v, want remote span %v", extracted, recorder.Context().SpanID)
	}
}

// TestExtractIgnoresGarbageEvenWithSpanInContext prevents invalid carrier data
// from being mistaken for the unrelated active span already in the context.
func TestExtractIgnoresGarbageEvenWithSpanInContext(t *testing.T) {
	harness := startHarness(t)

	ctx, recorder := harness.StartSpan(t.Context(), telemetry.SpanStart{
		Scope: "test",
		Name:  "active",
	})

	defer recorder.End()

	if _, extracted := harness.Extract(ctx, telemetry.MapCarrier{"traceparent": "garbage"}); extracted.IsValid() {
		t.Errorf("Extract returned %+v for garbage", extracted)
	}
}

// TestExportMapsRecordAndCorrelatesTrace checks log severity, timestamp, nested
// attributes, and active trace linkage using an in-memory exporter.
func TestExportMapsRecordAndCorrelatesTrace(t *testing.T) {
	harness := startHarness(t)

	ctx, recorder := harness.StartSpan(t.Context(), telemetry.SpanStart{
		Scope: "test",
		Name:  "work",
	})

	defer recorder.End()

	record := slog.NewRecord(testTime, slog.LevelWarn, "disk almost full", 0)
	record.AddAttrs(
		slog.String("disk", "sda"),
		slog.Group("limits", slog.Int("soft", 80), slog.Group("hard", slog.Float64("percent", 95.5)), slog.Group("empty")),
		slog.Any("err", errors.New("boom")),
		slog.Bool("critical", false),
	)

	if !harness.Enabled(ctx, slog.LevelWarn) {
		t.Fatalf("Enabled(Warn) = false")
	}

	if err := harness.Export(ctx, record); err != nil {
		t.Fatalf("Export: %v", err)
	}

	records := harness.logs.Records()

	if len(records) != 1 {
		t.Fatalf("exported %d records, want 1", len(records))
	}

	got := records[0]

	if got.Body().AsString() != "disk almost full" || got.Severity() != log.SeverityWarn || got.SeverityText() != "WARN" {
		t.Errorf("body, severity = %q, %v %q", got.Body().AsString(), got.Severity(), got.SeverityText())
	}

	if !got.Timestamp().Equal(testTime) {
		t.Errorf("timestamp = %v, want %v", got.Timestamp(), testTime)
	}

	if got.TraceID() != trace.TraceID(recorder.Context().TraceID) || got.SpanID() != trace.SpanID(recorder.Context().SpanID) {
		t.Errorf("record trace %v span %v, want the active span %+v", got.TraceID(), got.SpanID(), recorder.Context())
	}

	attrs := map[string]string{}
	got.WalkAttributes(func(attr attribute.KeyValue) bool {
		attrs[string(attr.Key)] = attr.Value.String()

		return true
	})

	want := map[string]string{"disk": "sda", "limits.soft": "80", "limits.hard.percent": "95.5", "err": "boom", "critical": "false"}

	if len(attrs) != len(want) {
		t.Errorf("attributes = %v, want %v", attrs, want)
	}

	for key, value := range want {
		if attrs[key] != value {
			t.Errorf("attribute %s = %q, want %q", key, attrs[key], value)
		}
	}
}

// requireSeverity verifies severityOf maps level to want.
func requireSeverity(t *testing.T, level slog.Level, want log.Severity) {
	t.Helper()

	if got := severityOf(level); got != want {
		t.Errorf("severityOf(%v) = %v, want %v", level, got, want)
	}
}

// TestSeverityMappingDebug protects the native severity for slog's debug level.
// The mapping is checked independently of exporter delivery.
func TestSeverityMappingDebug(t *testing.T) {
	t.Parallel()

	requireSeverity(t, slog.LevelDebug, log.SeverityDebug)
}

// TestSeverityMappingInfo protects the native severity for ordinary info logs.
// The mapping is checked independently of exporter delivery.
func TestSeverityMappingInfo(t *testing.T) {
	t.Parallel()

	requireSeverity(t, slog.LevelInfo, log.SeverityInfo)
}

// TestSeverityMappingWarn retains warning significance in native log records.
// It must not be rounded into the informational severity range.
func TestSeverityMappingWarn(t *testing.T) {
	t.Parallel()

	requireSeverity(t, slog.LevelWarn, log.SeverityWarn)
}

// TestSeverityMappingError retains error significance in native log records.
// The assertion does not rely on a network exporter or log collector.
func TestSeverityMappingError(t *testing.T) {
	t.Parallel()

	requireSeverity(t, slog.LevelError, log.SeverityError)
}

// TestSeverityMappingBelowDebugIsTrace covers custom slog levels below debug.
// Extreme values clamp to the native trace range instead of underflowing.
func TestSeverityMappingBelowDebugIsTrace(t *testing.T) {
	t.Parallel()

	requireSeverity(t, slog.LevelDebug-100, log.SeverityTrace1)
}

// TestSeverityMappingAboveErrorIsFatal covers custom slog levels above error.
// Extreme values clamp to the native fatal range instead of overflowing.
func TestSeverityMappingAboveErrorIsFatal(t *testing.T) {
	t.Parallel()

	requireSeverity(t, slog.LevelError+100, log.SeverityFatal4)
}

// TestStopFlushesCallerProvidersWithoutStoppingThem checks borrowed-provider
// ownership: pending spans flush, but the caller's provider remains usable.
func TestStopFlushesCallerProvidersWithoutStoppingThem(t *testing.T) {
	exporter := &spyExporter{}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(startTimeout)))

	driver := New(func(instrumentation *Builder) {
		instrumentation.TracerProvider(provider)
	}, func(instrumentation *Builder) {
		instrumentation.MeterProvider(sdkmetric.NewMeterProvider())
	}, func(instrumentation *Builder) {
		instrumentation.RuntimeMetrics(false)
	})

	if err := driver.Start(t.Context(), testSetup()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, recorder := driver.StartSpan(t.Context(), telemetry.SpanStart{
		Scope: "test",
		Name:  "pending",
	})

	recorder.End()

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if exporter.exported() != 1 {
		t.Errorf("exported %d spans after Stop, want the pending span flushed", exporter.exported())
	}

	if exporter.isShutDown() {
		t.Errorf("Shutdown stopped the caller's provider")
	}

	_, again := provider.Tracer("after").Start(t.Context(), "after")

	if !again.IsRecording() {
		t.Errorf("caller's provider stopped recording")
	}

	again.End()
	_ = provider.Shutdown(t.Context())
}

// spyExporter counts exported spans and remembers shutdown.
type spyExporter struct {
	// mutex guards the fields.
	mutex sync.Mutex

	// count is the number of exported spans.
	count int

	// stopped records Shutdown.
	stopped bool
}

// ExportSpans counts spans.
func (exporter *spyExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	exporter.mutex.Lock()
	defer exporter.mutex.Unlock()

	exporter.count += len(spans)

	return nil
}

// Shutdown records the call.
func (exporter *spyExporter) Shutdown(context.Context) error {
	exporter.mutex.Lock()
	defer exporter.mutex.Unlock()

	exporter.stopped = true

	return nil
}

// exported returns the number of exported spans.
func (exporter *spyExporter) exported() int {
	exporter.mutex.Lock()
	defer exporter.mutex.Unlock()

	return exporter.count
}

// isShutDown reports whether Shutdown was called.
func (exporter *spyExporter) isShutDown() bool {
	exporter.mutex.Lock()
	defer exporter.mutex.Unlock()

	return exporter.stopped
}

// TestOwnedProvidersExportOverOTLPHTTPOnStop captures all three signal paths on
// a local server and verifies configured headers survive shutdown flushing.
func TestOwnedProvidersExportOverOTLPHTTPOnStop(t *testing.T) {
	var (
		mutex   sync.Mutex
		paths   []string
		headers []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		paths = append(paths, r.URL.Path)
		headers = append(headers, r.Header.Get("X-Api-Key"))
		mutex.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	driver := New(
		func(instrumentation *Builder) {
			instrumentation.Endpoint(server.URL)
		},
		func(instrumentation *Builder) {
			instrumentation.Headers(map[string]string{"X-Api-Key": "secret"})
		},
		func(instrumentation *Builder) {
			instrumentation.RuntimeMetrics(false)
		},
	)

	if err := driver.Start(t.Context(), testSetup()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, span := driver.StartSpan(t.Context(), telemetry.SpanStart{
		Scope: "test",
		Name:  "owned",
	})

	span.End()

	counter, err := driver.Instrument(telemetry.InstrumentSpec{
		Scope: "test",
		Name:  "test.total",
		Kind:  telemetry.CounterKind,
	})

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	counter.Record(t.Context(), 1, nil)

	record := slog.NewRecord(testTime, slog.LevelError, "owned", 0)

	if err := driver.Export(t.Context(), record); err != nil {
		t.Fatalf("Export: %v", err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	mutex.Lock()
	defer mutex.Unlock()

	for _, path := range []string{"/v1/traces", "/v1/metrics", "/v1/logs"} {
		if !slices.Contains(paths, path) {
			t.Errorf("no export to %s; got %v", path, paths)
		}
	}

	for _, header := range headers {
		if header != "secret" {
			t.Errorf("X-Api-Key = %q, want secret", header)
		}
	}
}

// TestGlobalErrorHandlerLogsToSetupLogger routes SDK asynchronous errors to the
// configured logger, then restores the handler to avoid leaking test state.
func TestGlobalErrorHandlerLogsToSetupLogger(t *testing.T) {
	var output bytes.Buffer

	setup := testSetup()
	setup.Logger = slog.New(slog.NewTextHandler(&output, nil))

	harness := newHarness()

	if err := harness.Start(t.Context(), setup); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = harness.Stop(t.Context())
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {
		}))
	})

	otel.Handle(errors.New("exporter exploded"))

	if !strings.Contains(output.String(), "exporter exploded") {
		t.Errorf("logger output = %q, want the SDK error", output.String())
	}
}

// TestConcurrentUseDuringStop exercises metric and span calls racing shutdown.
// The in-memory harness keeps the test independent of collector availability.
func TestConcurrentUseDuringStop(t *testing.T) {
	harness := startHarness(t)

	counter, err := harness.Instrument(telemetry.InstrumentSpec{
		Scope: "test",
		Name:  "test.total",
		Kind:  telemetry.CounterKind,
		Keys:  []string{"k"},
	})

	if err != nil {
		t.Fatalf("Instrument: %v", err)
	}

	var group sync.WaitGroup

	for range 8 {
		group.Go(func() {
			for range 200 {
				ctx, recorder := harness.StartSpan(t.Context(), telemetry.SpanStart{
					Scope: "test",
					Name:  "busy",
				})

				counter.Record(ctx, 1, []string{"v"})
				recorder.End()
			}
		})
	}

	_ = harness.Stop(t.Context())
	group.Wait()
}
