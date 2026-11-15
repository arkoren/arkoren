package background_test

import (
	"context"
	"testing"
	"time"

	"arkoren.dev/ark/access/internal/background"
)

// TestGoRecoversPanicsAndWaitSettles verifies a panicking job neither crashes
// the process nor leaves Wait blocked.
func TestGoRecoversPanicsAndWaitSettles(t *testing.T) {
	t.Parallel()

	var group background.Group
	if !group.Go(func(context.Context) { panic("boom") }) {
		t.Fatal("Go refused a job on an empty group")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := group.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestGoDropsBeyondTheLimit verifies the bound: jobs past the limit are
// refused, not queued, and accepted again once slots free up.
func TestGoDropsBeyondTheLimit(t *testing.T) {
	t.Parallel()

	var group background.Group
	release := make(chan struct{})
	for range background.Limit {
		if !group.Go(func(context.Context) { <-release }) {
			t.Fatal("Go refused a job below the limit")
		}
	}
	if group.Go(func(context.Context) {}) {
		t.Fatal("Go accepted a job beyond the limit")
	}
	close(release)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := group.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !group.Go(func(context.Context) {}) {
		t.Fatal("Go still refuses after the jobs finished")
	}
}

// TestWaitReturnsTheContextErrorWithoutLeaking verifies Wait gives up when its
// context ends and a later Wait still sees the job through.
func TestWaitReturnsTheContextErrorWithoutLeaking(t *testing.T) {
	t.Parallel()

	var group background.Group
	release := make(chan struct{})
	group.Go(func(context.Context) { <-release })
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if err := group.Wait(expired); err == nil {
		t.Fatal("Wait with a canceled context returned nil")
	}
	close(release)
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	if err := group.Wait(ctx); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
}

// TestJobContextIsFresh verifies the job's context has its own deadline.
func TestJobContextIsFresh(t *testing.T) {
	t.Parallel()

	var group background.Group
	got := make(chan bool, 1)
	group.Go(func(ctx context.Context) {
		_, ok := ctx.Deadline()
		got <- ok
	})
	if !<-got {
		t.Fatal("the job context has no deadline")
	}
}
