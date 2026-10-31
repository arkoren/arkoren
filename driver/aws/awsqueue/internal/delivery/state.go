// Package delivery owns receipt renewal, settlement admission, and immutable
// message snapshots. Native requests and failure wire encoding stay in awsqueue.
package delivery

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"arkoren.dev/core/service/queue"
)

// State owns one receipt's message and serialization slot. Publish completes the
// message before exposing it to a handler; renewal never reads the message.
type State struct {
	// message owns payload and metadata independently of supplied envelopes.
	message queue.Message
	// guard serializes renewal RPCs with settlement admission.
	guard chan struct{}
	// settling excludes renewal while an admitted settlement is in flight.
	settling bool
	// settled permanently excludes renewal after successful settlement.
	settled bool
	// repeated preserves the owning driver's settlement error identity.
	repeated error
}

// New captures an envelope and allocates a receipt-local serialization slot.
func New(message queue.Message, repeated error) *State {
	state := &State{guard: make(chan struct{}, 1), repeated: repeated}
	state.Publish(message)

	return state
}

// Publish captures the decoded envelope before its first handler invocation.
// The caller must not replace a message after publishing the delivery to a
// handler. Nil containers remain nil; arbitrary metadata values are strings.
func (state *State) Publish(message queue.Message) {
	message.Payload = slices.Clone(message.Payload)
	message.Metadata = maps.Clone(message.Metadata)
	state.message = message
}

// Message returns detached payload and metadata snapshots for concurrent reads.
func (state *State) Message() queue.Message {
	message := state.message
	message.Payload = slices.Clone(message.Payload)
	message.Metadata = maps.Clone(message.Metadata)

	return message
}

// KeepAlive owns renewal through handler drain. Its idempotent stop cancels
// pending I/O before joining the worker. The native callback must honor context
// cancellation; no native operation is abandoned on a detached goroutine.
func (state *State) KeepAlive(
	ctx context.Context,
	visibility, maximum time.Duration,
	extend func(context.Context) error,
	failed func(context.Context, error),
) func() {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	var group sync.WaitGroup
	group.Go(func() {
		ticker := time.NewTicker(visibility / 2)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				state.extend(ctx, min(visibility/2, maximum), extend, failed)
			}
		}
	})

	return func() {
		cancel()
		group.Wait()
	}
}

// extend bounds native retries and serialization wait with one renewal budget.
// Logging remains inside the serialization slot, as in the owning driver.
func (state *State) extend(
	ctx context.Context,
	budget time.Duration,
	extend func(context.Context) error,
	failed func(context.Context, error),
) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	if err := state.lock(ctx); err != nil {
		return
	}

	defer func() { <-state.guard }()

	if state.settling || state.settled {
		return
	}

	if err := extend(ctx); err != nil {
		failed(ctx, err)
	}
}

// Settle admits one operation after pending renewal, preserving the caller's
// wait budget. Failure resumes renewal and allows another settlement; success
// permanently excludes renewal and repeated settlement.
func (state *State) Settle(ctx context.Context, operation func() error) error {
	if err := state.startSettlement(ctx); err != nil {
		return err
	}

	if err := operation(); err != nil {
		state.guard <- struct{}{}
		state.settling = false
		<-state.guard

		return err
	}

	state.guard <- struct{}{}
	state.settling = false
	state.settled = true
	<-state.guard

	return nil
}

// lock acquires the slot or returns cancellation. Admission checks ctx.Err
// again because a canceled context can race a ready slot.
func (state *State) lock(ctx context.Context) error {
	select {
	case state.guard <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startSettlement excludes subsequent renewal before releasing the slot.
func (state *State) startSettlement(ctx context.Context) error {
	if err := state.lock(ctx); err != nil {
		return err
	}

	defer func() { <-state.guard }()

	if state.settled || state.settling {
		return state.repeated
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	state.settling = true

	return nil
}
