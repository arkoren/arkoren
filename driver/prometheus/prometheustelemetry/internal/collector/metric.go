package collector

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"arkoren.dev/core/service/telemetry"

	"github.com/prometheus/client_golang/prometheus"
)

// histogramBucketLabel is the label Prometheus reserves for histogram bucket
// bounds.
const histogramBucketLabel = "le"

// errUnknownKind is returned for an instrument kind the driver cannot create.
var errUnknownKind = errors.New("unknown instrument kind")

// NewRecorder builds one native collector and its recorder. Each recorder owns
// its series-admission mutex and tuple index, and shares only the driver's
// atomic lifecycle gate. Limits are captured before publication, not read back
// through a parent driver or mutable construction draft.
func NewRecorder(name string, spec telemetry.InstrumentSpec, active *atomic.Bool, maxSeries int) (telemetry.InstrumentRecorder, prometheus.Collector, error) {
	labels, err := labelNames(spec)

	if err != nil {
		return nil, nil, err
	}

	base := &recorder{
		active:    active,
		maxSeries: maxSeries,
		keys:      len(labels),
		series:    make(map[string]struct{}),
	}
	help := cmp.Or(spec.Description, spec.Name)

	switch spec.Kind {
	case telemetry.CounterKind:
		vec := prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: name,
			Help: help,
		}, labels)

		return &counterRecorder{
			recorder: base,
			vec:      vec,
		}, vec, nil
	case telemetry.UpDownCounterKind:
		vec := prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: name,
			Help: help,
		}, labels)

		return &gaugeRecorder{
			recorder: base,
			vec:      vec,
			add:      true,
		}, vec, nil
	case telemetry.GaugeKind:
		vec := prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: name,
			Help: help,
		}, labels)

		return &gaugeRecorder{
			recorder: base,
			vec:      vec,
		}, vec, nil
	case telemetry.HistogramKind:
		buckets, err := histogramBuckets(spec.Buckets)

		if err != nil {
			return nil, nil, err
		}

		vec := prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    name,
			Help:    help,
			Buckets: buckets,
		}, labels)

		return &histogramRecorder{
			recorder: base,
			vec:      vec,
		}, vec, nil
	default:
		return nil, nil, errUnknownKind
	}
}

// labelNames translates the declared keys of spec. It fails when two keys
// translate to the same label name, or a histogram uses the reserved "le".
func labelNames(spec telemetry.InstrumentSpec) ([]string, error) {
	labels := make([]string, len(spec.Keys))

	for index, key := range spec.Keys {
		label := LabelName(key)

		if slices.Contains(labels[:index], label) {
			return nil, fmt.Errorf("label key %q translates to %q, which another key already uses", key, label)
		}

		if label == histogramBucketLabel && spec.Kind == telemetry.HistogramKind {
			return nil, fmt.Errorf("label key %q: %q is reserved for histogram buckets", key, label)
		}

		labels[index] = label
	}

	return labels, nil
}

// histogramBuckets returns the bounds to use: declared, or the Prometheus
// defaults when none are. Bounds must increase strictly, which Prometheus
// enforces by panicking, so they are checked here.
func histogramBuckets(declared []float64) ([]float64, error) {
	if len(declared) == 0 {
		return prometheus.DefBuckets, nil
	}

	for index, bound := range declared {
		if math.IsNaN(bound) || index > 0 && bound <= declared[index-1] {
			return nil, fmt.Errorf("histogram buckets %v are not strictly increasing numbers", declared)
		}
	}

	return slices.Clone(declared), nil
}

// recorder is the part of every recorder that decides whether to record.
type recorder struct {
	// active is the shared lifecycle gate, with no parent-driver dependency.
	active *atomic.Bool

	// maxSeries bounds retained label tuples for this recorder's lifetime.
	maxSeries int

	// keys is the number of label values a recording must carry.
	keys int

	// mutex serializes lookup and admission so concurrent recordings cannot
	// overrun MaxSeries. It is not held while updating Prometheus values.
	mutex sync.Mutex

	// series holds collision-free encodings of admitted value tuples only.
	// Rejected tuples are never retained or passed to the vector constructor.
	series map[string]struct{}
}

// accepts atomically admits values, or recognizes an already admitted tuple.
// It validates UTF-8 before consuming capacity. Length prefixes distinguish
// tuples containing separators or NUL bytes, preventing admission-key collisions
// from creating unbounded actual vector children. No caller-owned slice is kept.
func (recorder *recorder) accepts(values []string) bool {
	if !recorder.active.Load() || len(values) != recorder.keys || recorder.maxSeries <= 0 {
		return false
	}

	var key strings.Builder

	for _, value := range values {
		if !utf8.ValidString(value) {
			return false
		}

		key.WriteString(strconv.Itoa(len(value)))
		key.WriteByte(':')
		key.WriteString(value)
	}

	encoded := key.String()

	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()

	if !recorder.active.Load() {
		return false
	}

	if _, ok := recorder.series[encoded]; ok {
		return true
	}

	if len(recorder.series) >= recorder.maxSeries {
		return false
	}

	recorder.series[encoded] = struct{}{}

	return true
}

// exemplarLabels returns the trace_id and span_id of the span in ctx when it is
// valid and sampled, otherwise nil.
func exemplarLabels(ctx context.Context) prometheus.Labels {
	spanContext := telemetry.SpanFromContext(ctx).Context()

	if !spanContext.IsValid() || !spanContext.Sampled {
		return nil
	}

	return prometheus.Labels{"trace_id": spanContext.TraceID.String(), "span_id": spanContext.SpanID.String()}
}

// counterRecorder records onto a counter.
type counterRecorder struct {
	// recorder shares lifecycle and series admission across counter children.
	// It bounds retained label tuples before the vector allocates a child.
	*recorder

	// vec holds the series.
	vec *prometheus.CounterVec
}

// Record adds a nonnegative value to the child identified by values. Invalid
// labels, wrong value counts, inactive lifecycle, and new tuples beyond
// MaxSeries are dropped without allocating children or exemplars. Admitted
// tuples retain cumulative totals and sampled-span exemplars at capacity.
//
//	recorder.Record(ctx, 1, []string{"billing"})
func (counterRecorder *counterRecorder) Record(ctx context.Context, value float64, values []string) {
	if value < 0 || !counterRecorder.accepts(values) {
		return
	}

	counter, err := counterRecorder.vec.GetMetricWithLabelValues(values...)

	if err != nil {
		return
	}

	if labels := exemplarLabels(ctx); labels != nil {
		if adder, ok := counter.(prometheus.ExemplarAdder); ok {
			adder.AddWithExemplar(value, labels)

			return
		}
	}

	counter.Add(value)
}

// gaugeRecorder records onto a gauge, either by adding (up-down counters) or
// by setting (gauges).
type gaugeRecorder struct {
	// recorder supplies the lifecycle and cardinality gate for gauge children.
	// Both additive and replacing gauges use the same admission policy.
	*recorder

	// vec holds the series.
	vec *prometheus.GaugeVec

	// add makes Record add the value instead of setting it.
	add bool
}

// Record adds value for an up-down counter, or replaces it for a gauge, on the
// child identified by values. Invalid labels, wrong value counts, inactive
// lifecycle, and new tuples beyond MaxSeries are ignored. Admitted children
// remain writable at capacity; negative values are valid for both kinds.
//
//	recorder.Record(ctx, -1, []string{"billing"})
func (gaugeRecorder *gaugeRecorder) Record(_ context.Context, value float64, values []string) {
	if !gaugeRecorder.accepts(values) {
		return
	}

	gauge, err := gaugeRecorder.vec.GetMetricWithLabelValues(values...)

	if err != nil {
		return
	}

	if gaugeRecorder.add {
		gauge.Add(value)

		return
	}

	gauge.Set(value)
}

// histogramRecorder records onto a histogram.
type histogramRecorder struct {
	// recorder bounds admitted label tuples before histogram buckets allocate.
	// Existing children remain writable after the series limit is reached.
	*recorder

	// vec holds the series.
	vec *prometheus.HistogramVec
}

// Record observes value on the histogram child identified by values. Invalid
// labels, wrong value counts, inactive lifecycle, and new tuples beyond
// MaxSeries are dropped before creating children, buckets, or exemplars.
// Admitted children retain their aggregates and sampled-span exemplars.
//
//	recorder.Record(ctx, 0.5, []string{"billing"})
func (histogramRecorder *histogramRecorder) Record(ctx context.Context, value float64, values []string) {
	if !histogramRecorder.accepts(values) {
		return
	}

	observer, err := histogramRecorder.vec.GetMetricWithLabelValues(values...)

	if err != nil {
		return
	}

	if labels := exemplarLabels(ctx); labels != nil {
		if exemplar, ok := observer.(prometheus.ExemplarObserver); ok {
			exemplar.ObserveWithExemplar(value, labels)

			return
		}
	}

	observer.Observe(value)
}

// DiscardRecorder is the recorder of instruments created while the driver is
// inactive.
type DiscardRecorder struct{}

// Record discards the measurement.
func (DiscardRecorder) Record(context.Context, float64, []string) {
}
