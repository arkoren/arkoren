package cache

import (
	"arkoren.dev/core/service/telemetry"
)

// DefaultName is the store name reported in telemetry when [Builder.Name] is
// empty.
//
// Example:
//
//	name := cache.DefaultName
//	cache.New(driver, func(store *cache.Builder) {
//		store.Name(name)
//	})
const DefaultName = "default"

// settings configures a [Cache].
type settings struct {
	// Telemetry receives the spans and metrics of every operation. Nil uses
	// [telemetry.Default], resolved each time an operation runs, so calling
	// [telemetry.SetDefault] after [New] still takes effect.
	Telemetry *telemetry.Telemetry

	// Name identifies the store in the arkoren.cache.store telemetry
	// attribute, which tells apart several caches in one process. Choose a
	// short, fixed value such as "sessions". Empty reports [DefaultName].
	Name string
}

// Builder configures New before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	cache.New(driver, func(store *cache.Builder) {
//		store.Name("sessions")
//	})
type Builder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// newSettings applies user policies once and publishes the completed settings.
// Normalization runs after the final callback, as in the original constructor.
func newSettings(configure ...BuilderFunc) settings {
	cache := &Builder{}

	for _, configure := range configure {
		configure(cache)
	}

	return cache.settings
}

// Telemetry sets the telemetry that records operations. Without it the
// cache follows [telemetry.Default].
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	cache.New(driver, func(store *cache.Builder) {
//		store.Telemetry(nil)
//	})
func (store *Builder) Telemetry(t *telemetry.Telemetry) *Builder {
	store.settings.Telemetry = t

	return store
}

// Name sets the store name reported as arkoren.cache.store.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	cache.New(driver, func(store *cache.Builder) {
//		store.Name("primary")
//	})
func (store *Builder) Name(name string) *Builder {
	store.settings.Name = name

	return store
}

// BuilderFunc configures a Builder.
// Example:
//
//	var policy cache.BuilderFunc = func(store *cache.Builder) {
//		store.Name("sessions")
//	}
//	_ = cache.New(driver, policy)
type BuilderFunc = func(*Builder)
