package memorycache

import (
	"context"
	"time"
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
func (driver *Driver) Increment(ctx context.Context, key string, delta int64) (int64, error) {
	return driver.store.Increment(ctx, key, delta)
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
func (driver *Driver) Decrement(ctx context.Context, key string, delta int64) (int64, error) {
	return driver.store.Decrement(ctx, key, delta)
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
func (driver *Driver) IncrementWithTTL(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, time.Duration, error) {
	return driver.store.IncrementWithTTL(ctx, key, delta, ttl)
}
