package memorycache

import (
	"context"
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache/internal/storage"
)

// Driver implements [cache.Driver] in process memory.
//
// Driver is suitable for single-process applications, local development, and
// tests where persistence across restarts is not required. It is safe for
// concurrent use. Expiry is checked on access, so correctness never depends
// on sweeping. Writes sweep expired entries inline at most once per
// [Builder.Cleanup], so memory stays bounded without calling [Driver.Start].
//
// Example:
//
//	driver := memorycache.New()
//	if err := driver.Put(ctx, "version", []byte("v1"), time.Minute); err != nil {
//		return err
//	}
type Driver struct {
	// store owns atomic data and lifecycle locks.
	store *storage.Store
}

var _ cache.Driver = (*Driver)(nil)

// Get returns a copy of the bytes stored for key. It returns
// [cache.ErrNotFound] when the key does not exist or has expired.
//
// Example:
//
//	raw, err := driver.Get(ctx, "version")
//	if err != nil {
//		return err
//	}
//	_ = raw // Independent bytes; mutation does not alter the stored value.
func (driver *Driver) Get(ctx context.Context, key string) ([]byte, error) {
	return driver.store.Get(ctx, key)
}

// Put stores a copy of value for key. A zero ttl stores the entry without
// expiration.
//
// Example:
//
//	if err := driver.Put(ctx, "version", []byte("v1"), time.Minute); err != nil {
//		return err
//	}
func (driver *Driver) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return driver.store.Put(ctx, key, value, ttl)
}

// Delete removes key. Deleting a missing key is a no-op.
//
// Example:
//
//	if err := driver.Delete(ctx, "version"); err != nil {
//		return err
//	}
func (driver *Driver) Delete(ctx context.Context, key string) error {
	return driver.store.Delete(ctx, key)
}

// Has reports whether key exists and has not expired.
//
// Example:
//
//	found, err := driver.Has(ctx, "version")
//	if err != nil {
//		return err
//	}
//	_ = found // Observation only; use an atomic operation to guard a write.
func (driver *Driver) Has(ctx context.Context, key string) (bool, error) {
	return driver.store.Has(ctx, key)
}

// Add stores a copy of value only when key is absent or expired.
//
// Example:
//
//	added, err := driver.Add(ctx, "receipt", []byte("claimed"), time.Hour)
//	if err != nil {
//		return err
//	}
//	_ = added
func (driver *Driver) Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	return driver.store.Add(ctx, key, value, ttl)
}

// Pull returns the bytes stored for key and removes the entry under the same
// lock, so concurrent callers cannot both receive the value. It returns
// [cache.ErrNotFound] when the key does not exist or has expired.
//
// Example:
//
//	raw, err := driver.Pull(ctx, "one-time-token")
//	if err != nil {
//		return err
//	}
//	_ = raw // The entry was removed atomically with this read.
func (driver *Driver) Pull(ctx context.Context, key string) ([]byte, error) {
	return driver.store.Pull(ctx, key)
}

// TTL returns the remaining lifetime for key, or zero when it never expires.
// It returns [cache.ErrNotFound] when the key does not exist or has expired.
//
// Example:
//
//	remaining, err := driver.TTL(ctx, "version")
//	if err != nil {
//		return err
//	}
//	_ = remaining
func (driver *Driver) TTL(ctx context.Context, key string) (time.Duration, error) {
	return driver.store.TTL(ctx, key)
}

// TryLock stores token under name for ttl when name is absent or
// expired, and reports whether it did. The entry is an ordinary one, so an
// expired lock is replaced like any expired key. It returns an error wrapping
// [cache.ErrInvalidTTL] when ttl is not positive.
//
// Example:
//
//	token := rand.Text() // crypto/rand: unique to this attempt.
//	held, err := driver.TryLock(ctx, "lock:report", token, time.Minute)
//	if err != nil {
//		return err
//	}
//	if held {
//		_, err = driver.Unlock(ctx, "lock:report", token)
//	}
//	return err
func (driver *Driver) TryLock(ctx context.Context, name, token string, ttl time.Duration) (bool, error) {
	return driver.store.TryLock(ctx, name, token, ttl)
}

// Unlock removes name only while it holds token and reports whether it
// did, comparing and deleting under one lock acquisition.
//
// Example:
//
//	released, err := driver.Unlock(ctx, "lock:report", token)
//	if err != nil {
//		return err
//	}
//	_ = released
func (driver *Driver) Unlock(ctx context.Context, name, token string) (bool, error) {
	return driver.store.Unlock(ctx, name, token)
}

// ExtendLock resets the expiry of name to ttl from now only while it holds
// token and reports whether it did. It returns an error wrapping
// [cache.ErrInvalidTTL] when ttl is not positive.
//
// Example:
//
//	extended, err := driver.ExtendLock(ctx, "lock:report", token, time.Minute)
//	if err != nil {
//		return err
//	}
//	_ = extended
func (driver *Driver) ExtendLock(ctx context.Context, name, token string, ttl time.Duration) (bool, error) {
	return driver.store.ExtendLock(ctx, name, token, ttl)
}
