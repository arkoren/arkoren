package memoryqueue

import (
	"log/slog"
	"time"

	"arkoren.dev/core/service/queue/memoryqueue/internal/broker"
)

// settings configures a [Driver].
type settings struct {
	// RetryDelay is used when a delivery requests a retry with a zero delay.
	RetryDelay time.Duration

	// Logger records delivery failures. A nil logger uses [slog.Default].
	Logger *slog.Logger
}

// Builder configures New before publication. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil callback entries panic;
// expanded nil or empty callback slices supply no edits. Retaining the draft
// cannot change published settings. Methods return the same draft.
//
// Example:
//
//	memoryqueue.New(func(consumer *memoryqueue.Builder) {
//		consumer.RetryDelay(time.Second)
//	})
type Builder struct {
	// settings holds the private construction values until publication.
	settings settings
}

// newSettings applies user policies once and publishes the completed settings.
// Normalization runs after the final callback, as in the original constructor.
func newSettings(configure ...BuilderFunc) settings {
	queue := &Builder{}

	for _, configure := range configure {
		configure(queue)
	}

	return queue.settings
}

// RetryDelay supplies the delay when a delivery requests a zero-delay retry.
// Zero or negative values enqueue that retry immediately; positive values defer
// it. An explicit nonzero per-delivery delay retains its runtime precedence.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	memoryqueue.New(func(consumer *memoryqueue.Builder) {
//		consumer.RetryDelay(time.Second)
//	})
func (consumer *Builder) RetryDelay(retryDelay time.Duration) *Builder {
	consumer.settings.RetryDelay = retryDelay

	return consumer
}

// Logger selects the shared logger for delivery failures and recovered panics.
// Nil resolves slog.Default after all construction callbacks.
//
// This setting replaces the previous value on the construction draft.
// Services and loggers remain shared; publication detaches owned settings.
//
// Example:
//
//	memoryqueue.New(func(consumer *memoryqueue.Builder) {
//		consumer.Logger(nil)
//	})
func (consumer *Builder) Logger(logger *slog.Logger) *Builder {
	consumer.settings.Logger = logger

	return consumer
}

// BuilderFunc edits the fresh draft used by New. Callbacks run directly once
// in order; nil entries panic. Publication detaches the settings, not loggers
// or closure captures. Retained drafts cannot alter a running queue.
//
//	policy := memoryqueue.BuilderFunc(func(consumer *memoryqueue.Builder) {
//		consumer.RetryDelay(time.Second)
//	})
//	driver := memoryqueue.New(policy)
type BuilderFunc = func(*Builder)

// New creates a non-durable in-memory job driver. It starts no goroutines.
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
//
// Example:
//
//	memoryqueue.New(func(consumer *memoryqueue.Builder) {
//		consumer.RetryDelay(time.Second)
//	})
func New(configure ...BuilderFunc) *Driver {
	config := newSettings(configure...)

	return &Driver{broker: broker.NewBroker(config.RetryDelay, config.Logger)}
}
