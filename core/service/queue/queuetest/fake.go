package queuetest

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/queue"
)

var (
	// ErrInvalidQueue is returned when a dispatched message names no queue.
	ErrInvalidQueue = errors.New("queuetest: message has no queue")

	// ErrInvalidHandler is returned when Consume is given a nil handler.
	ErrInvalidHandler = errors.New("queuetest: nil delivery handler")

	// ErrDeliverySettled is returned when a delivery is settled more than once.
	ErrDeliverySettled = errors.New("queuetest: delivery already settled")

	// ErrUnsettled marks a delivery whose handler returned without settling it.
	ErrUnsettled = errors.New("queuetest: delivery was not settled")
)

// Fake is an in-memory [queue.DispatcherDriver] and [queue.ConsumerDriver]
// for tests. It records every dispatched message, keeps undelivered messages
// pending, and executes them only when a test calls [Fake.Drain] or runs a
// consumer on it. A Fake is safe for concurrent use.
type Fake struct {
	// mu guards every field below.
	mu sync.Mutex

	// changed is closed and replaced whenever pending messages change.
	changed chan struct{}

	// dispatched records every accepted message in dispatch order.
	dispatched []queue.Message

	// pending holds undelivered messages per queue in arrival order.
	pending map[string][]pendingMessage

	// rejected records messages settled with Reject.
	rejected []queue.Message

	// failed records messages settled with Fail or left unsettled.
	failed []queue.Message

	// failErr is returned by every Dispatch until cleared.
	failErr error

	// failNext is returned by the next Dispatch only.
	failNext error

	// sequence numbers generated message IDs.
	sequence uint64
}

// pendingMessage is a message awaiting delivery from due on.
type pendingMessage struct {
	// message is the undelivered message.
	message queue.Message

	// due is the earliest delivery time.
	due time.Time
}

var _ queue.DispatcherDriver = (*Fake)(nil)
var _ queue.ConsumerDriver = (*Fake)(nil)

// New returns an empty Fake.
func New() *Fake {
	return &Fake{
		changed: make(chan struct{}),
		pending: map[string][]pendingMessage{},
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

// FailWith makes every following Dispatch return err until FailWith(nil).
func (fake *Fake) FailWith(err error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.failErr = err
}

// FailNext makes the next Dispatch return err. Later calls succeed again.
func (fake *Fake) FailNext(err error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.failNext = err
}

// Reset forgets all recorded and pending messages and clears injected
// failures.
func (fake *Fake) Reset() {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.dispatched = nil
	fake.rejected = nil
	fake.failed = nil
	fake.failErr = nil
	fake.failNext = nil
	fake.pending = map[string][]pendingMessage{}
	fake.signalLocked()
}

// Dispatch records message and keeps it pending for delivery. A missing ID is
// generated. It returns an injected failure, if any, without recording.
func (fake *Fake) Dispatch(ctx context.Context, message queue.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if message.Queue == "" {
		return ErrInvalidQueue
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	if err := fake.takeFailureLocked(); err != nil {
		return err
	}

	fake.sequence++

	if message.ID == "" {
		message.ID = fmt.Sprintf("fake-%d", fake.sequence)
	}

	message.Attempts = 0
	message.Payload = slices.Clone(message.Payload)
	message.Metadata = maps.Clone(message.Metadata)
	fake.dispatched = append(fake.dispatched, message)
	fake.enqueueLocked(message, time.Now())

	return nil
}

// takeFailureLocked returns the injected dispatch failure, consuming a
// FailNext failure.
func (fake *Fake) takeFailureLocked() error {
	if err := fake.failNext; err != nil {
		fake.failNext = nil

		return err
	}

	return fake.failErr
}

// enqueueLocked makes message pending from due on and wakes consumers.
func (fake *Fake) enqueueLocked(message queue.Message, due time.Time) {
	fake.pending[message.Queue] = append(fake.pending[message.Queue], pendingMessage{
		message: message,
		due:     due,
	})
	fake.signalLocked()
}

// signalLocked wakes every waiting consumer.
func (fake *Fake) signalLocked() {
	close(fake.changed)

	fake.changed = make(chan struct{})
}

// Dispatched returns a copy of every message accepted since the last Reset,
// in dispatch order, including messages already delivered.
func (fake *Fake) Dispatched() []queue.Message {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	return cloneMessages(fake.dispatched)
}

// Pending returns a copy of the messages not yet delivered or acknowledged,
// including delayed retries, across all queues.
func (fake *Fake) Pending() []queue.Message {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	var messages []queue.Message

	for _, entries := range fake.pending {
		for _, entry := range entries {
			messages = append(messages, entry.message)
		}
	}

	return cloneMessages(messages)
}

// Rejected returns a copy of the messages settled with [queue.Delivery.Reject].
func (fake *Fake) Rejected() []queue.Message {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	return cloneMessages(fake.rejected)
}

// Failed returns a copy of the messages settled with [queue.Delivery.Fail] or
// abandoned by a handler that returned without settling them.
func (fake *Fake) Failed() []queue.Message {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	return cloneMessages(fake.failed)
}

// cloneMessages deep-copies messages so callers cannot alter recorded state.
func cloneMessages(messages []queue.Message) []queue.Message {
	cloned := slices.Clone(messages)

	for index := range cloned {
		cloned[index].Payload = slices.Clone(cloned[index].Payload)
		cloned[index].Metadata = maps.Clone(cloned[index].Metadata)
	}

	return cloned
}

// Payloads decodes the payloads of the dispatched messages for job, in
// dispatch order. It returns an error when a payload does not decode into T.
func (fake *Fake) Payloads[T any](job queue.Job[T]) ([]T, error) {
	var payloads []T

	for _, message := range fake.Dispatched() {
		if message.Name != job.Name() || message.Queue != job.Queue() {
			continue
		}

		var payload T

		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", job.Name(), err)
		}

		payloads = append(payloads, payload)
	}

	return payloads, nil
}

// AssertDispatched fails t unless a message for job was dispatched whose
// decoded payload satisfies every predicate. Without predicates any payload
// matches.
func (fake *Fake) AssertDispatched[T any](t testing.TB, job queue.Job[T], preds ...func(T) bool) {
	t.Helper()

	if !fake.anyDispatched(t, job, preds) {
		t.Errorf("no %s job matching the predicate was dispatched; dispatched: %s", job.Name(), fake.describeDispatched())
	}
}

// AssertNotDispatched fails t if a message for job was dispatched whose
// decoded payload satisfies every predicate. Without predicates any payload
// matches.
func (fake *Fake) AssertNotDispatched[T any](t testing.TB, job queue.Job[T], preds ...func(T) bool) {
	t.Helper()

	if fake.anyDispatched(t, job, preds) {
		t.Errorf("a %s job matching the predicate was dispatched; dispatched: %s", job.Name(), fake.describeDispatched())
	}
}

// anyDispatched reports whether a dispatched payload for job satisfies every
// predicate, failing t when a payload cannot be decoded.
func (fake *Fake) anyDispatched[T any](t testing.TB, job queue.Job[T], preds []func(T) bool) bool {
	t.Helper()

	payloads, err := fake.Payloads(job)

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

// AssertDispatchedCount fails t unless exactly n messages were dispatched for
// job.
func (fake *Fake) AssertDispatchedCount[T any](t testing.TB, job queue.Job[T], n int) {
	t.Helper()

	payloads, err := fake.Payloads(job)

	if err != nil {
		t.Errorf("%v", err)

		return
	}

	if len(payloads) != n {
		t.Errorf("%d %s jobs dispatched, want %d; dispatched: %s", len(payloads), job.Name(), n, fake.describeDispatched())
	}
}

// AssertNothingDispatched fails t if any message was dispatched.
func (fake *Fake) AssertNothingDispatched(t testing.TB) {
	t.Helper()

	if len(fake.Dispatched()) > 0 {
		t.Errorf("jobs were dispatched, want none; dispatched: %s", fake.describeDispatched())
	}
}

// describeDispatched summarizes the dispatched messages for failure output.
func (fake *Fake) describeDispatched() string {
	messages := fake.Dispatched()

	if len(messages) == 0 {
		return "none"
	}

	descriptions := make([]string, len(messages))

	for index, message := range messages {
		descriptions[index] = fmt.Sprintf("%s@%s %s", message.Name, message.Queue, message.Payload)
	}

	return fmt.Sprint(descriptions)
}
