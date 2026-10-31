package broker

import (
	"container/heap"
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"arkoren.dev/core/service/queue"
)

// Dispatch adds message to its named queue. Payload is copied before
// acceptance and a missing ID is generated.
// Metadata is copied too; caller mutations after acceptance do not change the
// delivery. Input Attempts is ignored. An empty queue, canceled context, or
// closed driver returns an error without adding a message.
//
//	if err := driver.Dispatch(ctx, queue.Message{
//		Name: "send", Queue: "work", Payload: []byte(`"hello"`),
//	}); err != nil {
//		return err
//	}
func (driver *Broker) Dispatch(ctx context.Context, message queue.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if message.Queue == "" {
		return ErrInvalidQueue
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.closed {
		return ErrDriverClosed
	}

	driver.sequence++

	if message.ID == "" {
		message.ID = fmt.Sprintf("memory-%d", driver.sequence)
	}

	// Attempt counts are supplied at delivery time, not dispatch time.
	message.Attempts = 0
	message.Payload = slices.Clone(message.Payload)
	message.Metadata = maps.Clone(message.Metadata)
	driver.enqueueLocked(message)

	return nil
}

// reserve checks cancellation and shutdown before promoting due retries and
// removing the next ready message of name. Admission shares the lock with Stop,
// so shutdown cannot discard a message between its admission and removal.
// Cancellation after admission does not revoke the delivery or its settlement.
// Without a message it returns the change channel and earliest delayed due time,
// or zero due when none is delayed. Admission errors leave queued work untouched.
// Removed backing-array slots are cleared so completed payloads and metadata
// are not retained by the remaining ready queue.
func (driver *Broker) reserve(ctx context.Context, name string) (*queue.Message, <-chan struct{}, time.Time, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, nil, time.Time{}, err
	}

	if driver.closed {
		return nil, nil, time.Time{}, ErrDriverClosed
	}

	driver.promoteDueLocked(time.Now())

	messages := driver.ready[name]

	if len(messages) == 0 {
		return nil, driver.changed, driver.nextDueLocked(), nil
	}

	message := messages[0]
	messages[0] = queue.Message{}
	driver.ready[name] = messages[1:]
	message.Attempts++

	return &message, nil, time.Time{}, nil
}

// enqueueLocked appends message to its queue and wakes idle consumers.
func (driver *Broker) enqueueLocked(message queue.Message) {
	driver.ready[message.Queue] = append(driver.ready[message.Queue], message)
	driver.signalLocked()
}

// signalLocked wakes every idle consumer.
func (driver *Broker) signalLocked() {
	close(driver.changed)

	driver.changed = make(chan struct{})
}

// retry requeues message after delay, or after the configured default when
// delay is zero.
func (driver *Broker) retry(ctx context.Context, message queue.Message, delay time.Duration) error {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.closed {
		return ErrDriverClosed
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if delay == 0 {
		delay = driver.RetryDelay
	}

	if delay <= 0 {
		driver.enqueueLocked(message)

		return nil
	}

	driver.sequence++
	heap.Push(&driver.delayed, delayedMessage{
		message:  message,
		due:      time.Now().Add(delay),
		sequence: driver.sequence,
	})
	driver.signalLocked()

	return nil
}

// promoteDueLocked moves delayed messages due at or before now to their queues.
func (driver *Broker) promoteDueLocked(now time.Time) {
	for len(driver.delayed) > 0 && !driver.delayed[0].due.After(now) {
		message := heap.Pop(&driver.delayed).(delayedMessage)
		driver.ready[message.message.Queue] = append(driver.ready[message.message.Queue], message.message)
	}
}

// nextDueLocked returns the due time of the earliest delayed message, or the
// zero time when none is delayed.
func (driver *Broker) nextDueLocked() time.Time {
	if len(driver.delayed) == 0 {
		return time.Time{}
	}

	return driver.delayed[0].due
}

// delayedMessage is a retry that becomes ready at due.
type delayedMessage struct {
	// message is the message to requeue.
	message queue.Message

	// due is when the message becomes ready.
	due time.Time

	// sequence breaks ties between messages due at the same instant.
	sequence uint64
}

// delayedHeap is a min-heap of delayed messages ordered by due time.
type delayedHeap []delayedMessage

// Len returns the number of delayed records available to container/heap, not
// backing-array capacity. A nil heap has length zero; callers hold driver.mu.
func (delayedHeap delayedHeap) Len() int {
	return len(delayedHeap)
}

// Less orders delayed records by due time and then by enqueue sequence, so
// equal deadlines retain submission order. The heap package supplies valid
// indexes while the caller holds driver.mu.
func (delayedHeap delayedHeap) Less(first, second int) bool {
	if delayedHeap[first].due.Equal(delayedHeap[second].due) {
		return delayedHeap[first].sequence < delayedHeap[second].sequence
	}

	return delayedHeap[first].due.Before(delayedHeap[second].due)
}

// Swap exchanges records in the heap's owned backing array without copying
// payloads or metadata. The heap package supplies valid indexes under driver.mu.
func (delayedHeap delayedHeap) Swap(first, second int) {
	delayedHeap[first], delayedHeap[second] = delayedHeap[second], delayedHeap[first]
}

// Push appends the delayedMessage supplied by container/heap. It updates the
// owned slice header under driver.mu; values of another type violate the heap
// adapter's internal contract and panic rather than being silently discarded.
func (delayedHeap *delayedHeap) Push(value any) {
	*delayedHeap = append(*delayedHeap, value.(delayedMessage))
}

// Pop removes the final heap record after container/heap has positioned the
// minimum there. It clears unused backing capacity to release payload and
// metadata references. The heap package supplies a nonempty collection; calling
// Pop directly on an empty heap violates that invariant and panics.
func (delayedHeap *delayedHeap) Pop() any {
	old := *delayedHeap
	last := len(old) - 1
	message := old[last]
	old[last] = delayedMessage{}
	*delayedHeap = old[:last]

	return message
}
