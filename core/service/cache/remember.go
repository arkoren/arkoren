package cache

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"
)

// Remember retrieves key or computes and stores its value when it is absent.
// A stored value that no longer decodes as T, for example after T changed,
// is recomputed and overwritten. When storing the computed value fails, the
// value is still returned together with the storage error. It is traced as a
// single span whose arkoren.cache.hit tells whether compute ran.
//
// Concurrent misses on one key do not stampede: the first takes a lock named
// after the key, the others wait for it (until ctx ends) and then read the
// value it stored, so compute runs once per miss. The lock is a lease of ten
// seconds; a compute that runs longer than that may overlap with another
// caller's. Hits never take the lock.
// Best-effort release uses an independent ten-second cleanup deadline, retaining
// context values but ignoring request cancellation. Cleanup errors are ignored
// because the lease expires; a backend must honor context cancellation for the
// deadline to bound I/O. This does not bound compute or renew the lease.
// The completed key name coordinates misses regardless of its declaration
// path. Writes use key.TTL; zero means no expiry. Validation precedes compute.
//
//	value, err := store.Remember(ctx, cache.NewKey[string]("version"), func() (string, error) {
//		return "v1", nil
//	})
//	if err != nil {
//		return err
//	}
//	_ = value
func (cache *Cache) Remember[T any](
	ctx context.Context,
	key Key[T],
	compute func() (T, error),
) (res T, err error) {
	ctx, op := cache.begin(ctx, operationRemember)
	hit := hitUnknown
	defer func() { op.end(ctx, err, hit) }()

	if err = validateKey(ctx, key); err != nil {
		return res, err
	}

	res, hit, err = cache.remember(ctx, key.Name(), key.TTL(), compute)

	return res, err
}

// rememberLockTTL is the lease [Cache.Remember] holds while computing.
const rememberLockTTL = 10 * time.Second

// rememberLockPrefix is the reserved internal naming convention used by
// [Cache.Remember]. Callers must avoid these names when using [Cache.Lock]
// or [Cache.TryLock]; the prefix does not prevent deliberate collisions.
const rememberLockPrefix = "remember:"

// lookup reads and decodes key for [Cache.Remember]. A missing key and a
// stored value that no longer decodes as T are both reported as not found;
// only driver failures are errors.
func (cache *Cache) lookup[T any](ctx context.Context, key string) (res T, found bool, err error) {
	raw, err := cache.driver.Get(ctx, key)

	if errors.Is(err, ErrNotFound) {
		return res, false, nil
	}

	if err != nil {
		return res, false, err
	}

	if json.Unmarshal(raw, &res) != nil {
		var zero T

		return zero, false, nil
	}

	return res, true, nil
}

// remember is Remember without telemetry. It reports whether the value came
// from the cache.
func (cache *Cache) remember[T any](ctx context.Context, key string, ttl time.Duration, compute func() (T, error)) (res T, hit hitState, err error) {
	cached, found, err := cache.lookup[T](ctx, key)

	if err != nil {
		return res, hitUnknown, err
	}

	if found {
		return cached, hitYes, nil
	}

	lease, err := cache.acquire(ctx, rememberLockPrefix+key, rememberLockTTL, true)

	if err != nil {
		return res, hitUnknown, err
	}

	defer func() {
		// A lost lease only means compute outlived it; the value is stored
		// either way. Cleanup survives request cancellation for at most one
		// lease interval; expiry is the fallback when release fails.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), rememberLockTTL)
		defer cancel()
		_ = lease.Unlock(cleanup)
	}()

	// Another caller may have stored the value while this one waited.
	cached, found, err = cache.lookup[T](ctx, key)

	if err != nil {
		return res, hitUnknown, err
	}

	if found {
		return cached, hitYes, nil
	}

	value, err := compute()

	if err != nil {
		return res, hitNo, err
	}

	return value, hitNo, cache.store(ctx, key, value, ttl)
}
