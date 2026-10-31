package cache

import (
	"context"
	"time"
)

// Increment atomically increases the int64 counter by delta. The key's write
// TTL is ignored: existing expiry is preserved; missing counters have no expiry.
// A mathematical result outside int64 returns the driver's native error without
// changing the target value or expiry. An absent target remains absent.
//
//	value, err := store.Increment(ctx, cache.NewKey[int64]("visits"), 1)
//	if err != nil {
//		return err
//	}
//	_ = value
func (cache *Cache) Increment(ctx context.Context, key Key[int64], delta int64) (value int64, err error) {
	ctx, op := cache.begin(ctx, operationIncrement)
	defer func() { op.end(ctx, err, hitUnknown) }()

	if err = validateKey(ctx, key); err != nil {
		return 0, err
	}

	value, err = cache.driver.Increment(ctx, key.Name(), delta)

	return value, err
}

// Decrement atomically decreases the int64 counter by delta, following
// Increment's expiry rules rather than the key's write TTL.
// Mathematical subtraction supports every int64 delta, including MinInt64.
// An out-of-range result returns a backend-native error without changing the
// target value or TTL or creating a missing target.
//
//	value, err := store.Decrement(ctx, cache.NewKey[int64]("visits"), 1)
//	if err != nil {
//		return err
//	}
//	_ = value
func (cache *Cache) Decrement(ctx context.Context, key Key[int64], delta int64) (value int64, err error) {
	ctx, op := cache.begin(ctx, operationDecrement)
	defer func() { op.end(ctx, err, hitUnknown) }()

	if err = validateKey(ctx, key); err != nil {
		return 0, err
	}

	value, err = cache.driver.Decrement(ctx, key.Name(), delta)

	return value, err
}

// IncrementWithTTL atomically increases the integer value at key by delta,
// gives a counter without an expiry ttl, and returns the new value with the
// remaining lifetime. An existing expiry is kept. A ttl that is not positive
// fails with an error wrapping [ErrInvalidTTL]. A driver that cannot do this
// atomically returns [ErrUnsupported].
// The explicit ttl controls counter initialization; key.TTL is ignored.
// An out-of-range mathematical sum returns a backend-native error without
// changing the target value or TTL. No TTL is added on overflow, and an absent
// target remains absent.
//
//	value, remaining, err := store.IncrementWithTTL(ctx, cache.NewKey[int64]("window"), 1, time.Minute)
//	if err != nil {
//		return err
//	}
//	_ = value
//	_ = remaining
func (cache *Cache) IncrementWithTTL(
	ctx context.Context,
	key Key[int64],
	delta int64,
	ttl time.Duration,
) (value int64, remaining time.Duration, err error) {
	ctx, op := cache.begin(ctx, operationIncrementWithTTL)
	defer func() { op.end(ctx, err, hitUnknown) }()

	if err = validateKey(ctx, key); err != nil {
		return 0, 0, err
	}

	if ttl <= 0 {
		return 0, 0, ErrInvalidTTL
	}

	value, remaining, err = cache.driver.IncrementWithTTL(ctx, key.Name(), delta, ttl)

	return value, remaining, err
}
