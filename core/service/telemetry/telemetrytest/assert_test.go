package telemetrytest_test

import (
	"fmt"
	"log/slog"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestDocumentedAssertions exercises the live public test-helper examples with
// a real testing.T. Assertions verify emitted data, not documentation structure
// or source counts, and the helper owns deterministic in-process cleanup.
func TestDocumentedAssertions(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	service := fake.Telemetry(t)
	ctx, span := service.Tracer("example.com/cache").Start(t.Context(), "cache get",
		telemetry.Attrs(telemetry.Bool("cached", true)))
	meter := service.Meter("example.com/cache")
	meter.Counter("requests", telemetry.Keys("result")).Add(ctx, 2, telemetry.String("result", "hit"))
	meter.Histogram("duration", telemetry.Keys("result")).Record(ctx, 0.25, telemetry.String("result", "hit"))
	meter.Gauge("depth", telemetry.Keys("queue")).Set(ctx, 12, telemetry.String("queue", "default"))

	logger := slog.New(service.Handler(slog.DiscardHandler))
	logger.InfoContext(ctx, "operation completed", slog.Group("cache", "result", "hit"))
	span.End()

	fake.AssertSpan(t, "cache get", telemetry.Bool("cached", true))
	fake.AssertNoSpan(t, "cache miss")
	fake.AssertSpanCount(t, 1)
	fake.AssertCounter(t, "requests", 2, telemetry.String("result", "hit"))
	fake.AssertHistogram(t, "duration", 1, telemetry.String("result", "hit"))
	fake.AssertGauge(t, "depth", 12, telemetry.String("queue", "default"))
	fake.AssertLog(t, slog.LevelInfo, "operation completed", slog.String("cache.result", "hit"))

	if recorded, ok := fake.Span("cache get"); !ok || recorded.Name != "cache get" {
		t.Fatalf("Span lookup = %+v, found %v", recorded, ok)
	}

	logs := fake.Logs()

	if len(logs) != 1 || logs[0].TraceID == "" || logs[0].SpanID == "" {
		t.Fatalf("correlated log output = %+v", logs)
	}
}

// recordingTB captures failures instead of failing the real test.
type recordingTB struct {
	// TB forwards ordinary test behavior while Errorf captures assertion failures.
	testing.TB

	// failures holds the formatted Errorf messages.
	failures []string
}

// Errorf captures formatted assertion failures without failing the enclosing test.
func (recorder *recordingTB) Errorf(format string, args ...any) {
	recorder.failures = append(recorder.failures, fmt.Sprintf(format, args...))
}

// TestAssertionsPass checks that true statements pass.
func TestAssertionsPass(t *testing.T) {
	t.Parallel()

	fake := populated(t)

	fake.AssertSpan(t, "operation", telemetry.String("k", "v"))
	fake.AssertSpan(t, "operation")
	fake.AssertNoSpan(t, "other")
	fake.AssertSpanCount(t, 1)
	fake.AssertCounter(t, "requests", 5)
	fake.AssertCounter(t, "requests", 2, telemetry.String("route", "/a"))
	fake.AssertCounter(t, "requests", 3, telemetry.Int("status", 500))
	fake.AssertHistogram(t, "duration", 1)
	fake.AssertGauge(t, "depth", 9)
	fake.AssertLog(t, slog.LevelError, "failed", slog.Int("a", 1), slog.Int("b", 2))

	if span, ok := fake.Span("operation"); !ok || span.Name != "operation" {
		t.Fatalf("Span = %+v, %v", span, ok)
	}

	if len(fake.Logs()) != 1 {
		t.Fatalf("logs = %d", len(fake.Logs()))
	}
}

// TestAssertionsFail checks that false statements fail.
func TestAssertionsFail(t *testing.T) {
	t.Parallel()

	fake := populated(t)
	tb := &recordingTB{TB: t}

	fake.AssertSpan(tb, "missing")
	fake.AssertSpan(tb, "operation", telemetry.String("k", "other"))
	fake.AssertNoSpan(tb, "operation")
	fake.AssertSpanCount(tb, 2)
	fake.AssertCounter(tb, "requests", 4)
	fake.AssertCounter(tb, "requests", 2, telemetry.String("undeclared", "x"))
	fake.AssertCounter(tb, "absent", 0)
	fake.AssertHistogram(tb, "duration", 2)
	fake.AssertGauge(tb, "depth", 1)
	fake.AssertLog(tb, slog.LevelInfo, "failed")
	fake.AssertLog(tb, slog.LevelError, "failed", slog.Int("a", 2))

	if len(tb.failures) != 11 {
		t.Fatalf("%d assertions failed, want 11: %q", len(tb.failures), tb.failures)
	}
}
