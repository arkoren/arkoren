package memorybroadcast

import (
	"log/slog"
)

// BuilderFunc configures the fresh memory broker draft used by New. Callbacks
// run directly once in order; nil entries panic. Publication detaches the
// settings without cloning loggers or closure captures.
//
//	policy := memorybroadcast.BuilderFunc(func(broker *memorybroadcast.Builder) {
//		broker.MaxConcurrentDeliveries(8)
//	})
//	driver := memorybroadcast.New(policy)
type BuilderFunc = func(*Builder)

// Builder configures New before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	memorybroadcast.New(func(broker *memorybroadcast.Builder) {
//		broker.MaxConcurrentDeliveries(8)
//	})
type Builder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// newSettings applies user policies once and publishes the completed settings.
// Normalization runs after the final callback, as in the original constructor.
func newSettings(configure ...BuilderFunc) settings {
	broker := &Builder{
		settings: defaultConfig(),
	}

	for _, configure := range configure {
		configure(broker)
	}

	return broker.settings
}

// MaxConcurrentDeliveries limits simultaneously running delivery handlers.
// Positive values set the bound; zero or negative values normalize to
// DefaultMaxConcurrentDeliveries after all construction callbacks.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	memorybroadcast.New(func(broker *memorybroadcast.Builder) {
//		broker.MaxConcurrentDeliveries(8)
//	})
func (broker *Builder) MaxConcurrentDeliveries(maxConcurrentDeliveries int) *Builder {
	broker.settings.MaxConcurrentDeliveries = maxConcurrentDeliveries

	return broker
}

// Logger selects the shared logger for recovered delivery panics and failures.
// Nil resolves slog.Default after the final construction callback, not per send.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	memorybroadcast.New(func(broker *memorybroadcast.Builder) {
//		broker.Logger(nil)
//	})
func (broker *Builder) Logger(logger *slog.Logger) *Builder {
	broker.settings.Logger = logger

	return broker
}
