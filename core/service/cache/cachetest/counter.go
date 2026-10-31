package cachetest

import (
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"arkoren.dev/core/service/cache"
)

// counterBounds checks exact int64 arithmetic, including subtraction of the
// minimum delta, whose negation cannot itself be represented as int64.
func counterBounds(t *testing.T, s suite) {
	s.sub(t, "IncrementCreatesEitherBound", false, func(t *testing.T, d cache.Driver, key string) {
		for _, delta := range []int64{math.MinInt64, math.MaxInt64} {
			must(t, "Delete", d.Delete(t.Context(), key))

			got, err := d.Increment(t.Context(), key, delta)

			wantCount(t, "Increment", got, err, delta)
			wantValue(t, d, key, strconv.FormatInt(delta, 10))
			wantNoExpiry(t, d, key)
		}
	})
	s.sub(t, "IncrementWithTTLCreatesEitherBound", true, func(t *testing.T, d cache.Driver, key string) {
		for _, delta := range []int64{math.MinInt64, math.MaxInt64} {
			must(t, "Delete", d.Delete(t.Context(), key))

			got, remaining, err := d.IncrementWithTTL(t.Context(), key, delta, ttl)

			wantCount(t, "IncrementWithTTL", got, err, delta)

			if remaining <= 0 || remaining > ttl {
				t.Errorf("remaining = %s, want in (0, %s]", remaining, ttl)
			}

			wantValue(t, d, key, strconv.FormatInt(delta, 10))
			wantTTLWithin(t, d, key, ttl)
		}
	})
	s.sub(t, "DecrementMinimumFromMinimumIsZero", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "-9223372036854775808", ttl)

		got, err := d.Decrement(t.Context(), key, math.MinInt64)

		wantCount(t, "Decrement", got, err, 0)
		wantValue(t, d, key, "0")
		wantTTLWithin(t, d, key, ttl)
	})
	s.sub(t, "DecrementMinimumFromNegativeOneIsMaximum", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "-1", 0)

		got, err := d.Decrement(t.Context(), key, math.MinInt64)

		wantCount(t, "Decrement", got, err, math.MaxInt64)
		wantValue(t, d, key, "9223372036854775807")
		wantNoExpiry(t, d, key)
	})
	s.sub(t, "DecrementMinimumFromMissingDoesNotCreate", false, func(t *testing.T, d cache.Driver, key string) {
		if _, err := d.Decrement(t.Context(), key, math.MinInt64); err == nil {
			t.Error("Decrement(MinInt64) from missing succeeded, want overflow error")
		}

		wantMissing(t, d, key)
	})
	s.sub(t, "DecrementMinimumFromExpiredDoesNotRevive", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "-1", ttl)
		s.sleep(ttl)

		if _, err := d.Decrement(t.Context(), key, math.MinInt64); err == nil {
			t.Error("Decrement(MinInt64) from expired succeeded, want overflow error")
		}

		wantMissing(t, d, key)
	})
	s.sub(t, "DecrementMinimumFromZeroFailsUnchanged", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "0", 0)

		wantCounterRejected(t, d, key, func() error {
			_, err := d.Decrement(t.Context(), key, math.MinInt64)

			return err
		})
	})
	s.sub(t, "DecrementMinimumFromZeroPreservesExpiry", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "0", ttl)
		s.sleep(ttl / 2)

		wantCounterRejected(t, d, key, func() error {
			_, err := d.Decrement(t.Context(), key, math.MinInt64)

			return err
		})
		s.sleep(ttl)

		wantMissing(t, d, key)
	})
}

// wantCounterRejected requires an error without prescribing its backend-native
// identity. It checks exact stored bytes and preserves both expiry presence and
// the remaining lifetime; elapsed time may reduce a TTL but cannot increase it.
func wantCounterRejected(t *testing.T, d cache.Driver, key string, change func() error) {
	t.Helper()

	before, err := d.Get(t.Context(), key)

	must(t, "Get before rejection", err)

	lifetime, err := d.TTL(t.Context(), key)

	must(t, "TTL before rejection", err)

	if err := change(); err == nil {
		t.Error("counter overflow succeeded, want error")
	}

	wantValue(t, d, key, string(before))

	remaining, err := d.TTL(t.Context(), key)

	must(t, "TTL after rejection", err)

	if (remaining == 0) != (lifetime == 0) || remaining > lifetime {
		t.Errorf("overflow changed expiry: remaining %s, previously %s", remaining, lifetime)
	}
}

// counterOverflow rejects both mathematical bounds for every counter method.
// Each case checks an aged expiry and a persistent entry so failure cannot
// refresh, clear, or add an expiry, including IncrementWithTTL's write policy.
func counterOverflow(t *testing.T, s suite) {
	s.sub(t, "IncrementAboveMaximum", true, func(t *testing.T, d cache.Driver, key string) {
		change := func() error {
			_, err := d.Increment(t.Context(), key, 1)

			return err
		}
		put(t, d, key, "9223372036854775807", ttl)
		s.sleep(ttl / 2)

		wantCounterRejected(t, d, key, change)
		s.sleep(ttl)

		wantMissing(t, d, key)
		put(t, d, key, "9223372036854775807", 0)

		wantCounterRejected(t, d, key, change)
	})
	s.sub(t, "IncrementBelowMinimum", true, func(t *testing.T, d cache.Driver, key string) {
		change := func() error {
			_, err := d.Increment(t.Context(), key, -1)

			return err
		}
		put(t, d, key, "-9223372036854775808", ttl)
		s.sleep(ttl / 2)

		wantCounterRejected(t, d, key, change)
		s.sleep(ttl)

		wantMissing(t, d, key)
		put(t, d, key, "-9223372036854775808", 0)

		wantCounterRejected(t, d, key, change)
	})
	s.sub(t, "DecrementAboveMaximum", true, func(t *testing.T, d cache.Driver, key string) {
		change := func() error {
			_, err := d.Decrement(t.Context(), key, -1)

			return err
		}
		put(t, d, key, "9223372036854775807", ttl)
		s.sleep(ttl / 2)

		wantCounterRejected(t, d, key, change)
		s.sleep(ttl)

		wantMissing(t, d, key)
		put(t, d, key, "9223372036854775807", 0)

		wantCounterRejected(t, d, key, change)
	})
	s.sub(t, "DecrementBelowMinimum", true, func(t *testing.T, d cache.Driver, key string) {
		change := func() error {
			_, err := d.Decrement(t.Context(), key, 1)

			return err
		}
		put(t, d, key, "-9223372036854775808", ttl)
		s.sleep(ttl / 2)

		wantCounterRejected(t, d, key, change)
		s.sleep(ttl)

		wantMissing(t, d, key)
		put(t, d, key, "-9223372036854775808", 0)

		wantCounterRejected(t, d, key, change)
	})
	s.sub(t, "IncrementWithTTLAboveMaximum", true, func(t *testing.T, d cache.Driver, key string) {
		change := func() error {
			_, _, err := d.IncrementWithTTL(t.Context(), key, 1, 2*ttl)

			return err
		}
		put(t, d, key, "9223372036854775807", ttl)
		s.sleep(ttl / 2)

		wantCounterRejected(t, d, key, change)
		s.sleep(ttl)

		wantMissing(t, d, key)
		put(t, d, key, "9223372036854775807", 0)

		wantCounterRejected(t, d, key, change)
	})
	s.sub(t, "IncrementWithTTLBelowMinimum", true, func(t *testing.T, d cache.Driver, key string) {
		change := func() error {
			_, _, err := d.IncrementWithTTL(t.Context(), key, -1, 2*ttl)

			return err
		}
		put(t, d, key, "-9223372036854775808", ttl)
		s.sleep(ttl / 2)

		wantCounterRejected(t, d, key, change)
		s.sleep(ttl)

		wantMissing(t, d, key)
		put(t, d, key, "-9223372036854775808", 0)

		wantCounterRejected(t, d, key, change)
	})
}

// counterOverflowConcurrency ensures checking the bound and writing the result
// form one atomic operation: only one contender may consume the final unit.
func counterOverflowConcurrency(t *testing.T, s suite) {
	s.sub(t, "IncrementHasOneWinner", false, func(t *testing.T, d cache.Driver, key string) {
		wantOneCounterWinner(t, d, key, func() (int64, error) {
			return d.Increment(t.Context(), key, 1)
		})
	})
	s.sub(t, "DecrementHasOneWinner", false, func(t *testing.T, d cache.Driver, key string) {
		wantOneCounterWinner(t, d, key, func() (int64, error) {
			return d.Decrement(t.Context(), key, -1)
		})
	})
	s.sub(t, "IncrementWithTTLHasOneWinner", true, func(t *testing.T, d cache.Driver, key string) {
		wantOneCounterWinner(t, d, key, func() (int64, error) {
			got, _, err := d.IncrementWithTTL(t.Context(), key, 1, ttl)

			return got, err
		})
	})
}

// wantOneCounterWinner races twenty calls from MaxInt64-1. Backend-native
// overflow errors are allowed; exactly one call must return the final value.
func wantOneCounterWinner(t *testing.T, d cache.Driver, key string, change func() (int64, error)) {
	t.Helper()

	put(t, d, key, "9223372036854775806", 0)

	// contenders exceeds the single available unit to expose check/write races.
	const contenders = 20
	var winners atomic.Int64
	var workers sync.WaitGroup

	for range contenders {
		workers.Go(func() {
			got, err := change()

			if err != nil {
				return
			}

			winners.Add(1)

			if got != math.MaxInt64 {
				t.Errorf("successful counter = %d, want MaxInt64", got)
			}
		})
	}

	workers.Wait()

	if got := winners.Load(); got != 1 {
		t.Errorf("successful counters = %d, want 1", got)
	}

	wantValue(t, d, key, "9223372036854775807")
}
