package oteltelemetry

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"

	"arkoren.dev/core/service/telemetry"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// errUnknownKind is returned for an instrument kind the driver cannot create.
var errUnknownKind = errors.New("unknown instrument kind")

// maxCachedAttributeSets bounds the label combinations an instrument keeps
// pre-built. Combinations beyond it are built per recording.
const maxCachedAttributeSets = 2048

// Instrument creates the OpenTelemetry instrument for spec. Before Start and
// after Stop it returns a recorder that discards measurements. Native SDK
// construction errors are returned without disabling other instruments. The
// recorder owns its declared keys and copies cached label tuples; callers may
// reuse their recording slices after Record returns.
//
// With a started driver:
//
//	recorder, err := driver.Instrument(telemetry.InstrumentSpec{
//		Name: "jobs", Kind: telemetry.CounterKind, Keys: []string{"queue"},
//	})
//	if err != nil {
//		return err
//	}
//	recorder.Record(ctx, 1, []string{"billing"})
func (driver *Driver) Instrument(spec telemetry.InstrumentSpec) (telemetry.InstrumentRecorder, error) {
	run := driver.state.Load()

	if run == nil || run.MeterProvider == nil {
		return discardRecorder{}, nil
	}

	record, err := newRecordFunc(run.MeterProvider.Meter(spec.Scope), spec)

	if err != nil {
		return nil, err
	}

	keys := make([]attribute.Key, len(spec.Keys))

	for index, key := range spec.Keys {
		keys[index] = attribute.Key(key)
	}

	return &instrumentRecorder{
		driver: driver,
		record: record,
		keys:   keys,
		sets:   make(map[string]metric.MeasurementOption),
	}, nil
}

// newRecordFunc creates the instrument for spec and returns the function that
// records one value on it.
func newRecordFunc(meter metric.Meter, spec telemetry.InstrumentSpec) (func(context.Context, float64, metric.MeasurementOption), error) {
	switch spec.Kind {
	case telemetry.CounterKind:
		counter, err := meter.Float64Counter(spec.Name, metric.WithDescription(spec.Description), metric.WithUnit(spec.Unit))

		if err != nil {
			return nil, err
		}

		return func(ctx context.Context, value float64, option metric.MeasurementOption) {
			counter.Add(ctx, value, option)
		}, nil
	case telemetry.UpDownCounterKind:
		counter, err := meter.Float64UpDownCounter(spec.Name, metric.WithDescription(spec.Description), metric.WithUnit(spec.Unit))

		if err != nil {
			return nil, err
		}

		return func(ctx context.Context, value float64, option metric.MeasurementOption) {
			counter.Add(ctx, value, option)
		}, nil
	case telemetry.HistogramKind:
		return newHistogramRecordFunc(meter, spec)
	case telemetry.GaugeKind:
		gauge, err := meter.Float64Gauge(spec.Name, metric.WithDescription(spec.Description), metric.WithUnit(spec.Unit))

		if err != nil {
			return nil, err
		}

		return func(ctx context.Context, value float64, option metric.MeasurementOption) {
			gauge.Record(ctx, value, option)
		}, nil
	default:
		return nil, errUnknownKind
	}
}

// newHistogramRecordFunc applies optional aggregation boundaries at the native
// instrument boundary. Its returned closure records without retaining the
// mutable InstrumentSpec or invoking configuration again.
func newHistogramRecordFunc(meter metric.Meter, spec telemetry.InstrumentSpec) (func(context.Context, float64, metric.MeasurementOption), error) {
	options := []metric.Float64HistogramOption{metric.WithDescription(spec.Description), metric.WithUnit(spec.Unit)}

	if len(spec.Buckets) > 0 {
		options = append(options, metric.WithExplicitBucketBoundaries(spec.Buckets...))
	}

	histogram, err := meter.Float64Histogram(spec.Name, options...)

	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, value float64, option metric.MeasurementOption) {
		histogram.Record(ctx, value, option)
	}, nil
}

// instrumentRecorder records measurements on one OpenTelemetry instrument.
type instrumentRecorder struct {
	// driver is consulted so recordings after Stop are discarded.
	driver *Driver

	// record records one value with pre-built attributes.
	record func(context.Context, float64, metric.MeasurementOption)

	// keys are the declared label keys in order.
	keys []attribute.Key

	// mutex guards sets.
	mutex sync.RWMutex

	// sets caches the attribute set of each label combination, keyed by the
	// values' byte lengths followed by their exact bytes. Embedded NULs and
	// empty values therefore cannot alias another tuple.
	sets map[string]metric.MeasurementOption
}

// Record records value with the label values. Empty values are omitted from
// the attributes, since semantic conventions treat them as absent.
func (instrumentRecorder *instrumentRecorder) Record(ctx context.Context, value float64, values []string) {
	if instrumentRecorder.driver.state.Load() == nil {
		return
	}

	instrumentRecorder.record(ctx, value, instrumentRecorder.measurementOption(values))
}

// measurementOption returns the cached attribute set for values, building and
// caching it on first use while the cache has room. Each value is prefixed
// with its unsigned-varint byte length, so no input byte acts as a separator.
// A nil or empty tuple has an empty key; its input slice is never retained.
func (instrumentRecorder *instrumentRecorder) measurementOption(values []string) metric.MeasurementOption {
	var buffer [128]byte

	key := buffer[:0]

	for _, value := range values {
		key = binary.AppendUvarint(key, uint64(len(value)))
		key = append(key, value...)
	}

	instrumentRecorder.mutex.RLock()
	option, ok := instrumentRecorder.sets[string(key)]
	instrumentRecorder.mutex.RUnlock()

	if ok {
		return option
	}

	option = metric.WithAttributeSet(instrumentRecorder.attributeSet(values))

	instrumentRecorder.mutex.Lock()

	if len(instrumentRecorder.sets) < maxCachedAttributeSets {
		instrumentRecorder.sets[string(key)] = option
	}

	instrumentRecorder.mutex.Unlock()

	return option
}

// attributeSet builds the attribute set of values, leaving out empty ones.
func (instrumentRecorder *instrumentRecorder) attributeSet(values []string) attribute.Set {
	attrs := make([]attribute.KeyValue, 0, len(values))

	for index, value := range values {
		if value != "" {
			attrs = append(attrs, instrumentRecorder.keys[index].String(value))
		}
	}

	return attribute.NewSet(attrs...)
}

// discardRecorder is the recorder of instruments created while the driver is
// inactive.
type discardRecorder struct{}

// Record discards the measurement.
func (discardRecorder) Record(context.Context, float64, []string) {
}
