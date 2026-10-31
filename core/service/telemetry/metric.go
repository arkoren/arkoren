package telemetry

import (
	"context"
	"math"
	"slices"
	"sync/atomic"
)

// InstrumentKind identifies how an instrument aggregates measurements.
// Its string values are portable across drivers; the empty value is not a kind.
//
//	spec := telemetry.InstrumentSpec{Name: "requests", Kind: telemetry.CounterKind}
type InstrumentKind string

const (
	// CounterKind is a monotonically increasing sum.
	//
	//	spec := telemetry.InstrumentSpec{Name: "requests", Kind: telemetry.CounterKind}
	CounterKind InstrumentKind = "counter"

	// UpDownCounterKind is a sum that can also decrease.
	//
	//	spec := telemetry.InstrumentSpec{Name: "active", Kind: telemetry.UpDownCounterKind}
	UpDownCounterKind InstrumentKind = "up_down_counter"

	// HistogramKind is a distribution of observed values.
	//
	//	spec := telemetry.InstrumentSpec{Name: "duration", Kind: telemetry.HistogramKind}
	HistogramKind InstrumentKind = "histogram"

	// GaugeKind is the last observed value.
	//
	//	spec := telemetry.InstrumentSpec{Name: "temperature", Kind: telemetry.GaugeKind}
	GaugeKind InstrumentKind = "gauge"
)

// InstrumentOption configures an instrument created by a [Meter]. Options run
// synchronously in order on one mutable InstrumentSpec; nil entries panic.
// After callbacks return, publication copies normalized key and bucket
// containers independently of the draft, so retaining it cannot change an
// existing instrument. Strings and other scalar metadata are copied by value.
// A zero meter or empty positional name creates a no-op without running options.
//
//	var option telemetry.InstrumentOption = func(definition *telemetry.InstrumentSpec) {
//		definition.Unit = "ms"
//	}
//	duration := meter.Histogram("duration", option)
//	duration.Record(ctx, 12)
type InstrumentOption func(*InstrumentSpec)

// Unit sets the UCUM unit of the instrument, such as "s", "By", or
// "{request}".
//
//	duration := meter.Histogram("duration", telemetry.Unit("s"))
//	duration.Record(ctx, 0.25)
func Unit(unit string) InstrumentOption {
	return func(spec *InstrumentSpec) {
		spec.Unit = unit
	}
}

// Description sets the human-readable help text of the instrument.
// It is metadata for operators, not a per-measurement label.
//
//	requests := meter.Counter("requests", telemetry.Description("Completed cache operations."))
//	requests.Add(ctx, 1)
func Description(description string) InstrumentOption {
	return func(spec *InstrumentSpec) {
		spec.Description = description
	}
}

// Keys declares the label keys of the instrument. Recordings keep only
// declared keys, in this order; undeclared attributes are dropped and
// missing keys get the value "". Declaring keys is what bounds cardinality
// and satisfies backends that need label names up front. Empty and
// duplicate keys are ignored.
//
//	requests := meter.Counter("requests", telemetry.Keys("result"))
//	requests.Add(ctx, 1, telemetry.String("result", "hit"))
func Keys(keys ...string) InstrumentOption {
	return func(spec *InstrumentSpec) {
		spec.Keys = append(spec.Keys, keys...)
	}
}

// Buckets hints explicit histogram bucket upper bounds. Bounds are sorted
// and deduplicated, and non-finite bounds are dropped. Drivers may ignore
// the hint, and it has no effect on other instrument kinds.
//
//	duration := meter.Histogram("duration", telemetry.Unit("s"), telemetry.Buckets(0.1, 0.5, 1))
//	duration.Record(ctx, 0.25)
func Buckets(bounds ...float64) InstrumentOption {
	return func(spec *InstrumentSpec) {
		spec.Buckets = append(spec.Buckets, bounds...)
	}
}

// Meter creates instruments for one instrumentation scope. It is a cheap
// value; create one with [Telemetry.Meter]. The zero Meter creates no-op
// instruments.
//
// Create each instrument once, for example in a constructor, and reuse it:
// creating it declares its labels. Creating the same name twice in a scope
// shares one backend instrument; if the kind or keys differ, the later
// instrument is disabled and a warning goes to [Setup.Logger].
//
//	meter := service.Meter("example.com/cache")
//	requests := meter.Counter("requests", telemetry.Keys("result"))
//	requests.Add(ctx, 1, telemetry.String("result", "hit"))
type Meter struct {
	// telemetry owns the driver. Nil means no-op.
	telemetry *Telemetry

	// scope is the instrumentation scope name.
	scope string
}

// Counter creates a monotonically increasing counter.
//
//	requests := meter.Counter("http.server.requests", telemetry.Unit("{request}"),
//		telemetry.Keys("http.request.method", "http.route"))
//	requests.Add(ctx, 1, telemetry.String("http.request.method", "GET"), telemetry.String("http.route", "/users/{id}"))
func (meter Meter) Counter(name string, opts ...InstrumentOption) Counter {
	return Counter{instrument: meter.instrument(CounterKind, name, opts)}
}

// UpDownCounter creates a counter that can decrease, such as in-flight
// requests.
//
//	active := meter.UpDownCounter("active", telemetry.Unit("{request}"))
//	active.Add(ctx, 1)
//	active.Add(ctx, -1)
func (meter Meter) UpDownCounter(name string, opts ...InstrumentOption) UpDownCounter {
	return UpDownCounter{instrument: meter.instrument(UpDownCounterKind, name, opts)}
}

// Histogram creates a distribution instrument, such as a duration in
// seconds.
//
//	duration := meter.Histogram("duration", telemetry.Unit("s"), telemetry.Buckets(0.1, 1))
//	duration.Record(ctx, 0.25)
func (meter Meter) Histogram(name string, opts ...InstrumentOption) Histogram {
	return Histogram{instrument: meter.instrument(HistogramKind, name, opts)}
}

// Gauge creates an instrument that records the current value of something,
// such as queue depth.
//
//	depth := meter.Gauge("queue.depth", telemetry.Unit("{message}"))
//	depth.Set(ctx, 12)
func (meter Meter) Gauge(name string, opts ...InstrumentOption) Gauge {
	return Gauge{instrument: meter.instrument(GaugeKind, name, opts)}
}

// instrument builds the shared state of an instrument, or nil when the meter
// is the zero Meter or name is empty. It does not require a metric driver:
// an observer may attach later and fill in for it. Normalization is published
// into a separate value, never back into the callback-retainable draft, so its
// newly owned containers cannot be reached through a retained draft pointer.
func (meter Meter) instrument(kind InstrumentKind, name string, opts []InstrumentOption) *instrument {
	if name == "" || meter.telemetry == nil {
		return nil
	}

	spec := InstrumentSpec{
		Scope: meter.scope,
		Name:  name,
		Kind:  kind,
	}

	for _, opt := range opts {
		opt(&spec)
	}

	published := spec
	published.Keys = normalizeKeys(spec.Keys)
	published.Buckets = normalizeBuckets(kind, spec.Buckets)

	return &instrument{
		telemetry: meter.telemetry,
		spec:      published,
	}
}

// normalizeKeys returns keys without empty and duplicate entries.
func normalizeKeys(keys []string) []string {
	var normalized []string

	for _, key := range keys {
		if key != "" && !slices.Contains(normalized, key) {
			normalized = append(normalized, key)
		}
	}

	return normalized
}

// normalizeBuckets returns sorted, unique, finite bounds for histograms and
// nil for every other kind.
func normalizeBuckets(kind InstrumentKind, bounds []float64) []float64 {
	if kind != HistogramKind {
		return nil
	}

	var normalized []float64

	for _, bound := range bounds {
		if !math.IsNaN(bound) && !math.IsInf(bound, 0) {
			normalized = append(normalized, bound)
		}
	}

	slices.Sort(normalized)

	return slices.Compact(normalized)
}

// instrument is the facade state behind every instrument kind. It binds to
// the metric driver in effect lazily on the first recording after Start, so
// instruments can be created before the application starts, and binds again
// when the driver in effect changes.
type instrument struct {
	// telemetry owns the driver.
	telemetry *Telemetry

	// spec is the normalised description sent to the driver.
	spec InstrumentSpec

	// bound is the binding to the metric driver last in effect, nil before
	// the first recording.
	bound atomic.Pointer[boundInstrument]
}

// record normalises attrs to the declared keys, forwards the measurement to
// the metric driver in effect, then to the observers. It does nothing before
// Start and after Stop, and nothing when there is neither a driver nor an
// observer.
func (instrument *instrument) record(ctx context.Context, value float64, attrs []Attr) {
	if instrument == nil || !instrument.telemetry.active.Load() {
		return
	}

	table := instrument.telemetry.signals.Load()

	if table.metrics == nil && len(table.measures) == 0 {
		return
	}

	bound := instrument.binding(ctx, table)

	if bound.disabled || !instrument.telemetry.active.Load() {
		return
	}

	values := labelValues(instrument.spec.Keys, attrs)

	if bound.recorder != nil {
		bound.recorder.Record(ctx, value, values)
	}

	for _, observer := range table.measures {
		if !instrument.telemetry.active.Load() {
			return
		}

		observer.Measured(instrument.spec, value, values)
	}
}

// binding resolves the instrument's atomic fast-path cache against the current
// metric registration. Misses use the facade's shared binding registry; only
// fully initialized bindings are published to recording goroutines.
func (instrument *instrument) binding(ctx context.Context, table *signals) *boundInstrument {
	bound := instrument.bound.Load()

	if bound != nil && bound.source == table.metricSource {
		return bound
	}

	bound = instrument.telemetry.bind(ctx, instrument.spec, table)
	instrument.bound.Store(bound)

	return bound
}

// labelValues returns one value per key in key order. The last attribute with
// a key wins and missing keys get "".
func labelValues(keys []string, attrs []Attr) []string {
	values := make([]string, len(keys))

	for _, attr := range attrs {
		if index := slices.Index(keys, attr.key); index >= 0 && attr.kind != AttrInvalid {
			values[index] = attr.Text()
		}
	}

	return values
}

// Counter is a monotonically increasing sum. The zero Counter is a no-op.
// Create it once and reuse it; negative increments are ignored by Add.
//
//	requests := meter.Counter("requests", telemetry.Keys("result"))
//	requests.Add(ctx, 1, telemetry.String("result", "hit"))
type Counter struct {
	// instrument is nil for a no-op counter.
	instrument *instrument
}

// Add increases the counter by delta. Negative deltas are ignored. Attributes
// outside the declared keys are dropped.
//
//	requests.Add(ctx, 1, telemetry.String("http.route", route))
func (counter Counter) Add(ctx context.Context, delta int64, attrs ...Attr) {
	if delta < 0 {
		return
	}

	counter.instrument.record(ctx, float64(delta), attrs)
}

// UpDownCounter is a sum that can increase and decrease. The zero value is a
// no-op.
//
//	active := meter.UpDownCounter("active")
//	active.Add(ctx, 1)
//	active.Add(ctx, -1)
type UpDownCounter struct {
	// instrument is nil for a no-op counter.
	instrument *instrument
}

// Add changes the sum by delta, which may be negative. Attributes outside the
// declared keys are dropped.
//
//	active := meter.UpDownCounter("active", telemetry.Keys("worker"))
//	active.Add(ctx, -1, telemetry.String("worker", "primary"))
func (counter UpDownCounter) Add(ctx context.Context, delta int64, attrs ...Attr) {
	counter.instrument.record(ctx, float64(delta), attrs)
}

// Histogram records a distribution of values. The zero Histogram is a no-op.
// Histogram values are in the unit selected when the instrument is declared.
//
//	duration := meter.Histogram("duration", telemetry.Unit("s"))
//	duration.Record(ctx, 0.25)
type Histogram struct {
	// instrument is nil for a no-op histogram.
	instrument *instrument
}

// Record observes value, in the unit declared with [Unit]. Attributes outside
// the declared keys are dropped. NaN is ignored.
//
//	duration.Record(ctx, time.Since(start).Seconds(), telemetry.String("http.route", route))
func (histogram Histogram) Record(ctx context.Context, value float64, attrs ...Attr) {
	if math.IsNaN(value) {
		return
	}

	histogram.instrument.record(ctx, value, attrs)
}

// Gauge records the current value of something. The zero Gauge is a no-op.
// Set reports an absolute sample rather than an increment or decrement.
//
//	depth := meter.Gauge("queue.depth")
//	depth.Set(ctx, 12)
type Gauge struct {
	// instrument is nil for a no-op gauge.
	instrument *instrument
}

// Set records value as the current value. Attributes outside the declared
// keys are dropped. NaN is ignored.
//
//	depth := meter.Gauge("queue.depth", telemetry.Keys("queue"))
//	depth.Set(ctx, 12, telemetry.String("queue", "default"))
func (gauge Gauge) Set(ctx context.Context, value float64, attrs ...Attr) {
	if math.IsNaN(value) {
		return
	}

	gauge.instrument.record(ctx, value, attrs)
}
