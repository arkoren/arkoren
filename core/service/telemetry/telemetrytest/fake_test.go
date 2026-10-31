package telemetrytest_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestNewForwardsConstructionOnce checks the fake's component-construction
// seam, not the separate telemetrytest.Run conformance-suite options.
func TestNewForwardsConstructionOnce(t *testing.T) {
	t.Parallel()

	var retained *memorytelemetry.Builder
	calls := 0
	fake := telemetrytest.New(func(store *memorytelemetry.Builder) {
		retained = store
		calls++
		store.MaxSpans(9)
	}, func(store *memorytelemetry.Builder) {
		calls++

		if store != retained {
			t.Fatal("forwarding created a second draft")
		}

		store.MaxSpans(calls)
	})
	retained.MaxSpans(0)

	facade := fake.Telemetry(t)

	for range 3 {
		_, span := facade.Tracer("test").Start(t.Context(), "work")
		span.End()
	}

	if calls != 2 || len(fake.Spans()) != 2 {
		t.Fatalf("calls=%d, spans=%d", calls, len(fake.Spans()))
	}
}

// TestNewNilCallbackPanics preserves the underlying memory component's
// direct invocation contract instead of treating nil as a suite option.
func TestNewNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	telemetrytest.New(nil)
}

// TestNewEmptyCallbackExpansions checks that forwarding preserves omission
// and default retention rather than synthesizing a nil policy entry.
func TestNewEmptyCallbackExpansions(t *testing.T) {
	t.Parallel()

	var omitted []func(*memorytelemetry.Builder)

	for _, configure := range [][]func(*memorytelemetry.Builder){nil, omitted, {}, {func(store *memorytelemetry.Builder) {
	}}} {
		fake := telemetrytest.New(configure...)
		_, span := fake.Telemetry(t).Tracer("test").Start(t.Context(), "work")
		span.End()

		if len(fake.Spans()) != 1 {
			t.Fatal("empty callback expansion changed default retention")
		}
	}
}

// TestNewCallbackPanicPropagates ensures forwarding does not catch or replay
// user policies while assembling the fake's independent runtime recorders.
func TestNewCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "policy failed" {
			t.Fatalf("panic=%v", got)
		}
	}()
	telemetrytest.New(func(store *memorytelemetry.Builder) {
		panic("policy failed")
	})
}

// populated returns a fake holding one span, one metric of each kind, and
// one log.
func populated(t *testing.T) *telemetrytest.Fake {
	t.Helper()

	fake := telemetrytest.New()
	facade := fake.Telemetry(t)
	meter := facade.Meter("test")

	ctx, span := facade.Tracer("test").Start(t.Context(), "operation",
		telemetry.Attrs(telemetry.String("k", "v"), telemetry.Int("n", 1)))
	span.End()

	meter.Counter("requests", telemetry.Keys("route", "status")).Add(ctx, 2, telemetry.String("route", "/a"), telemetry.Int("status", 200))
	meter.Counter("requests", telemetry.Keys("route", "status")).Add(ctx, 3, telemetry.String("route", "/b"), telemetry.Int("status", 500))
	meter.Histogram("duration").Record(ctx, 0.25)
	meter.Gauge("depth").Set(ctx, 9)

	slog.New(facade.Handler(slog.DiscardHandler)).With("a", 1).ErrorContext(ctx, "failed", "b", 2)

	return fake
}

// TestFakeReset checks that Reset clears every signal.
func TestFakeReset(t *testing.T) {
	t.Parallel()

	fake := populated(t)
	fake.Reset()

	fake.AssertSpanCount(t, 0)
	fake.AssertNoSpan(t, "operation")

	if len(fake.Logs()) != 0 {
		t.Fatal("logs survived Reset")
	}
}

// TestFakeTelemetryIsShared checks that repeated calls return one value.
func TestFakeTelemetryIsShared(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	first, second := fake.Telemetry(t), fake.Telemetry(t)

	if first != second {
		t.Fatal("Telemetry returned different values")
	}
}

// TestFakeIsInactiveUntilStarted checks that an unstarted fake records
// nothing, including logs.
func TestFakeIsInactiveUntilStarted(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	record := slog.NewRecord(time.Time{}, slog.LevelError, "early", 0)

	if err := fake.Export(t.Context(), record); err != nil {
		t.Fatal(err)
	}

	if len(fake.Logs()) != 0 {
		t.Fatal("unstarted fake exported a log")
	}
}

// ExampleFake records a span and a counter and reads them back.
func ExampleFake() {
	fake := telemetrytest.New()
	facade := telemetry.New(fake)

	if err := facade.Start(context.Background(), telemetry.Setup{}); err != nil {
		fmt.Println(err)

		return
	}

	defer func() {
		// The fake's memory driver never fails to stop.
		_ = facade.Stop(context.Background())
	}()

	ctx, span := facade.Tracer("example").Start(context.Background(), "payment charge",
		telemetry.Attrs(telemetry.String("currency", "EUR")))
	facade.Meter("example").Counter("charges", telemetry.Keys("currency")).Add(ctx, 1, telemetry.String("currency", "EUR"))
	span.End()

	_, found := fake.Span("payment charge")
	fmt.Println(found, len(fake.Spans()), fake.Metrics()[0].Name)
	// Output: true 1 charges
}
