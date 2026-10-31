// Package lease owns cache-lock polling and serialized holder state.
package lease

import (
	"context"
	cryptorand "crypto/rand"
	"math/rand/v2"
	"sync"
	"time"
)

// Driver supplies atomic ownership operations without depending on the facade.
type Driver interface {
	// TryLock takes an absent or expired lock for one holder.
	TryLock(context.Context, string, string, time.Duration) (bool, error)
	// Unlock removes the lock only for its current holder.
	Unlock(context.Context, string, string) (bool, error)
	// ExtendLock refreshes the lock only for its current holder.
	ExtendLock(context.Context, string, string, time.Duration) (bool, error)
}

// State retains one holder's identity and serializes release and renewal.
type State struct {
	// driver performs atomic operations in the backing cache.
	driver Driver
	// key is the completed physical lock key.
	key string
	// token identifies this holder independently of its key.
	token string
	// mu guards released and serializes ownership operations.
	mu sync.Mutex
	// released records confirmed release or confirmed loss.
	released bool
}

const (
	// minBackoff is the initial polling interval before jitter.
	minBackoff = 10 * time.Millisecond
	// maxBackoff caps the polling interval before jitter.
	maxBackoff = 250 * time.Millisecond
)

// Acquire takes a validated lock, polling only when wait is true.
func Acquire(ctx context.Context, driver Driver, key string, ttl time.Duration, wait bool) (*State, error) {
	state := &State{driver: driver, key: key, token: cryptorand.Text()}
	backoff := minBackoff

	for {
		held, err := driver.TryLock(ctx, state.key, state.token, ttl)

		if err != nil {
			return nil, err
		}

		if held {
			return state, nil
		}

		if !wait {
			return nil, nil
		}

		timer := time.NewTimer(backoff - rand.N(backoff/2))

		select {
		case <-ctx.Done():
			timer.Stop()

			return nil, ctx.Err()
		case <-timer.C:
		}

		backoff = min(backoff*2, maxBackoff)
	}
}

// Unlock publishes confirmed loss only once; backend failures remain retryable.
func (state *State) Unlock(ctx context.Context, lost error) error {
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.released {
		return nil
	}

	released, err := state.driver.Unlock(ctx, state.key, state.token)

	if err != nil {
		return err
	}

	state.released = true

	if !released {
		return lost
	}

	return nil
}

// Extend renews a validated duration without changing confirmed release state.
func (state *State) Extend(ctx context.Context, ttl time.Duration, lost error) error {
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.released {
		return lost
	}

	extended, err := state.driver.ExtendLock(ctx, state.key, state.token, ttl)

	if err != nil {
		return err
	}

	if !extended {
		return lost
	}

	return nil
}
