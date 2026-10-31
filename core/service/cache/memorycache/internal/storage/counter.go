package storage

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"arkoren.dev/core/service/cache"
)

// Increment increases the integer stored at key by delta and returns the new
// value. A missing key is created from zero. The remaining TTL is preserved.
// A mathematical result outside int64 returns an error wrapping strconv.ErrRange
// without changing the target value or TTL. An absent target remains absent.
//
// Example:
//
//	count, err := driver.Increment(ctx, "views", 1)
//	if err != nil {
//		return err
//	}
//	_ = count
func (driver *Store) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	next, _, err := driver.adjust(key, delta, 0, time.Now())

	return next, err
}

// Decrement decreases the integer stored at key by delta and returns the new
// value. A missing key is created from zero. The remaining TTL is preserved.
// Subtraction is checked directly, including delta == math.MinInt64. A result
// outside int64 returns an error wrapping strconv.ErrRange without changing
// the target value or expiry; a missing target remains absent.
//
// Example:
//
//	count, err := driver.Decrement(ctx, "available", 1)
//	if err != nil {
//		return err
//	}
//	_ = count
func (driver *Store) Decrement(ctx context.Context, key string, delta int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	stored, current, err := driver.counter(key, time.Now())

	if err != nil {
		return 0, err
	}

	next, err := subtractCounter(current, delta)

	if err != nil {
		return 0, fmt.Errorf("memory: key %q: counter overflow: %w", key, err)
	}

	stored.value = []byte(strconv.FormatInt(next, 10))
	driver.Entries[key] = stored

	return next, nil
}

// IncrementWithTTL increases the integer stored at key by delta, ensures the
// key expires, and returns the new value with the remaining lifetime. A key
// that is missing or has no expiry gets ttl; an existing expiry is kept. It
// returns an error when ttl is not positive.
// A mathematical result outside int64 returns an error wrapping strconv.ErrRange
// before changing the target value or expiry. Overflow never adds a TTL to a
// persistent target or creates an absent one.
//
// Example:
//
//	count, remaining, err := driver.IncrementWithTTL(ctx, "requests", 1, time.Minute)
//	if err != nil {
//		return err
//	}
//	_, _ = count, remaining
func (driver *Store) IncrementWithTTL(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}

	if ttl <= 0 {
		return 0, 0, fmt.Errorf("memory: counter ttl %s: %w", ttl, cache.ErrInvalidTTL)
	}

	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	return driver.adjust(key, delta, ttl, time.Now())
}

// adjust checks addition before storing the counter and its remaining lifetime.
// A positive defaultTTL is applied only after successful arithmetic to a counter
// without expiry. The mutex must be held throughout lookup, check, and write;
// an overflow must not change the target's value or expiry or create a key.
func (driver *Store) adjust(key string, delta int64, defaultTTL time.Duration, now time.Time) (int64, time.Duration, error) {
	stored, current, err := driver.counter(key, now)

	if err != nil {
		return 0, 0, err
	}

	next, err := addCounter(current, delta)

	if err != nil {
		return 0, 0, fmt.Errorf("memory: key %q: counter overflow: %w", key, err)
	}

	stored.value = []byte(strconv.FormatInt(next, 10))

	if stored.expiresAt.IsZero() && defaultTTL > 0 {
		stored.expiresAt = now.Add(defaultTTL)
	}

	driver.Entries[key] = stored

	return next, stored.remaining(now), nil
}

// counter loads a live counter after the normal write-side sweep. It returns
// zero for an absent or expired key without inserting it. Parsing errors retain
// their strconv cause; stored values must be base-10 int64 text. The caller must
// hold mu until its eventual write so the value cannot change between lookup
// and the checked arithmetic.
func (driver *Store) counter(key string, now time.Time) (entry, int64, error) {
	driver.sweepDue(now)

	stored, found := driver.live(key, now)

	if !found {
		return entry{}, 0, nil
	}

	current, err := strconv.ParseInt(string(stored.value), 10, 64)

	if err != nil {
		return entry{}, 0, fmt.Errorf("memory: key %q: parse counter value: %w", key, err)
	}

	return stored, current, nil
}

// addCounter returns the mathematical sum if it fits int64. Bounds are checked
// before addition, and strconv.ErrRange identifies a rejected result without
// introducing a portable cache error identity.
func addCounter(current, delta int64) (int64, error) {
	if delta > 0 && current > math.MaxInt64-delta {
		return 0, strconv.ErrRange
	}

	if delta < 0 && current < math.MinInt64-delta {
		return 0, strconv.ErrRange
	}

	return current + delta, nil
}

// subtractCounter returns the mathematical difference if it fits int64, or
// strconv.ErrRange otherwise. It never negates delta: MinInt64 is a valid
// operand even though its positive counterpart is not representable.
func subtractCounter(current, delta int64) (int64, error) {
	if delta > 0 && current < math.MinInt64+delta {
		return 0, strconv.ErrRange
	}

	if delta < 0 && current > math.MaxInt64+delta {
		return 0, strconv.ErrRange
	}

	return current - delta, nil
}
