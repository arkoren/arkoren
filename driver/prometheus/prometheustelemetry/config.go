package prometheustelemetry

import (
	"errors"
	"reflect"

	"github.com/prometheus/client_golang/prometheus"
)

// BuilderFunc edits one fresh construction draft synchronously during New.
// Callbacks run once in order; nil entries panic and empty expansions omit edits.
// Native registry handles remain shared rather than cloned or invoked here.
//
//	var policy prometheustelemetry.BuilderFunc = func(metrics *prometheustelemetry.Builder) {
//		metrics.Namespace("billing")
//	}
//	driver := prometheustelemetry.New(policy)
//	_ = driver
type BuilderFunc = func(*Builder)

// defaultMaxSeries bounds retained label combinations for each instrument.
// Dropping new combinations preserves existing cumulative values under a flood.
const defaultMaxSeries = 1000

// defaultMaxInstruments bounds instrument definitions retained by one driver.
// Otherwise dynamically chosen names or keys could bypass the per-series cap.
const defaultMaxInstruments = 1000

// settings configures a [Driver]'s registry, optional built-in collectors, and
// retained-state bounds. [New] normalizes zero-valued limits to finite defaults
// of 1000 after applying construction callbacks.
// Registerer and Gatherer remain caller-owned and may contain native collectors.
type settings struct {
	// Registerer receives the collectors of the driver. Nil uses Gatherer
	// when it also implements [prometheus.Registerer], as a
	// [*prometheus.Registry] does; otherwise a registry the driver creates,
	// which is also the default Gatherer.
	Registerer prometheus.Registerer

	// Gatherer is what [Driver.Handler] serves. Nil uses Registerer when it
	// also implements [prometheus.Gatherer], as a [*prometheus.Registry]
	// does; otherwise the handler answers 501 Not Implemented.
	Gatherer prometheus.Gatherer

	// Namespace prefixes every instrument name, as in namespace_name. Empty
	// adds no prefix. Runtime metrics and target_info keep their names.
	Namespace string

	// RuntimeMetrics registers the Go and process collectors on Start and
	// unregisters them on Stop. A collector the registry already holds is
	// left alone. [New] enables it.
	RuntimeMetrics bool

	// TargetInfo exposes the resource as a target_info gauge, as the
	// OpenTelemetry to Prometheus compatibility specification asks. Resource
	// attributes are never added to other series. [New] enables it.
	TargetInfo bool

	// MaxSeries bounds distinct label combinations retained per instrument,
	// including the unlabeled combination. New combinations beyond the limit
	// are dropped without creating Prometheus children or exemplars; admitted
	// combinations continue recording without eviction or counter resets.
	// Zero selects the default of 1000; a negative value records no metrics.
	MaxSeries int

	// MaxInstruments bounds distinct instrument definitions per driver, so
	// variable names and label keys cannot bypass MaxSeries. [Driver.Instrument]
	// returns an error for a new definition beyond the limit; repeated definitions
	// still return their original recorder. Zero selects the default of 1000;
	// a negative value admits no instruments. Native collectors are not counted.
	// This bounds driver state, not the telemetry facade's definition cache;
	// instrument names and keys must remain trusted, fixed declarations.
	MaxInstruments int
}

// Builder is the mutable construction draft used by [New]. Settings replace
// earlier values and return the same draft; native registries remain shared.
//
//	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
//		metrics.Namespace("shop")
//	})
//	_ = driver
type Builder struct {
	// config stores optional settings independently of the published driver.
	config settings
}

// newConfig returns detached settings with construction callbacks applied once.
// Runtime metrics and target_info are on. At most 1000 instrument definitions
// and 1000 label combinations per instrument are retained by default. After all
// callbacks apply, zero limits select these finite defaults; negative limits
// disable their respective admission, never remove the bound.
func newConfig(configure ...BuilderFunc) settings {
	metrics := &Builder{
		config: settings{
			RuntimeMetrics: true,
			TargetInfo:     true,
			MaxSeries:      defaultMaxSeries,
			MaxInstruments: defaultMaxInstruments,
		},
	}

	for _, configure := range configure {
		configure(metrics)
	}

	config := metrics.config

	if config.MaxSeries == 0 {
		config.MaxSeries = defaultMaxSeries
	}

	if config.MaxInstruments == 0 {
		config.MaxInstruments = defaultMaxInstruments
	}

	return config
}

// resolved fills the registry defaults described on [settings.Registerer] and
// [settings.Gatherer].
func (settings settings) resolved() settings {
	if settings.Registerer == nil {
		settings.Registerer, _ = settings.Gatherer.(prometheus.Registerer)
	}

	if settings.Registerer == nil {
		registry := prometheus.NewRegistry()
		settings.Registerer = registry

		if settings.Gatherer == nil {
			settings.Gatherer = registry
		}
	}

	if settings.Gatherer == nil {
		settings.Gatherer, _ = settings.Registerer.(prometheus.Gatherer)
	}

	return settings
}

// validate rejects typed-nil native pointers without replacing borrowed handles
// or changing nil-interface default resolution. Native method panics from other
// invalid custom implementations remain the caller's responsibility.
func (settings settings) validate() error {
	for _, handle := range []any{settings.Registerer, settings.Gatherer} {
		if handle == nil {
			continue
		}

		value := reflect.ValueOf(handle)

		if value.Kind() == reflect.Pointer && value.IsNil() {
			return errors.New("prometheus telemetry: typed-nil registry handle")
		}
	}

	return nil
}

// Registry sets both native registration and gathering to registry. The handle
// remains shared; construction does not register any collectors. A nil pointer
// remains an invalid native handle: Start returns an error and Handler answers
// 503. Use omitted settings or nil Registerer/Gatherer interfaces for defaults.
//
//	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
//		metrics.Registry(prometheus.NewRegistry())
//	})
//	_ = driver
func (metrics *Builder) Registry(registry *prometheus.Registry) *Builder {
	metrics.config.Registerer, metrics.config.Gatherer = registry, registry

	return metrics
}

// Registerer retains the native registration target. Nil infers it from the
// gatherer when possible, otherwise New creates an isolated registry. Typed-nil
// pointers are rejected at Start; no native object is cloned or closed.
//
//	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
//		metrics.Registerer(prometheus.NewRegistry())
//	})
//	_ = driver
func (metrics *Builder) Registerer(registerer prometheus.Registerer) *Builder {
	metrics.config.Registerer = registerer

	return metrics
}

// Gatherer retains the native scrape source. Nil infers it from the registerer
// when possible; without a gatherer Handler answers 501 Not Implemented. A
// typed-nil pointer is invalid and Handler answers a fixed 503 instead.
//
//	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
//		metrics.Gatherer(prometheus.NewRegistry())
//	})
//	_ = driver
func (metrics *Builder) Gatherer(gatherer prometheus.Gatherer) *Builder {
	metrics.config.Gatherer = gatherer

	return metrics
}

// Namespace prefixes instrument names. Empty removes the prefix; native runtime
// metrics and target_info keep their standard names.
//
//	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
//		metrics.Namespace("shop")
//	})
//	_ = driver
func (metrics *Builder) Namespace(namespace string) *Builder {
	metrics.config.Namespace = namespace

	return metrics
}

// RuntimeMetrics controls Go and process collectors registered at Start. New
// enables them initially; explicit false is not overwritten by defaults.
//
//	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
//		metrics.RuntimeMetrics(false)
//	})
//	_ = driver
func (metrics *Builder) RuntimeMetrics(enabled bool) *Builder {
	metrics.config.RuntimeMetrics = enabled

	return metrics
}

// TargetInfo controls the resource gauge without adding resource labels to other
// series. New enables it initially; false explicitly disables registration.
//
//	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
//		metrics.TargetInfo(false)
//	})
//	_ = driver
func (metrics *Builder) TargetInfo(enabled bool) *Builder {
	metrics.config.TargetInfo = enabled

	return metrics
}

// MaxSeries sets the number of label combinations each instrument retains.
// New combinations at capacity are dropped before allocating a child, including
// histogram buckets and exemplars. Existing combinations continue to record.
// Zero selects the finite default of 1000; a negative value records no metrics.
// Neither selects unlimited retention.
//
//	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
//		metrics.MaxSeries(200)
//	})
//	_ = driver
func (metrics *Builder) MaxSeries(maxSeries int) *Builder {
	metrics.config.MaxSeries = maxSeries

	return metrics
}

// MaxInstruments sets the number of instrument definitions the driver keeps.
// Use fixed instrument names and label keys; this second bound prevents dynamic
// definitions from allocating unbounded collectors despite MaxSeries.
// Zero selects the finite default of 1000; a negative value admits no definitions.
// Runtime and target_info are unaffected.
// The telemetry facade still caches supplied definitions, including rejections;
// this driver limit does not make request-derived instrument names safe.
//
//	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
//		metrics.MaxInstruments(100)
//	})
//	_ = driver
func (metrics *Builder) MaxInstruments(maxInstruments int) *Builder {
	metrics.config.MaxInstruments = maxInstruments

	return metrics
}
