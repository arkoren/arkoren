package scheduletest_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/scheduletest"
)

// reminder carries a recipient through the scheduler's typed task boundary.
// Tests inspect it after both assertion decoding and dispatch.
type reminder struct {
	// UserID is the recipient selected by the scheduling caller.
	UserID int
}

// remind is the reusable immutable task identity for reminder payloads.
// Each test registers its own execution body on a fresh scheduler.
var remind = schedule.NewTask[reminder]("remind")

// TestFakeRecordsDeferAndDrains checks that recording is separate from
// execution and that an explicit scheduled drain can dispatch future jobs.
func TestFakeRecordsDeferAndDrains(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := scheduletest.New()
		scheduler := schedule.New(fake)

		var got atomic.Int32

		if err := scheduler.Handle(remind, func(_ context.Context, r reminder) error {
			got.Store(int32(r.UserID))

			return nil
		}); err != nil {
			t.Fatal(err)
		}

		id, err := scheduler.After(t.Context(), remind, reminder{
			UserID: 42,
		}, time.Hour)

		if err != nil || id == "" {
			t.Fatalf("After = %q, %v", id, err)
		}

		fake.AssertDeferred(t, remind, func(payload reminder) bool {
			return payload.UserID == 42
		})
		fake.AssertNotDeferred(t, remind, func(payload reminder) bool {
			return payload.UserID == 1
		})
		fake.AssertDeferredCount(t, remind, 1)

		if err := fake.Drain(t.Context()); !errors.Is(err, scheduletest.ErrNotRunning) {
			t.Fatalf("Drain before Run error = %v", err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)

		go func() {
			done <- scheduler.Run(ctx)
		}()

		synctest.Wait()

		if err := fake.Drain(ctx); err != nil || got.Load() != 0 {
			t.Fatalf("Drain of a future job = %v, handled %d; want nothing", err, got.Load())
		}

		if err := fake.Drain(ctx, scheduletest.WithScheduled()); err != nil || got.Load() != 42 {
			t.Fatalf("Drain = %v, handled %d; want 42", err, got.Load())
		}

		if len(fake.Pending()) != 0 || len(fake.Deferred()) != 1 {
			t.Fatalf("pending %d, deferred %d", len(fake.Pending()), len(fake.Deferred()))
		}

		cancel()

		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

// TestFakeDrainDueAndOrder checks due-time ordering and aggregation of
// dispatch failures while leaving future jobs pending.
func TestFakeDrainDueAndOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := scheduletest.New()
		now := time.Now()

		for _, job := range []schedule.Job{
			{
				Name: "b",
				At:   now.Add(-time.Minute),
			},
			{
				Name: "a",
				At:   now.Add(-time.Hour),
			},
			{
				Name: "later",
				At:   now.Add(time.Hour),
			},
		} {
			if _, err := fake.Defer(t.Context(), job); err != nil {
				t.Fatal(err)
			}
		}

		var order []string

		dispatch := func(_ context.Context, name string, _ []byte) error {
			order = append(order, name)

			return errors.New("failed " + name)
		}

		ctx, cancel := context.WithCancel(t.Context())

		go func() {
			_ = fake.Run(ctx, nil, dispatch)
		}()

		synctest.Wait()

		err := fake.Drain(ctx)

		if len(order) != 2 || order[0] != "a" || order[1] != "b" {
			t.Fatalf("order = %v", order)
		}

		if err == nil {
			t.Fatal("Drain hid the dispatch errors")
		}

		cancel()
	})
}

// TestFakeCancelAndFailure exercises cancellation, injected failures, and
// Reset on one recorded history, including removal of pending work.
func TestFakeCancelAndFailure(t *testing.T) {
	fake := scheduletest.New()
	job := schedule.Job{
		Name: "x",
		At:   time.Now(),
	}

	id, err := fake.Defer(t.Context(), job)

	if err != nil {
		t.Fatal(err)
	}

	if err := fake.Cancel(t.Context(), id); err != nil {
		t.Fatal(err)
	}

	if err := fake.Cancel(t.Context(), "unknown"); err != nil {
		t.Fatal(err)
	}

	fake.AssertCanceled(t, id)

	if len(fake.Pending()) != 0 {
		t.Fatal("canceled job is still pending")
	}

	boom := errors.New("boom")
	fake.FailNext(boom)

	if _, err := fake.Defer(t.Context(), job); !errors.Is(err, boom) {
		t.Fatalf("FailNext error = %v", err)
	}

	fake.FailWith(boom)

	if _, err := fake.Defer(t.Context(), job); !errors.Is(err, boom) {
		t.Fatalf("FailWith error = %v", err)
	}

	fake.Reset()

	fake.AssertNothingDeferred(t)

	if _, err := fake.Defer(t.Context(), job); err != nil {
		t.Fatalf("Defer after Reset: %v", err)
	}
}

// TestFakeRunRecordsEntries checks that even an already canceled Run captures
// the entries supplied by the scheduler without executing them.
func TestFakeRunRecordsEntries(t *testing.T) {
	fake := scheduletest.New()
	entry := schedule.NewEntry("tick", func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Every(time.Minute))
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := fake.Run(ctx, []schedule.Entry{entry}, nil); err != nil {
		t.Fatal(err)
	}

	if entries := fake.Entries(); len(entries) != 1 || entries[0].Name() != "tick" {
		t.Fatalf("Entries = %v", entries)
	}
}

// recorder captures assertion failures.
type recorder struct {
	// TB supplies testing methods not intercepted by this assertion probe.
	testing.TB

	// failed records whether a fake assertion reported an error.
	failed bool
}

// Helper intentionally ignores helper registration so the embedded test
// handle's state is unchanged while fake assertions use this probe.
func (recorder *recorder) Helper() {
}

// Errorf records assertion failure without failing the enclosing test. The
// message is irrelevant because these checks exercise failure detection.
func (recorder *recorder) Errorf(string, ...any) {
	recorder.failed = true
}

// TestFakeAssertionsFail checks that missing expected work and unexpected
// recorded work are both reported through the supplied testing handle.
func TestFakeAssertionsFail(t *testing.T) {
	fake := scheduletest.New()
	r := &recorder{
		TB: t,
	}

	fake.AssertDeferred(r, remind)

	if !r.failed {
		t.Fatal("AssertDeferred passed without a job")
	}

	if _, err := fake.Defer(t.Context(), schedule.Job{
		Name:    "remind",
		Payload: []byte(`{}`),
		At:      time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	r = &recorder{
		TB: t,
	}

	fake.AssertNothingDeferred(r)

	if !r.failed {
		t.Fatal("AssertNothingDeferred passed after a Defer")
	}
}
