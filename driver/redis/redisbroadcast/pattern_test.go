package redisbroadcast

import (
	"bytes"
	"log/slog"
	"testing"
)

// TestRedisPatternEscapesLiteralPrefix checks every Redis glob metacharacter,
// including the escape byte itself, without broadening the event-name pattern.
func TestRedisPatternEscapesLiteralPrefix(t *testing.T) {
	t.Parallel()

	got := redisPattern(`tenant[*?]\.`, "users.created")

	if want := `tenant\[\*\?\]\\.users.created`; got != want {
		t.Fatalf("pattern = %q, want %q", got, want)
	}
}

// TestRedisPatternKeepsSingleTokenWildcard ensures prefix escaping does not
// escape the caller's portable wildcard; receive still limits it to one token.
func TestRedisPatternKeepsSingleTokenWildcard(t *testing.T) {
	t.Parallel()

	if got := redisPattern("tenant.", "users.*"); got != "tenant.users.*" {
		t.Fatalf("pattern = %q", got)
	}
}

// TestRedisPatternIncludesZeroTokenTail keeps the Redis subscription broad
// enough for users.# to receive users as well as names with following tokens.
func TestRedisPatternIncludesZeroTokenTail(t *testing.T) {
	t.Parallel()

	if got := redisPattern("tenant.", "users.#"); got != "tenant.users*" {
		t.Fatalf("pattern = %q", got)
	}
}

// TestRedisPatternCatchAllScopesItsPrefix preserves the broad Consume pattern
// while treating a question mark in its namespace as a literal channel byte.
func TestRedisPatternCatchAllScopesItsPrefix(t *testing.T) {
	t.Parallel()

	if got := redisPattern("tenant?.", "#"); got != `tenant\?.*` {
		t.Fatalf("pattern = %q", got)
	}
}

// TestDeliverToHandlerPreservesChannelSuffix verifies that the internal
// callback receives transport identity separately from uninterpreted bytes.
func TestDeliverToHandlerPreservesChannelSuffix(t *testing.T) {
	t.Parallel()

	driver := &Driver{
		logger: slog.New(slog.DiscardHandler),
	}
	payload := []byte(`{"name":"not.the.channel"}`)
	var name string
	var got []byte

	driver.deliverToHandler(func(event string, payload []byte) {
		name, got = event, payload
	}, "#", "users.created", payload)

	if name != "users.created" || !bytes.Equal(got, payload) {
		t.Fatalf("callback received %q, %q", name, got)
	}
}

// TestDeliverToHandlerRecoversPanic ensures a callback panic is contained to
// one delivery instead of terminating the receive goroutine.
func TestDeliverToHandlerRecoversPanic(t *testing.T) {
	t.Parallel()

	driver := &Driver{
		logger: slog.New(slog.DiscardHandler),
	}

	driver.deliverToHandler(func(string, []byte) {
		panic("listener failed")
	}, "#", "users.created", nil)
}
