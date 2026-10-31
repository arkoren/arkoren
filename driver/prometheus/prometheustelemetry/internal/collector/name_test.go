package collector

import (
	"testing"

	"arkoren.dev/core/service/telemetry"
)

// requireMetricName verifies metricName translates spec under namespace to want.
func requireMetricName(t *testing.T, namespace string, spec telemetry.InstrumentSpec, want string) {
	t.Helper()

	if got := MetricName(namespace, spec); got != want {
		t.Errorf("metricName(%q, %q, %q) = %q, want %q", namespace, spec.Name, spec.Unit, got, want)
	}
}

// TestMetricNameHistogramSeconds verifies the Prometheus name of the http.server.request.duration instrument.
func TestMetricNameHistogramSeconds(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "http.server.request.duration",
		Unit: "s",
		Kind: telemetry.HistogramKind,
	}, "http_server_request_duration_seconds")
}

// TestMetricNameUpDownCounterAnnotationUnit verifies the Prometheus name of the http.server.active_requests instrument.
func TestMetricNameUpDownCounterAnnotationUnit(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "http.server.active_requests",
		Unit: "{request}",
		Kind: telemetry.UpDownCounterKind,
	}, "http_server_active_requests")
}

// TestMetricNameCounterTotalSuffix verifies the Prometheus name of the arkoren.cache.operations instrument.
func TestMetricNameCounterTotalSuffix(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "arkoren.cache.operations",
		Unit: "{operation}",
		Kind: telemetry.CounterKind,
	}, "arkoren_cache_operations_total")
}

// TestMetricNameCounterKeepsTotal verifies the Prometheus name of the requests.total instrument.
func TestMetricNameCounterKeepsTotal(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "requests.total",
		Kind: telemetry.CounterKind,
	}, "requests_total")
}

// TestMetricNameCounterBytes verifies the Prometheus name of the payload instrument.
func TestMetricNameCounterBytes(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "payload",
		Unit: "By",
		Kind: telemetry.CounterKind,
	}, "payload_bytes_total")
}

// TestMetricNameHistogramKeepsBytes verifies the Prometheus name of the payload_bytes instrument.
func TestMetricNameHistogramKeepsBytes(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "payload_bytes",
		Unit: "By",
		Kind: telemetry.HistogramKind,
	}, "payload_bytes")
}

// TestMetricNameGaugeBytesPerSecond verifies the Prometheus name of the throughput instrument.
func TestMetricNameGaugeBytesPerSecond(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "throughput",
		Unit: "By/s",
		Kind: telemetry.GaugeKind,
	}, "throughput_bytes_per_second")
}

// TestMetricNameHistogramMilliseconds verifies the Prometheus name of the latency instrument.
func TestMetricNameHistogramMilliseconds(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "latency",
		Unit: "ms",
		Kind: telemetry.HistogramKind,
	}, "latency_milliseconds")
}

// TestMetricNameGaugeRatio verifies the Prometheus name of the utilization instrument.
func TestMetricNameGaugeRatio(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "utilization",
		Unit: "1",
		Kind: telemetry.GaugeKind,
	}, "utilization_ratio")
}

// TestMetricNameCounterUnitOne verifies the Prometheus name of the hits instrument.
func TestMetricNameCounterUnitOne(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "hits",
		Unit: "1",
		Kind: telemetry.CounterKind,
	}, "hits_total")
}

// TestMetricNameGaugeUnknownUnit verifies the Prometheus name of the odd instrument.
func TestMetricNameGaugeUnknownUnit(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "odd",
		Unit: "furlong",
		Kind: telemetry.GaugeKind,
	}, "odd")
}

// TestMetricNameGaugeSanitizesName verifies the Prometheus name of the my-app..metric instrument.
func TestMetricNameGaugeSanitizesName(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "my-app..metric",
		Kind: telemetry.GaugeKind,
	}, "my_app_metric")
}

// TestMetricNameGaugeLeadingDigit verifies the Prometheus name of the 9lives instrument.
func TestMetricNameGaugeLeadingDigit(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "", telemetry.InstrumentSpec{
		Name: "9lives",
		Kind: telemetry.GaugeKind,
	}, "_9lives")
}

// TestMetricNameCounterNamespace verifies the Prometheus name of the orders.created instrument.
func TestMetricNameCounterNamespace(t *testing.T) {
	t.Parallel()

	requireMetricName(t, "shop", telemetry.InstrumentSpec{
		Name: "orders.created",
		Kind: telemetry.CounterKind,
	}, "shop_orders_created_total")
}

// requireLabelName verifies labelName translates key to want.
func requireLabelName(t *testing.T, key, want string) {
	t.Helper()

	if got := LabelName(key); got != want {
		t.Errorf("labelName(%q) = %q, want %q", key, got, want)
	}
}

// TestLabelNameDottedMethod verifies the Prometheus label name of http.request.method.
func TestLabelNameDottedMethod(t *testing.T) {
	t.Parallel()

	requireLabelName(t, "http.request.method", "http_request_method")
}

// TestLabelNameDottedStatusCode verifies the Prometheus label name of http.response.status_code.
func TestLabelNameDottedStatusCode(t *testing.T) {
	t.Parallel()

	requireLabelName(t, "http.response.status_code", "http_response_status_code")
}

// TestLabelNameColon verifies the Prometheus label name of a:b.
func TestLabelNameColon(t *testing.T) {
	t.Parallel()

	requireLabelName(t, "a:b", "a_b")
}

// TestLabelNameLeadingUnderscores verifies the Prometheus label name of __x.
func TestLabelNameLeadingUnderscores(t *testing.T) {
	t.Parallel()

	requireLabelName(t, "__x", "_x")
}

// TestLabelNamePlain verifies the Prometheus label name of plain.
func TestLabelNamePlain(t *testing.T) {
	t.Parallel()

	requireLabelName(t, "plain", "plain")
}
