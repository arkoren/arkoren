package hatchetworkflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
)

// requireChildUnsupported checks repeated rejection before backend access. A nil
// native context also makes any memo or durable trigger an immediate test failure.
func requireChildUnsupported(t *testing.T, ctx context.Context) {
	t.Helper()

	driver := &Driver{
		backend: func() (backend, error) {
			t.Fatal("Child accessed the backend")

			return nil, nil
		},
	}

	rt := &runtime{
		ctx: ctx,
		runner: &Runner{
			driver: driver,
		},
	}

	for range 2 {
		out, err := rt.Child("child", "child-id", []byte("input"))

		if out != nil || !errors.Is(err, workflow.ErrUnsupported) {
			t.Fatalf("Child() = %q, %v; want nil, ErrUnsupported", out, err)
		}
	}
}

// requireSpawnUnsupported checks repeated rejection before backend access. A nil
// native context also makes any memo or durable trigger an immediate test failure.
func requireSpawnUnsupported(t *testing.T, ctx context.Context) {
	t.Helper()

	driver := &Driver{
		backend: func() (backend, error) {
			t.Fatal("Spawn accessed the backend")

			return nil, nil
		},
	}

	rt := &runtime{
		ctx: ctx,
		runner: &Runner{
			driver: driver,
		},
	}

	for range 2 {
		if err := rt.Spawn("child", "child-id", []byte("input")); !errors.Is(err, workflow.ErrUnsupported) {
			t.Fatalf("Spawn() = %v; want ErrUnsupported", err)
		}
	}
}

// TestChildUnsupportedDoesNotSubmit rejects live calls before lookup or trigger.
// Repetition must not create a memo or turn rejection into a submission.
func TestChildUnsupportedDoesNotSubmit(t *testing.T) {
	t.Parallel()

	requireChildUnsupported(t, t.Context())
}

// TestSpawnUnsupportedDoesNotSubmit rejects live calls before lookup or trigger.
// Repetition must not create a memo or turn rejection into a submission.
func TestSpawnUnsupportedDoesNotSubmit(t *testing.T) {
	t.Parallel()

	requireSpawnUnsupported(t, t.Context())
}

// TestChildUnsupportedDoesNotReadNilContext proves rejection needs no run state.
// The runtime must not access even the local cancellation context.
func TestChildUnsupportedDoesNotReadNilContext(t *testing.T) {
	t.Parallel()

	requireChildUnsupported(t, nil)
}

// TestSpawnUnsupportedDoesNotReadNilContext proves rejection needs no run state.
// The runtime must not access even the local cancellation context.
func TestSpawnUnsupportedDoesNotReadNilContext(t *testing.T) {
	t.Parallel()

	requireSpawnUnsupported(t, nil)
}

// TestChildUnsupportedAfterCancellation keeps containment independent of timing.
// A canceled call must still reject without touching engine or durable state.
func TestChildUnsupportedAfterCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	requireChildUnsupported(t, ctx)
}

// TestSpawnUnsupportedAfterCancellation keeps containment independent of timing.
// A canceled call must still reject without touching engine or durable state.
func TestSpawnUnsupportedAfterCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	requireSpawnUnsupported(t, ctx)
}

// TestChildUnsupportedAfterDeadline uses an already expired deadline, not a sleep.
// Timeout handling must not reach the former start, poll, or cancellation path.
func TestChildUnsupportedAfterDeadline(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
	t.Cleanup(cancel)

	requireChildUnsupported(t, ctx)
}

// TestSpawnUnsupportedAfterDeadline uses an already expired deadline, not a sleep.
// Timeout handling must not reach the former workflow submission path.
func TestSpawnUnsupportedAfterDeadline(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
	t.Cleanup(cancel)

	requireSpawnUnsupported(t, ctx)
}
