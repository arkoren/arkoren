package storage

import (
	"context"
	"fmt"
	"time"

	"arkoren.dev/core/service/cache"
)

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
func (driver *Store) TryLock(ctx context.Context, name, token string, ttl time.Duration) (bool, error) {
	if err := validateLockTTL(ctx, ttl); err != nil {
		return false, err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	now := time.Now()
	driver.sweepDue(now)

	if _, found := driver.live(name, now); found {
		return false, nil
	}

	driver.Entries[name] = newEntry([]byte(token), ttl, now)

	return true, nil
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
func (driver *Store) Unlock(ctx context.Context, name, token string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	stored, found := driver.live(name, time.Now())

	if !found || string(stored.value) != token {
		return false, nil
	}

	delete(driver.Entries, name)

	return true, nil
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
func (driver *Store) ExtendLock(ctx context.Context, name, token string, ttl time.Duration) (bool, error) {
	if err := validateLockTTL(ctx, ttl); err != nil {
		return false, err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	now := time.Now()
	stored, found := driver.live(name, now)

	if !found || string(stored.value) != token {
		return false, nil
	}

	stored.expiresAt = now.Add(ttl)
	driver.Entries[name] = stored

	return true, nil
}

// validateLockTTL returns the context error or [cache.ErrInvalidTTL] for a
// ttl that is not positive, since a lock must expire.
func validateLockTTL(ctx context.Context, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if ttl <= 0 {
		return fmt.Errorf("memory: lock ttl %s: %w", ttl, cache.ErrInvalidTTL)
	}

	return nil
}
