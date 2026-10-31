package health

import (
	"errors"
	"testing"
	"time"
)

// TestStoreRetainsTimestampForUnchangedVerdict checks cache history independently
// of the background scheduler and the public diagnostic representations.
func TestStoreRetainsTimestampForUnchangedVerdict(t *testing.T) {
	t.Parallel()

	state := &readiness{}
	startedAt := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	state.store([]Result{{Name: "database", Err: errors.New("first failure")}}, startedAt)
	state.store([]Result{{Name: "database", Err: errors.New("another failure")}}, startedAt.Add(time.Second))

	snapshot := state.snapshot()

	if snapshot.Results[0].ChangedAt != startedAt {
		t.Fatalf("transition = %v, want %v", snapshot.Results[0].ChangedAt, startedAt)
	}
}

// TestStoreUpdatesTimestampOnRecovery checks the transition's completion time.
func TestStoreUpdatesTimestampOnRecovery(t *testing.T) {
	t.Parallel()

	state := &readiness{}
	startedAt := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	recoveredAt := startedAt.Add(time.Second)
	state.store([]Result{{Name: "database", Err: errors.New("down")}}, startedAt)
	state.store([]Result{{Name: "database"}}, recoveredAt)

	if snapshot := state.snapshot(); snapshot.Results[0].ChangedAt != recoveredAt {
		t.Fatalf("transition = %v, want %v", snapshot.Results[0].ChangedAt, recoveredAt)
	}
}
