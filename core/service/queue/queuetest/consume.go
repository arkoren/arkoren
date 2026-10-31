package queuetest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"arkoren.dev/core/service/queue"
)

// ErrDrainLimit is returned by [Fake.Drain] when a message is retried more
// times than the limit set by [WithMaxAttempts], which stops handlers that
// retry forever.
var ErrDrainLimit = errors.New("queuetest: drain attempt limit reached")

// ErrForeignConsumer is returned by [Fake.Drain] for a consumer that is not
// backed by the Fake.
var ErrForeignConsumer = errors.New("queuetest: consumer is not backed by this fake")

// DefaultMaxAttempts is the attempt limit of [Fake.Drain] when
// [WithMaxAttempts] is not used.
const DefaultMaxAttempts = 100

// DrainOption changes how [Fake.Drain] runs pending messages.
type DrainOption func(*drainState)

// WithScheduled makes Drain also run messages whose delivery lies in the
// future, such as retries requested with a delay. Time does not advance; the
// delays are skipped.
func WithScheduled() DrainOption {
	return func(state *drainState) {
		state.scheduled = true
	}
}

// WithMaxAttempts sets how often one message may be delivered during a Drain
// before it fails with [ErrDrainLimit]. Values below one are ignored.
func WithMaxAttempts(attempts int) DrainOption {
	return func(state *drainState) {
		if attempts > 0 {
			state.maxAttempts = attempts
		}
	}
}

// drainState configures one Drain and marks its context.
type drainState struct {
	// scheduled includes messages that are not yet due.
	scheduled bool

	// maxAttempts is the delivery limit of one message.
	maxAttempts int

	// delivered counts the messages delivered so far, so Drain can tell
	// whether another pass over the queues made progress.
	delivered atomic.Int64
}

// drainKey is the context key carrying a [drainState] to [Fake.Consume].
type drainKey struct{}

// Drain synchronously runs the pending messages of every queue that consumer
// handles until none is left to run. consumer must be backed by the Fake.
// Messages of one queue run in arrival order through the registered handlers,
// so retries, rejections, failures, and decoding errors are settled exactly as
// in production. Queues run concurrently, and Drain repeats the pass while a
// handler's dispatch to another queue keeps producing work.
//
// By default only messages due now run, including immediate retries; messages
// delayed by [queue.RetryAfter] or a backoff stay pending until
// [WithScheduled] is used. A message delivered more than [DefaultMaxAttempts]
// times fails the Drain with [ErrDrainLimit].
//
// Drain returns the first settlement failure, [queue.ErrNoQueues] when
// consumer handles no job, or the error of ctx.
//
// Example:
//
//	_ = fake.Drain(ctx, consumer)
func (fake *Fake) Drain(ctx context.Context, consumer *queue.Consumer, opts ...DrainOption) error {
	if backing, ok := consumer.DriverAs[*Fake](); !ok || backing != fake {
		return ErrForeignConsumer
	}

	state := &drainState{
		maxAttempts: DefaultMaxAttempts,
	}

	for _, opt := range opts {
		opt(state)
	}

	ctx = context.WithValue(ctx, drainKey{}, state)

	for {
		before := state.delivered.Load()

		if err := consumer.Run(ctx); err != nil {
			return err
		}

		if state.delivered.Load() == before {
			return nil
		}
	}
}

// Consume delivers pending messages of name to handler until ctx is canceled
// or handler fails. During [Fake.Drain] it instead returns nil once no
// message is left to run. Delayed retries are delivered when they fall due.
// A handler that returns an error or leaves a delivery unsettled fails that
// delivery and ends Consume.
func (fake *Fake) Consume(ctx context.Context, name string, handler queue.DeliveryHandler) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if name == "" {
		return ErrInvalidQueue
	}

	if handler == nil {
		return ErrInvalidHandler
	}

	state, _ := ctx.Value(drainKey{}).(*drainState)

	for {
		drained, err := fake.consumeNext(ctx, name, handler, state)

		if err != nil || drained {
			return err
		}
	}
}

// consumeNext delivers one message or waits for one. It reports drained when
// a Drain has no message left to run.
func (fake *Fake) consumeNext(ctx context.Context, name string, handler queue.DeliveryHandler, state *drainState) (drained bool, err error) {
	message, wake, due := fake.reserve(name, state != nil && state.scheduled)

	if message == nil && state != nil {
		return true, nil
	}

	if message == nil {
		return false, wait(ctx, wake, due)
	}

	if state != nil {
		state.delivered.Add(1)
	}

	return false, fake.deliver(ctx, handler, &delivery{
		fake:    fake,
		message: *message,
		state:   state,
	})
}

// reserve removes the next message of name that is due, or any message when
// scheduled is true. Without one it returns the channel signaling a change
// and the earliest due time, zero when nothing is pending.
func (fake *Fake) reserve(name string, scheduled bool) (*queue.Message, <-chan struct{}, time.Time) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	now := time.Now()
	entries := fake.pending[name]
	index := slices.IndexFunc(entries, func(entry pendingMessage) bool {
		return scheduled || !entry.due.After(now)
	})

	if index < 0 {
		return nil, fake.changed, earliestDue(entries)
	}

	message := entries[index].message
	fake.pending[name] = slices.Delete(slices.Clone(entries), index, index+1)
	message.Attempts++

	return &message, nil, time.Time{}
}

// earliestDue returns the smallest due time of entries, zero for none.
func earliestDue(entries []pendingMessage) time.Time {
	var earliest time.Time

	for _, entry := range entries {
		if earliest.IsZero() || entry.due.Before(earliest) {
			earliest = entry.due
		}
	}

	return earliest
}

// wait blocks until ctx ends, messages change, or due passes. A zero due
// waits without a timer.
func wait(ctx context.Context, wake <-chan struct{}, due time.Time) error {
	var timeout <-chan time.Time

	if !due.IsZero() {
		timer := time.NewTimer(time.Until(due))
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wake:
	case <-timeout:
	}

	return nil
}

// deliver runs handler and fails the delivery when the handler errors,
// panics, or leaves it unsettled. The returned error ends Consume.
func (fake *Fake) deliver(ctx context.Context, handler queue.DeliveryHandler, delivery *delivery) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("queuetest: delivery handler panicked: %v", recovered)
		}

		if err != nil {
			_ = delivery.Fail(context.WithoutCancel(ctx), err)

			return
		}

		if !delivery.settled() {
			err = ErrUnsettled
			_ = delivery.Fail(context.WithoutCancel(ctx), err)
		}
	}()

	return handler(ctx, delivery)
}

// delivery is one reserved message, settled at most once.
type delivery struct {
	// fake receives the settlement.
	fake *Fake

	// message is the reserved message.
	message queue.Message

	// state is the Drain this delivery belongs to, nil outside Drain.
	state *drainState

	// mu guards done.
	mu sync.Mutex

	// done reports a completed settlement.
	done bool
}

// Message returns a copy of the delivered message.
func (delivery *delivery) Message() queue.Message {
	message := delivery.message
	message.Payload = slices.Clone(message.Payload)
	message.Metadata = maps.Clone(message.Metadata)

	return message
}

// Acknowledge settles the delivery as done.
func (delivery *delivery) Acknowledge(ctx context.Context) error {
	return delivery.settle(ctx, func() error {
		return nil
	})
}

// Retry makes the message pending again after delay. A zero delay retries
// immediately.
func (delivery *delivery) Retry(ctx context.Context, delay time.Duration) error {
	return delivery.settle(ctx, func() error {
		if delivery.state != nil && delivery.message.Attempts >= delivery.state.maxAttempts {
			return fmt.Errorf("%w: %s after %d attempts", ErrDrainLimit, delivery.message.Name, delivery.message.Attempts)
		}

		delivery.fake.requeue(delivery.message, max(delay, 0))

		return nil
	})
}

// Reject records the message as rejected and drops it.
func (delivery *delivery) Reject(ctx context.Context) error {
	return delivery.settle(ctx, func() error {
		delivery.fake.record(&delivery.fake.rejected, delivery.message)

		return nil
	})
}

// Fail records the message as failed and drops it.
func (delivery *delivery) Fail(ctx context.Context, _ error) error {
	return delivery.settle(ctx, func() error {
		delivery.fake.record(&delivery.fake.failed, delivery.message)

		return nil
	})
}

// settle runs operation as the one settlement of the delivery. A failed
// operation leaves the delivery open.
func (delivery *delivery) settle(ctx context.Context, operation func() error) error {
	delivery.mu.Lock()
	defer delivery.mu.Unlock()

	if delivery.done {
		return ErrDeliverySettled
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := operation(); err != nil {
		return err
	}

	delivery.done = true

	return nil
}

// settled reports whether the delivery has been settled.
func (delivery *delivery) settled() bool {
	delivery.mu.Lock()
	defer delivery.mu.Unlock()

	return delivery.done
}

// requeue makes message pending again after delay.
func (fake *Fake) requeue(message queue.Message, delay time.Duration) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.enqueueLocked(message, time.Now().Add(delay))
}

// record appends message to list.
func (fake *Fake) record(list *[]queue.Message, message queue.Message) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	*list = append(*list, message)
}
