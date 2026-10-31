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

// putFails is a driver whose Put always fails.
type putFails struct {
	// Driver supplies the operations not overridden by the failing write.
	cache.Driver

	// err is the failure returned by the test driver.
	err error
}

// Put returns the configured write failure without storing a value.
func (driver putFails) Put(context.Context, string, []byte, time.Duration) error {
	return driver.err
}

// TestErrors verifies the cache error messages.
func TestErrors(t *testing.T) {
	t.Parallel()

	if cache.ErrNotFound.Error() != "cache key not found" || cache.ErrUnsupported.Error() != "cache unsupported operation" {
		t.Fatal("cache error messages changed")
	}
}

// TestNamespaceDefaultDoesNotAliasLegacyEntries verifies colon identities never
// read or rewrite an existing dotted entry. Standalone keys keep exact bytes.
func TestNamespaceDefaultDoesNotAliasLegacyEntries(t *testing.T) {
	t.Parallel()

	store := cache.New(memorycache.New())
	legacy := cache.NewKey[string]("app.v1.entry")

	if err := store.Put(t.Context(), legacy, "legacy"); err != nil {
		t.Fatal(err)
	}

	key := cache.NewNamespace[string]("app.v1").Key("entry")

	if _, err := store.Get(t.Context(), key); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("default namespace read legacy entry: %v", err)
	}

	if err := store.Put(t.Context(), key, "current"); err != nil {
		t.Fatal(err)
	}

	value, err := store.Get(t.Context(), legacy)

	if err != nil || value != "legacy" {
		t.Fatalf("legacy entry changed: %q, %v", value, err)
	}
}

// TestCacheGetPutAndPull verifies typed cache retrieval, storage, and removal.
func TestCacheGetPutAndPull(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	value := map[string]string{"hello": "world"}
	key := cache.NewKey[map[string]string]("key", func(entry *cache.KeyBuilder) {
		entry.TTL(time.Minute)
	})

	wrapped := cache.New(memorycache.New())

	if err := wrapped.Put(ctx, key, value); err != nil {
		t.Fatal(err)
	}

	result, err := wrapped.Get(ctx, key)

	if err != nil || !equalStringMaps(value, result) {
		t.Fatalf("result = %#v, err = %v", result, err)
	}

	result, err = wrapped.Pull(ctx, key)

	if err != nil || !equalStringMaps(value, result) {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

// equalStringMaps compares the keys and values returned by typed cache reads.
func equalStringMaps(first, second map[string]string) bool {
	if len(first) != len(second) {
		return false
	}

	for key, value := range first {
		if second[key] != value {
			return false
		}
	}

	return true
}

// TestPullHasOneWinner verifies concurrent Pull calls hand the value to
// exactly one caller.
func TestPullHasOneWinner(t *testing.T) {
	t.Parallel()

	store := cache.New(memorycache.New())

	if err := store.Put(t.Context(), cache.NewKey[string]("token"), "once"); err != nil {
		t.Fatal(err)
	}

	var winners atomic.Int32
	var wg sync.WaitGroup

	for range waiters {
		wg.Go(func() {
			value, err := store.Pull(t.Context(), cache.NewKey[string]("token"))

			switch {
			case err == nil && value == "once":
				winners.Add(1)
			case !errors.Is(err, cache.ErrNotFound):
				t.Errorf("Pull = %q, %v", value, err)
			}
		})
	}

	wg.Wait()

	if got := winners.Load(); got != 1 {
		t.Errorf("Pull winners = %d, want 1", got)
	}
}

// stateReleaseDriver models a slow, context-aware backend release and records
// its public operation deadline. Missing deadlines fail promptly instead of
// hanging the regression test indefinitely.
type stateReleaseDriver struct {
	// Driver supplies real cache storage, atomic writes, and expiring leases.
	cache.Driver

	// budget is the independent deadline supplied to the release operation.
	budget time.Duration

	// trace is an application context value retained across detached cleanup.
	trace any
}

// stateTraceKey models a caller-owned trace slot at the exported driver seam.
// The cleanup deadline must change cancellation, not erase request values.
type stateTraceKey struct{}

// Unlock waits for the provided deadline, simulating a failed network cleanup
// while leaving the real lock to expire according to the ordinary lease policy.
func (driver *stateReleaseDriver) Unlock(ctx context.Context, _, _ string) (bool, error) {
	driver.trace = ctx.Value(stateTraceKey{})
	deadline, ok := ctx.Deadline()

	if !ok {
		return false, errors.New("release has no deadline")
	}

	driver.budget = time.Until(deadline)
	<-ctx.Done()

	return false, ctx.Err()
}

// TestStateCacheCodecDetachesBorrowedValues checks neither nested input slices
// nor returned decoded values can mutate persisted data after a facade call.
func TestStateCacheCodecDetachesBorrowedValues(t *testing.T) {
	t.Parallel()

	store := cache.New(memorycache.New())
	key := cache.NewKey[map[string][]int]("profile")
	input := map[string][]int{"counts": {1}}

	if err := store.Put(t.Context(), key, input); err != nil {
		t.Fatal(err)
	}

	input["counts"][0] = 2

	first, err := store.Get(t.Context(), key)

	if err != nil || first["counts"][0] != 1 {
		t.Fatalf("input mutation changed cache = %v, %v", first, err)
	}

	first["counts"][0] = 3

	second, err := store.Get(t.Context(), key)

	if err != nil || second["counts"][0] != 1 {
		t.Fatalf("output mutation changed cache = %v, %v", second, err)
	}
}

// TestInvalidJSONKeysFailBeforeCodecOrDriver uses an unencodable value and a
// nil driver to prove declaration validation precedes either boundary.
func TestInvalidJSONKeysFailBeforeCodecOrDriver(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, nil)
	var key cache.Key[chan int]
	value := make(chan int)

	if err := store.Put(t.Context(), key, value); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("Put = %v", err)
	}

	if _, err := store.Add(t.Context(), key, value); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("Add = %v", err)
	}

	if _, err := store.Get(t.Context(), key); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("Get = %v", err)
	}

	if _, err := store.Pull(t.Context(), key); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("Pull = %v", err)
	}

	fake.AssertSpanCount(t, 4)
	fake.AssertSpan(t, "cache put", telemetry.String("error.type", "cache.ErrInvalidKey"))
}

// rememberReadForbidden wraps seeded or empty storage to reject any attempt to
// read through the facade before a negative write lifetime has been validated.
type rememberReadForbidden struct {
	// Driver retains the underlying store without changing its raw semantics.
	cache.Driver

	// t reports unexpected I/O at its source rather than masking it with errors.
	t *testing.T
}

// Get fails the test if Remember reaches storage with an invalid definition.
// Validation must occur before both the initial hit lookup and miss processing.
func (driver rememberReadForbidden) Get(context.Context, string) ([]byte, error) {
	driver.t.Fatal("negative-TTL Remember reached driver.Get")

	return nil, nil
}

// TestInvalidMetadataKeysSkipDriver verifies existence, deletion, and expiry
// inspect typed declarations before calling a raw driver.
func TestInvalidMetadataKeysSkipDriver(t *testing.T) {
	t.Parallel()

	store := cache.New(nil)
	var key cache.Key[string]

	if _, err := store.Has(t.Context(), key); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("Has = %v", err)
	}

	if err := store.Delete(t.Context(), key); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("Delete = %v", err)
	}

	if _, err := store.TTL(t.Context(), key); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("TTL = %v", err)
	}
}

// TestInvalidCounterKeysSkipDriver verifies counters require valid int64 keys
// even though their expiry semantics are independent of key write policy.
func TestInvalidCounterKeysSkipDriver(t *testing.T) {
	t.Parallel()

	store := cache.New(nil)
	var key cache.Key[int64]

	if _, err := store.Increment(t.Context(), key, 1); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("Increment = %v", err)
	}

	if _, err := store.Decrement(t.Context(), key, 1); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("Decrement = %v", err)
	}

	if _, _, err := store.IncrementWithTTL(t.Context(), key, 1, time.Minute); !errors.Is(err, cache.ErrInvalidKey) {
		t.Fatalf("IncrementWithTTL = %v", err)
	}
}

// TestCanceledContextPrecedesKeyAndCodec verifies canceled requests never reach
// invalid metadata, encoding, computation, or I/O, but still emit one span each.
func TestCanceledContextPrecedesKeyAndCodec(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var key cache.Key[chan int]

	if err := store.Put(ctx, key, make(chan int)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put = %v", err)
	}

	if _, err := store.Get(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get = %v", err)
	}

	if _, err := store.Remember(ctx, key, func() (chan int, error) {
		t.Error("canceled request invoked loader")

		return nil, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Remember = %v", err)
	}

	fake.AssertSpanCount(t, 3)
}

// TestTypedWriteLifetimeAndStandaloneKey verifies typed writes preserve the
// empty identity, use key policy, and allow Derive to remove expiry on overwrite.
func TestTypedWriteLifetimeAndStandaloneKey(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := cache.New(memorycache.New())
		key := cache.NewKey[string]("", func(entry *cache.KeyBuilder) {
			entry.TTL(time.Minute)
		})

		if err := store.Put(t.Context(), key, "first"); err != nil {
			t.Fatal(err)
		}

		remaining, err := store.TTL(t.Context(), key)

		if err != nil || remaining != time.Minute {
			t.Fatalf("TTL = %v, %v", remaining, err)
		}

		permanent := key.Derive(func(entry *cache.KeyBuilder) {
			entry.TTL(0)
		})

		if err := store.Put(t.Context(), permanent, "second"); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Hour)

		value, err := store.Get(t.Context(), key)

		if err != nil || value != "second" {
			t.Fatalf("Get = %q, %v", value, err)
		}
	})
}

// TestPullDecodeFailureStillDestroysEntry verifies destructive decode remains
// intentional and both Get and Pull report read hits when stored bytes exist.
func TestPullDecodeFailureStillDestroysEntry(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()

	if err := driver.Put(t.Context(), "key", []byte(`"not an int"`), 0); err != nil {
		t.Fatal(err)
	}

	store, fake := newTraced(t, driver)
	key := cache.NewKey[int]("key")

	if _, err := store.Get(t.Context(), key); err == nil {
		t.Fatal("Get decoded the wrong type")
	}

	if _, err := store.Pull(t.Context(), key); err == nil {
		t.Fatal("Pull decoded the wrong type")
	}

	if _, err := driver.Get(t.Context(), "key"); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("entry not removed: %v", err)
	}

	fake.AssertSpan(t, "cache get", telemetry.Bool("arkoren.cache.hit", true))
	fake.AssertSpan(t, "cache pull", telemetry.Bool("arkoren.cache.hit", true))
}

// hasFails models a failed existence read without introducing a fake facade.
type hasFails struct {
	// Driver supplies operations not exercised by this failure test.
	cache.Driver
}

// Has fails without revealing whether the entry exists.
func (driver hasFails) Has(context.Context, string) (bool, error) {
	return false, errors.New("backend unavailable")
}

// TestHasFailureHasUnknownHit guards against reporting backend failures as
// cache misses, which would incorrectly inflate miss telemetry.
func TestHasFailureHasUnknownHit(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, hasFails{})

	if _, err := store.Has(t.Context(), cache.NewKey[string]("key")); err == nil {
		t.Fatal("missing driver error")
	}

	span, found := fake.Span("cache has")

	if !found {
		t.Fatal("missing Has span")
	}

	if _, found := span.Attr("arkoren.cache.hit"); found {
		t.Fatal("failed Has reported a hit state")
	}
}

// TestCountersKeepDistinctExpiryRules verifies a definition TTL does not leak
// into counters, whose explicit initialization policy remains separate.
func TestCountersKeepDistinctExpiryRules(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := cache.New(memorycache.New())
		key := cache.NewKey[int64]("counter", func(entry *cache.KeyBuilder) {
			entry.TTL(time.Hour)
		})

		if _, err := store.Increment(t.Context(), key, 1); err != nil {
			t.Fatal(err)
		}

		remaining, err := store.TTL(t.Context(), key)

		if err != nil || remaining != 0 {
			t.Fatalf("Increment adopted key TTL: %v, %v", remaining, err)
		}

		_, remaining, err = store.IncrementWithTTL(t.Context(), key, 1, time.Minute)

		if err != nil || remaining != time.Minute {
			t.Fatalf("explicit TTL ignored: %v, %v", remaining, err)
		}
	})
}

// TestCanceledMetadataOperationsSkipDriver gives cancellation precedence over
// zero-key errors for every non-value operation, with no driver available.
func TestCanceledMetadataOperationsSkipDriver(t *testing.T) {
	t.Parallel()

	store := cache.New(nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var key cache.Key[int64]

	if _, err := store.Has(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("Has = %v", err)
	}

	if err := store.Delete(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete = %v", err)
	}

	if _, err := store.TTL(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("TTL = %v", err)
	}

	if _, err := store.Increment(ctx, key, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Increment = %v", err)
	}

	if _, err := store.Decrement(ctx, key, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Decrement = %v", err)
	}

	if _, _, err := store.IncrementWithTTL(ctx, key, 1, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("IncrementWithTTL = %v", err)
	}
}
