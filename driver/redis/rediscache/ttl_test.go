package rediscache_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"arkoren.dev/core/service/cache"
)

// TestTTLRetainsSubsecondPrecision prevents a live expiring entry from being
// reported as persistent. The RESP peer returns a 250-millisecond lifetime;
// production Redis, not this fixture, remains responsible for expiry itself.
func TestTTLRetainsSubsecondPrecision(t *testing.T) {
	t.Parallel()

	driver, commands := newCounterRESPDriver(t, ":250\r\n")

	remaining, err := driver.TTL(t.Context(), "short-lived")

	if err != nil {
		t.Fatal(err)
	}

	if remaining != 250*time.Millisecond {
		t.Fatalf("TTL = %s, want 250ms", remaining)
	}

	request := <-commands

	if request[0] != "pttl" {
		t.Fatalf("wire command = %q, want PTTL", request)
	}
}

// TestTTLMaximumPrecisionSaturates covers the final millisecond rounded above
// time.Duration's limit by a valid maximum retention window. Decoding it must
// not wrap into a negative lifetime while the key is still live.
func TestTTLMaximumPrecisionSaturates(t *testing.T) {
	t.Parallel()

	driver, _ := newCounterRESPDriver(t, ":9223372036855\r\n")

	remaining, err := driver.TTL(t.Context(), "long-lived")

	if err != nil || remaining != time.Duration(math.MaxInt64) {
		t.Fatalf("TTL = %s, %v, want maximum duration, nil", remaining, err)
	}
}

// TestTTLPersistentEntryHasZeroLifetime preserves PTTL's no-expiry sentinel
// independently of positive millisecond conversion and saturation.
func TestTTLPersistentEntryHasZeroLifetime(t *testing.T) {
	t.Parallel()

	driver, _ := newCounterRESPDriver(t, ":-1\r\n")

	remaining, err := driver.TTL(t.Context(), "persistent")

	if err != nil || remaining != 0 {
		t.Fatalf("TTL = %s, %v, want zero, nil", remaining, err)
	}
}

// TestTTLMissingEntryKeepsContractError maps raw PTTL absence to the public
// cache sentinel before performing any duration conversion.
func TestTTLMissingEntryKeepsContractError(t *testing.T) {
	t.Parallel()

	driver, _ := newCounterRESPDriver(t, ":-2\r\n")

	_, err := driver.TTL(t.Context(), "missing")

	if !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("TTL error = %v, want cache.ErrNotFound", err)
	}
}
