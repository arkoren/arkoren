package memorytelemetry

// BuilderFunc configures one memory-driver construction draft. Settings are
// copied at publication, so retaining the callback's draft cannot resize a store.
//
//	var configure memorytelemetry.BuilderFunc = func(store *memorytelemetry.Builder) {
//		store.MaxSpans(100).MaxLogs(100).MaxSeries(50)
//	}
//	store := memorytelemetry.New(configure)
//	fmt.Println(len(store.Spans())) // Construction records nothing.
type BuilderFunc = func(*Builder)

const (
	// defaultMaxSpans is how many finished spans the ring keeps by default.
	defaultMaxSpans = 1000

	// defaultMaxLogs is how many exported log records the ring keeps by
	// default.
	defaultMaxLogs = 1000

	// defaultMaxSeries is how many label combinations each instrument keeps
	// by default.
	defaultMaxSeries = 1000
)

// settings holds recording capacities copied into each driver. Nonpositive
// capacities disable retention without disabling ID generation.
type settings struct {
	// MaxSpans is the capacity of the ring of finished spans; the oldest
	// span is dropped when it is full. Zero or negative keeps no spans: the
	// driver then only generates and propagates IDs. The default from
	// [New] is 1000.
	MaxSpans int

	// MaxLogs is the capacity of the ring of exported log records; the oldest
	// record is dropped when it is full. Zero or negative keeps no logs. The
	// default from [New] is 1000.
	MaxLogs int

	// MaxSeries is how many distinct label combinations one instrument keeps.
	// Recordings with a new combination beyond it are dropped. Zero or
	// negative keeps no metrics. The default from [New] is 1000.
	MaxSeries int
}

// Builder configures recording capacities before [New] publishes a driver.
// Settings replace previous values. New copies the completed root, so a
// retained draft cannot resize a running store.
//
//	store := memorytelemetry.New(func(store *memorytelemetry.Builder) {
//		store.MaxSpans(500)
//	})
type Builder struct {
	// settings holds construction state, not the driver's live state.
	settings settings
}

// defaultConfig returns the default configuration.
func defaultConfig() settings {
	return settings{
		MaxSpans:  defaultMaxSpans,
		MaxLogs:   defaultMaxLogs,
		MaxSeries: defaultMaxSeries,
	}
}

// MaxSpans replaces finished-span capacity and returns the same draft.
// The default is 1000; zero or negative retains no spans but still generates
// and propagates IDs. At capacity the oldest finished span is dropped.
//
//	store := memorytelemetry.New(func(store *memorytelemetry.Builder) {
//		store.MaxSpans(500)
//	})
func (store *Builder) MaxSpans(maxSpans int) *Builder {
	store.settings.MaxSpans = maxSpans

	return store
}

// MaxSeries replaces per-instrument label-combination capacity and returns
// the same draft. The default is 1000; zero or negative keeps no metrics.
// New combinations beyond the capacity are dropped, not existing series.
//
//	store := memorytelemetry.New(func(store *memorytelemetry.Builder) {
//		store.MaxSeries(50)
//	})
func (store *Builder) MaxSeries(maxSeries int) *Builder {
	store.settings.MaxSeries = maxSeries

	return store
}

// MaxLogs replaces log-record capacity and returns the same draft.
// The default is 1000; zero or negative keeps no logs. At capacity the
// oldest record is dropped. This does not change span or series capacity.
//
//	store := memorytelemetry.New(func(store *memorytelemetry.Builder) {
//		store.MaxLogs(500)
//	})
func (store *Builder) MaxLogs(maxLogs int) *Builder {
	store.settings.MaxLogs = maxLogs

	return store
}
