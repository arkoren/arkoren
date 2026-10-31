// Package delivery owns settlement admission and confirmed transfer ordering.
package delivery

import (
	"context"
	"sync"
)

// State reserves one delivery during settlement. The zero value is ready.
type State struct {
	// mutex protects local transitions, not native broker operations.
	mutex sync.Mutex
	// settling excludes concurrent settlement while publication is pending.
	settling bool
	// settled permanently excludes settlement after success.
	settled bool
}

// Settle performs one admitted operation. Failure reopens admission; success
// permanently settles. Existing settlement takes precedence over cancellation.
func (state *State) Settle(ctx context.Context, repeated error, operation func() error) error {
	state.mutex.Lock()

	if state.settled || state.settling {
		state.mutex.Unlock()

		return repeated
	}

	if err := ctx.Err(); err != nil {
		state.mutex.Unlock()

		return err
	}

	state.settling = true
	state.mutex.Unlock()

	if err := operation(); err != nil {
		state.mutex.Lock()
		state.settling = false
		state.mutex.Unlock()

		return err
	}

	state.mutex.Lock()
	state.settling = false
	state.settled = true
	state.mutex.Unlock()

	return nil
}

// Transfer acknowledges the source only after the publisher has confirmed both
// routing and acceptance. A failed or uncertain publication leaves it unsettled.
func Transfer(publish func() error, acknowledge func() error) error {
	if err := publish(); err != nil {
		return err
	}

	return acknowledge()
}
