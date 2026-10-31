package memorycache

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// entryCount returns the number of stored entries, expired ones included.
func entryCount(driver *Driver) int {
	driver.store.Mu.Lock()
	defer driver.store.Mu.Unlock()

	return len(driver.store.Entries)
}

// TestWriteSweepsExpiredEntriesWithoutStart verifies that a write removes
// abandoned expired keys once the default cleanup interval has passed, with no
// janitor running.
func TestWriteSweepsExpiredEntriesWithoutStart(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		driver := New()

		for _, key := range []string{"a", "b", "c"} {
			if err := driver.Put(ctx, key, []byte("v"), time.Second); err != nil {
				t.Fatalf("Put %s: %v", key, err)
			}
		}

		time.Sleep(2 * time.Minute)

		if _, err := driver.Increment(ctx, "counter", 1); err != nil {
			t.Fatalf("Increment: %v", err)
		}

		if got := entryCount(driver); got != 1 {
			t.Fatalf("entries after sweeping write = %d, want 1", got)
		}
	})
}

// TestNegativeCleanupDisablesSweeping verifies that expired entries stay
// stored after writes when Cleanup is negative.
func TestNegativeCleanupDisablesSweeping(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		driver := New(func(store *Builder) {
			store.Cleanup(-1)
		})

		if err := driver.Put(ctx, "a", []byte("v"), time.Second); err != nil {
			t.Fatalf("Put: %v", err)
		}

		time.Sleep(2 * time.Minute)

		if err := driver.Put(ctx, "b", []byte("v"), time.Second); err != nil {
			t.Fatalf("Put: %v", err)
		}

		if got := entryCount(driver); got != 2 {
			t.Fatalf("entries = %d, want 2", got)
		}

		if err := driver.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}

		if driver.store.Cancel != nil {
			t.Fatal("Start launched a janitor despite negative Cleanup")
		}
	})
}
