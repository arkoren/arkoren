package storage

import (
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/telemetry"
)

// DefaultName is the store name reported in telemetry when [Builder.Name] is
// empty.
//
// Example:
//
//	name := storage.DefaultName
//	storage.New(driver, func(store *storage.Builder) {
//		store.Name(name)
//	})
const DefaultName = "default"

// settings configures a [Storage].
type settings struct {
	// Telemetry receives the spans and metrics of every operation. Nil uses
	// [telemetry.Default], resolved each time an operation runs, so calling
	// [telemetry.SetDefault] after [New] still takes effect.
	Telemetry *telemetry.Telemetry

	// Name identifies the store in the arkoren.storage.store telemetry
	// attribute, which tells apart several stores in one process. Choose a
	// short, fixed value such as "uploads". Empty reports [DefaultName].
	Name string

	// Events receives [ObjectWritten] and [ObjectDeleted]. Nil, the default,
	// publishes nothing.
	Events *event.Bus
}

// Builder configures New before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	storage.New(driver, func(store *storage.Builder) {
//		store.Name("uploads")
//	})
type Builder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// newSettings applies policies once and publishes an independent settings value.
// Shared telemetry/event services are retained, not copied or started here.
func newSettings(configure ...BuilderFunc) settings {
	storage := &Builder{}

	for _, configure := range configure {
		configure(storage)
	}

	return storage.settings
}

// Telemetry sets the telemetry that records operations. Without it the
// store follows [telemetry.Default].
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	storage.New(driver, func(store *storage.Builder) {
//		store.Telemetry(nil)
//	})
func (store *Builder) Telemetry(t *telemetry.Telemetry) *Builder {
	store.settings.Telemetry = t

	return store
}

// Name sets the store name reported as arkoren.storage.store and in mutation
// events. Empty selects DefaultName when an operation runs; choose a fixed,
// non-secret label rather than a user ID or object key.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	storage.New(driver, func(store *storage.Builder) {
//		store.Name("primary")
//	})
func (store *Builder) Name(name string) *Builder {
	store.settings.Name = name

	return store
}

// Events selects the shared bus receiving ObjectWritten and ObjectDeleted
// after successful writes and deletes. Nil publishes nothing; the bus and
// driver remain live dependencies rather than copied configuration data.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	storage.New(driver, func(store *storage.Builder) {
//		store.Events(nil)
//	})
func (store *Builder) Events(bus *event.Bus) *Builder {
	store.settings.Events = bus

	return store
}

// BuilderFunc is a construction policy accepted by New. Policies share one
// draft, run directly once in order, and return no value. A supplied nil policy
// panics; expanding a nil or empty slice supplies no policies.
//
// Example:
//
//	policies := []storage.BuilderFunc{
//		func(store *storage.Builder) {
//			store.Name("uploads")
//		},
//	}
//	files := storage.New(driver, policies...)
//	_ = files.Driver()
type BuilderFunc = func(*Builder)
