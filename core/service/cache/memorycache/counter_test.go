package memorycache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/service/cache/memorycache"
)

// TestMemoryIncrementRejectsCanceledContext verifies canceled increments leave values unchanged.
func TestMemoryIncrementRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()

	if err := driver.Put(t.Context(), "key", []byte("1"), time.Minute); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := driver.Increment(ctx, "key", 1)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Increment error = %v, want %v", err, context.Canceled)
	}

	value, err := driver.Get(t.Context(), "key")

	if err != nil {
		t.Fatal(err)
	}

	if string(value) != "1" {
		t.Errorf("Get = %q, want %q", value, "1")
	}
}

// TestMemoryDecrementRejectsCanceledContext verifies canceled decrements leave values unchanged.
func TestMemoryDecrementRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()

	if err := driver.Put(t.Context(), "key", []byte("1"), time.Minute); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := driver.Decrement(ctx, "key", 1)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Decrement error = %v, want %v", err, context.Canceled)
	}

	value, err := driver.Get(t.Context(), "key")

	if err != nil {
		t.Fatal(err)
	}

	if string(value) != "1" {
		t.Errorf("Get = %q, want %q", value, "1")
	}
}

// TestMemoryIncrementCreatesMissingKey verifies counters start from zero.
func TestMemoryIncrementCreatesMissingKey(t *testing.T) {
	t.Parallel()

	value, err := memorycache.New().Increment(t.Context(), "missing", 2)

	if err != nil {
		t.Fatal(err)
	}

	if value != 2 {
		t.Errorf("Increment = %d, want 2", value)
	}
}

// TestMemoryDecrementDecreasesValue verifies decrementing an integer cache value.
func TestMemoryDecrementDecreasesValue(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()

	if err := driver.Put(t.Context(), "counter", []byte("10"), time.Minute); err != nil {
		t.Fatal(err)
	}

	value, err := driver.Decrement(t.Context(), "counter", 3)

	if err != nil {
		t.Fatal(err)
	}

	if value != 7 {
		t.Errorf("Decrement = %d, want %d", value, 7)
	}
}

// assertMemoryIncrement verifies an increment produces want.
func assertMemoryIncrement(t *testing.T, driver *memorycache.Driver, ctx context.Context, key string, delta, want int64) {
	t.Helper()

	value, err := driver.Increment(ctx, key, delta)

	if err != nil {
		t.Fatal(err)
	}

	if value != want {
		t.Errorf("Increment = %d, want %d", value, want)
	}
}

// assertMemoryDecrement verifies a decrement produces want.
func assertMemoryDecrement(t *testing.T, driver *memorycache.Driver, ctx context.Context, key string, delta, want int64) {
	t.Helper()

	value, err := driver.Decrement(ctx, key, delta)

	if err != nil {
		t.Fatal(err)
	}

	if value != want {
		t.Errorf("Decrement = %d, want %d", value, want)
	}
}
