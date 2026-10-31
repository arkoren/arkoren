package event_test

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"

	"arkoren.dev/core/event"
)

// busEvent supplies a typed payload for listener and routing assertions.
// Its integer distinguishes delivery from merely invoking a callback.
type busEvent struct {
	// Value is the payload marker expected by the receiving listener.
	Value int
}

// contextKey isolates this package's context-propagation test value.
// An unexported empty type avoids collisions with keys used by the bus.
type contextKey struct{}

// must returns unsubscribe, and panics with err when the registration
// failed, so tests with fixed valid names stay linear.
func must(unsubscribe event.Unsubscribe, err error) event.Unsubscribe {
	if err != nil {
		panic(err)
	}

	return unsubscribe
}

var (
	// ordersCreated is the primary topic used by dispatch assertions.
	ordersCreated = event.NewTopic[busEvent]("orders.created")
	// ordersDeleted shares an area prefix but must route independently.
	ordersDeleted = event.NewTopic[busEvent]("orders.deleted")
	// userCreated exercises unsubscribe outside the orders prefix.
	userCreated = event.NewTopic[busEvent]("user.created")
)

// TestTypedListenReceivesPayload checks payload delivery through a typed topic.
// The topic must retain its name and deliver the original integer marker.
func TestTypedListenReceivesPayload(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := 0

	must(bus.Listen(ordersCreated, func(_ context.Context, payload busEvent) error {
		got = payload.Value

		return nil
	}))

	if err := bus.Dispatch(t.Context(), ordersCreated, busEvent{Value: 3}); err != nil {
		t.Fatal(err)
	}

	if got != 3 {
		t.Fatalf("got = %d, want 3", got)
	}

	if ordersCreated.Name() != "orders.created" {
		t.Fatalf("name = %q", ordersCreated.Name())
	}
}

// TestTypedListenUnsubscribe checks removal of a typed listener registration.
// Dispatch after removal must not invoke the callback.
func TestTypedListenUnsubscribe(t *testing.T) {
	t.Parallel()

	bus := event.New()
	called := 0
	unsubscribe := must(bus.Listen(ordersCreated, func(context.Context, busEvent) error {
		called++

		return nil
	}))
	unsubscribe()

	if err := bus.Dispatch(t.Context(), ordersCreated, busEvent{}); err != nil {
		t.Fatal(err)
	}

	if called != 0 {
		t.Fatalf("called = %d, want 0", called)
	}
}

// TestBusDispatchesSynchronously checks completion before Dispatch returns.
// The listener's payload-dependent side effect must already be visible.
func TestBusDispatchesSynchronously(t *testing.T) {
	t.Parallel()

	bus := event.New()
	called := false

	must(bus.ListenEvent("orders.created", func(_ context.Context, payload any) error {
		called = payload.(busEvent).Value == 7

		return nil
	}))

	if err := bus.Dispatch(t.Context(), ordersCreated, busEvent{Value: 7}); err != nil {
		t.Fatal(err)
	}

	if !called {
		t.Fatal("listener did not complete before dispatch returned")
	}
}

// TestBusStopsAtFirstError checks exact-listener order and error propagation.
// A third listener must not run after the second returns an error.
func TestBusStopsAtFirstError(t *testing.T) {
	t.Parallel()

	bus := event.New()
	want := errors.New("listener failed")
	order := make([]int, 0, 3)

	must(bus.ListenEvent("orders.created", func(context.Context, any) error {
		order = append(order, 1)

		return nil
	}))
	must(bus.ListenEvent("orders.created", func(context.Context, any) error {
		order = append(order, 2)

		return want
	}))
	must(bus.ListenEvent("orders.created", func(context.Context, any) error {
		order = append(order, 3)

		return nil
	}))

	if err := bus.Dispatch(t.Context(), ordersCreated, busEvent{}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want listener error", err)
	}

	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("order = %v, want [1 2]", order)
	}
}

// TestBusRoutesByName checks exact-name isolation between related topics.
// Each dispatch must invoke only its own registered listener.
func TestBusRoutesByName(t *testing.T) {
	t.Parallel()

	bus := event.New()
	created := 0
	deleted := 0

	must(bus.ListenEvent("orders.created", func(context.Context, any) error {
		created++

		return nil
	}))
	must(bus.ListenEvent("orders.deleted", func(context.Context, any) error {
		deleted++

		return nil
	}))

	_ = bus.Dispatch(t.Context(), ordersCreated, busEvent{})
	_ = bus.Dispatch(t.Context(), ordersDeleted, busEvent{})

	if created != 1 || deleted != 1 {
		t.Fatalf("created = %d, deleted = %d, want 1, 1", created, deleted)
	}
}

// TestBusDispatchesToRegexListener checks matching by area prefix.
// A regex listener receives both the full matched name and original payload.
func TestBusDispatchesToRegexListener(t *testing.T) {
	t.Parallel()

	bus := event.New()
	receivedName := ""

	must(bus.ListenRegex(regexp.MustCompile(`^orders\.`), func(_ context.Context, name string, payload any) error {
		receivedName = name

		if payload.(busEvent).Value != 7 {
			t.Fatalf("payload = %#v", payload)
		}

		return nil
	}))

	if err := bus.Dispatch(t.Context(), ordersCreated, busEvent{Value: 7}); err != nil {
		t.Fatal(err)
	}

	if receivedName != "orders.created" {
		t.Fatalf("name = %q, want orders.created", receivedName)
	}
}

// TestBusIgnoresNonmatchingRegexListener checks pattern isolation.
// A listener for another prefix must remain untouched by dispatch.
func TestBusIgnoresNonmatchingRegexListener(t *testing.T) {
	t.Parallel()

	bus := event.New()
	called := false

	must(bus.ListenRegex(regexp.MustCompile(`^users\.`), func(context.Context, string, any) error {
		called = true

		return nil
	}))

	if err := bus.Dispatch(t.Context(), ordersCreated, busEvent{}); err != nil {
		t.Fatal(err)
	}

	if called {
		t.Fatal("nonmatching regex listener ran")
	}
}

// TestBusRunsExactListenersBeforeRegexListeners checks cross-family ordering.
// Exact listeners run first even when the regex listener was registered first.
func TestBusRunsExactListenersBeforeRegexListeners(t *testing.T) {
	t.Parallel()

	bus := event.New()
	order := make([]int, 0, 2)

	must(bus.ListenRegex(regexp.MustCompile(`^orders\.`), func(context.Context, string, any) error {
		order = append(order, 2)

		return nil
	}))
	must(bus.ListenEvent("orders.created", func(context.Context, any) error {
		order = append(order, 1)

		return nil
	}))

	if err := bus.Dispatch(t.Context(), ordersCreated, busEvent{}); err != nil {
		t.Fatal(err)
	}

	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("order = %v, want [1 2]", order)
	}
}

// TestBusStopsAfterRegexListenerError checks failure within pattern fan-out.
// Later matching patterns must not run after an earlier listener fails.
func TestBusStopsAfterRegexListenerError(t *testing.T) {
	t.Parallel()

	bus := event.New()
	want := errors.New("regex listener failed")
	called := false

	must(bus.ListenRegex(regexp.MustCompile(`^orders\.`), func(context.Context, string, any) error {
		return want
	}))
	must(bus.ListenRegex(regexp.MustCompile(`created$`), func(context.Context, string, any) error {
		called = true

		return nil
	}))

	if err := bus.Dispatch(t.Context(), ordersCreated, busEvent{}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want regex listener error", err)
	}

	if called {
		t.Fatal("listener ran after regex listener error")
	}
}

// TestBusPassesContext checks preservation of caller context values.
// Dispatch must not substitute a fresh context when invoking listeners.
func TestBusPassesContext(t *testing.T) {
	t.Parallel()

	bus := event.New()
	key := contextKey{}
	ctx := context.WithValue(t.Context(), key, "value")
	seen := ""

	must(bus.ListenEvent("orders.created", func(received context.Context, _ any) error {
		seen, _ = received.Value(key).(string)

		return nil
	}))

	if err := bus.Dispatch(ctx, ordersCreated, busEvent{}); err != nil {
		t.Fatal(err)
	}

	if seen != "value" {
		t.Fatalf("context value = %q, want value", seen)
	}
}

// TestBusRegistrationDuringDispatchAffectsNextDispatch checks listener snapshots.
// Registration inside a callback must affect only a later dispatch.
func TestBusRegistrationDuringDispatchAffectsNextDispatch(t *testing.T) {
	t.Parallel()

	bus := event.New()
	called := 0

	must(bus.ListenEvent("orders.created", func(context.Context, any) error {
		must(bus.ListenEvent("orders.created", func(context.Context, any) error {
			called++

			return nil
		}))

		return nil
	}))

	_ = bus.Dispatch(t.Context(), ordersCreated, busEvent{})

	if called != 0 {
		t.Fatal("new listener ran during active dispatch")
	}

	_ = bus.Dispatch(t.Context(), ordersCreated, busEvent{})

	if called != 1 {
		t.Fatalf("calls = %d, want 1", called)
	}
}

// TestBusConcurrentUse exercises registration and dispatch from many workers.
// Waiting for every worker keeps the race-checking window inside the test.
func TestBusConcurrentUse(t *testing.T) {
	bus := event.New()
	var wait sync.WaitGroup

	for range 16 {
		wait.Go(func() {
			for range 100 {
				must(bus.ListenEvent("orders.created", func(context.Context, any) error {
					return nil
				}))

				if err := bus.Dispatch(t.Context(), ordersCreated, busEvent{}); err != nil {
					t.Error(err)
				}
			}
		})
	}

	wait.Wait()
}

// TestBusUnsubscribe checks removal of exact and pattern registrations.
// Repeated removal is safe and leaves neither callback active.
func TestBusUnsubscribe(t *testing.T) {
	t.Parallel()

	bus := event.New()
	calls := 0
	unsubscribe := must(bus.ListenEvent("user.created", func(context.Context, any) error {
		calls++

		return nil
	}))

	unsubscribeRegex := must(bus.ListenRegex(regexp.MustCompile(`^user\.`), func(context.Context, string, any) error {
		calls++

		return nil
	}))

	unsubscribe()
	unsubscribe()
	unsubscribeRegex()

	if err := bus.Dispatch(context.Background(), userCreated, busEvent{}); err != nil {
		t.Fatal(err)
	}

	if calls != 0 {
		t.Fatalf("calls = %d, want 0", calls)
	}
}

// TestBusRejectsInvalidNames checks all name-validating bus entry points.
// Typed registration, raw registration, and dispatch share the same sentinel.
func TestBusRejectsInvalidNames(t *testing.T) {
	t.Parallel()

	bus := event.New()
	invalid := event.NewTopic[busEvent]("Orders_Created")

	if _, err := bus.Listen(invalid, func(context.Context, busEvent) error {
		return nil
	}); !errors.Is(err, event.ErrInvalidName) {
		t.Errorf("Listen error = %v, want ErrInvalidName", err)
	}

	if _, err := bus.ListenEvent("orders..created", func(context.Context, any) error {
		return nil
	}); !errors.Is(err, event.ErrInvalidName) {
		t.Errorf("ListenEvent error = %v, want ErrInvalidName", err)
	}

	if err := bus.Dispatch(t.Context(), invalid, busEvent{}); !errors.Is(err, event.ErrInvalidName) {
		t.Errorf("Dispatch error = %v, want ErrInvalidName", err)
	}
}

// TestBusRejectsNilListeners checks callback misuse across registration APIs.
// A nil pattern is rejected with the same sentinel as a nil listener.
func TestBusRejectsNilListeners(t *testing.T) {
	t.Parallel()

	bus := event.New()

	if _, err := bus.Listen[busEvent](ordersCreated, nil); !errors.Is(err, event.ErrNilFunc) {
		t.Errorf("Listen error = %v, want ErrNilFunc", err)
	}

	if _, err := bus.ListenEvent("orders.created", nil); !errors.Is(err, event.ErrNilFunc) {
		t.Errorf("ListenEvent error = %v, want ErrNilFunc", err)
	}

	if _, err := bus.ListenRegex(nil, func(context.Context, string, any) error {
		return nil
	}); !errors.Is(err, event.ErrNilFunc) {
		t.Errorf("ListenRegex error = %v, want ErrNilFunc", err)
	}
}
