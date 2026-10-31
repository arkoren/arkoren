package event

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sync"

	"arkoren.dev/core/internal/name"
)

// Listener handles an in-process event.
type Listener = func(context.Context, any) error

// RegexListener handles an in-process event matched by a regular expression.
type RegexListener = func(context.Context, string, any) error

// Unsubscribe removes a listener registration. Calling it more than once is
// safe.
type Unsubscribe = func()

// namedListener associates a listener with its registration ID.
type namedListener struct {
	// id identifies this registration, not the listener function.
	id uint64
	// listener receives events with the registered exact name.
	listener Listener
}

// regexListener associates a compiled pattern with its listener.
type regexListener struct {
	// id lets the unsubscribe closure remove only this registration.
	id uint64
	// pattern selects event names before the listener snapshot is released.
	pattern *regexp.Regexp
	// listener receives the matched name and its payload.
	listener RegexListener
}

// Bus synchronously dispatches in-process events by name.
type Bus struct {
	// mu protects registration IDs and both listener collections.
	mu sync.RWMutex
	// nextID increases for each exact or regular-expression registration.
	nextID uint64
	// listeners preserves registration order within each exact event name.
	listeners map[string][]namedListener
	// regexListeners preserves registration order across all patterns.
	regexListeners []regexListener

	// logger receives the listener failures [Bus.Emit] swallows. Nil uses
	// [slog.Default].
	logger *slog.Logger
}

// Topic names one kind of event and fixes the payload type T carried by
// it. Topics with the same name share listeners, so two topics must not
// use one name with different payload types. The name is validated when the
// topic is listened to or dispatched; see [Bus.ListenEvent] for the rules.
type Topic[T any] struct {
	// name is the event name used for listener matching.
	name string
}

// Void is the payload type of a signal: a topic that carries no data.
type Void struct{}

// Empty is the [Void] payload, for [Bus.Dispatch] on a signal.
//
//	err := bus.Dispatch(ctx, Started, event.Empty)
var Empty Void

// NewSignal returns the topic called eventName that carries no data. Publish it
// with [Bus.Signal]. It is unrelated to workflow signals, which are messages
// sent to a running workflow.
//
//	var Started = event.NewSignal("app.started")
func NewSignal(eventName string) Topic[Void] {
	return NewTopic[Void](eventName)
}

// NewTopic returns the topic called eventName that carries payloads of type T.
// Construction does not validate the name; registration and dispatch report
// invalid names. Topics sharing a name must also share their payload type.
//
//	topic := event.NewTopic[int]("orders.created")
//	fmt.Println(topic.Name())
func NewTopic[T any](eventName string) Topic[T] {
	return Topic[T]{name: eventName}
}

// Name returns the event name of the topic.
func (topic Topic[T]) Name() string {
	return topic.name
}

// Listen registers a typed listener for topic and returns a function that
// removes it. Events on the same name whose payload is not a T are
// skipped. It returns [ErrInvalidName] when the topic name is not a valid
// event name and [ErrNilFunc] when listener is nil.
//
// Example:
//
//	unsubscribe, err := bus.Listen(UserCreated, func(ctx context.Context, user User) error {
//		return sendWelcome(ctx, user)
//	})
func (bus *Bus) Listen[T any](topic Topic[T], listener func(context.Context, T) error) (Unsubscribe, error) {
	if listener == nil {
		return nil, fmt.Errorf("listen %s: %w", topic.name, ErrNilFunc)
	}

	return bus.ListenEvent(topic.name, func(ctx context.Context, payload any) error {
		typed, ok := payload.(T)

		if !ok && payload != nil {
			return nil
		}

		return listener(ctx, typed)
	})
}

// Dispatch invokes the listeners of topic with payload. It stops at the
// first listener error. Exact listeners run before matching regular
// expression listeners. A listener panic is not recovered; use [Bus.Emit]
// when a listener must never affect the caller. It returns [ErrInvalidName] without invoking any
// listener when the topic name is not a valid event name.
func (bus *Bus) Dispatch[T any](ctx context.Context, topic Topic[T], payload T) error {
	if err := name.Routable(topic.name, false); err != nil {
		return fmt.Errorf("dispatch %q: %w: %w", topic.name, ErrInvalidName, err)
	}

	return bus.dispatch(ctx, topic.name, payload)
}

// ListenEvent registers listener for eventName and returns a
// function that removes it. A valid name is one or more dot-separated tokens
// of 1-64 lowercase letters, digits, or hyphens with no hyphen at either end
// (at most 255 bytes in total). It returns [ErrInvalidName] for any other
// name and [ErrNilFunc] when listener is nil.
//
//	bus := event.New()
//	unsubscribe, err := bus.ListenEvent("orders.created", func(ctx context.Context, payload any) error {
//		return nil
//	})
//	if err != nil {
//		panic(err)
//	}
//	unsubscribe()
func (bus *Bus) ListenEvent(eventName string, listener Listener) (Unsubscribe, error) {
	if err := name.Routable(eventName, false); err != nil {
		return nil, fmt.Errorf("listen %q: %w: %w", eventName, ErrInvalidName, err)
	}

	if listener == nil {
		return nil, fmt.Errorf("listen %q: %w", eventName, ErrNilFunc)
	}

	bus.mu.Lock()
	defer bus.mu.Unlock()

	bus.nextID++
	id := bus.nextID
	bus.listeners[eventName] = append(bus.listeners[eventName], namedListener{id: id, listener: listener})

	return func() {
		bus.mu.Lock()
		defer bus.mu.Unlock()

		bus.listeners[eventName] = slices.DeleteFunc(bus.listeners[eventName], func(item namedListener) bool {
			return item.id == id
		})
	}, nil
}

// ListenRegex registers listener for event names matched by pattern and
// returns a function that removes it. The pattern is not anchored: it
// matches any name that contains a match, so `storage` also matches
// "app.storage-check"; write `^storage\.` to match a prefix. It returns [ErrNilFunc] when pattern or
// listener is nil.
func (bus *Bus) ListenRegex(pattern *regexp.Regexp, listener RegexListener) (Unsubscribe, error) {
	if pattern == nil || listener == nil {
		return nil, fmt.Errorf("listen regex: %w", ErrNilFunc)
	}

	bus.mu.Lock()
	defer bus.mu.Unlock()

	bus.nextID++
	id := bus.nextID
	bus.regexListeners = append(bus.regexListeners, regexListener{
		id:       id,
		pattern:  pattern,
		listener: listener,
	})

	return func() {
		bus.mu.Lock()
		defer bus.mu.Unlock()

		bus.regexListeners = slices.DeleteFunc(bus.regexListeners, func(item regexListener) bool {
			return item.id == id
		})
	}, nil
}

// dispatch invokes listeners for eventName in registration order. It stops at the
// first listener error. Exact listeners run before matching regular expression
// listeners.
func (bus *Bus) dispatch(ctx context.Context, eventName string, event any) error {
	listeners, regexListeners := bus.snapshot(eventName)

	for _, listener := range listeners {
		if err := listener(ctx, event); err != nil {
			return err
		}
	}

	for _, listener := range regexListeners {
		if err := listener(ctx, eventName, event); err != nil {
			return err
		}
	}

	return nil
}

// snapshot copies listeners so callbacks can register listeners safely. It
// allocates nothing when no listener matches.
func (bus *Bus) snapshot(eventName string) ([]Listener, []RegexListener) {
	bus.mu.RLock()
	defer bus.mu.RUnlock()

	var listeners []Listener

	for _, registered := range bus.listeners[eventName] {
		listeners = append(listeners, registered.listener)
	}

	var regexListeners []RegexListener

	for _, registered := range bus.regexListeners {
		if registered.pattern.MatchString(eventName) {
			regexListeners = append(regexListeners, registered.listener)
		}
	}

	return listeners, regexListeners
}
