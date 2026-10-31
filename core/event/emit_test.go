package event_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"arkoren.dev/core/event"
)

// TestEmitWithNilBusDoesNothing checks optional observer wiring.
// Emitting through a nil bus must be safe without a caller-side guard.
func TestEmitWithNilBusDoesNothing(t *testing.T) {
	t.Parallel()

	var bus *event.Bus

	bus.Emit(context.Background(), ordersCreated, busEvent{Value: 1})
}

// TestEmitDeliversPayload checks typed delivery through the safe emission API.
// The listener must receive the original marker rather than a zero payload.
func TestEmitDeliversPayload(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := 0

	must(bus.Listen(ordersCreated, func(_ context.Context, payload busEvent) error {
		got = payload.Value

		return nil
	}))

	bus.Emit(context.Background(), ordersCreated, busEvent{Value: 7})

	if got != 7 {
		t.Fatalf("payload = %d, want 7", got)
	}
}

// TestEmitLogsListenerError checks the observer-error reporting boundary.
// Logging must identify both the event and the swallowed listener error.
func TestEmitLogsListenerError(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	bus := event.New(func(events *event.Builder) {
		events.Logger(slog.New(slog.NewTextHandler(&buffer, nil)))
	})

	must(bus.Listen(ordersCreated, func(context.Context, busEvent) error {
		return errors.New("listener broke")
	}))

	bus.Emit(context.Background(), ordersCreated, busEvent{})

	if !strings.Contains(buffer.String(), "listener broke") || !strings.Contains(buffer.String(), "orders.created") {
		t.Fatalf("log = %q, want the event name and the listener error", buffer.String())
	}
}

// TestEmitRecoversListenerPanic checks that observer panics stay contained.
// The recovered value must be visible in the configured logger's output.
func TestEmitRecoversListenerPanic(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	bus := event.New(func(events *event.Builder) {
		events.Logger(slog.New(slog.NewTextHandler(&buffer, nil)))
	})

	must(bus.Listen(ordersCreated, func(context.Context, busEvent) error {
		panic("listener exploded")
	}))

	bus.Emit(context.Background(), ordersCreated, busEvent{})

	if !strings.Contains(buffer.String(), "listener exploded") {
		t.Fatalf("log = %q, want the panic value", buffer.String())
	}
}

// TestSignalDeliversToSignalListeners checks data-free emission and nil safety.
// Only the configured bus may invoke the registered signal callback.
func TestSignalDeliversToSignalListeners(t *testing.T) {
	t.Parallel()

	started := event.NewSignal("orders.started")
	bus := event.New()
	calls := 0

	must(bus.Listen(started, func(context.Context, event.Void) error {
		calls++

		return nil
	}))

	bus.Signal(context.Background(), started)
	var none *event.Bus

	none.Signal(context.Background(), started)

	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

// TestConstructionCallbacksPublishIndependentLogger verifies ordered, once-only
// callbacks on one root and shared logger identity after draft mutation.
func TestConstructionCallbacksPublishIndependentLogger(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, nil))
	var retained *event.Builder
	calls := 0
	bus := event.New(func(events *event.Builder) {
		retained = events
		calls++
		events.Logger(slog.New(slog.DiscardHandler))
	}, func(events *event.Builder) {
		calls++

		if events != retained || events.Logger(logger) != events {
			t.Fatal("callbacks did not share one receiver")
		}
	})
	retained.Logger(nil)

	must(bus.Listen(ordersCreated, func(context.Context, busEvent) error {
		return errors.New("shared logger")
	}))
	bus.Emit(t.Context(), ordersCreated, busEvent{})

	if calls != 2 || !strings.Contains(buffer.String(), "shared logger") {
		t.Fatalf("calls=%d, log=%s", calls, buffer.String())
	}
}

// TestConstructionEmptyCallbackExpansions keeps default construction valid
// for omitted, nil-expanded, empty-expanded, and empty callback policies.
func TestConstructionEmptyCallbackExpansions(t *testing.T) {
	t.Parallel()

	var omitted []func(*event.Builder)

	for _, bus := range []*event.Bus{event.New(), event.New(omitted...), event.New([]func(*event.Builder){}...), event.New(func(events *event.Builder) {
	})} {
		if err := bus.Dispatch(t.Context(), ordersCreated, busEvent{}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestConstructionNilCallbackPanics distinguishes nil entries from omission
// and checks invocation stops at the supplied callback's exact position.
func TestConstructionNilCallbackPanics(t *testing.T) {
	t.Parallel()

	calls := 0
	defer func() {
		if recover() == nil || calls != 1 {
			t.Fatalf("nil callback did not panic in order: calls=%d", calls)
		}
	}()
	event.New(func(events *event.Builder) {
		calls++
	}, nil, func(events *event.Builder) {
		calls++
	})
}

// TestConstructionCallbackPanicPropagates ensures constructor policies are
// not handled like runtime listeners, whose panics Emit deliberately catches.
func TestConstructionCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "policy failed" {
			t.Fatalf("panic=%v", got)
		}
	}()
	event.New(func(events *event.Builder) {
		panic("policy failed")
	})
}

// TestConstructionNilLoggerResolvesDefaultAtEmission protects late default
// resolution. It is serial because slog.Default is process-global state.
func TestConstructionNilLoggerResolvesDefaultAtEmission(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(previous)
	})

	bus := event.New(func(events *event.Builder) {
		events.Logger(slog.New(slog.DiscardHandler))
		events.Logger(nil)
	})

	var buffer bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, nil)))

	must(bus.Listen(ordersCreated, func(context.Context, busEvent) error {
		return errors.New("late default")
	}))
	bus.Emit(t.Context(), ordersCreated, busEvent{})

	if !strings.Contains(buffer.String(), "late default") {
		t.Fatalf("nil logger was resolved too early: %s", buffer.String())
	}
}
