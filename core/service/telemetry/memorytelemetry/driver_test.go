package memorytelemetry_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// start returns a started telemetry over a new driver.
func start(t *testing.T, opts ...func(*memorytelemetry.Builder)) (*memorytelemetry.Driver, *telemetry.Telemetry) {
	t.Helper()

	driver := memorytelemetry.New(opts...)
	facade := telemetry.New(driver)

	if err := facade.Start(t.Context(), telemetry.Setup{
		Resource: telemetry.Resource{
			Service: "svc",
			Attrs:   []telemetry.Attr{telemetry.String("k", "v")},
		},
	}); err != nil {
		t.Fatal(err)
	}

	return driver, facade
}

// TestConformance runs the telemetry conformance suite.
func TestConformance(t *testing.T) {
	t.Parallel()

	telemetrytest.Run(t, func(*testing.T) telemetry.Lifecycle {
		return memorytelemetry.New()
	})
}

// TestSnapshotsAreImmutable checks that mutating a snapshot changes nothing.
func TestSnapshotsAreImmutable(t *testing.T) {
	t.Parallel()

	driver, facade := start(t)

	_, span := facade.Tracer("t").Start(t.Context(), "s", telemetry.Attrs(telemetry.String("k", "v")))
	span.AddEvent("event", telemetry.String("e", "v"))
	span.End()
	facade.Meter("t").Counter("c", telemetry.Keys("a")).Add(t.Context(), 1, telemetry.String("a", "x"))

	spans := driver.Spans()
	spans[0].Attrs[0] = telemetry.String("k", "changed")
	spans[0].Events[0].Attrs[0] = telemetry.String("e", "changed")
	spans[0].Name = "changed"

	metrics := driver.Metrics()
	metrics[0].Keys[0] = "changed"
	metrics[0].Points[0].Values[0] = "changed"

	again := driver.Spans()[0]

	if got, _ := again.Attr("k"); again.Name != "s" || got.AsString() != "v" || again.Events[0].Attrs[0].AsString() != "v" {
		t.Errorf("span snapshot was mutated: %+v", again)
	}

	metric := driver.Metrics()[0]

	if _, ok := metric.Point("x"); !ok || metric.Keys[0] != "a" {
		t.Errorf("metric snapshot was mutated: %+v", metric)
	}
}

// TestReset checks that Reset keeps instruments live.
func TestReset(t *testing.T) {
	t.Parallel()

	driver, facade := start(t)
	counter := facade.Meter("t").Counter("c")

	_, span := facade.Tracer("t").Start(t.Context(), "s")
	span.End()
	counter.Add(t.Context(), 1)

	driver.Reset()

	if len(driver.Spans()) != 0 || len(driver.Metrics()[0].Points) != 0 {
		t.Fatal("Reset left data behind")
	}

	counter.Add(t.Context(), 4)

	if point, _ := driver.Metrics()[0].Point(); point.Sum != 4 {
		t.Fatalf("instrument stopped recording after Reset: %+v", point)
	}
}

// TestResource checks that Start stores a copy of the resource.
func TestResource(t *testing.T) {
	t.Parallel()

	driver, _ := start(t)

	resource := driver.Resource()

	if resource.Service != "svc" || len(resource.Attrs) != 1 {
		t.Fatalf("resource = %+v", resource)
	}
}

// TestConcurrentUse exercises every operation under the race detector.
func TestConcurrentUse(t *testing.T) {
	t.Parallel()

	driver, facade := start(t, func(store *memorytelemetry.Builder) {
		store.MaxSpans(10)
	})

	tracer := facade.Tracer("t")
	counter := facade.Meter("t").Counter("c", telemetry.Keys("k"))

	var group sync.WaitGroup

	for worker := range 8 {
		group.Go(func() {
			for range 50 {
				ctx, span := tracer.Start(t.Context(), "root")
				_, child := tracer.Start(ctx, "child")
				child.SetAttributes(telemetry.Int("worker", worker))
				child.End()
				span.End()
				counter.Add(ctx, 1, telemetry.Int("k", worker))

				_ = driver.Spans()

				_ = driver.Metrics()
			}
		})
	}

	group.Wait()

	if point, _ := driver.Metrics()[0].Point("0"); point.Sum != 50 {
		t.Fatalf("worker 0 counted %v, want 50", point.Sum)
	}
}

// TestStopWithCanceledContextReleasesRecording validates the memory driver's
// existing cleanup contract. An expired flush budget must not leave the owned
// recording state active, and this in-process driver has no exporter I/O to wait
// for or resources whose release depends on a live context.
func TestStopWithCanceledContextReleasesRecording(t *testing.T) {
	t.Parallel()

	driver := memorytelemetry.New()

	if err := driver.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(t.Context()); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	if driver.Enabled(t.Context(), slog.LevelInfo) {
		t.Fatal("log recording remained enabled after canceled-context Stop")
	}

	_, recorder := driver.StartSpan(t.Context(), telemetry.SpanStart{Name: "late"})

	if recorder.Recording() || recorder.Context().IsValid() {
		t.Fatal("trace recording remained active after canceled-context Stop")
	}

	recorder.End()
}

// TestEmptySnapshotRepresentations protects the original distinction between
// allocated empty span/metric snapshots and nil log snapshots.
func TestEmptySnapshotRepresentations(t *testing.T) {
	t.Parallel()

	driver := memorytelemetry.New()

	if driver.Spans() == nil || driver.Metrics() == nil {
		t.Fatal("span and metric snapshots must retain allocated empty slices")
	}

	if driver.Logs() != nil || driver.LogsAfter(0) != nil {
		t.Fatal("empty log snapshots must remain nil")
	}
}

// ExampleDriver records a span and reads it back.
func ExampleDriver() {
	driver := memorytelemetry.New()
	facade := telemetry.New(driver)

	if err := facade.Start(context.Background(), telemetry.Setup{}); err != nil {
		fmt.Println(err)

		return
	}

	defer func() {
		// The concrete memory driver never fails to stop.
		_ = facade.Stop(context.Background())
	}()

	_, span := facade.Tracer("example").Start(context.Background(), "greeting send")
	span.End()

	spans := driver.Spans()
	fmt.Println(len(spans), spans[0].Name)
	// Output: 1 greeting send
}
