package rediscache

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/redis/go-redis/v9"

	"arkoren.dev/core/service/cache"
)

// IncrementWithTTL atomically increments key by delta and returns the exact
// int64 value with its remaining lifetime. Missing keys start at zero; keys
// without an expiry receive ttl, while existing expiries are preserved.
// Nonpositive ttl returns an error wrapping [cache.ErrInvalidTTL]. Positive
// durations round up to Redis's millisecond precision without overflowing;
// a remaining lifetime beyond time.Duration's range saturates at its maximum.
// Redis errors, including noninteger values and counter overflow, are returned.
// An already-canceled context takes precedence over invalid TTL validation.
//
// Example:
//
//	driver := rediscache.New()
//	count, remaining, err := driver.IncrementWithTTL(context.Background(), "requests", 1, time.Minute)
//	if err != nil {
//		log.Print(err)
//		return
//	}
//	log.Printf("count=%d remaining=%s", count, remaining)
func (driver *Driver) IncrementWithTTL(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}

	if ttl <= 0 {
		return 0, 0, fmt.Errorf("redis: counter TTL must be positive: %w", cache.ErrInvalidTTL)
	}

	// milliseconds rounds up using the remainder rather than adding to ttl,
	// which could overflow for valid durations near time.Duration's maximum.
	milliseconds := ttl.Milliseconds()

	if ttl%time.Millisecond != 0 {
		milliseconds++
	}

	// Read the stored decimal inside the same atomic script: Lua's numeric
	// INCRBY reply cannot represent every int64 value without rounding.
	result, err := (*redis.Client)(driver).Eval(ctx, `
local remaining = redis.call('PTTL', KEYS[1])
if remaining == -2 then
  redis.call('SET', KEYS[1], '0', 'PX', ARGV[1])
end
redis.call('INCRBY', KEYS[1], ARGV[2])
local count = redis.call('GET', KEYS[1])
if remaining == -1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
remaining = redis.call('PTTL', KEYS[1])
return {count, remaining}
`, []string{
		key,
	}, milliseconds, delta).Int64Slice()

	if err != nil {
		return 0, 0, err
	}

	if result[1] > math.MaxInt64/int64(time.Millisecond) {
		return result[0], time.Duration(math.MaxInt64), nil
	}

	return result[0], time.Duration(result[1]) * time.Millisecond, nil
}
