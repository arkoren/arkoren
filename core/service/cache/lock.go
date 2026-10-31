package cache

import (
	"context"
	"fmt"
	"time"

	"arkoren.dev/core/service/cache/internal/lease"
)

// lockKeyPrefix namespaces lock names away from entries, so a [Cache.Put] on
// a key equal to a lock name cannot overwrite or release the lock.
const lockKeyPrefix = "lock:"

// Lock waits until the named lock is held or ctx ends, then returns the
// lease. The lock is exclusive, not reentrant, and expires ttl after it was
// taken unless [Lease.Extend] renews it. An empty name fails with
// [ErrEmptyLockName] and a ttl that is not positive with [ErrInvalidTTL];
// when ctx ends first, Lock returns the context's error.
//
// The cache has no blocking primitive, so Lock polls the driver with a
// jittered backoff that grows from 10ms to 250ms. The names live apart from
// cache keys: a lock called "report" never collides with a key "report".
//
// Taking the lock, including the wait, is traced as a lock client span;
// releasing and extending it are not.
// Reserve physical "lock:" keys and logical "remember:" lease names for the
// facade. These prefixes do not create a separate backend namespace: ordinary
// writes to the completed lock key can overwrite a lease. Waiting can make
// several driver attempts; native transport retry policy belongs to the adapter.
//
// Example:
//
//	lease, err := store.Lock(ctx, "nightly-report", time.Minute)
//	if err != nil {
//		return err
//	}
//	defer lease.Unlock(ctx)
func (cache *Cache) Lock(ctx context.Context, name string, ttl time.Duration) (*Lease, error) {
	ctx, op := cache.begin(ctx, operationLock)

	lease, err := cache.acquire(ctx, name, ttl, true)
	op.end(ctx, err, hitUnknown)

	return lease, err
}

// TryLock invokes the driver once to take the named lock. It returns the lease
// and true when the lock is now held, and nil, false and a nil error when
// another holder has it. The name and ttl rules are those of [Cache.Lock].
//
// Use TryLock when only one process should do a piece of work and the others
// should skip it rather than queue behind it. The attempt is traced as a
// try_lock client span, whether or not it succeeded.
//
// Example:
//
//	lease, held, err := store.TryLock(ctx, "nightly-report", time.Minute)
//	if err != nil || !held {
//		return err // Another instance is running the report.
//	}
//	defer lease.Unlock(ctx)
func (cache *Cache) TryLock(ctx context.Context, name string, ttl time.Duration) (*Lease, bool, error) {
	ctx, op := cache.begin(ctx, operationTryLock)

	lease, err := cache.acquire(ctx, name, ttl, false)
	op.end(ctx, err, hitUnknown)

	return lease, lease != nil, err
}

// Lease is a held cache lock. It is returned by [Cache.Lock] and
// [Cache.TryLock] and is safe for concurrent use.
//
// A lease is time-limited: the lock expires ttl after it was taken or last
// extended, whether or not the holder is still working, and another process
// may then take it. Long work must call [Lease.Extend] before the lease runs
// out, and must treat [ErrLockLost] as "someone else may have run
// concurrently". A cache lock is therefore a way to avoid duplicate work, not
// a mutual-exclusion guarantee for correctness; use a database lock (see
// the database service) or fencing in the protected resource for that.
//
// Example:
//
//	lease, held, err := store.TryLock(ctx, "report", time.Minute)
//	if err != nil {
//		return err
//	}
//	if held {
//		return lease.Unlock(ctx)
//	}
type Lease struct {
	// state owns holder identity and serialized release/renewal state.
	state *lease.State
}

// acquire validates the arguments and takes the lock without telemetry. With
// wait false it makes one attempt and returns a nil lease and a nil error
// when the lock is held elsewhere; with wait true it polls until the lock is
// taken or ctx ends.
func (cache *Cache) acquire(ctx context.Context, name string, ttl time.Duration, wait bool) (*Lease, error) {
	if name == "" {
		return nil, ErrEmptyLockName
	}

	if ttl <= 0 {
		return nil, fmt.Errorf("cache: lock ttl %s: %w", ttl, ErrInvalidTTL)
	}

	state, err := lease.Acquire(ctx, cache.driver, lockKeyPrefix+name, ttl, wait)

	if err != nil || state == nil {
		return nil, err
	}

	return &Lease{state: state}, nil
}

// Unlock releases the lock if the lease still holds it. It is safe to call
// more than once and from several goroutines: after a confirmed release or
// confirmed loss, later calls do nothing and return nil. It returns
// [ErrLockLost], once, when the lease had expired and the lock was no longer
// this holder's, in which case another holder's lock is left untouched. A
// cache failure is returned as is and the call can be retried. Releasing is
// not traced.
// A backend error can leave release uncertain; a retry may report ErrLockLost
// because the earlier call released it. Neither that error nor successful
// cleanup proves the protected work never overlapped another holder.
//
// Example:
//
//	if err := lease.Unlock(ctx); err != nil {
//		if !errors.Is(err, cache.ErrLockLost) {
//			return err // A backend failure remains retryable with a fresh context.
//		}
//	}
func (lease *Lease) Unlock(ctx context.Context) error {
	return lease.state.Unlock(ctx, ErrLockLost)
}

// Extend sets the lock to expire ttl from now, so long work can keep the
// lease alive. It returns [ErrLockLost] when the lease no longer holds the
// lock, because it expired first or was already released, and [ErrInvalidTTL]
// for a ttl that is not positive. Extending is not traced.
//
// Example:
//
//	for _, batch := range batches {
//		if err := lease.Extend(ctx, time.Minute); err != nil {
//			return err // Stop: another process may be working too.
//		}
//		process(batch)
//	}
func (lease *Lease) Extend(ctx context.Context, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("cache: lock ttl %s: %w", ttl, ErrInvalidTTL)
	}

	return lease.state.Extend(ctx, ttl, ErrLockLost)
}
