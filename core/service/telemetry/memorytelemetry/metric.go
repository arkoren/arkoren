package memorytelemetry

import (
	"context"
	"slices"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry/internal/aggregate"
	"arkoren.dev/foundation/collection"
)

// Metric is the snapshot of one instrument and its aggregates.
// Obtain it through Driver.Metrics; Points follow first-recording order and
// Point looks up one exact declared label tuple without mutating the snapshot.
//
//	for _, metric := range store.Metrics() {
//		if point, ok := metric.Point("hit"); ok {
//			fmt.Println(metric.Name, point.Count, point.Sum)
//		}
//	}
type Metric struct {
	// Scope is the instrumentation scope of the meter.
	// Names may repeat in distinct scopes without sharing an aggregate.
	//
	//	for _, metric := range store.Metrics() {
	//		fmt.Println(metric.Scope, metric.Name)
	//	}
	Scope string

	// Name is the instrument name.
	// It is the declared portable name rather than a backend-translated spelling.
	//
	//	for _, metric := range store.Metrics() {
	//		fmt.Println(metric.Name)
	//	}
	Name string

	// Description is the help text.
	// Empty means no description was declared.
	//
	//	for _, metric := range store.Metrics() {
	//		fmt.Println(metric.Name, metric.Description)
	//	}
	Description string

	// Unit is the UCUM unit.
	// It describes values already recorded in that unit; no conversion is applied.
	//
	//	for _, metric := range store.Metrics() {
	//		fmt.Println(metric.Name, metric.Unit)
	//	}
	Unit string

	// Kind is the instrument kind.
	// Inspect it before interpreting Sum or Last as an aggregate result.
	//
	//	for _, metric := range store.Metrics() {
	//		if metric.Kind == telemetry.CounterKind {
	//			fmt.Println(metric.Name)
	//		}
	//	}
	Kind telemetry.InstrumentKind

	// Keys are the declared label keys in declaration order.
	// Every Point.Values entry has the corresponding position in this slice.
	//
	//	for _, metric := range store.Metrics() {
	//		fmt.Println(metric.Name, metric.Keys)
	//	}
	Keys []string

	// Points are the aggregates, one per label combination in order of first
	// recording.
	//
	//	for _, metric := range store.Metrics() {
	//		for _, point := range metric.Points {
	//			fmt.Println(metric.Name, point.Values, point.Count)
	//		}
	//	}
	Points []Point
}

// Point is the aggregate for one label combination.
//
// Count is the number of recordings. Sum is the total of recorded values: the
// counter total, the up-down counter total, or the sum of a histogram. Last
// is the most recent value, which is the gauge value.
// Count counts calls, not counter increments; recording a counter increment of
// five contributes one to Count and five to Sum.
//
//	if point, ok := metric.Point("hit"); ok {
//		fmt.Println(point.Count, point.Sum, point.Last)
//	}
type Point struct {
	// Values are the label values, one per [Metric.Keys] entry in the same
	// order.
	// Point returns an owned copy of this slice for application-side edits.
	//
	//	if point, ok := metric.Point("hit"); ok {
	//		point.Values[0] = "display hit"
	//		fmt.Println(point.Values)
	//	}
	Values []string

	// Count is the number of recordings.
	// It is distinct from a counter's total increments or a histogram's sum.
	//
	//	if point, ok := metric.Point("hit"); ok {
	//		fmt.Println(point.Count)
	//	}
	Count uint64

	// Sum is the total of the recorded values.
	// For a counter this is its total, and for a histogram it is the value sum.
	//
	//	if point, ok := metric.Point("hit"); ok {
	//		fmt.Println(point.Sum)
	//	}
	Sum float64

	// Last is the most recently recorded value.
	// Use it for gauges rather than summing all samples over time.
	//
	//	if point, ok := metric.Point("primary"); ok {
	//		fmt.Println(point.Last)
	//	}
	Last float64
}

// Point returns the aggregate whose label values equal values, given in
// declared key order. The result owns its Values slice; editing it does not
// change metric or another lookup. No match returns the zero Point and false.
//
//	for _, metric := range driver.Metrics() {
//		if point, ok := metric.Point("eu"); ok {
//			fmt.Println(point.Sum)
//		}
//	}
func (metric Metric) Point(values ...string) (Point, bool) {
	index := slices.IndexFunc(metric.Points, func(point Point) bool {
		return slices.Equal(point.Values, values)
	})

	if index < 0 {
		return Point{}, false
	}

	point := metric.Points[index]
	point.Values = slices.Clone(point.Values)

	return point, true
}

// instrumentState holds the aggregates of one instrument. The driver's mu
// guards it.
type instrumentState struct {
	// metric is the metadata; its Points field is unused.
	metric Metric

	// series owns bounded, ordered label aggregates under the driver's lock.
	series aggregate.Series
}

// instrumentRecorder is the [telemetry.InstrumentRecorder] of one instrument.
type instrumentRecorder struct {
	// driver guards the state and gates recording.
	driver *Driver

	// state receives the recordings.
	state *instrumentState
}

// discardRecorder is returned before Start and records nothing.
type discardRecorder struct{}

// Record does nothing.
func (discardRecorder) Record(context.Context, float64, []string) {
}

// Instrument registers the instrument, or returns the recorder of an already
// registered one with the same scope and name.
// With store already started, raw recordings supply one value per key:
//
//	recorder, err := store.Instrument(telemetry.InstrumentSpec{
//		Name: "requests", Kind: telemetry.CounterKind, Keys: []string{"result"},
//	})
//	if err != nil {
//		return err
//	}
//	recorder.Record(ctx, 1, []string{"hit"})
func (driver *Driver) Instrument(spec telemetry.InstrumentSpec) (telemetry.InstrumentRecorder, error) {
	if !driver.active.Load() {
		return discardRecorder{}, nil
	}

	return &instrumentRecorder{
		driver: driver,
		state:  driver.instrumentState(spec),
	}, nil
}

// instrumentState returns the state registered for the scope and name of
// spec, creating it on first use. Both strings retain their exact bytes;
// embedded separators cannot alias another scope/name pair.
func (driver *Driver) instrumentState(spec telemetry.InstrumentSpec) *instrumentState {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	key := aggregate.Identity(spec.Scope, spec.Name)
	state, ok := driver.instruments[key]

	if !ok {
		state = &instrumentState{
			metric: Metric{
				Scope:       spec.Scope,
				Name:        spec.Name,
				Description: spec.Description,
				Unit:        spec.Unit,
				Kind:        spec.Kind,
				Keys:        slices.Clone(spec.Keys),
			},
		}
		driver.instruments[key] = state
		driver.order = append(driver.order, state)
	}

	return state
}

// Record aggregates one measurement. Recordings that would exceed
// the [Builder.MaxSeries] capacity are dropped.
func (recorder *instrumentRecorder) Record(_ context.Context, value float64, values []string) {
	recorder.driver.record(recorder.state, value, values)
}

// record adds one measurement to state, unless the driver is stopped or the
// label combination would exceed the [Builder.MaxSeries] capacity. It keeps no reference
// to values. The active check under mu synchronizes aggregate commits with
// Stop; the initial check preserves the cheap disabled path.
func (driver *Driver) record(state *instrumentState, value float64, values []string) {
	if !driver.active.Load() {
		return
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if !driver.active.Load() {
		return
	}

	state.series.Record(value, values, driver.config.MaxSeries)
}

// Metrics returns a snapshot of every instrument in creation order. Mutating
// it does not affect the driver.
// Existing instruments remain in the result after Reset, with empty Points
// until another recording; unregistered names are not synthesized.
// The eager snapshot copies keys, points, and label values before wrapping
// them. Later recordings and Reset do not change it; it is not a live query.
//
//	byScope := store.Metrics().GroupBy(func(_ int, metric memorytelemetry.Metric) string {
//		return metric.Scope
//	})
//	for _, metric := range byScope["example.com/billing"] {
//		fmt.Println(metric.Scope, metric.Name, len(metric.Points))
//	}
func (driver *Driver) Metrics() collection.Slice[Metric] {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	metrics := make([]Metric, 0, len(driver.order))

	for _, state := range driver.order {
		metric := state.metric
		metric.Keys = slices.Clone(metric.Keys)

		points := state.series.Snapshot()
		metric.Points = make([]Point, len(points))

		for index, point := range points {
			metric.Points[index] = Point(point)
		}

		metrics = append(metrics, metric)
	}

	return collection.NewSlice(metrics)
}
