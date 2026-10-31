package event

import "log/slog"

// BuilderFunc configures a Builder.
type BuilderFunc = func(*Builder)

// New creates an empty bus. Callbacks run directly once, synchronously in
// order on one fresh draft. A supplied nil callback or callback panic
// propagates; omitted callbacks and expanded nil/empty slices make no edits.
// Publication copies settings, but the logger remains shared.
//
//	events := event.New(func(events *event.Builder) {
//		events.Logger(slog.Default())
//	})
func New(configure ...BuilderFunc) *Bus {
	events := &Builder{}

	for _, configure := range configure {
		configure(events)
	}

	return &Bus{
		listeners: make(map[string][]namedListener),
		logger:    events.logger,
	}
}

// Builder configures one event bus during [New]. Settings are copied before
// New returns; retaining this draft cannot reconfigure the published bus.
// Loggers remain shared dependencies, not cloned values.
//
//	events := event.New(func(events *event.Builder) {
//		events.Logger(slog.Default())
//	})
type Builder struct {
	// logger receives the listener errors and panics that [Bus.Emit]
	// swallows. Nil uses [slog.Default], resolved when one is logged.
	logger *slog.Logger
}

// Logger selects the logger for errors and panics swallowed by [Bus.Emit].
// Nil restores slog.Default resolution when an error is logged. It returns
// the same draft, and does not invoke or copy the logger.
//
//	events := event.New(func(events *event.Builder) {
//		events.Logger(slog.Default())
//	})
func (events *Builder) Logger(logger *slog.Logger) *Builder {
	events.logger = logger

	return events
}
