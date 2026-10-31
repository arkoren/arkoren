package rediscache

import (
	"context"
	"time"

	"arkoren.dev/core/service/cache"

	"github.com/redis/go-redis/v9"
)

// compareAndPutScript compares the unexpired guard's exact bytes and writes
// the destination in one Redis execution. A missing guard never matches empty
// content. SET replaces any old TTL; positive lifetimes use PX milliseconds.
const compareAndPutScript = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
if ARGV[3] == '0' then
  redis.call('SET', KEYS[2], ARGV[2])
else
  redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
end
return 1
`

// guardedWriteCmd keeps the native integer reply decoder but disables command
// replay after an uncertain response. Retrying a successful same-key write
// could otherwise return a mismatch and falsely report that nothing changed.
type guardedWriteCmd struct {
	// IntCmd supplies native RESP decoding and the command's arguments.
	*redis.IntCmd
}

// NoRetry tells go-redis not to replay a possibly executed guarded write.
// Pre-write connection recovery remains the native client's responsibility;
// once sent, an uncertain result is an error rather than a second comparison.
func (cmd *guardedWriteCmd) NoRetry() bool {
	return true
}

// CompareAndPut atomically compares match and writes entry with a Lua script.
// Missing, expired, or unequal guards return false without changing either
// entry's value or TTL. Positive TTLs round up to whole milliseconds without
// overflowing; zero removes the destination's expiry. Redis errors are returned
// as received, and a transport failure can leave the applied outcome unknown.
// Possibly executed writes are not automatically retried, even if the native
// client's retry policy allows retries for ordinary commands.
// This driver uses one standalone Redis client, not a Redis Cluster client.
//
// Example:
//
//	driver := rediscache.New()
//	applied, err := driver.CompareAndPut(ctx,
//		cache.Match{Key: "document", Value: []byte("draft")},
//		cache.Entry{Key: "document", Value: []byte("revised"), TTL: time.Minute})
//	if err != nil {
//		return err
//	}
//	_ = applied // false if the document is missing or no longer contains draft.
func (driver *Driver) CompareAndPut(ctx context.Context, match cache.Match, entry cache.Entry) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	if err := validateTTL(entry.TTL); err != nil {
		return false, err
	}

	milliseconds := entry.TTL.Milliseconds()

	if entry.TTL%time.Millisecond != 0 {
		milliseconds++
	}

	cmd := &guardedWriteCmd{IntCmd: redis.NewIntCmd(ctx, "eval", compareAndPutScript, 2,
		match.Key, entry.Key, string(match.Value), string(entry.Value), milliseconds)}
	// EVAL's first key follows the command name, script, and key count.
	cmd.SetFirstKeyPos(3)

	if err := (*redis.Client)(driver).Process(ctx, cmd); err != nil {
		return false, err
	}

	return cmd.Val() == 1, nil
}
