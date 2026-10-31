package rediscache_test

import (
	"context"
	"errors"
	"math"
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/driver/redis/rediscache"

	"github.com/redis/go-redis/v9"
)

// newCompareRESPDriver retains native retry policy while using a local RESP
// peer, so tests can detect accidental replay as well as inspect the exported
// adapter's wire arguments. It reuses the existing RESP parser/handshake fixture
// but does not interpret or assert Lua source. Script execution belongs to the
// Redis conformance integration test, not this protocol fixture.
func newCompareRESPDriver(t *testing.T, reply string) (*rediscache.Driver, <-chan []string, *atomic.Int32) {
	t.Helper()

	connection, peer := net.Pipe()
	commands := make(chan []string, 1)
	done := make(chan error, 1)

	go func() {
		err := serveCounterRESP(peer, commands, []string{reply})
		done <- errors.Join(err, peer.Close())
	}()

	dials := &atomic.Int32{}
	driver := rediscache.NewFrom(redis.NewClient(&redis.Options{
		Protocol:        2,
		DisableIdentity: true,
		MaxRetries:      2,
		ReadTimeout:     redisOperationTimeout,
		WriteTimeout:    redisOperationTimeout,
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)

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

	return driver, commands, dials
}

// TestRedisCompareAndPutProtocol verifies two literal keys, arbitrary binary
// bytes, ceiling TTL conversion, and successful native RESP reply decoding.
func TestRedisCompareAndPutProtocol(t *testing.T) {
	t.Parallel()

	driver, commands, _ := newCompareRESPDriver(t, ":1\r\n")

	applied, err := driver.CompareAndPut(t.Context(), cache.Match{Key: "old", Value: []byte{0, 0xff}},
		cache.Entry{Key: "new", Value: []byte{0xfe, '\n'}, TTL: time.Millisecond + time.Nanosecond})

	if err != nil || !applied {
		t.Fatalf("CompareAndPut = %t, %v, want true, nil", applied, err)
	}

	request := <-commands

	if request[0] != "eval" || !slices.Equal(request[2:], []string{"2", "old", "new", "\x00\xff", "\xfe\n", "2"}) {
		t.Fatalf("wire request = %q, want one guarded two-key EVAL with 2ms TTL", request)
	}
}

// TestRedisCompareAndPutMismatchProtocol keeps the same-key shape and zero
// lifetime visible at the vendor seam; a zero reply is a normal mismatch.
func TestRedisCompareAndPutMismatchProtocol(t *testing.T) {
	t.Parallel()

	driver, commands, _ := newCompareRESPDriver(t, ":0\r\n")

	applied, err := driver.CompareAndPut(t.Context(), cache.Match{Key: "same"}, cache.Entry{Key: "same"})

	if err != nil || applied {
		t.Fatalf("CompareAndPut = %t, %v, want false, nil", applied, err)
	}

	request := <-commands

	if !slices.Equal(request[2:], []string{"2", "same", "same", "", "", "0"}) {
		t.Fatalf("wire arguments = %q, want same keys, empty bytes, zero TTL", request[2:])
	}
}

// TestRedisCompareAndPutSubmillisecondTTL prevents a positive write lifetime
// from becoming zero (persistent) when converted to Redis millisecond units.
func TestRedisCompareAndPutSubmillisecondTTL(t *testing.T) {
	t.Parallel()

	driver, commands, _ := newCompareRESPDriver(t, ":1\r\n")

	applied, err := driver.CompareAndPut(t.Context(), cache.Match{Key: "guard"},
		cache.Entry{Key: "target", TTL: time.Nanosecond})

	if err != nil || !applied {
		t.Fatalf("CompareAndPut = %t, %v, want true, nil", applied, err)
	}

	request := <-commands

	if request[len(request)-1] != "1" {
		t.Fatalf("wire TTL = %q, want 1ms", request[len(request)-1])
	}
}

// TestRedisCompareAndPutMaximumTTL checks ceiling conversion cannot overflow
// even at the largest valid duration; Redis receives decimal milliseconds.
func TestRedisCompareAndPutMaximumTTL(t *testing.T) {
	t.Parallel()

	driver, commands, _ := newCompareRESPDriver(t, ":1\r\n")

	applied, err := driver.CompareAndPut(t.Context(), cache.Match{Key: "guard"},
		cache.Entry{Key: "target", TTL: time.Duration(math.MaxInt64)})

	if err != nil || !applied {
		t.Fatalf("CompareAndPut = %t, %v, want true, nil", applied, err)
	}

	request := <-commands

	if request[len(request)-1] != "9223372036855" {
		t.Fatalf("wire TTL = %q, want 9223372036855ms", request[len(request)-1])
	}
}

// TestRedisCompareAndPutLostReplyIsNotRetried models an applied command whose
// acknowledgement was lost. Native retries are enabled, but the adapter must
// return an uncertain error instead of replaying and reporting a false mismatch.
func TestRedisCompareAndPutLostReplyIsNotRetried(t *testing.T) {
	t.Parallel()

	driver, commands, dials := newCompareRESPDriver(t, "")

	applied, err := driver.CompareAndPut(t.Context(), cache.Match{Key: "guard", Value: []byte("draft")},
		cache.Entry{Key: "guard", Value: []byte("revised")})

	if err == nil || applied {
		t.Fatalf("CompareAndPut = %t, %v, want false and uncertain transport error", applied, err)
	}

	<-commands

	if got := dials.Load(); got != 1 {
		t.Fatalf("connection attempts = %d, want one without replay", got)
	}
}

// TestRedisCompareAndPutValidationSkipsNetwork uses a driver with no native
// client to prove cancellation and negative TTL rejection precede network I/O.
func TestRedisCompareAndPutValidationSkipsNetwork(t *testing.T) {
	t.Parallel()

	driver := &rediscache.Driver{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := driver.CompareAndPut(ctx, cache.Match{}, cache.Entry{TTL: -time.Second})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled call = %v, want context.Canceled", err)
	}

	_, err = driver.CompareAndPut(t.Context(), cache.Match{}, cache.Entry{TTL: -time.Second})

	if !errors.Is(err, cache.ErrInvalidTTL) {
		t.Fatalf("negative TTL = %v, want ErrInvalidTTL", err)
	}
}
