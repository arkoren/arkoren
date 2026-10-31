package event

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
)

// Emit dispatches payload like [Bus.Dispatch] for code that reports what
// happened, where a listener must never change the outcome: a listener error
// or panic is logged with the event name instead of returned, and the remaining
// listeners are skipped. [Builder.Logger] selects the logger; nil resolves
// [slog.Default] when the failure is logged. A nil bus does nothing, so a
// component built without an events option pays one comparison.
//
// Example:
//
//	events := event.New(func(events *event.Builder) {
//		events.Logger(logger)
//	})
//	events.Emit(ctx, event.NewTopic[int]("orders.created"), 42)
func (bus *Bus) Emit[T any](ctx context.Context, topic Topic[T], payload T) {
	if bus == nil {
		return
	}

	if err := protect(func() error {
		return bus.Dispatch(ctx, topic, payload)
	}); err != nil {
		logger := cmp.Or(bus.logger, slog.Default())
		logger.ErrorContext(ctx, "event listener failed", "event", topic.Name(), "error", err)
	}
}

// Signal publishes signal like [Bus.Emit] with the [Empty] payload.
//
// Example:
//
//	app.Events().Signal(ctx, application.Started)
func (bus *Bus) Signal(ctx context.Context, signal Topic[Void]) {
	bus.Emit(ctx, signal, Empty)
}

// protect runs fn and turns a panic into an error carrying the stack.
func protect(fn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v\n%s", recovered, debug.Stack())
		}
	}()

	return fn()
}
