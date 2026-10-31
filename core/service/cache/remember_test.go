package cache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/telemetry"
)

// TestCacheRemember verifies missing values are computed and stored.
func TestCacheRemember(t *testing.T) {
	t.Parallel()

	wrapped := cache.New(memorycache.New())

	result, err := wrapped.Remember(context.Background(), cache.NewKey[string]("key"), func() (string, error) {
		return "computed", nil
	})

	if err != nil || result != "computed" {
		t.Fatalf("result = %q, err = %v", result, err)
	}
}

// TestCacheRememberRecomputesUndecodableValue verifies corrupt cached bytes
// are replaced with the newly computed typed value.
func TestCacheRememberRecomputesUndecodableValue(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()

	if err := driver.Put(context.Background(), "key", []byte(`"text"`), time.Minute); err != nil {
		t.Fatal(err)
	}

	wrapped := cache.New(driver)

	result, err := wrapped.Remember(context.Background(), cache.NewKey[int]("key"), func() (int, error) {
		return 7, nil
	})

	stored, getErr := driver.Get(context.Background(), "key")

	if err != nil || getErr != nil || result != 7 || string(stored) != "7" {
		t.Fatalf("result = %d, stored = %q, err = %v", result, stored, err)
	}
}

// TestCacheRememberReturnsValueWhenPutFails verifies a failed cache write
// preserves both the computed result and the driver's error.
func TestCacheRememberReturnsValueWhenPutFails(t *testing.T) {
	t.Parallel()

	putErr := errors.New("cache down")
	wrapped := cache.New(putFails{Driver: memorycache.New(), err: putErr})

	result, err := wrapped.Remember(context.Background(), cache.NewKey[string]("key"), func() (string, error) {
		return "computed", nil
	})

	if result != "computed" || !errors.Is(err, putErr) {
		t.Fatalf("result = %q, err = %v", result, err)
	}
}

// waiters is the number of concurrent callers in the concurrency tests.
const waiters = 20

// TestRememberComputesOnceForConcurrentMisses verifies concurrent misses on
// one key run compute once and all receive the stored value.
func TestRememberComputesOnceForConcurrentMisses(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := cache.New(memorycache.New())
		var computed atomic.Int32
		var wg sync.WaitGroup

		for range waiters {
			wg.Go(func() {
				key := cache.NewKey[string]("key", func(entry *cache.KeyBuilder) {
					entry.TTL(time.Minute)
				})

				value, err := store.Remember(t.Context(), key, func() (string, error) {
					computed.Add(1)
					time.Sleep(time.Second)

					return "computed", nil
				})

				if err != nil || value != "computed" {
					t.Errorf("Remember = %q, %v, want computed, nil", value, err)
				}
			})
		}

		wg.Wait()

		if got := computed.Load(); got != 1 {
			t.Errorf("compute ran %d times, want 1", got)
		}
	})
}

// TestRememberCoordinatesFinalName verifies distinct declaration paths with
// the same final identity coordinate one computation, including no-expiry keys.
func TestRememberCoordinatesFinalName(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := cache.New(memorycache.New())
		var computed atomic.Int32
		var wg sync.WaitGroup

		for index := range waiters {
			key := cache.NewNamespace[int]("app").Key("key")

			if index%2 == 0 {
				key = cache.NewKey[int]("app:key")
			}

			wg.Go(func() {
				_, err := store.Remember(t.Context(), key, func() (int, error) {
					computed.Add(1)
					time.Sleep(time.Second)

					return 1, nil
				})

				if err != nil {
					t.Errorf("Remember: %v", err)
				}
			})
		}

		wg.Wait()

		if got := computed.Load(); got != 1 {
			t.Errorf("compute ran %d times, want 1", got)
		}
	})
}

// TestRememberHitDoesNotWaitForLock verifies a hit never touches the lock.
func TestRememberHitDoesNotWaitForLock(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()
	store := cache.New(driver)

	if err := store.Put(t.Context(), cache.NewKey[string]("key"), "stored"); err != nil {
		t.Fatal(err)
	}

	if _, held, err := store.TryLock(t.Context(), "remember:key", time.Minute); err != nil || !held {
		t.Fatalf("TryLock = %t, %v, want true, nil", held, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	value, err := store.Remember(ctx, cache.NewKey[string]("key"), func() (string, error) {
		t.Error("compute ran on a hit")

		return "", nil
	})

	if err != nil || value != "stored" {
		t.Fatalf("Remember = %q, %v, want stored, nil", value, err)
	}
}

// TestRememberReleasesLockWhenComputeFails verifies a failed compute does not
// block the next caller until the lease expires.
func TestRememberReleasesLockWhenComputeFails(t *testing.T) {
	t.Parallel()

	store := cache.New(memorycache.New())
	failure := errors.New("compute failed")

	if _, err := store.Remember(t.Context(), cache.NewKey[string]("key"), func() (string, error) {
		return "", failure
	}); !errors.Is(err, failure) {
		t.Fatalf("Remember error = %v, want %v", err, failure)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	value, err := store.Remember(ctx, cache.NewKey[string]("key"), func() (string, error) {
		return "ok", nil
	})

	if err != nil || value != "ok" {
		t.Fatalf("Remember = %q, %v, want ok, nil", value, err)
	}
}

// TestRememberMissStopsWaitingWhenContextEnds verifies a caller waiting on
// another computation gives up with its context.
func TestRememberMissStopsWaitingWhenContextEnds(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := cache.New(memorycache.New())

		if _, held, err := store.TryLock(t.Context(), "remember:key", time.Minute); err != nil || !held {
			t.Fatalf("TryLock = %t, %v, want true, nil", held, err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, err := store.Remember(ctx, cache.NewKey[string]("key"), func() (string, error) {
			t.Error("compute ran without the lock")

			return "", nil
		})

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Remember error = %v, want DeadlineExceeded", err)
		}
	})
}

// TestStateRememberCleanupHasFiniteBudget checks detached lease cleanup does
// not turn a bounded public operation into unbounded background-context I/O.
func TestStateRememberCleanupHasFiniteBudget(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := &stateReleaseDriver{Driver: memorycache.New()}
		store := cache.New(driver)
		ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), stateTraceKey{}, "retained-trace"), time.Second)
		t.Cleanup(cancel)

		key := cache.NewKey[int]("computed")

		value, err := store.Remember(ctx, key, func() (int, error) {
			return 7, nil
		})

		if err != nil || value != 7 {
			t.Fatalf("Remember = %d, %v, want 7, nil", value, err)
		}

		if driver.budget <= 0 || driver.budget > 10*time.Second {
			t.Fatalf("release budget = %s, want (0, 10s]", driver.budget)
		}

		if driver.trace != "retained-trace" {
			t.Fatalf("cleanup lost request context values: %v", driver.trace)
		}

		stored, err := store.Get(t.Context(), key)

		if err != nil || stored != 7 {
			t.Fatalf("cleanup changed stored result = %d, %v", stored, err)
		}
	})
}

// TestInvalidRememberKeySkipsCompute verifies even nil-driver facades reject
// invalid metadata within the single outer operation before invoking a loader.
func TestInvalidRememberKeySkipsCompute(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, nil)
	key := cache.NewNamespace[string]("").Key("id")

	_, err := store.Remember(t.Context(), key, func() (string, error) {
		t.Fatal("invalid key invoked compute")

		return "", nil
	})

	if !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("Remember = %v", err)
	}

	fake.AssertSpanCount(t, 1)
	fake.AssertSpan(t, "cache remember", telemetry.String("error.type", "cache.ErrInvalidKey"))
}

// TestRememberNegativeTTLRejectsExistingHit protects against returning an
// already cached value before validating the completed definition's write policy.
func TestRememberNegativeTTLRejectsExistingHit(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()
	key := cache.NewKey[string]("existing")

	if err := cache.New(driver).Put(t.Context(), key, "cached"); err != nil {
		t.Fatal(err)
	}

	invalid := key.Derive(func(entry *cache.KeyBuilder) {
		entry.TTL(-time.Second)
	})

	store, fake := newTraced(t, rememberReadForbidden{Driver: driver, t: t})

	value, err := store.Remember(t.Context(), invalid, func() (string, error) {
		t.Fatal("negative-TTL hit invoked compute")

		return "computed", nil
	})

	if !errors.Is(err, cache.ErrInvalidTTL) || value != "" {
		t.Fatalf("Remember = %q, %v, want zero value and ErrInvalidTTL", value, err)
	}

	fake.AssertSpanCount(t, 1)
	fake.AssertSpan(t, "cache remember", telemetry.String("error.type", "cache.ErrInvalidTTL"))
}

// TestRememberNegativeTTLRejectsMiss protects against invoking a loader before
// discovering that its result cannot be written with the requested lifetime.
func TestRememberNegativeTTLRejectsMiss(t *testing.T) {
	t.Parallel()

	key := cache.NewKey[string]("absent", func(entry *cache.KeyBuilder) {
		entry.TTL(-time.Second)
	})

	store, fake := newTraced(t, rememberReadForbidden{Driver: memorycache.New(), t: t})

	value, err := store.Remember(t.Context(), key, func() (string, error) {
		t.Fatal("negative-TTL miss invoked compute")

		return "computed", nil
	})

	if !errors.Is(err, cache.ErrInvalidTTL) || value != "" {
		t.Fatalf("Remember = %q, %v, want zero value and ErrInvalidTTL", value, err)
	}

	fake.AssertSpanCount(t, 1)
	fake.AssertSpan(t, "cache remember", telemetry.String("error.type", "cache.ErrInvalidTTL"))
}

// TestAddAndRememberUseInheritedTTL verifies both conditional JSON writes use
// completed key policy rather than supplying independent call-site lifetimes.
func TestAddAndRememberUseInheritedTTL(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := cache.New(memorycache.New())
		space := cache.NewNamespace[string]("app", func(namespace *cache.NamespaceBuilder) {
			namespace.TTL(time.Minute)
		})

		added, err := store.Add(t.Context(), space.Key("added"), "value")

		if err != nil || !added {
			t.Fatalf("Add = %v, %v", added, err)
		}

		_, err = store.Remember(t.Context(), space.Key("remembered"), func() (string, error) {
			return "value", nil
		})

		if err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Minute)

		if _, err := store.Get(t.Context(), space.Key("added")); !errors.Is(err, cache.ErrNotFound) {
			t.Fatalf("Add lifetime ignored: %v", err)
		}

		if _, err := store.Get(t.Context(), space.Key("remembered")); !errors.Is(err, cache.ErrNotFound) {
			t.Fatalf("Remember lifetime ignored: %v", err)
		}
	})
}
