package prometheustelemetry

import (
	"fmt"
	"slices"
	"strings"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/driver/prometheus/prometheustelemetry/internal/collector"
)

// histogramSampleSuffixes are the generated classic histogram sample names.
// Both registration-order checks use this private, read-only vocabulary.
var histogramSampleSuffixes = [...]string{"_count", "_sum", "_bucket"}

// instrument is a created instrument: the spec it was made from and the
// recorder callers share.
type instrument struct {
	// spec is the instrument description, for detecting repeated creation.
	spec telemetry.InstrumentSpec

	// recorder records measurements.
	recorder telemetry.InstrumentRecorder
}

// Instrument creates the Prometheus collector for spec, registers it, and
// returns its recorder. Before Start and after Stop it returns a recorder
// that discards measurements.
//
// It returns an error, and registers nothing, when the translated name or a
// translated label name is invalid, two label keys translate to the same name,
// the name is already held by another collector, or another instrument already
// translated to it. Creating the same instrument twice returns the same
// recorder. At [Builder.MaxInstruments], new definitions fail before collector
// allocation; existing definitions remain usable. Its recorder drops new label
// tuples beyond [Builder.MaxSeries], without evicting admitted children. Keys
// and Buckets are copied before retention. Generated histogram sample names
// cannot overlap other managed instruments in either registration order.
// Native collectors remain caller-managed: their generated sample names and
// unchecked collection behavior cannot be inferred through Registerer alone.
//
//	recorder, err := driver.Instrument(telemetry.InstrumentSpec{
//		Name: "jobs", Kind: telemetry.CounterKind, Keys: []string{"queue"},
//	})
//	if err != nil {
//		return err
//	}
//	recorder.Record(ctx, 1, []string{"billing"})
func (driver *Driver) Instrument(spec telemetry.InstrumentSpec) (telemetry.InstrumentRecorder, error) {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	if driver.phase != phaseStarted {
		return collector.DiscardRecorder{}, nil
	}

	name := collector.MetricName(driver.config.Namespace, spec)

	if existing, ok := driver.instruments[name]; ok {
		if !sameSpec(existing.spec, spec) {
			return nil, fmt.Errorf("instrument %q (scope %q) translates to %q, already used by instrument %q (scope %q)",
				spec.Name, spec.Scope, name, existing.spec.Name, existing.spec.Scope)
		}

		return existing.recorder, nil
	}

	if len(driver.instruments) >= driver.config.MaxInstruments {
		return nil, fmt.Errorf("instrument limit %d reached", driver.config.MaxInstruments)
	}

	if err := driver.checkSampleNames(name, spec.Kind); err != nil {
		return nil, err
	}

	recorder, native, err := collector.NewRecorder(name, spec, &driver.active, driver.config.MaxSeries)

	if err != nil {
		return nil, fmt.Errorf("instrument %q: %w", spec.Name, err)
	}

	if err := driver.register(native); err != nil {
		return nil, fmt.Errorf("instrument %q as %q: %w", spec.Name, name, err)
	}

	spec.Keys = slices.Clone(spec.Keys)
	spec.Buckets = slices.Clone(spec.Buckets)
	driver.instruments[name] = &instrument{
		spec:     spec,
		recorder: recorder,
	}

	return recorder, nil
}

// checkSampleNames rejects overlap with the wire samples of managed histograms.
// Native Register checks descriptors, not generated _count/_sum/_bucket names;
// checking both registration orders here prevents later scrape-time data loss.
func (driver *Driver) checkSampleNames(name string, kind telemetry.InstrumentKind) error {
	for _, suffix := range histogramSampleSuffixes {
		parent, found := strings.CutSuffix(name, suffix)
		existing := driver.instruments[parent]

		if found && existing != nil && existing.spec.Kind == telemetry.HistogramKind {
			return fmt.Errorf("instrument %q collides with histogram %q", name, parent)
		}
	}

	if kind != telemetry.HistogramKind {
		return nil
	}

	for _, suffix := range histogramSampleSuffixes {
		if _, exists := driver.instruments[name+suffix]; exists {
			return fmt.Errorf("histogram %q collides with instrument %q", name, name+suffix)
		}
	}

	return nil
}

// sameSpec reports whether two specs describe the same instrument.
func sameSpec(left, right telemetry.InstrumentSpec) bool {
	if left.Scope != right.Scope || left.Name != right.Name {
		return false
	}

	if left.Description != right.Description || left.Unit != right.Unit {
		return false
	}

	return left.Kind == right.Kind && slices.Equal(left.Keys, right.Keys) && slices.Equal(left.Buckets, right.Buckets)
}
