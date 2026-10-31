package broadcasttest

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"

	"arkoren.dev/core/service/broadcast"
)

// ErrDrainLimit is returned by [Fake.Drain] when listeners keep
// publishing new messages and the delivery count passes [MaxDeliveries].
var ErrDrainLimit = errors.New("broadcasttest: drain limit reached")

// ErrForeignConsumer is returned by [Fake.Drain] for a consumer that is not
// backed by the Fake.
var ErrForeignConsumer = errors.New("broadcasttest: consumer is not backed by this fake")

// ErrInvalidHandler is returned when Consume is given a nil handler.
var ErrInvalidHandler = errors.New("broadcasttest: nil handler")

// MaxDeliveries is the most messages one [Fake.Drain] routes, which stops
// listeners that publish on every message from looping forever.
const MaxDeliveries = 1000

// subscriberBuffer is the buffer of each running Consume call.
const subscriberBuffer = 1024

// Fake is an in-memory [broadcast.PublisherDriver] and
// [broadcast.ConsumerDriver] for tests. It records every published message
// and routes messages to listeners only when a test calls [Fake.Drain] or
// runs a consumer on it. A Fake is safe for concurrent use.
type Fake struct {
	// mu guards every field below.
	mu sync.Mutex

	// published records every accepted message in publish order.
	published []record

	// subscribers holds the buffer of every running Consume call.
	subscribers map[chan broadcast.Message]struct{}

	// failErr is returned by every Publish until cleared.
	failErr error

	// failNext is returned by the next Publish only.
	failNext error
}

// record is a published message and whether a consumer has received it.
type record struct {
	// message is the published message.
	message broadcast.Message

	// delivered reports whether a consumer received the message.
	delivered bool
}

var _ broadcast.PublisherDriver = (*Fake)(nil)
var _ broadcast.ConsumerDriver = (*Fake)(nil)

// New returns an empty Fake.
func New() *Fake {
	return &Fake{
		subscribers: map[chan broadcast.Message]struct{}{},
	}
}

// Start does nothing. It exists so a Fake can stand in for drivers wired
// into the application lifecycle.
func (fake *Fake) Start(context.Context) error {
	return nil
}

// Stop does nothing. It exists so a Fake can stand in for drivers wired into
// the application lifecycle.
func (fake *Fake) Stop(context.Context) error {
	return nil
}

// FailWith makes every following Publish return err until FailWith(nil).
func (fake *Fake) FailWith(err error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.failErr = err
}

// FailNext makes the next Publish return err. Later calls succeed again.
func (fake *Fake) FailNext(err error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.failNext = err
}

// Reset forgets all recorded messages and clears injected failures. Running
// Consume calls keep running.
func (fake *Fake) Reset() {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.published = nil
	fake.failErr = nil
	fake.failNext = nil
}

// Publish validates and records message and hands it to running Consume
// calls. It returns [broadcast.ErrInvalidName] for an invalid name and an
// injected failure, if any, without recording.
func (fake *Fake) Publish(ctx context.Context, message broadcast.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := broadcast.ValidateName(message.Name); err != nil {
		return err
	}

	message.Payload = slices.Clone(message.Payload)
	message.Metadata = maps.Clone(message.Metadata)

	subscribers, err := fake.record(message)

	if err != nil {
		return err
	}

	for _, subscriber := range subscribers {
		copied := message
		copied.Payload = slices.Clone(message.Payload)
		copied.Metadata = maps.Clone(message.Metadata)

		select {
		case subscriber <- copied:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// record stores message and returns the running subscribers, or the injected
// failure.
func (fake *Fake) record(message broadcast.Message) ([]chan broadcast.Message, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	if err := fake.takeFailureLocked(); err != nil {
		return nil, err
	}

	subscribers := make([]chan broadcast.Message, 0, len(fake.subscribers))

	for subscriber := range fake.subscribers {
		subscribers = append(subscribers, subscriber)
	}

	fake.published = append(fake.published, record{
		message:   message,
		delivered: len(subscribers) > 0,
	})

	return subscribers, nil
}

// takeFailureLocked returns the injected publish failure, consuming a
// FailNext failure.
func (fake *Fake) takeFailureLocked() error {
	if err := fake.failNext; err != nil {
		fake.failNext = nil

		return err
	}

	return fake.failErr
}

// Consume receives messages for handler until ctx is canceled, returning the
// context error. Handler errors are ignored: the Fake never redelivers.
// During [Fake.Drain] it instead routes the recorded messages that no
// consumer has received yet and returns the joined handler errors.
func (fake *Fake) Consume(ctx context.Context, handler broadcast.Handler) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if handler == nil {
		return ErrInvalidHandler
	}

	if _, draining := ctx.Value(deliverKey{}).(struct{}); draining {
		return fake.deliverUndelivered(ctx, handler)
	}

	messages := make(chan broadcast.Message, subscriberBuffer)
	fake.subscribe(messages)
	defer fake.unsubscribe(messages)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message := <-messages:
			_ = handler(ctx, message)
		}
	}
}

// subscribe registers messages as a running consumer.
func (fake *Fake) subscribe(messages chan broadcast.Message) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.subscribers[messages] = struct{}{}
}

// unsubscribe removes messages from the running consumers.
func (fake *Fake) unsubscribe(messages chan broadcast.Message) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	delete(fake.subscribers, messages)
}

// deliverKey is the context key marking a [Fake.Drain] run.
type deliverKey struct{}

// Drain synchronously routes the recorded messages that no consumer has
// received yet through consumer's listeners, in publish order, including
// messages that listeners publish meanwhile. consumer must be backed by the
// Fake. It returns the joined listener errors, [ErrDrainLimit], or the
// error of ctx. Every message is delivered once, even when a listener fails.
func (fake *Fake) Drain(ctx context.Context, consumer *broadcast.Consumer) error {
	if backing, ok := consumer.DriverAs[*Fake](); !ok || backing != fake {
		return ErrForeignConsumer
	}

	return consumer.Run(context.WithValue(ctx, deliverKey{}, struct{}{}))
}

// deliverUndelivered passes undelivered messages to handler until none is left.
func (fake *Fake) deliverUndelivered(ctx context.Context, handler broadcast.Handler) error {
	var errs []error

	for delivered := 0; ; delivered++ {
		if delivered == MaxDeliveries && len(fake.Undelivered()) > 0 {
			return errors.Join(append(errs, ErrDrainLimit)...)
		}

		message, found := fake.takeUndelivered()

		if !found {
			return errors.Join(errs...)
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		errs = append(errs, handler(ctx, message))
	}
}

// takeUndelivered marks the oldest undelivered message delivered and returns it.
func (fake *Fake) takeUndelivered() (broadcast.Message, bool) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	index := slices.IndexFunc(fake.published, func(entry record) bool {
		return !entry.delivered
	})

	if index < 0 {
		return broadcast.Message{}, false
	}

	fake.published[index].delivered = true

	message := fake.published[index].message
	message.Payload = slices.Clone(message.Payload)
	message.Metadata = maps.Clone(message.Metadata)

	return message, true
}

// Published returns a copy of every message accepted since the last Reset, in
// publish order, including messages already delivered.
func (fake *Fake) Published() []broadcast.Message {
	return fake.messages(func(record) bool {
		return true
	})
}

// Undelivered returns a copy of the published messages no consumer has
// received yet, in publish order.
func (fake *Fake) Undelivered() []broadcast.Message {
	return fake.messages(func(entry record) bool {
		return !entry.delivered
	})
}

// messages returns copies of the recorded messages accepted by keep.
func (fake *Fake) messages(keep func(record) bool) []broadcast.Message {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	var messages []broadcast.Message

	for _, entry := range fake.published {
		if keep(entry) {
			message := entry.message
			message.Payload = slices.Clone(message.Payload)
			message.Metadata = maps.Clone(message.Metadata)
			messages = append(messages, message)
		}
	}

	return messages
}

// Payloads decodes the payloads of the published messages for topic, in
// publish order. It returns an error when a payload does not decode into T.
func (fake *Fake) Payloads[T any](topic broadcast.Topic[T]) ([]T, error) {
	var payloads []T

	for _, message := range fake.Published() {
		if message.Name != topic.Name() {
			continue
		}

		var payload T

		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", topic.Name(), err)
		}

		payloads = append(payloads, payload)
	}

	return payloads, nil
}

// AssertPublished fails t unless a message for topic was published whose
// decoded payload satisfies every predicate. Without predicates any payload
// matches.
func (fake *Fake) AssertPublished[T any](t testing.TB, topic broadcast.Topic[T], preds ...func(T) bool) {
	t.Helper()

	if !fake.anyPublished(t, topic, preds) {
		t.Errorf("no %s broadcast matching the predicate was published; published: %s", topic.Name(), fake.describePublished())
	}
}

// AssertNotPublished fails t if a message for topic was published whose
// decoded payload satisfies every predicate. Without predicates any payload
// matches.
func (fake *Fake) AssertNotPublished[T any](t testing.TB, topic broadcast.Topic[T], preds ...func(T) bool) {
	t.Helper()

	if fake.anyPublished(t, topic, preds) {
		t.Errorf("a %s broadcast matching the predicate was published; published: %s", topic.Name(), fake.describePublished())
	}
}

// anyPublished reports whether a published payload for topic satisfies every
// predicate, failing t when a payload cannot be decoded.
func (fake *Fake) anyPublished[T any](t testing.TB, topic broadcast.Topic[T], preds []func(T) bool) bool {
	t.Helper()

	payloads, err := fake.Payloads(topic)

	if err != nil {
		t.Errorf("%v", err)

		return false
	}

	return slices.ContainsFunc(payloads, func(payload T) bool {
		return matchesAll(payload, preds)
	})
}

// matchesAll reports whether payload satisfies every predicate.
func matchesAll[T any](payload T, preds []func(T) bool) bool {
	return !slices.ContainsFunc(preds, func(pred func(T) bool) bool {
		return !pred(payload)
	})
}

// AssertPublishedCount fails t unless exactly n messages were published for
// topic.
func (fake *Fake) AssertPublishedCount[T any](t testing.TB, topic broadcast.Topic[T], n int) {
	t.Helper()

	payloads, err := fake.Payloads(topic)

	if err != nil {
		t.Errorf("%v", err)

		return
	}

	if len(payloads) != n {
		t.Errorf("%d %s broadcasts published, want %d; published: %s", len(payloads), topic.Name(), n, fake.describePublished())
	}
}

// AssertNothingPublished fails t if any message was published.
func (fake *Fake) AssertNothingPublished(t testing.TB) {
	t.Helper()

	if len(fake.Published()) > 0 {
		t.Errorf("broadcasts were published, want none; published: %s", fake.describePublished())
	}
}

// describePublished summarizes the published messages for failure output.
func (fake *Fake) describePublished() string {
	messages := fake.Published()

	if len(messages) == 0 {
		return "none"
	}

	descriptions := make([]string, len(messages))

	for index, message := range messages {
		descriptions[index] = fmt.Sprintf("%s %s", message.Name, message.Payload)
	}

	return fmt.Sprint(descriptions)
}
