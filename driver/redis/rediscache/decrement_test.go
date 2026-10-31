package rediscache_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"testing"

	"arkoren.dev/driver/redis/rediscache"

	"github.com/redis/go-redis/v9"
)

// TestDecrementMinimumDeltaCanProduceZero covers Redis's native DECRBY argument
// limitation: Redis rejects negating MinInt64 even when the mathematical result
// is representable. The peer models that native error, not Lua source execution;
// the integration scenario separately verifies the atomic script on Redis.
func TestDecrementMinimumDeltaCanProduceZero(t *testing.T) {
	t.Parallel()

	driver := minimumDecrementDriver(t)

	if err := driver.Put(t.Context(), "counter", []byte("-9223372036854775808"), redisCounterTTL); err != nil {
		t.Fatal(err)
	}

	count, err := driver.Decrement(t.Context(), "counter", math.MinInt64)

	if err != nil || count != 0 {
		t.Fatalf("Decrement(MinInt64, MinInt64) = %d, %v, want 0, nil", count, err)
	}
}

// minimumDecrementDriver uses a local RESP peer to distinguish unsupported native
// DECRBY from the atomic adapter path, retaining real SDK encoding and decoding.
// Both sockets and the peer are released and joined at test cleanup.
func minimumDecrementDriver(t *testing.T) *rediscache.Driver {
	t.Helper()

	connection, peer := net.Pipe()
	done := make(chan error, 1)

	go func() {
		done <- errors.Join(minimumDecrementPeer(peer), peer.Close())
	}()

	driver := rediscache.NewFrom(redis.NewClient(&redis.Options{
		Protocol: 2, DisableIdentity: true, MaxRetries: -1,
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return connection, nil
		},
	}))
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}

		_ = peer.Close() // Release the test's local socket even after assertion failure.

		if err := <-done; err != nil {
			t.Errorf("RESP peer: %v", err)
		}
	})

	return driver
}

// minimumDecrementPeer accepts RESP2 initialization and one SET, then returns
// Redis's real minimum-delta error for DECRBY or a decimal zero for atomic EVAL.
// It does not interpret or assert the script's text or implementation details.
func minimumDecrementPeer(peer net.Conn) error {
	reader := bufio.NewReader(peer)

	if _, err := readCounterRESPCommand(reader); err != nil {
		return err
	}

	if _, err := io.WriteString(peer, "-ERR unknown command 'hello'\r\n"); err != nil {
		return err
	}

	if _, err := readCounterRESPCommand(reader); err != nil {
		return err
	}

	if _, err := io.WriteString(peer, "+OK\r\n"); err != nil {
		return err
	}

	command, err := readCounterRESPCommand(reader)

	if err != nil {
		return err
	}

	var reply string

	switch command[0] {
	case "decrby":
		reply = "-ERR decrement would overflow\r\n"
	case "eval":
		reply = "$1\r\n0\r\n"
	default:
		return fmt.Errorf("unexpected counter command %q", command[0])
	}

	_, err = io.WriteString(peer, reply)

	return err
}

// TestDecrementMinimumDeltaOverflowPreservesTarget keeps a rejected mathematical
// overflow nonmutating and preserves Redis's native error classification. The
// existing expiry must not disappear or be renewed by the special script path.
func TestDecrementMinimumDeltaOverflowPreservesTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	driver, ctx := newRedisDriver(t)
	key := redisTestKey(t)
	cleanupTypedRedisKeys(t, driver, key)

	if err := driver.Put(ctx, key, []byte("0"), redisCounterTTL); err != nil {
		t.Fatal(err)
	}

	before, err := driver.TTL(ctx, key)

	if err != nil {
		t.Fatal(err)
	}

	_, err = driver.Decrement(ctx, key, math.MinInt64)

	var native redis.Error

	if !errors.As(err, &native) {
		t.Fatalf("overflow error = %v, want native Redis error", err)
	}

	stored, err := driver.Get(ctx, key)

	if err != nil || string(stored) != "0" {
		t.Fatalf("stored counter = %q, %v, want unchanged 0", stored, err)
	}

	after, err := driver.TTL(ctx, key)

	if err != nil || after <= 0 || after > before {
		t.Fatalf("TTL after overflow = %s, %v, want positive and <= %s", after, err, before)
	}
}
