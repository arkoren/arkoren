// Package delivery serializes native heartbeats with settlement admission.
package delivery

import (
	"context"
	"sync"
	"time"
)

// State owns one delivery's heartbeat and settlement transitions. Its zero value
// is ready; callbacks retain native message and error identities in the adapter.
type State struct {
	// mu serializes heartbeat publication and settlement admission.
	mu sync.Mutex
	// settling excludes renewal during an admitted settlement.
	settling bool
	// settled permanently excludes renewal after successful settlement.
	settled bool
}

// KeepAlive renews at interval and stops on the first unexpected failure. Stop
// is idempotent and joins the worker even when the enclosing handler panics.
func (state *State) KeepAlive(interval time.Duration, progress func() error, failed func(error)) func() {
	done := make(chan struct{})
	var group sync.WaitGroup
	group.Go(func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}

			if err := state.Progress(progress); err != nil {
				failed(err)

				return
			}
		}
	})
	var once sync.Once

	return func() {
		once.Do(func() { close(done) })
		group.Wait()
	}
}

// Progress holds admission until the native heartbeat is queued, so any later
// NAK follows it on the same connection and cannot have its delay shortened.
func (state *State) Progress(progress func() error) error {
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.settling || state.settled {
		return nil
	}

	return progress()
}

// Settle excludes heartbeat publication before performing native settlement.
// Failure resumes renewal; successful settlement permanently closes admission.
func (state *State) Settle(ctx context.Context, repeated error, operation func() error) error {
	state.mu.Lock()

	if state.settled || state.settling {
		state.mu.Unlock()

		return repeated
	}

	if err := ctx.Err(); err != nil {
		state.mu.Unlock()

		return err
	}

	state.settling = true
	state.mu.Unlock()

	if err := operation(); err != nil {
		state.mu.Lock()
		state.settling = false
		state.mu.Unlock()

		return err
	}

	state.mu.Lock()
	state.settling = false
	state.settled = true
	state.mu.Unlock()

	return nil
}
