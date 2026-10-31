package storage

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"arkoren.dev/core/service/cache"
)

// entry is one stored value with its absolute expiry.
type entry struct {
	// value is an owned copy of the stored bytes.
	value []byte

	// expiresAt is when the entry expires. The zero time never expires.
	expiresAt time.Time
}

// expired reports whether the entry is no longer valid at now.
func (entry entry) expired(now time.Time) bool {
	return !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt)
}

// remaining returns the lifetime left at now, or zero for entries that never
// expire.
func (entry entry) remaining(now time.Time) time.Duration {
	if entry.expiresAt.IsZero() {
		return 0
	}

	return entry.expiresAt.Sub(now)
}

// live returns the unexpired entry for key, removing it when expired. The
// mutex must be held.
func (driver *Store) live(key string, now time.Time) (entry, bool) {
	stored, found := driver.Entries[key]

	if !found {
		return entry{}, false
	}

	if stored.expired(now) {
		delete(driver.Entries, key)

		return entry{}, false
	}

	return stored, true
}

// newEntry builds an entry holding a copy of value that expires after ttl.
func newEntry(value []byte, ttl time.Duration, now time.Time) entry {
	stored := entry{value: bytes.Clone(value)}

	if ttl > 0 {
		stored.expiresAt = now.Add(ttl)
	}

	return stored
}

// validate returns the context error or [cache.ErrInvalidTTL] for a negative
// ttl.
func validate(ctx context.Context, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if ttl < 0 {
		return fmt.Errorf("memory: ttl %s: %w", ttl, cache.ErrInvalidTTL)
	}

	return nil
}

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
func (driver *Store) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	stored, found := driver.live(key, time.Now())

	if !found {
		return nil, cache.ErrNotFound
	}

	return bytes.Clone(stored.value), nil
}

// Put stores a copy of value for key. A zero ttl stores the entry without
// expiration.
//
// Example:
//
//	if err := driver.Put(ctx, "version", []byte("v1"), time.Minute); err != nil {
//		return err
//	}
func (driver *Store) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := validate(ctx, ttl); err != nil {
		return err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	now := time.Now()
	driver.sweepDue(now)

	driver.Entries[key] = newEntry(value, ttl, now)

	return nil
}

// Delete removes key. Deleting a missing key is a no-op.
//
// Example:
//
//	if err := driver.Delete(ctx, "version"); err != nil {
//		return err
//	}
func (driver *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	delete(driver.Entries, key)

	return nil
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
func (driver *Store) Has(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	_, found := driver.live(key, time.Now())

	return found, nil
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
func (driver *Store) Add(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if err := validate(ctx, ttl); err != nil {
		return false, err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	now := time.Now()
	driver.sweepDue(now)

	if _, found := driver.live(key, now); found {
		return false, nil
	}

	driver.Entries[key] = newEntry(value, ttl, now)

	return true, nil
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
func (driver *Store) Pull(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	stored, found := driver.live(key, time.Now())

	if !found {
		return nil, cache.ErrNotFound
	}

	delete(driver.Entries, key)

	return stored.value, nil
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
func (driver *Store) TTL(ctx context.Context, key string) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	now := time.Now()
	stored, found := driver.live(key, now)

	if !found {
		return 0, cache.ErrNotFound
	}

	return stored.remaining(now), nil
}
