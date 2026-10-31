package cachetest

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/cache"
)

// basics covers storage, deletion and isolation of byte slices.
func basics(t *testing.T, s suite) {
	s.sub(t, "GetMissingReturnsNotFound", false, func(t *testing.T, d cache.Driver, key string) {
		wantMissing(t, d, key)
	})
	s.sub(t, "PutThenGet", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "value", 0)

		wantValue(t, d, key, "value")

		if has, err := d.Has(t.Context(), key); err != nil || !has {
			t.Errorf("Has = %t, %v, want true, nil", has, err)
		}
	})
	s.sub(t, "StoresArbitraryBytes", false, func(t *testing.T, d cache.Driver, key string) {
		want := []byte{0, 1, 0xff, '\n', 0x80}

		must(t, "Put", d.Put(t.Context(), key, want, 0))

		got, err := d.Get(t.Context(), key)

		must(t, "Get", err)

		if string(got) != string(want) {
			t.Errorf("Get = %v, want %v", got, want)
		}
	})
	s.sub(t, "PutReplacesValue", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "old", 0)
		put(t, d, key, "new", 0)

		wantValue(t, d, key, "new")
	})
	s.sub(t, "DeleteRemovesKey", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "value", 0)

		must(t, "Delete", d.Delete(t.Context(), key))
		wantMissing(t, d, key)
	})
	s.sub(t, "DeleteMissingSucceeds", false, func(t *testing.T, d cache.Driver, key string) {
		must(t, "Delete", d.Delete(t.Context(), key))
	})
	s.sub(t, "PutDoesNotRetainInput", false, func(t *testing.T, d cache.Driver, key string) {
		value := []byte("value")

		must(t, "Put", d.Put(t.Context(), key, value, 0))

		value[0] = 'x'

		wantValue(t, d, key, "value")
	})
	s.sub(t, "GetReturnsIndependentCopy", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "value", 0)

		got, err := d.Get(t.Context(), key)

		must(t, "Get", err)

		got[0] = 'x'

		wantValue(t, d, key, "value")
	})
	s.sub(t, "NegativeTTLIsRejected", false, func(t *testing.T, d cache.Driver, key string) {
		err := d.Put(t.Context(), key, []byte("value"), -time.Second)

		if !errors.Is(err, cache.ErrInvalidTTL) {
			t.Errorf("Put error = %v, want ErrInvalidTTL", err)
		}

		added, err := d.Add(t.Context(), key, []byte("value"), -time.Second)

		if !errors.Is(err, cache.ErrInvalidTTL) || added {
			t.Errorf("Add = %t, %v, want false, ErrInvalidTTL", added, err)
		}

		wantMissing(t, d, key)
	})
}

// expiry covers TTL reporting and lazy expiry.
func expiry(t *testing.T, s suite) {
	s.sub(t, "EntryExpiresAfterTTL", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "value", ttl)
		s.sleep(ttl / 2)

		wantValue(t, d, key, "value")
		s.sleep(ttl)

		wantMissing(t, d, key)
	})
	s.sub(t, "ZeroTTLNeverExpires", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "value", 0)
		s.sleep(2 * ttl)

		wantValue(t, d, key, "value")
		wantNoExpiry(t, d, key)
	})
	s.sub(t, "TTLReportsRemainingLifetime", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "value", ttl)

		before := wantTTLWithin(t, d, key, ttl)
		s.sleep(ttl / 2)

		after := wantTTLWithin(t, d, key, ttl)

		if after >= before {
			t.Errorf("TTL did not decrease: %s then %s", before, after)
		}
	})
	s.sub(t, "PutReplacesTTL", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "old", ttl)
		put(t, d, key, "new", 0)

		wantNoExpiry(t, d, key)
		put(t, d, key, "newer", ttl)

		wantTTLWithin(t, d, key, ttl)
	})
	s.sub(t, "DeleteClearsTTL", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "old", ttl)

		must(t, "Delete", d.Delete(t.Context(), key))
		put(t, d, key, "new", 0)
		s.sleep(2 * ttl)

		wantValue(t, d, key, "new")
	})
}

// addCases covers conditional writes.
func addCases(t *testing.T, s suite) {
	s.sub(t, "StoresWhenAbsent", true, func(t *testing.T, d cache.Driver, key string) {
		added, err := d.Add(t.Context(), key, []byte("first"), ttl)

		if err != nil || !added {
			t.Fatalf("Add = %t, %v, want true, nil", added, err)
		}

		wantValue(t, d, key, "first")
		wantTTLWithin(t, d, key, ttl)
	})
	s.sub(t, "KeepsExistingEntry", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "first", 0)

		added, err := d.Add(t.Context(), key, []byte("second"), ttl)

		if err != nil || added {
			t.Fatalf("Add = %t, %v, want false, nil", added, err)
		}

		wantValue(t, d, key, "first")
		wantNoExpiry(t, d, key)
	})
	s.sub(t, "StoresAfterExpiry", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "old", ttl)
		s.sleep(ttl)

		added, err := d.Add(t.Context(), key, []byte("new"), 0)

		if err != nil || !added {
			t.Fatalf("Add = %t, %v, want true, nil", added, err)
		}

		wantValue(t, d, key, "new")
	})
	s.sub(t, "DoesNotRetainInput", false, func(t *testing.T, d cache.Driver, key string) {
		value := []byte("value")

		if _, err := d.Add(t.Context(), key, value, 0); err != nil {
			t.Fatal(err)
		}

		value[0] = 'x'

		wantValue(t, d, key, "value")
	})
}

// counters covers Increment and Decrement.
func counters(t *testing.T, s suite) {
	s.sub(t, "IncrementCreatesMissingKeyFromZero", false, func(t *testing.T, d cache.Driver, key string) {
		got, err := d.Increment(t.Context(), key, 5)

		wantCount(t, "Increment", got, err, 5)
		wantValue(t, d, key, "5")
		wantNoExpiry(t, d, key)
	})
	s.sub(t, "DecrementCreatesMissingKeyFromZero", false, func(t *testing.T, d cache.Driver, key string) {
		got, err := d.Decrement(t.Context(), key, 5)

		wantCount(t, "Decrement", got, err, -5)
		wantValue(t, d, key, "-5")
	})
	s.sub(t, "AdjustsStoredInteger", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "10", 0)

		got, err := d.Increment(t.Context(), key, 5)

		wantCount(t, "Increment", got, err, 15)

		got, err = d.Decrement(t.Context(), key, 3)

		wantCount(t, "Decrement", got, err, 12)

		got, err = d.Increment(t.Context(), key, -20)

		wantCount(t, "Increment(-20)", got, err, -8)
		wantValue(t, d, key, "-8")
	})
	s.sub(t, "PreservesTTL", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "1", ttl)
		s.sleep(ttl / 2)

		got, err := d.Increment(t.Context(), key, 1)

		wantCount(t, "Increment", got, err, 2)
		wantTTLWithin(t, d, key, ttl-ttl/2+time.Second)
		s.sleep(ttl)

		wantMissing(t, d, key)
	})
	s.sub(t, "StartsOverAfterExpiry", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "10", ttl)
		s.sleep(ttl)

		got, err := d.Increment(t.Context(), key, 1)

		wantCount(t, "Increment", got, err, 1)
		wantNoExpiry(t, d, key)
	})
	s.sub(t, "NonIntegerValueFailsUnchanged", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "text", 0)

		if _, err := d.Increment(t.Context(), key, 1); err == nil {
			t.Error("Increment on non-integer succeeded, want error")
		}

		if _, err := d.Decrement(t.Context(), key, 1); err == nil {
			t.Error("Decrement on non-integer succeeded, want error")
		}

		wantValue(t, d, key, "text")
	})
}

// incrementWithTTL covers IncrementWithTTL.
func incrementWithTTL(t *testing.T, s suite) {
	s.sub(t, "CreatesKeyWithTTL", true, func(t *testing.T, d cache.Driver, key string) {
		got, remaining, err := d.IncrementWithTTL(t.Context(), key, 3, ttl)

		wantCount(t, "IncrementWithTTL", got, err, 3)

		if remaining <= 0 || remaining > ttl {
			t.Errorf("remaining = %s, want in (0, %s]", remaining, ttl)
		}

		wantTTLWithin(t, d, key, ttl)
	})
	s.sub(t, "KeepsExistingExpiry", true, func(t *testing.T, d cache.Driver, key string) {
		_, first, err := d.IncrementWithTTL(t.Context(), key, 1, ttl)

		must(t, "IncrementWithTTL", err)
		s.sleep(ttl / 2)

		got, second, err := d.IncrementWithTTL(t.Context(), key, 1, ttl)

		wantCount(t, "IncrementWithTTL", got, err, 2)

		if second >= first {
			t.Errorf("expiry was reset: remaining %s then %s", first, second)
		}
	})
	s.sub(t, "AppliesTTLToKeyWithoutExpiry", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "10", 0)

		got, remaining, err := d.IncrementWithTTL(t.Context(), key, 1, ttl)

		wantCount(t, "IncrementWithTTL", got, err, 11)

		if remaining <= 0 || remaining > ttl {
			t.Errorf("remaining = %s, want in (0, %s]", remaining, ttl)
		}
	})
	s.sub(t, "StartsOverAfterExpiry", true, func(t *testing.T, d cache.Driver, key string) {
		_, _, err := d.IncrementWithTTL(t.Context(), key, 5, ttl)

		must(t, "IncrementWithTTL", err)
		s.sleep(ttl)

		got, _, err := d.IncrementWithTTL(t.Context(), key, 1, ttl)

		wantCount(t, "IncrementWithTTL", got, err, 1)
	})
	s.sub(t, "RejectsNonPositiveTTL", false, func(t *testing.T, d cache.Driver, key string) {
		for _, bad := range []time.Duration{0, -time.Second} {
			if _, _, err := d.IncrementWithTTL(t.Context(), key, 1, bad); !errors.Is(err, cache.ErrInvalidTTL) {
				t.Errorf("IncrementWithTTL(ttl=%s) error = %v, want ErrInvalidTTL", bad, err)
			}
		}

		wantMissing(t, d, key)
	})
}

// cancellation covers operations on an already canceled context.
func cancellation(t *testing.T, s suite) {
	s.sub(t, "OperationsFailWithContextError", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "1", 0)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		ops := map[string]func() error{
			"Get": func() error {
				_, err := d.Get(ctx, key)

				return err
			},
			"Put": func() error {
				return d.Put(ctx, key, []byte("2"), 0)
			},
			"Delete": func() error {
				return d.Delete(ctx, key)
			},
			"Has": func() error {
				_, err := d.Has(ctx, key)

				return err
			},
			"Add": func() error {
				_, err := d.Add(ctx, key+"-add", []byte("2"), 0)

				return err
			},
			"Increment": func() error {
				_, err := d.Increment(ctx, key, 1)

				return err
			},
			"Decrement": func() error {
				_, err := d.Decrement(ctx, key, 1)

				return err
			},
			"IncrementWithTTL": func() error {
				_, _, err := d.IncrementWithTTL(ctx, key, 1, ttl)

				return err
			},
			"Pull": func() error {
				_, err := d.Pull(ctx, key)

				return err
			},
			"TryLock": func() error {
				_, err := d.TryLock(ctx, key+"-lock", "a", ttl)

				return err
			},
			"Unlock": func() error {
				_, err := d.Unlock(ctx, key, "a")

				return err
			},
			"ExtendLock": func() error {
				_, err := d.ExtendLock(ctx, key, "a", ttl)

				return err
			},
			"TTL": func() error {
				_, err := d.TTL(ctx, key)

				return err
			},
		}

		for name, op := range ops {
			if err := op(); !errors.Is(err, context.Canceled) {
				t.Errorf("%s error = %v, want context.Canceled", name, err)
			}
		}

		wantValue(t, d, key, "1")

		if has, err := d.Has(t.Context(), key+"-add"); err != nil || has {
			t.Errorf("canceled Add stored a value: %t, %v", has, err)
		}

		if has, err := d.Has(t.Context(), key+"-lock"); err != nil || has {
			t.Errorf("canceled TryLock stored a lock: %t, %v", has, err)
		}
	})
}

// concurrency covers atomicity of Add, Increment, Pull and TryLock.
func concurrency(t *testing.T, s suite) {
	const workers = 20

	s.sub(t, "IncrementIsAtomic", false, func(t *testing.T, d cache.Driver, key string) {
		var wg sync.WaitGroup

		for range workers {
			wg.Go(func() {
				if _, err := d.Increment(t.Context(), key, 1); err != nil {
					t.Errorf("Increment: %v", err)
				}
			})
		}

		wg.Wait()

		wantValue(t, d, key, "20")
	})
	s.sub(t, "AddHasOneWinner", false, func(t *testing.T, d cache.Driver, key string) {
		var mu sync.Mutex
		winners := 0
		var wg sync.WaitGroup

		for range workers {
			wg.Go(func() {
				added, err := d.Add(t.Context(), key, []byte("v"), 0)

				if err != nil {
					t.Errorf("Add: %v", err)
				}

				if added {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			})
		}

		wg.Wait()

		if winners != 1 {
			t.Errorf("Add winners = %d, want 1", winners)
		}
	})
	s.sub(t, "PullHasOneWinner", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "once", 0)

		var mu sync.Mutex
		var received []string
		var wg sync.WaitGroup

		for range workers {
			wg.Go(func() {
				value, err := d.Pull(t.Context(), key)

				if errors.Is(err, cache.ErrNotFound) {
					return
				}

				if err != nil {
					t.Errorf("Pull: %v", err)

					return
				}

				mu.Lock()

				received = append(received, string(value))
				mu.Unlock()
			})
		}

		wg.Wait()

		if len(received) != 1 || received[0] != "once" {
			t.Errorf("Pull received %q, want exactly [\"once\"]", received)
		}

		wantMissing(t, d, key)
	})
	s.sub(t, "TryLockHasOneWinner", false, func(t *testing.T, d cache.Driver, key string) {
		var mu sync.Mutex
		winners := 0
		var wg sync.WaitGroup

		for i := range workers {
			wg.Go(func() {
				held, err := d.TryLock(t.Context(), key, "token-"+strconv.Itoa(i), time.Minute)

				if err != nil {
					t.Errorf("TryLock: %v", err)
				}

				if held {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			})
		}

		wg.Wait()

		if winners != 1 {
			t.Errorf("TryLock winners = %d, want 1", winners)
		}
	})
}

// pullCases covers the atomic read-and-delete.
func pullCases(t *testing.T, s suite) {
	s.sub(t, "ReturnsAndRemovesValue", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "value", ttl)

		got, err := d.Pull(t.Context(), key)

		must(t, "Pull", err)

		if string(got) != "value" {
			t.Errorf("Pull = %q, want %q", got, "value")
		}

		wantMissing(t, d, key)
	})
	s.sub(t, "MissingReturnsNotFound", false, func(t *testing.T, d cache.Driver, key string) {
		if _, err := d.Pull(t.Context(), key); !errors.Is(err, cache.ErrNotFound) {
			t.Errorf("Pull error = %v, want ErrNotFound", err)
		}
	})
	s.sub(t, "ExpiredReturnsNotFound", true, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "value", ttl)
		s.sleep(ttl)

		if _, err := d.Pull(t.Context(), key); !errors.Is(err, cache.ErrNotFound) {
			t.Errorf("Pull error = %v, want ErrNotFound", err)
		}
	})
	s.sub(t, "SecondPullReturnsNotFound", false, func(t *testing.T, d cache.Driver, key string) {
		put(t, d, key, "value", 0)

		if _, err := d.Pull(t.Context(), key); err != nil {
			t.Fatalf("first Pull: %v", err)
		}

		if _, err := d.Pull(t.Context(), key); !errors.Is(err, cache.ErrNotFound) {
			t.Errorf("second Pull error = %v, want ErrNotFound", err)
		}
	})
}

// locks covers the token-based driver lock operations.
func locks(t *testing.T, s suite) {
	s.sub(t, "AcquireWhenFree", true, func(t *testing.T, d cache.Driver, key string) {
		if !acquire(t, d, key, "a") {
			t.Fatal("TryLock on a free name = false, want true")
		}

		wantTTLWithin(t, d, key, ttl)
	})
	s.sub(t, "AcquireWhenHeldFails", true, func(t *testing.T, d cache.Driver, key string) {
		if !acquire(t, d, key, "a") {
			t.Fatal("first TryLock = false, want true")
		}

		if acquire(t, d, key, "b") {
			t.Error("second TryLock = true, want false")
		}

		if acquire(t, d, key, "a") {
			t.Error("TryLock by the holder = true, want false: locks are not reentrant")
		}

		released, err := d.Unlock(t.Context(), key, "a")

		if err != nil || !released {
			t.Errorf("Unlock by the holder = %t, %v, want true, nil", released, err)
		}
	})
	s.sub(t, "ReleaseThenAcquire", true, func(t *testing.T, d cache.Driver, key string) {
		acquire(t, d, key, "a")

		released, err := d.Unlock(t.Context(), key, "a")

		if err != nil || !released {
			t.Fatalf("Unlock = %t, %v, want true, nil", released, err)
		}

		wantMissing(t, d, key)

		if !acquire(t, d, key, "b") {
			t.Error("TryLock after release = false, want true")
		}
	})
	s.sub(t, "AcquireAfterExpiry", true, func(t *testing.T, d cache.Driver, key string) {
		acquire(t, d, key, "a")
		s.sleep(ttl)

		if !acquire(t, d, key, "b") {
			t.Error("TryLock after expiry = false, want true")
		}
	})
	s.sub(t, "ReleaseWithWrongTokenDoesNothing", true, func(t *testing.T, d cache.Driver, key string) {
		acquire(t, d, key, "a")

		released, err := d.Unlock(t.Context(), key, "b")

		if err != nil || released {
			t.Errorf("Unlock with wrong token = %t, %v, want false, nil", released, err)
		}

		wantLockHeld(t, d, key)
	})
	s.sub(t, "ReleaseMissingReportsFalse", false, func(t *testing.T, d cache.Driver, key string) {
		released, err := d.Unlock(t.Context(), key, "a")

		if err != nil || released {
			t.Errorf("Unlock on a free name = %t, %v, want false, nil", released, err)
		}
	})
	s.sub(t, "ReleaseAfterExpiryKeepsNewHolder", true, func(t *testing.T, d cache.Driver, key string) {
		acquire(t, d, key, "a")
		s.sleep(ttl)
		acquire(t, d, key, "b")

		released, err := d.Unlock(t.Context(), key, "a")

		if err != nil || released {
			t.Errorf("Unlock by the expired holder = %t, %v, want false, nil", released, err)
		}

		wantLockHeld(t, d, key)
	})
	s.sub(t, "ExtendWithWrongTokenDoesNothing", true, func(t *testing.T, d cache.Driver, key string) {
		acquire(t, d, key, "a")
		s.sleep(ttl / 2)

		extended, err := d.ExtendLock(t.Context(), key, "b", 10*ttl)

		if err != nil || extended {
			t.Errorf("ExtendLock with wrong token = %t, %v, want false, nil", extended, err)
		}

		wantTTLWithin(t, d, key, ttl)
		s.sleep(ttl)

		if !acquire(t, d, key, "c") {
			t.Error("lock outlived its ttl after a foreign ExtendLock")
		}
	})
	s.sub(t, "ExtendKeepsLock", true, func(t *testing.T, d cache.Driver, key string) {
		acquire(t, d, key, "a")
		s.sleep(ttl / 2)

		extended, err := d.ExtendLock(t.Context(), key, "a", ttl)

		if err != nil || !extended {
			t.Fatalf("ExtendLock = %t, %v, want true, nil", extended, err)
		}

		// Past the original expiry, inside the extended one.
		s.sleep(ttl / 2)

		wantLockHeld(t, d, key)
		s.sleep(ttl)

		if !acquire(t, d, key, "b") {
			t.Error("lock did not expire after being extended")
		}
	})
	s.sub(t, "ExtendAfterExpiryFails", true, func(t *testing.T, d cache.Driver, key string) {
		acquire(t, d, key, "a")
		s.sleep(ttl)

		extended, err := d.ExtendLock(t.Context(), key, "a", ttl)

		if err != nil || extended {
			t.Errorf("ExtendLock after expiry = %t, %v, want false, nil", extended, err)
		}

		wantMissing(t, d, key)
	})
	s.sub(t, "NonPositiveTTLIsRejected", false, func(t *testing.T, d cache.Driver, key string) {
		for _, bad := range []time.Duration{0, -time.Second} {
			held, err := d.TryLock(t.Context(), key, "a", bad)

			if !errors.Is(err, cache.ErrInvalidTTL) || held {
				t.Errorf("TryLock(ttl=%s) = %t, %v, want false, ErrInvalidTTL", bad, held, err)
			}
		}

		wantMissing(t, d, key)

		acquire(t, d, key, "a")

		for _, bad := range []time.Duration{0, -time.Second} {
			extended, err := d.ExtendLock(t.Context(), key, "a", bad)

			if !errors.Is(err, cache.ErrInvalidTTL) || extended {
				t.Errorf("ExtendLock(ttl=%s) = %t, %v, want false, ErrInvalidTTL", bad, extended, err)
			}
		}

		wantTTLWithin(t, d, key, ttl)
	})
}

// leaseFacade covers the [cache.Lease] behavior that depends on the driver:
// key prefixing, waiting, extending and losing a lock.
func leaseFacade(t *testing.T, s suite) {
	s.sub(t, "PutOnLockNameDoesNotTouchLock", true, func(t *testing.T, d cache.Driver, name string) {
		store := cache.New(d)

		lease, held, err := store.TryLock(t.Context(), name, ttl)

		if err != nil || !held {
			t.Fatalf("TryLock = %t, %v, want true, nil", held, err)
		}

		put(t, d, name, "entry", 0)

		if _, held, _ := store.TryLock(t.Context(), name, ttl); held {
			t.Error("a Put on the lock name released the lock")
		}

		must(t, "Unlock", lease.Unlock(t.Context()))
		wantValue(t, d, name, "entry")
	})
	s.sub(t, "TryLockReportsHeldWithoutError", true, func(t *testing.T, d cache.Driver, name string) {
		store := cache.New(d)

		first, held, err := store.TryLock(t.Context(), name, ttl)

		if err != nil || !held {
			t.Fatalf("first TryLock = %t, %v, want true, nil", held, err)
		}

		second, held, err := store.TryLock(t.Context(), name, ttl)

		if err != nil || held || second != nil {
			t.Errorf("second TryLock = %v, %t, %v, want nil, false, nil", second, held, err)
		}

		must(t, "Unlock", first.Unlock(t.Context()))

		if _, held, err := store.TryLock(t.Context(), name, ttl); err != nil || !held {
			t.Errorf("TryLock after Unlock = %t, %v, want true, nil", held, err)
		}
	})
	s.sub(t, "LockWaitsForRelease", true, func(t *testing.T, d cache.Driver, name string) {
		store := cache.New(d)

		first, err := store.Lock(t.Context(), name, ttl)

		must(t, "Lock", err)

		acquired := make(chan *cache.Lease, 1)

		go func() {
			lease, err := store.Lock(t.Context(), name, ttl)

			if err != nil {
				t.Errorf("waiting Lock: %v", err)
			}

			acquired <- lease
		}()

		s.sleep(ttl / 4)

		select {
		case <-acquired:
			t.Fatal("Lock returned while the lock was held")
		default:
		}

		must(t, "Unlock", first.Unlock(t.Context()))

		if lease := <-acquired; lease == nil {
			t.Fatal("waiting Lock returned no lease")
		}
	})
	s.sub(t, "LockStopsWhenContextEnds", true, func(t *testing.T, d cache.Driver, name string) {
		store := cache.New(d)

		if _, err := store.Lock(t.Context(), name, time.Minute); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), ttl/4)
		defer cancel()

		if _, err := store.Lock(ctx, name, ttl); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Lock error = %v, want DeadlineExceeded", err)
		}
	})
	s.sub(t, "LockOnCanceledContextFails", false, func(t *testing.T, d cache.Driver, name string) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		store := cache.New(d)

		if _, err := store.Lock(ctx, name, ttl); !errors.Is(err, context.Canceled) {
			t.Errorf("Lock error = %v, want Canceled", err)
		}

		if _, held, err := store.TryLock(ctx, name, ttl); !errors.Is(err, context.Canceled) || held {
			t.Errorf("TryLock = %t, %v, want false, Canceled", held, err)
		}

		if _, held, err := store.TryLock(t.Context(), name, ttl); err != nil || !held {
			t.Errorf("TryLock after canceled attempts = %t, %v, want true, nil", held, err)
		}
	})
	s.sub(t, "InvalidArgumentsAreRejected", false, func(t *testing.T, d cache.Driver, name string) {
		store := cache.New(d)

		if _, err := store.Lock(t.Context(), "", ttl); !errors.Is(err, cache.ErrEmptyLockName) {
			t.Errorf("Lock(empty name) error = %v, want ErrEmptyLockName", err)
		}

		if _, _, err := store.TryLock(t.Context(), "", ttl); !errors.Is(err, cache.ErrEmptyLockName) {
			t.Errorf("TryLock(empty name) error = %v, want ErrEmptyLockName", err)
		}

		for _, bad := range []time.Duration{0, -time.Second} {
			if _, err := store.Lock(t.Context(), name, bad); !errors.Is(err, cache.ErrInvalidTTL) {
				t.Errorf("Lock(ttl=%s) error = %v, want ErrInvalidTTL", bad, err)
			}

			if _, _, err := store.TryLock(t.Context(), name, bad); !errors.Is(err, cache.ErrInvalidTTL) {
				t.Errorf("TryLock(ttl=%s) error = %v, want ErrInvalidTTL", bad, err)
			}
		}
	})
	s.sub(t, "UnlockIsIdempotent", true, func(t *testing.T, d cache.Driver, name string) {
		store := cache.New(d)

		lease, _, err := store.TryLock(t.Context(), name, ttl)

		must(t, "TryLock", err)
		must(t, "first Unlock", lease.Unlock(t.Context()))

		other, held, err := store.TryLock(t.Context(), name, ttl)

		if err != nil || !held {
			t.Fatalf("TryLock after Unlock = %t, %v, want true, nil", held, err)
		}

		must(t, "second Unlock", lease.Unlock(t.Context()))

		if _, held, _ := store.TryLock(t.Context(), name, ttl); held {
			t.Error("a repeated Unlock released another holder's lock")
		}

		must(t, "other Unlock", other.Unlock(t.Context()))
	})
	s.sub(t, "UnlockAfterLossReportsErrLockLost", true, func(t *testing.T, d cache.Driver, name string) {
		store := cache.New(d)

		lease, _, err := store.TryLock(t.Context(), name, ttl)

		must(t, "TryLock", err)
		s.sleep(ttl)

		other, held, err := store.TryLock(t.Context(), name, ttl)

		if err != nil || !held {
			t.Fatalf("TryLock after expiry = %t, %v, want true, nil", held, err)
		}

		if err := lease.Unlock(t.Context()); !errors.Is(err, cache.ErrLockLost) {
			t.Errorf("Unlock error = %v, want ErrLockLost", err)
		}

		if err := lease.Unlock(t.Context()); err != nil {
			t.Errorf("second Unlock error = %v, want nil", err)
		}

		if _, held, _ := store.TryLock(t.Context(), name, ttl); held {
			t.Error("the expired lease's Unlock released the new holder's lock")
		}

		must(t, "other Unlock", other.Unlock(t.Context()))
	})
	s.sub(t, "ExtendKeepsLeaseAndFailsAfterLoss", true, func(t *testing.T, d cache.Driver, name string) {
		store := cache.New(d)

		lease, _, err := store.TryLock(t.Context(), name, ttl)

		must(t, "TryLock", err)

		s.sleep(ttl / 2)

		must(t, "Extend", lease.Extend(t.Context(), ttl))
		s.sleep(ttl / 2)

		if _, held, _ := store.TryLock(t.Context(), name, ttl); held {
			t.Error("lock expired although it was extended")
		}

		if err := lease.Extend(t.Context(), 0); !errors.Is(err, cache.ErrInvalidTTL) {
			t.Errorf("Extend(0) error = %v, want ErrInvalidTTL", err)
		}

		s.sleep(2 * ttl)

		if err := lease.Extend(t.Context(), ttl); !errors.Is(err, cache.ErrLockLost) {
			t.Errorf("Extend after expiry error = %v, want ErrLockLost", err)
		}
	})
	s.sub(t, "ExtendAfterUnlockFails", false, func(t *testing.T, d cache.Driver, name string) {
		store := cache.New(d)

		lease, _, err := store.TryLock(t.Context(), name, ttl)

		must(t, "TryLock", err)
		must(t, "Unlock", lease.Unlock(t.Context()))

		if err := lease.Extend(t.Context(), ttl); !errors.Is(err, cache.ErrLockLost) {
			t.Errorf("Extend after Unlock error = %v, want ErrLockLost", err)
		}
	})
}
