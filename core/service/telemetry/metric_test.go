package telemetry_test

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestTupleIdentityFacadeScopesRemainDistinct exercises Meter through the
// MetricDriver boundary. Embedded NUL bytes must not merge different scope/name
// pairs, while repeated declarations of the exact pair keep one binding.
func TestTupleIdentityFacadeScopesRemainDistinct(t *testing.T) {
	t.Parallel()

	driver := newStub("metrics", &callLog{})
	service := telemetry.New(driver)

	if err := service.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := service.Stop(t.Context()); err != nil {
			t.Error(err)
		}
	})

	first := service.Meter("checkout\x00worker").Counter("requests")
	second := service.Meter("checkout").Counter("worker\x00requests")
	first.Add(t.Context(), 2)
	second.Add(t.Context(), 3)
	service.Meter("checkout\x00worker").Counter("requests").Add(t.Context(), 5)

	if len(driver.specs) != 2 {
		t.Fatalf("Instrument calls = %d, want two distinct bindings", len(driver.specs))
	}

	if driver.specs[0].Scope != "checkout\x00worker" || driver.specs[0].Name != "requests" {
		t.Fatalf("first declaration = %+v", driver.specs[0])
	}

	if driver.specs[1].Scope != "checkout" || driver.specs[1].Name != "worker\x00requests" {
		t.Fatalf("second declaration = %+v", driver.specs[1])
	}

	if len(driver.records) != 3 {
		t.Fatalf("recordings = %d, want 3", len(driver.records))
	}
}

// schemaObserver retains read-only measurement metadata at the public observer
// seam. The embedded observer supplies unrelated span and log notifications.
type schemaObserver struct {
	// countingObserver provides the mutex and the remaining Observer methods.
	*countingObserver

	// spec is the most recently delivered immutable instrument declaration.
	spec telemetry.InstrumentSpec
}

// Measured captures the declaration without modifying its shared containers.
// The observer mutex makes capture safe if the facade records concurrently.
func (observer *schemaObserver) Measured(spec telemetry.InstrumentSpec, _ float64, _ []string) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.spec = spec
}

// TestInstrumentMetadataIsOwnedByBackend records through a backend that
// rewrites its own declared keys. The facade's label filtering and observer
// metadata must retain the original independent instrument declaration.
func TestInstrumentMetadataIsOwnedByBackend(t *testing.T) {
	t.Parallel()

	driver := &instrumentHookDriver{stubDriver: newStub("metrics", &callLog{})}
	service := startTelemetry(t, driver)
	observer := &schemaObserver{countingObserver: &countingObserver{}}
	service.Observe(observer)

	driver.declare = func(spec telemetry.InstrumentSpec) {
		spec.Keys[0] = "native"
		spec.Buckets[0] = 99
	}

	histogram := service.Meter("test").Histogram("duration", telemetry.Keys("region"), telemetry.Buckets(1, 2))
	histogram.Record(t.Context(), 1, telemetry.String("region", "eu"))

	if len(driver.records) != 1 {
		t.Fatalf("recordings = %d, want 1", len(driver.records))
	}

	if got := driver.records[0].values[0]; got != "eu" {
		t.Fatalf("recorded region = %q, want eu", got)
	}

	if observer.spec.Keys[0] != "region" || observer.spec.Buckets[0] != 1 {
		t.Fatalf("observer metadata changed with backend input: %+v", observer.spec)
	}

	service.Meter("test").Histogram("duration", telemetry.Keys("region"), telemetry.Buckets(1, 2)).Record(t.Context(), 2)

	if len(driver.specs) != 1 || len(driver.records) != 2 {
		t.Fatalf("declarations = %d, recordings = %d; want 1, 2", len(driver.specs), len(driver.records))
	}
}

// TestInstrumentOptionDraftCannotMutatePublishedSchema retains a public
// InstrumentOption's draft, then edits its keys and buckets after publication.
// Recorded labels and observer declarations must retain the normalized original.
func TestInstrumentOptionDraftCannotMutatePublishedSchema(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	service := fake.Telemetry(t)
	observer := &schemaObserver{countingObserver: &countingObserver{}}
	service.Observe(observer)
	var retained *telemetry.InstrumentSpec
	histogram := service.Meter("test").Histogram("duration", func(definition *telemetry.InstrumentSpec) {
		retained = definition
		definition.Keys = []string{"region"}
		definition.Buckets = []float64{1, 2}
	})

	retained.Keys[0] = "changed"
	retained.Buckets[0] = 99
	histogram.Record(t.Context(), 1, telemetry.String("region", "eu"))

	if observer.spec.Keys[0] != "region" || observer.spec.Buckets[0] != 1 {
		t.Fatalf("published instrument schema changed with retained option: %+v", observer.spec)
	}

	metrics := fake.Metrics()

	if len(metrics) != 1 {
		t.Fatalf("metrics = %d, want 1", len(metrics))
	}

	if point, ok := metrics[0].Point("eu"); !ok || point.Sum != 1 {
		t.Fatalf("recorded original label tuple = %+v, found %v", point, ok)
	}
}

// noncomparableMetricDriver is a valid value-type metric driver whose slice
// prevents interface equality. The embedded public driver methods retain their
// normal lifecycle and measurement behavior.
type noncomparableMetricDriver struct {
	// stubDriver records calls made through telemetry's public contracts.
	*stubDriver

	// names makes the driver noncomparable without changing its behavior.
	names []string
}

// instrumentHookDriver wraps the public metric-driver seam with a declaration
// callback. Tests use it to exercise instrumentation performed by a backend
// while another instrument is being created.
type instrumentHookDriver struct {
	// stubDriver retains declarations and recordings after the hook returns.
	*stubDriver

	// declare is invoked synchronously before the wrapped Instrument call.
	declare func(telemetry.InstrumentSpec)
}

// Instrument invokes the test's declaration hook before returning the ordinary
// recorder. The hook owns its synchronization and may record another metric.
func (driver *instrumentHookDriver) Instrument(spec telemetry.InstrumentSpec) (telemetry.InstrumentRecorder, error) {
	driver.declare(spec)

	return driver.stubDriver.Instrument(spec)
}

// TestMeterCachesNoncomparableDriver uses an ordinary value driver with no Go
// equality. Repeated recordings must neither panic nor recreate its instrument.
func TestMeterCachesNoncomparableDriver(t *testing.T) {
	t.Parallel()

	driver := noncomparableMetricDriver{stubDriver: newStub("metrics", &callLog{}), names: []string{"requests"}}
	service := startTelemetry(t, driver)
	counter := service.Meter("test").Counter("requests")
	counter.Add(t.Context(), 1)
	counter.Add(t.Context(), 2)

	if len(driver.specs) != 1 || len(driver.records) != 2 {
		t.Fatalf("declarations = %d, recordings = %d; want 1, 2", len(driver.specs), len(driver.records))
	}
}

// TestInstrumentDeclarationCanRecordAnotherInstrument exercises reentrancy at
// the public backend seam. Creating one instrument must not hold a facade-wide
// lock that prevents the backend from declaring and recording a different one.
func TestInstrumentDeclarationCanRecordAnotherInstrument(t *testing.T) {
	t.Parallel()

	driver := &instrumentHookDriver{stubDriver: newStub("metrics", &callLog{})}
	service := startTelemetry(t, driver)
	driver.declare = func(spec telemetry.InstrumentSpec) {
		if spec.Name == "outer" {
			service.Meter("test").Counter("inner").Add(t.Context(), 1)
		}
	}
	service.Meter("test").Counter("outer").Add(t.Context(), 2)

	if len(driver.specs) != 2 || len(driver.records) != 2 {
		t.Fatalf("declarations = %d, recordings = %d; want 2 each", len(driver.specs), len(driver.records))
	}
}

// TestMeasuredObserverIsNotNotifiedAfterDeclarationStopsTelemetry stops the
// facade from the public Instrument callback. When declaration returns, the
// recording must not notify observers while telemetry is disabled.
func TestMeasuredObserverIsNotNotifiedAfterDeclarationStopsTelemetry(t *testing.T) {
	t.Parallel()

	driver := &instrumentHookDriver{stubDriver: newStub("metrics", &callLog{})}
	service := startTelemetry(t, driver)
	observer := &countingObserver{}
	service.Observe(observer)

	driver.declare = func(telemetry.InstrumentSpec) {
		if err := service.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	service.Meter("test").Counter("requests").Add(t.Context(), 1)

	if observer.measures != 0 {
		t.Fatalf("measurements after Stop = %d, want 0", observer.measures)
	}
}

// TestDeclaredKeys checks filtering, ordering, filling, and text conversion.
func TestDeclaredKeys(t *testing.T) {
	t.Parallel()

	driver := newStub("m", &callLog{})
	facade := telemetry.New(driver)

	if err := facade.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	counter := facade.Meter("scope").Counter("requests",
		telemetry.Unit("{request}"), telemetry.Description("Requests."),
		telemetry.Keys("status", "route", "status", ""),
	)

	counter.Add(t.Context(), 2,
		telemetry.String("route", "/a"),
		telemetry.Int("status", 200),
		telemetry.String("undeclared", "x"),
		telemetry.Attr{},
		telemetry.Int("status", 201),
	)
	counter.Add(t.Context(), 1)
	counter.Add(t.Context(), -3)

	if len(driver.specs) != 1 {
		t.Fatalf("specs = %+v, want one instrument", driver.specs)
	}

	spec := driver.specs[0]

	if spec.Scope != "scope" || spec.Name != "requests" || spec.Unit != "{request}" ||
		spec.Description != "Requests." || spec.Kind != telemetry.CounterKind ||
		!slices.Equal(spec.Keys, []string{"status", "route"}) {
		t.Fatalf("spec = %+v", spec)
	}

	want := []record{
		{
			value:  2,
			values: []string{"201", "/a"},
		},
		{
			value:  1,
			values: []string{"", ""},
		},
	}

	if len(driver.records) != len(want) {
		t.Fatalf("records = %+v, want %+v", driver.records, want)
	}

	for index, got := range driver.records {
		if got.value != want[index].value || !slices.Equal(got.values, want[index].values) {
			t.Errorf("record %d = %+v, want %+v", index, got, want[index])
		}
	}
}

// TestBuckets checks bucket normalisation for histograms only.
func TestBuckets(t *testing.T) {
	t.Parallel()

	driver := newStub("m", &callLog{})
	facade := telemetry.New(driver)
	_ = facade.Start(t.Context(), telemetry.Setup{})
	meter := facade.Meter("scope")

	meter.Histogram("h", telemetry.Buckets(5, 1, 1, 0.5)).Record(t.Context(), 1)
	meter.Counter("c", telemetry.Buckets(1)).Add(t.Context(), 1)

	if got := driver.specs[0].Buckets; !slices.Equal(got, []float64{0.5, 1, 5}) {
		t.Errorf("histogram buckets = %v", got)
	}

	if got := driver.specs[1].Buckets; got != nil {
		t.Errorf("counter buckets = %v, want nil", got)
	}
}

// TestInstrumentsBeforeStart checks that instruments created early work after
// Start and record nothing before it.
func TestInstrumentsBeforeStart(t *testing.T) {
	t.Parallel()

	driver := newStub("m", &callLog{})
	facade := telemetry.New(driver)
	gauge := facade.Meter("scope").Gauge("depth")

	gauge.Set(t.Context(), 1)

	if len(driver.specs) != 0 || len(driver.records) != 0 {
		t.Fatal("instrument reached the driver before Start")
	}

	_ = facade.Start(t.Context(), telemetry.Setup{})
	gauge.Set(t.Context(), 2)

	if len(driver.records) != 1 || driver.records[0].value != 2 {
		t.Fatalf("records = %+v, want the value recorded after Start", driver.records)
	}

	_ = facade.Stop(t.Context())
	gauge.Set(t.Context(), 3)

	if len(driver.records) != 1 {
		t.Fatal("instrument recorded after Stop")
	}
}

// TestSharedInstrumentName checks deduplication and conflict handling.
func TestSharedInstrumentName(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	driver := newStub("m", &callLog{})
	facade := telemetry.New(driver)
	_ = facade.Start(t.Context(), telemetry.Setup{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	meter := facade.Meter("scope")

	first := meter.Counter("c", telemetry.Keys("a"))
	second := meter.Counter("c", telemetry.Keys("a"))
	conflicting := meter.Counter("c", telemetry.Keys("b"))

	first.Add(t.Context(), 1)
	second.Add(t.Context(), 1)
	conflicting.Add(t.Context(), 1)

	if len(driver.specs) != 1 || len(driver.records) != 2 {
		t.Fatalf("specs = %d, records = %d; want 1 shared instrument and 2 recordings", len(driver.specs), len(driver.records))
	}

	if !strings.Contains(logs.String(), "redefined") {
		t.Fatalf("no warning logged for the conflicting instrument: %q", logs.String())
	}
}

// TestRejectedInstrument checks that a driver error disables one instrument.
func TestRejectedInstrument(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	driver := newStub("m", &callLog{})
	driver.rejectInstrument = true
	facade := telemetry.New(driver)
	_ = facade.Start(t.Context(), telemetry.Setup{Logger: slog.New(slog.NewTextHandler(&logs, nil))})

	facade.Meter("scope").Counter("c").Add(t.Context(), 1)

	if !strings.Contains(logs.String(), "rejected") {
		t.Fatalf("no warning logged: %q", logs.String())
	}
}

// TestEmptyInstrumentName checks that an unnamed instrument is a no-op.
func TestEmptyInstrumentName(t *testing.T) {
	t.Parallel()

	driver := newStub("m", &callLog{})
	facade := telemetry.New(driver)
	_ = facade.Start(t.Context(), telemetry.Setup{})

	facade.Meter("scope").Counter("").Add(t.Context(), 1)

	if len(driver.specs) != 0 {
		t.Fatal("unnamed instrument reached the driver")
	}
}
