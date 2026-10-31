package memorytelemetry_test

import (
	"reflect"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
)

// TestTupleIdentityMemoryScopesRemainDistinct registers directly through
// Instrument so a facade binding cannot mask the driver's scope/name identity.
// Snapshots must retain separate aggregates in first-registration order and
// repeated registrations must continue the original aggregate.
func TestTupleIdentityMemoryScopesRemainDistinct(t *testing.T) {
	t.Parallel()

	driver, service := start(t)
	t.Cleanup(func() {
		if err := service.Stop(t.Context()); err != nil {
			t.Error(err)
		}
	})

	firstSpec := telemetry.InstrumentSpec{
		Scope: "checkout\x00worker",
		Name:  "requests",
		Kind:  telemetry.CounterKind,
	}

	first, err := driver.Instrument(firstSpec)

	if err != nil {
		t.Fatal(err)
	}

	second, err := driver.Instrument(telemetry.InstrumentSpec{
		Scope: "checkout",
		Name:  "worker\x00requests",
		Kind:  telemetry.CounterKind,
	})

	if err != nil {
		t.Fatal(err)
	}

	again, err := driver.Instrument(firstSpec)

	if err != nil {
		t.Fatal(err)
	}

	first.Record(t.Context(), 2, nil)
	second.Record(t.Context(), 3, nil)
	again.Record(t.Context(), 5, nil)

	want := []memorytelemetry.Metric{
		{
			Scope:  "checkout\x00worker",
			Name:   "requests",
			Kind:   telemetry.CounterKind,
			Points: []memorytelemetry.Point{{Count: 2, Sum: 7, Last: 5}},
		},
		{
			Scope:  "checkout",
			Name:   "worker\x00requests",
			Kind:   telemetry.CounterKind,
			Points: []memorytelemetry.Point{{Count: 1, Sum: 3, Last: 3}},
		},
	}

	if got := driver.Metrics().Items(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Metrics = %#v, want %#v", got, want)
	}
}

// TestTupleIdentityMemoryLabelsRespectSeriesLimit records through Meter and
// checks public snapshots. NUL-bearing label tuples must occupy separate series
// in first-recording order; reaching MaxSeries must still allow updates to
// either retained tuple while dropping a new one.
func TestTupleIdentityMemoryLabelsRespectSeriesLimit(t *testing.T) {
	t.Parallel()

	driver, service := start(t, func(store *memorytelemetry.Builder) {
		store.MaxSeries(2)
	})
	t.Cleanup(func() {
		if err := service.Stop(t.Context()); err != nil {
			t.Error(err)
		}
	})

	counter := service.Meter("checkout").Counter("requests", telemetry.Keys("region", "result"))
	counter.Add(t.Context(), 2, telemetry.String("region", "west\x00api"), telemetry.String("result", "ok"))
	counter.Add(t.Context(), 3, telemetry.String("region", "west"), telemetry.String("result", "api\x00ok"))
	counter.Add(t.Context(), 11, telemetry.String("region", "overflow"))
	counter.Add(t.Context(), 5, telemetry.String("region", "west\x00api"), telemetry.String("result", "ok"))
	counter.Add(t.Context(), 7, telemetry.String("region", "west"), telemetry.String("result", "api\x00ok"))

	want := []memorytelemetry.Point{
		{Values: []string{"west\x00api", "ok"}, Count: 2, Sum: 7, Last: 5},
		{Values: []string{"west", "api\x00ok"}, Count: 2, Sum: 10, Last: 7},
	}

	metrics := driver.Metrics()

	if len(metrics) != 1 {
		t.Fatalf("instruments = %d, want 1", len(metrics))
	}

	if !reflect.DeepEqual(metrics[0].Points, want) {
		t.Fatalf("Points = %#v, want %#v", metrics[0].Points, want)
	}
}

// TestMetricPointOwnsReturnedValues uses recorded metric output rather than a
// private aggregation map. Editing one returned point must not mutate the
// metric snapshot used for later lookups of the original label tuple.
func TestMetricPointOwnsReturnedValues(t *testing.T) {
	t.Parallel()

	driver, service := start(t)
	t.Cleanup(func() {
		if err := service.Stop(t.Context()); err != nil {
			t.Error(err)
		}
	})

	service.Meter("test").Counter("requests", telemetry.Keys("region")).Add(t.Context(), 2,
		telemetry.String("region", "eu"))

	metrics := driver.Metrics()

	if len(metrics) != 1 {
		t.Fatalf("metrics = %d, want 1", len(metrics))
	}

	point, ok := metrics[0].Point("eu")

	if !ok {
		t.Fatal("recorded point not found")
	}

	point.Values[0] = "changed"

	if again, ok := metrics[0].Point("eu"); !ok || again.Sum != 2 {
		t.Fatalf("original point after result edit = %+v, found %v", again, ok)
	}
}

// TestMetricSnapshotOperations checks creation order, grouping, and copied
// aggregate state across later recordings and Reset, including label values.
func TestMetricSnapshotOperations(t *testing.T) {
	t.Parallel()

	driver, facade := start(t)
	meter := facade.Meter("snapshot")
	first := meter.Counter("zeta", telemetry.Keys("region"))
	first.Add(t.Context(), 2, telemetry.String("region", "west"))
	meter.Counter("alpha").Add(t.Context(), 1)

	snapshot := driver.Metrics()
	grouped := snapshot.GroupBy(func(_ int, metric memorytelemetry.Metric) string {
		return metric.Scope
	})

	if grouped["snapshot"].Len() != 2 || snapshot[0].Name != "zeta" {
		t.Fatalf("metric order = %+v", snapshot)
	}

	first.Add(t.Context(), 3, telemetry.String("region", "west"))
	driver.Reset()

	if snapshot[0].Points[0].Sum != 2 || snapshot[0].Points[0].Values[0] != "west" {
		t.Fatalf("metric snapshot changed: %+v", snapshot)
	}

	snapshot[0].Keys[0] = "changed"

	if driver.Metrics()[0].Keys[0] != "region" {
		t.Fatal("snapshot changed metric registration")
	}
}

// TestSeriesLimit checks that new label combinations beyond the cap are
// dropped while existing ones keep counting.
func TestSeriesLimit(t *testing.T) {
	t.Parallel()

	driver, facade := start(t, func(store *memorytelemetry.Builder) {
		store.MaxSeries(2)
	})

	counter := facade.Meter("t").Counter("c", telemetry.Keys("k"))

	for _, value := range []string{"a", "b", "c", "a"} {
		counter.Add(t.Context(), 1, telemetry.String("k", value))
	}

	metric := driver.Metrics()[0]

	if len(metric.Points) != 2 {
		t.Fatalf("points = %+v, want 2", metric.Points)
	}

	if point, _ := metric.Point("a"); point.Sum != 2 {
		t.Errorf("a = %+v, want sum 2", point)
	}
}
