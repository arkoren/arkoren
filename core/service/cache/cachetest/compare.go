package cachetest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/cache"
)

// compareAndPutCases covers exact comparison, both destination shapes, empty
// bytes, and ownership through the public raw-driver contract.
func compareAndPutCases(t *testing.T, s suite) {
	s.sub(t, "ReplacesMatchingGuard", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "draft", ttl)

		applied, err := d.CompareAndPut(t.Context(), cache.Match{Key: key, Value: []byte("draft")},
			cache.Entry{Key: key, Value: []byte("revised")})

		if err != nil || !applied {
			t.Fatalf("CompareAndPut = %t, %v, want true, nil", applied, err)
		}

		wantValue(t, d, key, "revised")
		wantNoExpiry(t, d, key)
	})
	s.sub(t, "WritesDifferentKeyWithoutChangingGuard", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "draft", 0)

		target := key + ":destination"

		applied, err := d.CompareAndPut(t.Context(), cache.Match{Key: key, Value: []byte("draft")},
			cache.Entry{Key: target, Value: []byte("revised")})

		if err != nil || !applied {
			t.Fatalf("CompareAndPut = %t, %v, want true, nil", applied, err)
		}

		wantValue(t, d, key, "draft")
		wantNoExpiry(t, d, key)
		wantValue(t, d, target, "revised")
	})
	s.sub(t, "MissingGuardDoesNotMatchEmptyBytes", false, func(t *testing.T, d cache.Driver, key string) {
		target := key + ":destination"
		put(t, d, target, "keep", 0)

		applied, err := d.CompareAndPut(t.Context(), cache.Match{Key: key},
			cache.Entry{Key: target, Value: []byte("replace"), TTL: ttl})

		if err != nil || applied {
			t.Fatalf("CompareAndPut = %t, %v, want false, nil", applied, err)
		}

		wantMissing(t, d, key)
		wantValue(t, d, target, "keep")
		wantNoExpiry(t, d, target)
	})
	s.sub(t, "ExistingEmptyGuardMatchesNil", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "", 0)

		applied, err := d.CompareAndPut(t.Context(), cache.Match{Key: key}, cache.Entry{Key: key})

		if err != nil || !applied {
			t.Fatalf("CompareAndPut = %t, %v, want true, nil", applied, err)
		}

		wantValue(t, d, key, "")
	})
	s.sub(t, "CopiesBinaryDestination", false, func(t *testing.T, d cache.Driver, key string) {
		original := []byte{'a', 0, 0xff}

		must(t, "Put", d.Put(t.Context(), key, original, 0))

		value := []byte{'b', 0, 0xfe}

		applied, err := d.CompareAndPut(t.Context(), cache.Match{Key: key, Value: original},
			cache.Entry{Key: key, Value: value})

		if err != nil || !applied {
			t.Fatalf("CompareAndPut = %t, %v, want true, nil", applied, err)
		}

		original[0] = 'x'
		value[0] = 'x'

		wantValue(t, d, key, string([]byte{'b', 0, 0xfe}))
	})
}

// compareAndPutExpiry checks success replaces only the destination's TTL and
// expiry never turns an absent guard into a matching empty value.
func compareAndPutExpiry(t *testing.T, s suite) {
	s.sub(t, "MismatchPreservesValuesAndExpiry", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "retired", ttl)

		target := key + ":destination"
		put(t, d, target, "keep", ttl)
		s.sleep(ttl / 2)

		applied, err := d.CompareAndPut(t.Context(), cache.Match{Key: key, Value: []byte("draft")},
			cache.Entry{Key: target, Value: []byte("replace")})

		if err != nil || applied {
			t.Fatalf("CompareAndPut = %t, %v, want false, nil", applied, err)
		}

		wantValue(t, d, key, "retired")
		wantValue(t, d, target, "keep")
		s.sleep(ttl)

		wantMissing(t, d, key)
		wantMissing(t, d, target)
	})
	s.sub(t, "SuccessReplacesDestinationTTLAndPreservesGuardExpiry", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "draft", ttl)

		target := key + ":destination"
		put(t, d, target, "old", 0)
		s.sleep(ttl / 2)

		applied, err := d.CompareAndPut(t.Context(), cache.Match{Key: key, Value: []byte("draft")},
			cache.Entry{Key: target, Value: []byte("revised"), TTL: 2 * ttl})

		if err != nil || !applied {
			t.Fatalf("CompareAndPut = %t, %v, want true, nil", applied, err)
		}

		s.sleep(ttl)

		wantMissing(t, d, key)
		wantValue(t, d, target, "revised")
		s.sleep(2 * ttl)

		wantMissing(t, d, target)
	})
	s.sub(t, "ExpiredGuardCannotAuthorizeCreation", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "draft", ttl)
		s.sleep(ttl)

		target := key + ":destination"

		applied, err := d.CompareAndPut(t.Context(), cache.Match{Key: key, Value: []byte("draft")},
			cache.Entry{Key: target, Value: []byte("revised"), TTL: ttl})

		if err != nil || applied {
			t.Fatalf("CompareAndPut = %t, %v, want false, nil", applied, err)
		}

		wantMissing(t, d, key)
		wantMissing(t, d, target)
	})
}

// compareAndPutFailures checks rejection precedes guard evaluation and leaves
// stored data unchanged, including when the operation context is canceled.
func compareAndPutFailures(t *testing.T, s suite) {
	s.sub(t, "NegativeTTLFailsEvenWhenGuardMissing", false, func(t *testing.T, d cache.Driver, key string) {
		target := key + ":destination"
		put(t, d, target, "keep", 0)

		applied, err := d.CompareAndPut(t.Context(), cache.Match{Key: key},
			cache.Entry{Key: target, Value: []byte("replace"), TTL: -time.Second})

		if !errors.Is(err, cache.ErrInvalidTTL) || applied {
			t.Fatalf("CompareAndPut = %t, %v, want false, ErrInvalidTTL", applied, err)
		}

		wantMissing(t, d, key)
		wantValue(t, d, target, "keep")
		wantNoExpiry(t, d, target)
	})
	s.sub(t, "CanceledContextLeavesMatchingGuardUnchanged", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "draft", 0)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		applied, err := d.CompareAndPut(ctx, cache.Match{Key: key, Value: []byte("draft")},
			cache.Entry{Key: key, Value: []byte("replace"), TTL: ttl})

		if !errors.Is(err, context.Canceled) || applied {
			t.Fatalf("CompareAndPut = %t, %v, want false, context.Canceled", applied, err)
		}

		wantValue(t, d, key, "draft")
		wantNoExpiry(t, d, key)
	})
}

// compareAndPutConcurrency requires a single winner when many callers try to
// replace the same observed version. Each adapter's atomic operation is tested
// through cache.Driver, not by inspecting its mutex or script implementation.
func compareAndPutConcurrency(t *testing.T, s suite) {
	s.sub(t, "OneWinnerForOneVersion", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "draft", 0)
		var winners atomic.Int32
		var workers sync.WaitGroup
		start := make(chan struct{})

		for range 32 {
			workers.Go(func() {
				<-start

				applied, err := d.CompareAndPut(t.Context(), cache.Match{Key: key, Value: []byte("draft")},
					cache.Entry{Key: key, Value: []byte("revised")})

				if err != nil {
					t.Errorf("CompareAndPut: %v", err)
				}

				if applied {
					winners.Add(1)
				}
			})
		}

		close(start)
		workers.Wait()

		if got := winners.Load(); got != 1 {
			t.Fatalf("winners = %d, want one", got)
		}

		wantValue(t, d, key, "revised")
	})
}
