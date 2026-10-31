package cache_test

import (
	"testing"
	"time"

	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/telemetry"
)

// TestLockTelemetryNeverRecordsNames verifies Lock and TryLock are each one
// span without the lock name.
func TestLockTelemetryNeverRecordsNames(t *testing.T) {
	t.Parallel()

	store, fake := newTraced(t, memorycache.New())

	lease, err := store.Lock(t.Context(), secretKey, time.Minute)

	if err != nil {
		t.Fatal(err)
	}

	if _, held, err := store.TryLock(t.Context(), secretKey, time.Minute); err != nil || held {
		t.Fatalf("TryLock = %t, %v, want false, nil", held, err)
	}

	if err := lease.Extend(t.Context(), time.Minute); err != nil {
		t.Fatal(err)
	}

	if err := lease.Unlock(t.Context()); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpanCount(t, 2)
	fake.AssertSpan(t, "cache lock", telemetry.String("arkoren.cache.operation", "lock"))
	fake.AssertSpan(t, "cache try_lock", telemetry.String("arkoren.cache.operation", "try_lock"))
}
