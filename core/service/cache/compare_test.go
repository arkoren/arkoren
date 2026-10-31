package cache_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/telemetry"
)

// ExampleCache_CompareAndPut shows the caller retaining an exact raw guard
// while replacing a typed value. It runs entirely in memory without time waits.
func ExampleCache_CompareAndPut() {
	ctx := context.Background()
	store := cache.New(memorycache.New())
	key := cache.NewKey[string]("document")

	if err := store.Put(ctx, key, "draft"); err != nil {
		fmt.Println(err)

		return
	}

	raw, err := store.Driver().Get(ctx, key.Name())

	if err != nil {
		fmt.Println(err)

		return
	}

	applied, err := store.CompareAndPut(ctx, cache.Match{Key: key.Name(), Value: raw}, key, "revised")

	if err != nil {
		fmt.Println(err)

		return
	}

	value, err := store.Get(ctx, key)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(applied, value)
	// Output: true revised
}

// TestCompareAndPutFacadeUsesTypedDestination checks raw observed guard bytes
// authorize a JSON-encoded, typed destination with its own lifetime policy.
func TestCompareAndPutFacadeUsesTypedDestination(t *testing.T) {
	t.Parallel()

	store := cache.New(memorycache.New())
	guard := cache.NewKey[string]("draft")

	if err := store.Put(t.Context(), guard, "v1"); err != nil {
		t.Fatal(err)
	}

	raw, err := store.Driver().Get(t.Context(), guard.Name())

	if err != nil {
		t.Fatal(err)
	}

	target := cache.NewKey[int]("revision", func(entry *cache.KeyBuilder) {
		entry.TTL(time.Hour)
	})

	applied, err := store.CompareAndPut(t.Context(), cache.Match{Key: guard.Name(), Value: raw}, target, 42)

	if err != nil || !applied {
		t.Fatalf("CompareAndPut = %t, %v, want true, nil", applied, err)
	}

	value, err := store.Get(t.Context(), target)

	if err != nil || value != 42 {
		t.Fatalf("destination = %d, %v, want 42, nil", value, err)
	}

	remaining, err := store.TTL(t.Context(), target)

	if err != nil || remaining <= 0 || remaining > time.Hour {
		t.Fatalf("destination TTL = %s, %v, want (0, 1h]", remaining, err)
	}
}

// TestCompareAndPutFacadeMismatchIsNotAnError checks exact raw comparison,
// rather than equivalent decoded JSON, and one non-error telemetry operation.
func TestCompareAndPutFacadeMismatchIsNotAnError(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()

	if err := driver.Put(t.Context(), "guard", []byte(" 42 "), 0); err != nil {
		t.Fatal(err)
	}

	store, fake := newTraced(t, driver)

	applied, err := store.CompareAndPut(t.Context(), cache.Match{Key: "guard", Value: []byte("42")},
		cache.NewKey[int]("target"), 43)

	if err != nil || applied {
		t.Fatalf("CompareAndPut = %t, %v, want false, nil", applied, err)
	}

	if _, err := driver.Get(t.Context(), "target"); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("destination error = %v, want ErrNotFound", err)
	}

	fake.AssertSpanCount(t, 1)
	fake.AssertSpan(t, "cache compare_and_put", telemetry.String("arkoren.cache.operation", "compare_and_put"))

	span, _ := fake.Span("cache compare_and_put")

	if _, found := span.Attr("error.type"); found || span.Status == telemetry.Error {
		t.Fatalf("mismatch recorded as error: %+v", span)
	}
}

// TestCompareAndPutFacadeCanceledContextPrecedesDefinitionAndCodec uses a nil
// driver and an unencodable value so only cancellation can satisfy the result.
func TestCompareAndPutFacadeCanceledContextPrecedesDefinitionAndCodec(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	store := cache.New(nil)
	var key cache.Key[chan int]

	applied, err := store.CompareAndPut(ctx, cache.Match{}, key, make(chan int))

	if !errors.Is(err, context.Canceled) || applied {
		t.Fatalf("CompareAndPut = %t, %v, want false, context.Canceled", applied, err)
	}
}

// TestCompareAndPutFacadeInvalidDefinitionPrecedesCodec checks destination
// validation never invokes the codec or driver for an unusable definition.
func TestCompareAndPutFacadeInvalidDefinitionPrecedesCodec(t *testing.T) {
	t.Parallel()

	store := cache.New(nil)
	var key cache.Key[chan int]

	applied, err := store.CompareAndPut(t.Context(), cache.Match{}, key, make(chan int))

	if !errors.Is(err, cache.ErrInvalidKey) || applied {
		t.Fatalf("CompareAndPut = %t, %v, want false, ErrInvalidKey", applied, err)
	}
}

// TestCompareAndPutFacadeEncodingFailureSkipsDriver verifies unsupported values
// are rejected before the atomic operation, with no unconditional fallback.
func TestCompareAndPutFacadeEncodingFailureSkipsDriver(t *testing.T) {
	t.Parallel()

	store := cache.New(nil)

	applied, err := store.CompareAndPut(t.Context(), cache.Match{}, cache.NewKey[chan int]("target"), make(chan int))

	if err == nil || applied {
		t.Fatalf("CompareAndPut = %t, %v, want false and encoding error", applied, err)
	}
}
