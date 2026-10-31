package rediscache

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/redis/go-redis/v9"

	"arkoren.dev/core/service/cache"
)

// TTL returns key's remaining lifetime with Redis millisecond precision. Zero
// means no expiry; missing or expired entries return cache.ErrNotFound. PTTL
// preserves subsecond lifetimes instead of rounding them to whole seconds.
// Remaining lifetimes beyond time.Duration's range saturate at its maximum.
//
// Example:
//
//	remaining, err := driver.TTL(ctx, "version")
//	if err != nil {
//		return err
//	}
//	_ = remaining
func (driver *Driver) TTL(ctx context.Context, key string) (time.Duration, error) {
	ttl, err := (*redis.Client)(driver).Do(ctx, "pttl", key).Int64()

	if redisTTLNotFound(err, ttl) {
		return 0, cache.ErrNotFound
	}

	if err != nil {
		return 0, err
	}

	if ttl == -1 {
		return 0, nil
	}

	if ttl > math.MaxInt64/int64(time.Millisecond) {
		return time.Duration(math.MaxInt64), nil
	}

	return time.Duration(ttl) * time.Millisecond, nil
}

// redisTTLNotFound recognizes the native missing-key error or PTTL's raw -2
// sentinel. Raw millisecond integers are decoded before duration conversion so
// large positive replies cannot wrap into an expiry sentinel.
func redisTTLNotFound(err error, ttl int64) bool {
	return errors.Is(err, redis.Nil) || ttl == -2
}
