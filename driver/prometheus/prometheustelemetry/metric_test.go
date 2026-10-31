package prometheustelemetry

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"

	"github.com/prometheus/client_golang/prometheus"
)

// ExampleBuilder_MaxSeries changes only the per-instrument child budget while
// retaining the safe default bound on the number of instrument definitions.
func ExampleBuilder_MaxSeries() {
	driver := New(func(metrics *Builder) {
		metrics.MaxSeries(200)
	})
	// The driver retains at most 200 label combinations per instrument.
	_ = driver
}

// ExampleBuilder_MaxInstruments bounds collector definitions independently of the
// child budget, which still applies separately to every admitted instrument.
func ExampleBuilder_MaxInstruments() {
	driver := New(func(metrics *Builder) {
		metrics.MaxInstruments(100)
	})
	// The driver admits at most 100 instrument definitions.
	_ = driver
}

// TestPrometheusSeriesLimitBoundsRetainedChildren checks the safe default on
// actual collectors, not an auxiliary admission map. All recorder kinds must
// retain at most 1000 children despite repeated distinct label combinations.
func TestPrometheusSeriesLimitBoundsRetainedChildren(t *testing.T) {
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

	for _, kind := range []telemetry.InstrumentKind{
		telemetry.CounterKind, telemetry.UpDownCounterKind, telemetry.GaugeKind, telemetry.HistogramKind,
	} {
		recorder, err := driver.Instrument(telemetry.InstrumentSpec{
			Name: fmt.Sprintf("limited%s", kind),
			Kind: kind,
			Keys: []string{"destination"},
		})

		if err != nil {
			t.Fatal(err)
		}

		for index := range 1100 {
			recorder.Record(t.Context(), 1, []string{fmt.Sprintf("destination%d", index)})
		}
	}

	families, err := registry.Gather()

	if err != nil {
		t.Fatal(err)
	}

	if len(families) != 4 {
		t.Fatalf("retained %d metric families, want all 4 recorder kinds", len(families))
	}

	for _, found := range families {
		if got := len(found.GetMetric()); got != 1000 {
			t.Fatalf("%s retained %d children, want 1000", found.GetName(), got)
		}
	}
}

// TestConcurrentSeriesAdmissionBoundsRetainedChildren races first admission
// across multiple recorders of one instrument. A check followed by an unlocked
// child insertion would exceed the limit and fail the actual Gather assertion.
func TestConcurrentSeriesAdmissionBoundsRetainedChildren(t *testing.T) {
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

	for _, kind := range []telemetry.InstrumentKind{
		telemetry.CounterKind, telemetry.UpDownCounterKind, telemetry.GaugeKind, telemetry.HistogramKind,
	} {
		var group sync.WaitGroup

		for worker := range 16 {
			recorder, err := driver.Instrument(telemetry.InstrumentSpec{
				Name: fmt.Sprintf("parallel%s", kind),
				Kind: kind,
				Keys: []string{"destination"},
			})

			if err != nil {
				t.Fatal(err)
			}

			group.Go(func() {
				for index := range 100 {
					recorder.Record(t.Context(), 0.5, []string{fmt.Sprintf("%d.%d", worker, index)})
				}
			})
		}

		group.Wait()
	}

	families, err := registry.Gather()

	if err != nil {
		t.Fatal(err)
	}

	if len(families) != 4 {
		t.Fatalf("retained %d metric families, want all 4 recorder kinds", len(families))
	}

	for _, found := range families {
		if got := len(found.GetMetric()); got != 1000 {
			t.Fatalf("%s retained %d children, want 1000", found.GetName(), got)
		}
	}
}

// TestSeriesLimitPreservesValuesAndExemplars checks that overflow drops only new
// tuples, with no eviction, cumulative reset, or exemplar allocation on dropped
// children. Each histogram keeps its fixed bucket storage for admitted tuples.
func TestSeriesLimitPreservesValuesAndExemplars(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	}, func(metrics *Builder) {
		metrics.MaxSeries(1)
	})
	facade := telemetry.New(telemetry.Compose(telemetry.Traces(memorytelemetry.New()), telemetry.Metrics(driver)))

	if err := facade.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = facade.Stop(context.Background())
	})

	ctx, span := facade.Tracer("test").Start(t.Context(), "sampled")
	t.Cleanup(span.End)

	meter := facade.Meter("test")
	counter := meter.Counter("events", telemetry.Keys("destination"))
	histogram := meter.Histogram("duration", telemetry.Keys("destination"), telemetry.Buckets(1, 2))

	counter.Add(ctx, 2, telemetry.String("destination", "kept"))
	histogram.Record(ctx, 0.5, telemetry.String("destination", "kept"))

	for index := range 100 {
		counter.Add(ctx, 99, telemetry.String("destination", fmt.Sprintf("drop%d", index)))
		histogram.Record(ctx, 99, telemetry.String("destination", fmt.Sprintf("drop%d", index)))
	}

	counter.Add(ctx, 3, telemetry.String("destination", "kept"))
	histogram.Record(ctx, 1.5, telemetry.String("destination", "kept"))

	count := family(t, registry, "events_total")

	if len(count.GetMetric()) != 1 || count.GetMetric()[0].GetCounter().GetValue() != 5 {
		t.Fatalf("counter after overflow = %v, want one child with value 5", count)
	}

	if got := exemplarValue(count.GetMetric()[0].GetCounter().GetExemplar(), "trace_id"); got != span.Context().TraceID.String() {
		t.Fatalf("counter exemplar trace_id = %q", got)
	}

	duration := family(t, registry, "duration")

	if len(duration.GetMetric()) != 1 {
		t.Fatalf("histogram after overflow = %v, want one child", duration)
	}

	sample := duration.GetMetric()[0]

	if sample.GetLabel()[0].GetName() != "destination" || sample.GetLabel()[0].GetValue() != "kept" {
		t.Fatalf("admitted labels changed: %v", sample.GetLabel())
	}

	if sample.GetHistogram().GetSampleCount() != 2 || sample.GetHistogram().GetSampleSum() != 2 {
		t.Fatalf("histogram after overflow = %v, want count 2, sum 2", sample)
	}

	if len(sample.GetHistogram().GetBucket()) != 2 {
		t.Fatalf("bucket storage changed: %v", sample.GetHistogram().GetBucket())
	}

	if got := exemplarValue(sample.GetHistogram().GetBucket()[1].GetExemplar(), "span_id"); got != span.Context().SpanID.String() {
		t.Fatalf("histogram exemplar span_id = %q", got)
	}
}

// TestSeriesLimitSeparatesLabelTuples ensures delimiter-containing values cannot
// alias an admitted key and then allocate additional Prometheus vector children.
func TestSeriesLimitSeparatesLabelTuples(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	}, func(metrics *Builder) {
		metrics.MaxSeries(1)
	})
	startDriver(t, driver, telemetry.Setup{})

	recorder, err := driver.Instrument(telemetry.InstrumentSpec{
		Name: "tuple",
		Kind: telemetry.CounterKind,
		Keys: []string{"a", "b"},
	})

	if err != nil {
		t.Fatal(err)
	}

	recorder.Record(t.Context(), 2, []string{"a\x00b", "c"})
	recorder.Record(t.Context(), 99, []string{"a", "b\x00c"})
	recorder.Record(t.Context(), 3, []string{"a\x00b", "c"})

	found := family(t, registry, "tuple_total")

	if len(found.GetMetric()) != 1 || found.GetMetric()[0].GetCounter().GetValue() != 5 {
		t.Fatalf("tuple collision bypassed capacity: %v", found)
	}
}

// TestSeriesLimitInvalidRecordsDoNotConsumeCapacity verifies rejected counter
// increments and invalid labels do not displace the first legitimate series.
func TestSeriesLimitInvalidRecordsDoNotConsumeCapacity(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	}, func(metrics *Builder) {
		metrics.MaxSeries(1)
	})
	startDriver(t, driver, telemetry.Setup{})

	recorder, err := driver.Instrument(telemetry.InstrumentSpec{
		Name: "valid",
		Kind: telemetry.CounterKind,
		Keys: []string{"k"},
	})

	if err != nil {
		t.Fatal(err)
	}

	recorder.Record(t.Context(), -1, []string{"negative"})
	recorder.Record(t.Context(), 1, []string{"\xff"})
	recorder.Record(t.Context(), 1, nil)
	recorder.Record(t.Context(), 7, []string{"kept"})

	if got := family(t, registry, "valid_total"); len(got.GetMetric()) != 1 || got.GetMetric()[0].GetCounter().GetValue() != 7 {
		t.Fatalf("invalid record consumed series capacity: %v", got)
	}
}

// TestNegativeSeriesLimitsDisableRecording verifies negative configuration
// disables retention rather than enabling unlimited series. Unlabeled children
// are also dropped, while instrument definitions remain admitted normally.
func TestNegativeSeriesLimitsDisableRecording(t *testing.T) {
	t.Parallel()

	for _, limit := range []int{-1, -10} {
		registry := prometheus.NewRegistry()
		driver := New(func(metrics *Builder) {
			metrics.Registry(registry)
		}, func(metrics *Builder) {
			metrics.RuntimeMetrics(false)
		}, func(metrics *Builder) {
			metrics.TargetInfo(false)
		}, func(metrics *Builder) {
			metrics.MaxSeries(limit)
		})
		startDriver(t, driver, telemetry.Setup{})

		for _, kind := range []telemetry.InstrumentKind{
			telemetry.CounterKind, telemetry.UpDownCounterKind, telemetry.GaugeKind, telemetry.HistogramKind,
		} {
			recorder, err := driver.Instrument(telemetry.InstrumentSpec{
				Name: fmt.Sprintf("off%s", kind),
				Kind: kind,
			})

			if err != nil {
				t.Fatal(err)
			}

			recorder.Record(t.Context(), 1, nil)
		}

		families, err := registry.Gather()

		if err != nil || len(families) != 0 {
			t.Fatalf("limit %d: Gather = %v, %v; want empty", limit, families, err)
		}
	}
}

// TestInstrumentLimitBoundsVariableNamesAndKeys verifies empty collectors and
// their declarations cannot bypass the retained-state bound by changing names.
// Repeated admitted definitions still work when the declaration budget is full.
func TestInstrumentLimitBoundsVariableNamesAndKeys(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	}, func(metrics *Builder) {
		metrics.MaxInstruments(1)
	})
	startDriver(t, driver, telemetry.Setup{})

	spec := telemetry.InstrumentSpec{
		Name: "kept",
		Kind: telemetry.GaugeKind,
		Keys: []string{"kept"},
	}
	recorder, err := driver.Instrument(spec)

	if err != nil {
		t.Fatal(err)
	}

	for index := range 100 {
		_, err := driver.Instrument(telemetry.InstrumentSpec{
			Name: fmt.Sprintf("drop%d", index),
			Kind: telemetry.GaugeKind,
			Keys: []string{fmt.Sprintf("key%d", index)},
		})

		if err == nil {
			t.Fatal("new instrument admitted beyond limit")
		}
	}

	again, err := driver.Instrument(spec)

	if err != nil || again != recorder {
		t.Fatalf("existing instrument = %v, %v; want original recorder", again, err)
	}

	recorder.Record(t.Context(), 7, []string{"v"})

	if got := family(t, registry, "kept"); len(got.GetMetric()) != 1 || got.GetMetric()[0].GetGauge().GetValue() != 7 {
		t.Fatalf("admitted instrument stopped recording: %v", got)
	}

	if len(driver.owned) != 1 || len(driver.instruments) != 1 {
		t.Fatalf("retained %d collectors, %d instruments", len(driver.owned), len(driver.instruments))
	}
}

// TestNegativeInstrumentLimitsDisableAdmission verifies negative values disable
// collector registration rather than enabling unlimited definitions. Negative
// callback edits remain explicit while omitted fields retain finite defaults.
func TestNegativeInstrumentLimitsDisableAdmission(t *testing.T) {
	t.Parallel()

	for _, limit := range []int{-1, -10} {
		driver := New(func(metrics *Builder) {
			metrics.MaxInstruments(limit)
		})
		startDriver(t, driver, telemetry.Setup{})

		if _, err := driver.Instrument(telemetry.InstrumentSpec{
			Name: "off",
			Kind: telemetry.CounterKind,
		}); err == nil {
			t.Fatalf("instrument limit %d admitted a definition", limit)
		}
	}
}

// TestConstructionNormalizesZeroLimits verifies callbacks publish the finite
// limits the driver actually uses. Normalizing after all callbacks preserves
// their order while treating zero as the safe default.
func TestConstructionNormalizesZeroLimits(t *testing.T) {
	t.Parallel()

	for _, config := range []settings{
		newConfig(func(metrics *Builder) {
			metrics.MaxSeries(0)
		}, func(metrics *Builder) {
			metrics.MaxInstruments(0)
		}),
		newConfig(),
		newConfig(func(metrics *Builder) {
			metrics.MaxSeries(-1)
			metrics.MaxInstruments(-1)
		}, func(metrics *Builder) {
			metrics.MaxSeries(0)
		}, func(metrics *Builder) {
			metrics.MaxInstruments(0)
		}),
	} {
		if config.MaxSeries != 1000 || config.MaxInstruments != 1000 {
			t.Fatalf("zero limits resolved to %d series and %d instruments, want 1000 each", config.MaxSeries, config.MaxInstruments)
		}
	}
}

// TestRegistryWithOmittedLimitsRecordsMetrics verifies callers can configure
// a registry without naming the limit settings. The
// real telemetry facade must still record into that caller-owned registry.
func TestRegistryWithOmittedLimitsRecordsMetrics(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	driver := New(func(metrics *Builder) {
		metrics.Registerer(registry)
	})
	facade := telemetry.New(driver)

	if err := facade.Start(t.Context(), telemetry.Setup{}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = facade.Stop(context.Background())
	})

	facade.Meter("compatibility").Counter("events").Add(t.Context(), 7)

	found := family(t, registry, "events_total")

	if len(found.GetMetric()) != 1 || found.GetMetric()[0].GetCounter().GetValue() != 7 {
		t.Fatalf("registry-only configuration lost metrics: %v", found)
	}
}

// TestExplicitZeroLimitsBoundRetainedState verifies explicit zero edits select
// finite defaults rather than disabling metrics or allowing unlimited retention.
// Gather inspects actual children and collectors after both defaults saturate.
func TestExplicitZeroLimitsBoundRetainedState(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	},
		func(metrics *Builder) {
			metrics.MaxSeries(0)
		}, func(metrics *Builder) {
			metrics.MaxInstruments(0)
		})
	startDriver(t, driver, telemetry.Setup{})

	recorder, err := driver.Instrument(telemetry.InstrumentSpec{
		Name: "events",
		Kind: telemetry.CounterKind,
		Keys: []string{"destination"},
	})

	if err != nil {
		t.Fatal(err)
	}

	for index := range 1100 {
		recorder.Record(t.Context(), 1, []string{fmt.Sprintf("destination%d", index)})
	}

	if got := len(family(t, registry, "events_total").GetMetric()); got != 1000 {
		t.Fatalf("explicit zero retained %d children, want 1000", got)
	}

	for index := range 1100 {
		recorder, err := driver.Instrument(telemetry.InstrumentSpec{
			Name: fmt.Sprintf("gauge%d", index),
			Kind: telemetry.GaugeKind,
		})

		if err == nil {
			recorder.Record(t.Context(), 1, nil)
		}
	}

	families, err := registry.Gather()

	if err != nil || len(families) != 1000 {
		t.Fatalf("explicit zero retained %d collectors, %v; want 1000", len(families), err)
	}
}

// TestConcurrentInstrumentAdmissionBoundsCollectors checks the default bound
// against actual gathered collectors under concurrent declarations with variable
// names and keys. This covers metadata retained even before child recording.
func TestConcurrentInstrumentAdmissionBoundsCollectors(t *testing.T) {
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

	var group sync.WaitGroup

	for worker := range 16 {
		group.Go(func() {
			for index := range 100 {
				recorder, err := driver.Instrument(telemetry.InstrumentSpec{
					Name: fmt.Sprintf("instrument%d_%d", worker, index),
					Kind: telemetry.GaugeKind,
					Keys: []string{fmt.Sprintf("key%d", index)},
				})

				if err == nil {
					recorder.Record(t.Context(), 1, []string{"v"})
				}
			}
		})
	}

	group.Wait()

	families, err := registry.Gather()

	if err != nil || len(families) != 1000 {
		t.Fatalf("Gather retained %d collectors, %v; want 1000", len(families), err)
	}

	if len(driver.owned) != 1000 {
		t.Fatalf("registered %d collectors, want 1000", len(driver.owned))
	}
}

// TestStoppedRecordersDoNotAllocateChildren inspects the unregistered collectors
// themselves after Stop. An empty registry alone would hide late allocations
// or modifications through retained recorders of every metric kind.
func TestStoppedRecordersDoNotAllocateChildren(t *testing.T) {
	t.Parallel()

	driver := New(func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	})
	startDriver(t, driver, telemetry.Setup{})

	var recorders []telemetry.InstrumentRecorder

	for _, kind := range []telemetry.InstrumentKind{
		telemetry.CounterKind, telemetry.UpDownCounterKind, telemetry.GaugeKind, telemetry.HistogramKind,
	} {
		recorder, err := driver.Instrument(telemetry.InstrumentSpec{
			Name: fmt.Sprintf("stopped%s", kind),
			Kind: kind,
			Keys: []string{"k"},
		})

		if err != nil {
			t.Fatal(err)
		}

		recorder.Record(t.Context(), 2, []string{"kept"})
		recorders = append(recorders, recorder)
	}

	collectors := slices.Clone(driver.owned)

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, recorder := range recorders {
		recorder.Record(t.Context(), 99, []string{"kept"})
		recorder.Record(t.Context(), 99, []string{"new"})
	}

	registry := prometheus.NewRegistry()

	for _, collector := range collectors {
		if err := registry.Register(collector); err != nil {
			t.Fatal(err)
		}
	}

	families, err := registry.Gather()

	if err != nil {
		t.Fatal(err)
	}

	if len(families) != 4 {
		t.Fatalf("retained %d stopped collectors, want 4", len(families))
	}

	for _, found := range families {
		if len(found.GetMetric()) != 1 {
			t.Fatalf("stopped collector allocated children: %v", found)
		}

		sample := found.GetMetric()[0]

		if sample.GetCounter() != nil && sample.GetCounter().GetValue() != 2 ||
			sample.GetGauge() != nil && sample.GetGauge().GetValue() != 2 ||
			sample.GetHistogram() != nil && sample.GetHistogram().GetSampleCount() != 1 {
			t.Fatalf("stopped collector changed: %v", found)
		}
	}
}

// messageStream models a transport delivering already validated routing names.
// It keeps this module's core-consumer regression independent of other driver
// modules, while exercising the real messaging instruments with no trace driver.
type messageStream []broadcast.Message

// Consume forwards messages synchronously and returns the first cancellation or
// handler error. Returning after the final message makes metric completion
// deterministic without sleeps, scraping, or a background broker.
func (messages messageStream) Consume(ctx context.Context, handler broadcast.Handler) error {
	for _, message := range messages {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := handler(ctx, message); err != nil {
			return err
		}
	}

	return nil
}

// TestMetricsOnlyBroadcastConsumerBoundsDestinations exercises the complete core
// instrumentation path for valid but numerous transport names. Both exact and
// wildcard listeners need the metric driver's bound, even without any scraping.
func TestMetricsOnlyBroadcastConsumerBoundsDestinations(t *testing.T) {
	t.Parallel()

	for _, pattern := range []string{"users.created", "#"} {
		registry := prometheus.NewRegistry()
		driver := New(func(metrics *Builder) {
			metrics.Registry(registry)
		}, func(metrics *Builder) {
			metrics.RuntimeMetrics(false)
		}, func(metrics *Builder) {
			metrics.TargetInfo(false)
		}, func(metrics *Builder) {
			metrics.MaxSeries(3)
		})
		facade := telemetry.New(driver)

		if err := facade.Start(t.Context(), telemetry.Setup{}); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			_ = facade.Stop(context.Background())
		})

		messages := messageStream{{
			Name:    "users.created",
			Payload: []byte("null"),
		}}

		for index := range 100 {
			messages = append(messages, broadcast.Message{
				Name:    fmt.Sprintf("x%d", index),
				Payload: []byte("null"),
			})
		}

		consumer := broadcast.NewConsumer(messages, func(consumer *broadcast.ConsumerBuilder) {
			consumer.Telemetry(facade)
		})

		if _, err := consumer.ListenPattern(pattern, func(context.Context, broadcast.Message) error {
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		if err := consumer.Run(t.Context()); err != nil {
			t.Fatal(err)
		}

		families, err := registry.Gather()

		if err != nil || len(families) != 2 {
			t.Fatalf("metrics-only Gather = %v, %v; want counter and histogram", families, err)
		}

		for _, found := range families {
			if len(found.GetMetric()) != 3 {
				t.Fatalf("%s retained %d children, want 3", found.GetName(), len(found.GetMetric()))
			}

			kept := false

			for _, sample := range found.GetMetric() {
				for _, label := range sample.GetLabel() {
					if label.GetName() == "messaging_destination_name" && label.GetValue() == "users.created" {
						kept = true
					}
				}
			}

			if !kept {
				t.Fatalf("admitted destination missing from %s", found.GetName())
			}
		}
	}
}

// BenchmarkCounterSeriesLimit measures bounded overflow traffic rather than
// growing a fresh retained child on every iteration. Inputs cycle over 1100 names.
func BenchmarkCounterSeriesLimit(b *testing.B) {
	benchmarkSeriesLimit(b, telemetry.CounterKind)
}

// BenchmarkHistogramSeriesLimit includes histogram child and bucket admission.
// Retention stays finite regardless of the benchmark's chosen iteration count.
func BenchmarkHistogramSeriesLimit(b *testing.B) {
	benchmarkSeriesLimit(b, telemetry.HistogramKind)
}

// benchmarkSeriesLimit records both admitted and overflowing combinations and
// reports the actual retained children after timing. It shares the same bounded
// workload between recorder kinds, with allocation accounting enabled.
func benchmarkSeriesLimit(b *testing.B, kind telemetry.InstrumentKind) {
	b.Helper()

	registry := prometheus.NewRegistry()
	driver := New(func(metrics *Builder) {
		metrics.Registry(registry)
	}, func(metrics *Builder) {
		metrics.RuntimeMetrics(false)
	}, func(metrics *Builder) {
		metrics.TargetInfo(false)
	})

	if err := driver.Start(b.Context(), telemetry.Setup{}); err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		_ = driver.Stop(context.Background())
	})

	recorder, err := driver.Instrument(telemetry.InstrumentSpec{
		Name: "bounded",
		Kind: kind,
		Keys: []string{"destination"},
	})

	if err != nil {
		b.Fatal(err)
	}

	values := make([][]string, 1100)

	for index := range values {
		values[index] = []string{fmt.Sprintf("destination%d", index)}
	}

	b.ReportAllocs()
	index := 0

	for b.Loop() {
		recorder.Record(b.Context(), 0.5, values[index%len(values)])
		index++
	}

	families, err := registry.Gather()

	if err != nil || len(families) != 1 || len(families[0].GetMetric()) > 1000 {
		b.Fatalf("unbounded children: %v, %v", families, err)
	}

	b.ReportMetric(float64(len(families[0].GetMetric())), "children")
}
