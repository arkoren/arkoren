package rediscache_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"slices"
	"strconv"
	"testing"
	"time"

	"arkoren.dev/driver/redis/rediscache"

	"github.com/redis/go-redis/v9"
)

// newCounterRESPDriver connects a public driver to an in-memory RESP peer. The
// native client's real encoder and decoder remain in use, without hooks or a
// network endpoint. Each supplied wire reply answers one observed command after
// the RESP2 handshake; the buffered channel exposes those protocol requests.
// Cleanup closes both pipe ends and joins the peer, including on test failure.
func newCounterRESPDriver(t *testing.T, replies ...string) (*rediscache.Driver, <-chan []string) {
	t.Helper()

	connection, peer := net.Pipe()
	commands := make(chan []string, len(replies))
	done := make(chan error, 1)

	go func() {
		err := serveCounterRESP(peer, commands, replies)
		err = errors.Join(err, peer.Close())
		close(commands)
		done <- err
	}()

	driver := rediscache.NewFrom(redis.NewClient(&redis.Options{
		Protocol:        2,
		DisableIdentity: true,
		MaxRetries:      -1,
		ReadTimeout:     redisOperationTimeout,
		WriteTimeout:    redisOperationTimeout,
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return connection, nil
		},
	}))
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Errorf("stop RESP driver: %v", err)
		}

		if err := peer.Close(); err != nil {
			t.Errorf("close RESP peer: %v", err)
		}

		if err := <-done; err != nil {
			t.Errorf("RESP fixture: %v", err)
		}
	})

	return driver, commands
}

// serveCounterRESP declines HELLO as an older RESP2 server would, then records
// native requests and sends the supplied RESP replies in order. It deliberately
// does not interpret Lua; integration tests retain responsibility for script
// execution and atomic counter/window behavior. Protocol and I/O errors surface
// through the fixture's cleanup, rather than failing from a goroutine.
func serveCounterRESP(peer net.Conn, commands chan<- []string, replies []string) error {
	reader := bufio.NewReader(peer)
	hello, err := readCounterRESPCommand(reader)

	if err != nil {
		return err
	}

	if hello[0] != "hello" {
		return fmt.Errorf("first RESP command = %q, want HELLO", hello)
	}

	if _, err := io.WriteString(peer, "-ERR unknown command 'hello'\r\n"); err != nil {
		return err
	}

	for _, reply := range replies {
		command, err := readCounterRESPCommand(reader)

		if err != nil {
			return err
		}

		commands <- command

		if _, err := io.WriteString(peer, reply); err != nil {
			return err
		}
	}

	return nil
}

// readCounterRESPCommand decodes the array-of-bulk-strings request format used
// by the native client. Bulk lengths preserve embedded newlines in Lua source;
// the fixture never inspects or asserts that source. Header parsing failures and
// short reads are returned as errors to the fixture owner.
func readCounterRESPCommand(reader *bufio.Reader) ([]string, error) {
	header, err := reader.ReadString('\n')

	if err != nil {
		return nil, err
	}

	var length int

	if _, err := fmt.Sscanf(header, "*%d\r\n", &length); err != nil {
		return nil, err
	}

	command := make([]string, length)

	for index := range command {
		if _, err := fmt.Fscanf(reader, "$%d\r\n", &length); err != nil {
			return nil, err
		}

		payload := make([]byte, length+2)

		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, err
		}

		command[index] = string(payload[:length])
	}

	return command, nil
}

// TestRedisCounterFidelitySubmillisecondTTL checks the wire TTL for positive
// durations below Redis precision. Zero would reject creation or delete a
// previously persistent counter, so every such input must become one millisecond.
func TestRedisCounterFidelitySubmillisecondTTL(t *testing.T) {
	t.Parallel()

	client, commands := newCounterRESPDriver(t,
		"*2\r\n$1\r\n3\r\n:1\r\n",
		"*2\r\n$1\r\n3\r\n:1\r\n",
		"*2\r\n$1\r\n3\r\n:1\r\n",
	)

	for _, ttl := range []time.Duration{time.Nanosecond, time.Microsecond, time.Millisecond - 1} {
		count, remaining, err := client.IncrementWithTTL(t.Context(), "counter", 3, ttl)

		if err != nil {
			t.Fatalf("IncrementWithTTL(%s): %v", ttl, err)
		}

		if count != 3 || remaining != time.Millisecond {
			t.Fatalf("IncrementWithTTL(%s) = (%d, %s), want (3, 1ms)", ttl, count, remaining)
		}

		request := <-commands

		if request[0] != "eval" || !slices.Equal(request[2:], []string{"1", "counter", "1", "3"}) {
			t.Fatalf("wire request = %q, want EVAL with one key, TTL 1ms and delta 3", request)
		}
	}
}

// TestRedisCounterFidelityFractionalMillisecondTTL checks that rounding is a
// ceiling, not merely a one-millisecond minimum for submillisecond inputs.
func TestRedisCounterFidelityFractionalMillisecondTTL(t *testing.T) {
	t.Parallel()

	client, commands := newCounterRESPDriver(t, "*2\r\n$1\r\n3\r\n:2\r\n")

	count, remaining, err := client.IncrementWithTTL(t.Context(), "counter", 3, time.Millisecond+time.Nanosecond)

	if err != nil {
		t.Fatalf("IncrementWithTTL(): %v", err)
	}

	if count != 3 || remaining != 2*time.Millisecond {
		t.Fatalf("IncrementWithTTL() = (%d, %s), want (3, 2ms)", count, remaining)
	}

	request := <-commands

	if request[0] != "eval" || !slices.Equal(request[2:], []string{"1", "counter", "2", "3"}) {
		t.Fatalf("wire request = %q, want EVAL with one key, TTL 2ms and delta 3", request)
	}
}

// TestRedisCounterFidelityMaximumTTL checks both conversion boundaries: rounding
// the largest duration must not overflow, nor may decoding its rounded Redis
// expiry produce a negative duration. Unrepresentable remaining time saturates.
func TestRedisCounterFidelityMaximumTTL(t *testing.T) {
	t.Parallel()

	client, commands := newCounterRESPDriver(t, "*2\r\n$1\r\n3\r\n:9223372036855\r\n")

	count, remaining, err := client.IncrementWithTTL(t.Context(), "counter", 3, time.Duration(math.MaxInt64))

	if err != nil {
		t.Fatalf("IncrementWithTTL(): %v", err)
	}

	if count != 3 || remaining != time.Duration(math.MaxInt64) {
		t.Fatalf("IncrementWithTTL() = (%d, %s), want (3, maximum duration)", count, remaining)
	}

	request := <-commands

	if request[0] != "eval" || !slices.Equal(request[2:], []string{"1", "counter", "9223372036855", "3"}) {
		t.Fatalf("wire request = %q, want EVAL with maximum rounded TTL and delta 3", request)
	}
}

// TestRedisCounterFidelityDecimalReply checks exact decimal requests and returned
// counts through the public operation at both int64 limits and beyond Lua's
// numeric range. Script execution remains the integration tests' responsibility.
func TestRedisCounterFidelityDecimalReply(t *testing.T) {
	t.Parallel()

	client, commands := newCounterRESPDriver(t,
		"*2\r\n$16\r\n9007199254740993\r\n:1000\r\n",
		"*2\r\n$17\r\n-9007199254740993\r\n:1000\r\n",
		"*2\r\n$19\r\n9223372036854775807\r\n:1000\r\n",
		"*2\r\n$20\r\n-9223372036854775808\r\n:1000\r\n",
	)

	for _, delta := range []int64{1<<53 + 1, -(1<<53 + 1), math.MaxInt64, math.MinInt64} {
		count, remaining, err := client.IncrementWithTTL(t.Context(), "counter", delta, time.Second)

		if err != nil {
			t.Fatalf("IncrementWithTTL(%d): %v", delta, err)
		}

		if count != delta || remaining != time.Second {
			t.Fatalf("IncrementWithTTL(%d) = (%d, %s), want (%d, 1s)", delta, count, remaining, delta)
		}

		request := <-commands
		arguments := []string{"1", "counter", "1000", strconv.FormatInt(delta, 10)}

		if request[0] != "eval" || !slices.Equal(request[2:], arguments) {
			t.Fatalf("wire request = %q, want EVAL with TTL 1000ms and exact delta %d", request, delta)
		}
	}
}

// TestRedisCounterFidelityCreatesSubmillisecondWindow verifies an absent counter
// accepts a positive submillisecond TTL and receives a real one-millisecond
// window. It inspects the script reply, avoiding timing-sensitive later reads.
func TestRedisCounterFidelityCreatesSubmillisecondWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, ctx := newRedisDriver(t)
	key := redisTestKey(t)
	cleanupTypedRedisKeys(t, client, key)

	count, remaining, err := client.IncrementWithTTL(ctx, key, 3, time.Nanosecond)

	if err != nil {
		t.Fatalf("IncrementWithTTL(): %v", err)
	}

	if count != 3 || remaining != time.Millisecond {
		t.Fatalf("IncrementWithTTL() = (%d, %s), want (3, 1ms)", count, remaining)
	}
}

// TestRedisCounterFidelityExpiresPersistentSubmillisecondWindow verifies that
// giving an existing persistent counter a positive submillisecond window does
// not delete it inside the script, as PEXPIRE with zero milliseconds would.
func TestRedisCounterFidelityExpiresPersistentSubmillisecondWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, ctx := newRedisDriver(t)
	key := redisTestKey(t)
	cleanupTypedRedisKeys(t, client, key)

	if err := client.Put(ctx, key, []byte("7"), 0); err != nil {
		t.Fatalf("Put(): %v", err)
	}

	count, remaining, err := client.IncrementWithTTL(ctx, key, 3, time.Nanosecond)

	if err != nil {
		t.Fatalf("IncrementWithTTL(): %v", err)
	}

	if count != 10 || remaining != time.Millisecond {
		t.Fatalf("IncrementWithTTL() = (%d, %s), want (10, 1ms)", count, remaining)
	}
}

// TestRedisCounterFidelityExactInt64 verifies the atomic script returns the
// actual stored decimal, not INCRBY's rounded Lua number. Both signs and both
// int64 limits are supported inputs; later GETs check the same persisted value.
func TestRedisCounterFidelityExactInt64(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, ctx := newRedisDriver(t)
	key := redisTestKey(t)
	cleanupTypedRedisKeys(t, client, key)

	for _, delta := range []int64{1<<53 + 1, -(1<<53 + 1), math.MaxInt64, math.MinInt64} {
		if err := client.Put(ctx, key, []byte("0"), 0); err != nil {
			t.Fatalf("Put(): %v", err)
		}

		count, _, err := client.IncrementWithTTL(ctx, key, delta, redisCounterTTL)

		if err != nil {
			t.Fatalf("IncrementWithTTL(%d): %v", delta, err)
		}

		if count != delta {
			t.Fatalf("IncrementWithTTL(%d) = %d, want exact delta", delta, count)
		}

		stored, err := client.Client().Get(ctx, key).Int64()

		if err != nil {
			t.Fatalf("Get(): %v", err)
		}

		if stored != count {
			t.Fatalf("stored counter = %d, script returned %d", stored, count)
		}
	}
}
