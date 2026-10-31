package telemetrytest

import (
	"log/slog"
	"slices"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

// Span returns the first finished span named name.
// Absence returns a zero snapshot and false; ongoing spans do not match.
//
//	fake := telemetrytest.New()
//	_, span := fake.Telemetry(t).Tracer("example.com/cache").Start(t.Context(), "cache get")
//	span.End()
//	if recorded, ok := fake.Span("cache get"); ok {
//		fmt.Println(recorded.Name)
//	}
func (fake *Fake) Span(name string) (memorytelemetry.Span, bool) {
	for _, span := range fake.Spans() {
		if span.Name == name {
			return span, true
		}
	}

	return memorytelemetry.Span{}, false
}

// AssertSpan fails t unless a finished span named name has every attr. Extra
// attributes on the span are allowed.
//
//	fake := telemetrytest.New()
//	_, span := fake.Telemetry(t).Tracer("example.com/cache").Start(t.Context(), "cache get",
//		telemetry.Attrs(telemetry.Bool("cached", true)))
//	span.End()
//	fake.AssertSpan(t, "cache get", telemetry.Bool("cached", true))
func (fake *Fake) AssertSpan(t testing.TB, name string, attrs ...telemetry.Attr) {
	t.Helper()

	if !slices.ContainsFunc(fake.Spans(), func(span memorytelemetry.Span) bool {
		return span.Name == name && hasAttrs(span, attrs)
	}) {
		t.Errorf("no finished span %q with attributes %v among %d spans", name, attrs, len(fake.Spans()))
	}
}

// AssertNoSpan fails t if a finished span is named name.
// This is a nonfatal testing assertion; it does not prevent future recordings.
//
//	fake := telemetrytest.New()
//	_ = fake.Telemetry(t)
//	fake.AssertNoSpan(t, "cache miss")
func (fake *Fake) AssertNoSpan(t testing.TB, name string) {
	t.Helper()

	if _, found := fake.Span(name); found {
		t.Errorf("unexpected finished span %q", name)
	}
}

// AssertSpanCount fails t unless exactly count spans finished.
// Count excludes currently open spans and those dropped by capture capacity.
//
//	fake := telemetrytest.New()
//	_, span := fake.Telemetry(t).Tracer("example.com/cache").Start(t.Context(), "cache get")
//	span.End()
//	fake.AssertSpanCount(t, 1)
func (fake *Fake) AssertSpanCount(t testing.TB, count int) {
	t.Helper()

	if got := len(fake.Spans()); got != count {
		t.Errorf("finished spans = %d, want %d", got, count)
	}
}

// AssertCounter fails t unless the counter or up-down counter name has total
// value want across the label combinations that contain every label.
//
//	fake := telemetrytest.New()
//	requests := fake.Telemetry(t).Meter("example.com/cache").Counter("requests", telemetry.Keys("result"))
//	requests.Add(t.Context(), 2, telemetry.String("result", "hit"))
//	fake.AssertCounter(t, "requests", 2, telemetry.String("result", "hit"))
func (fake *Fake) AssertCounter(t testing.TB, name string, want float64, labels ...telemetry.Attr) {
	t.Helper()

	got, found := fake.aggregate(name, labels, func(point memorytelemetry.Point) float64 {
		return point.Sum
	})

	if !found || got != want {
		t.Errorf("counter %q%v = %v (found %v), want %v", name, labels, got, found, want)
	}
}

// AssertHistogram fails t unless the histogram name recorded count values
// across the label combinations that contain every label.
//
//	fake := telemetrytest.New()
//	duration := fake.Telemetry(t).Meter("example.com/cache").Histogram("duration", telemetry.Keys("result"))
//	duration.Record(t.Context(), 0.25, telemetry.String("result", "hit"))
//	fake.AssertHistogram(t, "duration", 1, telemetry.String("result", "hit"))
func (fake *Fake) AssertHistogram(t testing.TB, name string, count uint64, labels ...telemetry.Attr) {
	t.Helper()

	got, found := fake.aggregate(name, labels, func(point memorytelemetry.Point) float64 {
		return float64(point.Count)
	})

	if !found || got != float64(count) {
		t.Errorf("histogram %q%v count = %v (found %v), want %d", name, labels, got, found, count)
	}
}

// AssertGauge fails t unless the gauge name has value want in the label
// combination that contains every label. With several matching combinations
// the values are summed.
//
//	fake := telemetrytest.New()
//	depth := fake.Telemetry(t).Meter("example.com/queue").Gauge("depth", telemetry.Keys("queue"))
//	depth.Set(t.Context(), 12, telemetry.String("queue", "default"))
//	fake.AssertGauge(t, "depth", 12, telemetry.String("queue", "default"))
func (fake *Fake) AssertGauge(t testing.TB, name string, want float64, labels ...telemetry.Attr) {
	t.Helper()

	got, found := fake.aggregate(name, labels, func(point memorytelemetry.Point) float64 {
		return point.Last
	})

	if !found || got != want {
		t.Errorf("gauge %q%v = %v (found %v), want %v", name, labels, got, found, want)
	}
}

// AssertLog fails t unless an exported record has level, message, and every
// attr. Attributes inside groups are matched by their dotted key.
//
//	fake := telemetrytest.New()
//	logger := slog.New(fake.Telemetry(t).Handler(slog.DiscardHandler))
//	logger.InfoContext(t.Context(), "operation completed", slog.Group("cache", "result", "hit"))
//	fake.AssertLog(t, slog.LevelInfo, "operation completed", slog.String("cache.result", "hit"))
func (fake *Fake) AssertLog(t testing.TB, level slog.Level, message string, attrs ...slog.Attr) {
	t.Helper()

	want := map[string]slog.Value{}

	for _, attr := range attrs {
		flatten(want, "", attr)
	}

	if !slices.ContainsFunc(fake.Logs(), func(entry Log) bool {
		return entry.Level == level && entry.Message == message && containsValues(entry.Attrs, want)
	}) {
		t.Errorf("no exported %v log %q with attributes %v among %d logs", level, message, attrs, len(fake.Logs()))
	}
}

// aggregate sums value over the points of the named instrument whose labels
// match, and reports whether any point matched.
func (fake *Fake) aggregate(name string, labels []telemetry.Attr, value func(memorytelemetry.Point) float64) (float64, bool) {
	var total float64

	found := false

	for _, metric := range fake.Metrics() {
		if metric.Name != name {
			continue
		}

		for _, point := range metric.Points {
			if matchesLabels(metric, point, labels) {
				total += value(point)
				found = true
			}
		}
	}

	return total, found
}

// matchesLabels reports whether point has every label. A label whose key the
// instrument did not declare never matches.
func matchesLabels(metric memorytelemetry.Metric, point memorytelemetry.Point, labels []telemetry.Attr) bool {
	for _, label := range labels {
		index := slices.Index(metric.Keys, label.Key())

		if index < 0 || point.Values[index] != label.Text() {
			return false
		}
	}

	return true
}

// hasAttrs reports whether span has every attr.
func hasAttrs(span memorytelemetry.Span, attrs []telemetry.Attr) bool {
	for _, want := range attrs {
		got, ok := span.Attr(want.Key())

		if !ok || !got.Equal(want) {
			return false
		}
	}

	return true
}

// containsValues reports whether have holds every entry of want.
func containsValues(have, want map[string]slog.Value) bool {
	for key, value := range want {
		got, ok := have[key]

		if !ok || !got.Equal(value) {
			return false
		}
	}

	return true
}
