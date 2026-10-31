package memorycache_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/cachetest"
	"arkoren.dev/core/service/cache/memorycache"
)

// TestMemoryGetRejectsCanceledContext verifies Get checks cancellation first.
func TestMemoryGetRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := memorycache.New().Get(ctx, "key")

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Get error = %v, want %v", err, context.Canceled)
	}
}

// TestMemoryPutRejectsCanceledContext verifies canceled writes leave values unchanged.
func TestMemoryPutRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()

	if err := driver.Put(t.Context(), "key", []byte("old"), time.Minute); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := driver.Put(ctx, "key", []byte("new"), time.Minute)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Put error = %v, want %v", err, context.Canceled)
	}

	value, err := driver.Get(t.Context(), "key")

	if err != nil {
		t.Fatal(err)
	}

	if string(value) != "old" {
		t.Errorf("Get = %q, want %q", value, "old")
	}
}

// TestMemoryDeleteRejectsCanceledContext verifies canceled deletion leaves values unchanged.
func TestMemoryDeleteRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()

	if err := driver.Put(t.Context(), "key", []byte("value"), time.Minute); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := driver.Delete(ctx, "key")

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Delete error = %v, want %v", err, context.Canceled)
	}

	value, err := driver.Get(t.Context(), "key")

	if err != nil {
		t.Fatal(err)
	}

	if string(value) != "value" {
		t.Errorf("Get = %q, want %q", value, "value")
	}
}

// TestMemoryHasRejectsCanceledContext verifies Has checks cancellation first.
func TestMemoryHasRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	found, err := memorycache.New().Has(ctx, "key")

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Has error = %v, want %v", err, context.Canceled)
	}

	if found {
		t.Error("Has = true, want false")
	}
}

// TestMemoryAddRejectsCanceledContext verifies canceled Add does not create a value.
func TestMemoryAddRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	added, err := driver.Add(ctx, "key", []byte("value"), time.Minute)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Add error = %v, want %v", err, context.Canceled)
	}

	if added {
		t.Error("Add = true, want false")
	}

	found, err := driver.Has(t.Context(), "key")

	if err != nil {
		t.Fatal(err)
	}

	if found {
		t.Error("Has = true, want false")
	}
}

// TestMemoryTTLRejectsCanceledContext verifies TTL checks cancellation first.
func TestMemoryTTLRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	ttl, err := memorycache.New().TTL(ctx, "key")

	if !errors.Is(err, context.Canceled) {
		t.Errorf("TTL error = %v, want %v", err, context.Canceled)
	}

	if ttl != 0 {
		t.Errorf("TTL = %s, want zero", ttl)
	}
}

// TestMemoryPutCopiesInput verifies Put does not retain the caller's byte slice.
func TestMemoryPutCopiesInput(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()
	value := []byte("value")

	if err := driver.Put(t.Context(), "key", value, time.Minute); err != nil {
		t.Fatal(err)
	}

	value[0] = 'x'

	stored, err := driver.Get(t.Context(), "key")

	if err != nil {
		t.Fatal(err)
	}

	if string(stored) != "value" {
		t.Errorf("Get = %q, want %q", stored, "value")
	}
}

// TestMemoryAddCopiesInput verifies successful Add does not retain caller bytes.
func TestMemoryAddCopiesInput(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()
	value := []byte("value")

	added, err := driver.Add(t.Context(), "key", value, time.Minute)

	if err != nil {
		t.Fatal(err)
	}

	if !added {
		t.Fatal("Add = false, want true")
	}

	value[0] = 'x'

	stored, err := driver.Get(t.Context(), "key")

	if err != nil {
		t.Fatal(err)
	}

	if string(stored) != "value" {
		t.Errorf("Get = %q, want %q", stored, "value")
	}
}

// TestMemoryGetCopiesOutput verifies callers cannot mutate cached bytes through Get.
func TestMemoryGetCopiesOutput(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()

	if err := driver.Put(t.Context(), "key", []byte("value"), time.Minute); err != nil {
		t.Fatal(err)
	}

	value, err := driver.Get(t.Context(), "key")

	if err != nil {
		t.Fatal(err)
	}

	value[0] = 'x'

	stored, err := driver.Get(t.Context(), "key")

	if err != nil {
		t.Fatal(err)
	}

	if string(stored) != "value" {
		t.Errorf("Get = %q, want %q", stored, "value")
	}
}

// TestMemoryGetReturnsNotFoundForMissingKey verifies missing-key retrieval.
func TestMemoryGetReturnsNotFoundForMissingKey(t *testing.T) {
	t.Parallel()

	_, err := memorycache.New().Get(context.Background(), "missing")

	if !errors.Is(err, cache.ErrNotFound) {
		t.Errorf("Get error = %v, want %v", err, cache.ErrNotFound)
	}
}

// TestMemoryPutOverwritesExistingValue verifies later writes replace a cache value.
func TestMemoryPutOverwritesExistingValue(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()

	if err := driver.Put(t.Context(), "key", []byte("old"), time.Minute); err != nil {
		t.Fatal(err)
	}

	if err := driver.Put(t.Context(), "key", []byte("new"), time.Minute); err != nil {
		t.Fatal(err)
	}

	value, err := driver.Get(t.Context(), "key")

	if err != nil {
		t.Fatal(err)
	}

	if string(value) != "new" {
		t.Errorf("Get = %q, want %q", value, "new")
	}
}

// TestMemoryDeleteRemovesKey verifies deletion makes a cache key unavailable.
func TestMemoryDeleteRemovesKey(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()

	if err := driver.Put(t.Context(), "key", []byte("value"), time.Minute); err != nil {
		t.Fatal(err)
	}

	if err := driver.Delete(t.Context(), "key"); err != nil {
		t.Fatal(err)
	}

	_, err := driver.Get(t.Context(), "key")

	if !errors.Is(err, cache.ErrNotFound) {
		t.Errorf("Get error = %v, want %v", err, cache.ErrNotFound)
	}
}

// TestMemoryHasReturnsFalseForMissingKey verifies missing-key existence checks.
func TestMemoryHasReturnsFalseForMissingKey(t *testing.T) {
	t.Parallel()

	found, err := memorycache.New().Has(t.Context(), "missing")

	if err != nil {
		t.Fatal(err)
	}

	if found {
		t.Error("Has = true, want false")
	}
}

// TestMemoryPutZeroTTLDoesNotExpire verifies zero TTL disables expiration.
func TestMemoryPutZeroTTLDoesNotExpire(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()

	if err := driver.Put(t.Context(), "key", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Millisecond)

	value, err := driver.Get(t.Context(), "key")

	if err != nil {
		t.Fatal(err)
	}

	if string(value) != "value" {
		t.Errorf("Get = %q, want %q", value, "value")
	}
}

// TestMemoryTTLReturnsNotFoundForMissingKey verifies missing-key TTL lookup.
func TestMemoryTTLReturnsNotFoundForMissingKey(t *testing.T) {
	t.Parallel()

	_, err := memorycache.New().TTL(t.Context(), "missing")

	if !errors.Is(err, cache.ErrNotFound) {
		t.Errorf("TTL error = %v, want %v", err, cache.ErrNotFound)
	}
}

// TestMemoryOperations verifies cache retrieval, counters, expiration, and deletion.
func TestMemoryOperations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	driver := memorycache.New()
	putMemoryValue(t, driver, ctx, "key", []byte("10"), time.Minute)

	assertMemoryValue(t, driver, ctx, "key", "10")
	assertMemoryPresent(t, driver, ctx, "key")
	assertMemoryIncrement(t, driver, ctx, "key", 5, 15)
	assertMemoryDecrement(t, driver, ctx, "key", 3, 12)
	assertMemoryTTLPositive(t, driver, ctx, "key")
	deleteMemoryValue(t, driver, ctx, "key")

	assertMemoryMissing(t, driver, ctx, "key")
}

// TestMemoryAddAndZeroTTL verifies conditional writes without expiration.
func TestMemoryAddAndZeroTTL(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	driver := memorycache.New()

	assertMemoryAdded(t, driver, ctx, "key", []byte("value"), 0, true, "first Add")
	assertMemoryAdded(t, driver, ctx, "key", []byte("other"), time.Minute, false, "second Add")
	assertMemoryTTLZero(t, driver, ctx, "key")
}

// putMemoryValue stores value and fails the test when the write fails.
func putMemoryValue(t *testing.T, driver *memorycache.Driver, ctx context.Context, key string, value []byte, ttl time.Duration) {
	t.Helper()

	if err := driver.Put(ctx, key, value, ttl); err != nil {
		t.Fatal(err)
	}
}

// assertMemoryValue verifies the raw string value stored under key.
func assertMemoryValue(t *testing.T, driver *memorycache.Driver, ctx context.Context, key, want string) {
	t.Helper()

	raw, err := driver.Get(ctx, key)

	if err != nil {
		t.Fatal(err)
	}

	if string(raw) != want {
		t.Errorf("Get = %q, want %q", raw, want)
	}
}

// assertMemoryPresent verifies that key exists in the memory cache.
func assertMemoryPresent(t *testing.T, driver *memorycache.Driver, ctx context.Context, key string) {
	t.Helper()

	found, err := driver.Has(ctx, key)

	if err != nil {
		t.Fatal(err)
	}

	if !found {
		t.Error("Has = false, want true")
	}
}

// assertMemoryTTLPositive verifies key has a positive remaining lifetime.
func assertMemoryTTLPositive(t *testing.T, driver *memorycache.Driver, ctx context.Context, key string) {
	t.Helper()

	ttl, err := driver.TTL(ctx, key)

	if err != nil {
		t.Fatal(err)
	}

	if ttl <= 0 {
		t.Errorf("TTL = %s, want positive duration", ttl)
	}
}

// deleteMemoryValue removes key and fails the test when deletion fails.
func deleteMemoryValue(t *testing.T, driver *memorycache.Driver, ctx context.Context, key string) {
	t.Helper()

	if err := driver.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
}

// assertMemoryMissing verifies that key returns the cache missing-key error.
func assertMemoryMissing(t *testing.T, driver *memorycache.Driver, ctx context.Context, key string) {
	t.Helper()

	_, err := driver.Get(ctx, key)

	if !errors.Is(err, cache.ErrNotFound) {
		t.Errorf("Get error = %v, want %v", err, cache.ErrNotFound)
	}
}

// assertMemoryAdded verifies Add returns want for key.
func assertMemoryAdded(
	t *testing.T,
	driver *memorycache.Driver,
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
	want bool,
	action string,
) {
	t.Helper()

	added, err := driver.Add(ctx, key, value, ttl)

	if err != nil {
		t.Fatal(err)
	}

	if added != want {
		t.Errorf("%s = %t, want %t", action, added, want)
	}
}

// assertMemoryTTLZero verifies key has no expiration.
func assertMemoryTTLZero(t *testing.T, driver *memorycache.Driver, ctx context.Context, key string) {
	t.Helper()

	ttl, err := driver.TTL(ctx, key)

	if err != nil {
		t.Fatal(err)
	}

	if ttl != 0 {
		t.Errorf("TTL = %s, want zero", ttl)
	}
}

// TestStopEndsPurgeLoop verifies Start and Stop bracket the purge loop and
// that Stop is idempotent.
func TestStopEndsPurgeLoop(t *testing.T) {
	t.Parallel()

	driver := memorycache.New(func(store *memorycache.Builder) {
		store.Cleanup(time.Millisecond)
	})

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestSessionsPersistInMemoryCache verifies session data survives between
// requests when the session middleware uses the memory cache.
func TestSessionsPersistInMemoryCache(t *testing.T) {
	t.Parallel()

	middleware := session.Middleware(cache.New(memorycache.New()), func(sessionPolicy *session.Builder) {
		sessionPolicy.AllowInsecure(true)
	})

	field := session.NewField[int]("user_id")

	write := middleware(router.Handler(func(_ http.ResponseWriter, r *http.Request) error {
		return request.MustSession(r).Put(field, 7)
	}))

	first := write.Record(httptest.NewRequest(http.MethodGet, "/", nil))
	cookies := first.Cookies()

	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}

	var userID int
	read := middleware(router.Handler(func(_ http.ResponseWriter, r *http.Request) error {
		value, err := request.MustSession(r).Get(field)
		userID = value

		return err
	}))

	httpRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	httpRequest.AddCookie(cookies[0])

	_ = read.Record(httpRequest)

	if userID != 7 {
		t.Fatalf("user_id = %d, want 7", userID)
	}
}

// TestConformance runs the cache conformance suite against the memory driver.
func TestConformance(t *testing.T) {
	t.Parallel()

	cachetest.Run(t, func(*testing.T) cache.Driver {
		return memorycache.New()
	})
}

// TestConformanceWithJanitor repeats the suite with the janitor running to
// show that background sweeping does not change behavior.
func TestConformanceWithJanitor(t *testing.T) {
	t.Parallel()

	cachetest.Run(t, func(t *testing.T) cache.Driver {
		driver := memorycache.New(func(store *memorycache.Builder) {
			store.Cleanup(500 * time.Millisecond)
		})

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			_ = driver.Stop(context.WithoutCancel(t.Context()))
		})

		return driver
	})
}
