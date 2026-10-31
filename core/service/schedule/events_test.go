package schedule_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/scheduletest"
)

// TestConstructionRetainedDraftCannotReplaceEvents exercises the final shared
// bus after retained-root mutation, keeping callback execution outside RunNow.
func TestConstructionRetainedDraftCannotReplaceEvents(t *testing.T) {
	t.Parallel()

	bus, failed := failedRuns(t)
	var retained *schedule.Builder
	scheduler := schedule.New(scheduletest.New(), quiet(), func(scheduler *schedule.Builder) {
		retained = scheduler
		scheduler.Events(bus)
	})
	retained.Events(nil)

	failure := errors.New("scheduled failure")

	if err := scheduler.Add(daily("snapshot", func(context.Context) error {
		return failure
	})); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunNow(t.Context(), "snapshot"); !errors.Is(err, failure) {
		t.Fatalf("RunNow=%v", err)
	}

	if len(*failed) != 1 {
		t.Fatalf("published events=%v", *failed)
	}
}

// failedRuns returns a bus and the slice its RunFailed payloads are
// appended to.
func failedRuns(t *testing.T) (*event.Bus, *[]schedule.RunFailedEvent) {
	t.Helper()

	bus := event.New()
	var failed []schedule.RunFailedEvent

	if _, err := bus.Listen(schedule.RunFailed, func(_ context.Context, payload schedule.RunFailedEvent) error {
		failed = append(failed, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return bus, &failed
}

// TestRunFailedPublishedForFailedEntry checks that the event identifies the
// failing entry and preserves the execution error returned to the caller.
func TestRunFailedPublishedForFailedEntry(t *testing.T) {
	t.Parallel()

	bus, failed := failedRuns(t)
	scheduler := schedule.New(scheduletest.New(), quiet(), func(scheduler *schedule.Builder) {
		scheduler.Events(bus)
	})

	failure := errors.New("disk full")

	if err := scheduler.Add(daily("bad", func(context.Context) error {
		return failure
	})); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunNow(t.Context(), "bad"); !errors.Is(err, failure) {
		t.Fatalf("got %v, want the failure", err)
	}

	if len(*failed) != 1 || (*failed)[0].Name != "bad" || (*failed)[0].Task || !errors.Is((*failed)[0].Err, failure) {
		t.Fatalf("RunFailed = %+v, want one entry failure", *failed)
	}
}

// TestRunFailedNotPublishedForSuccessOrSkip checks that neither successful nor
// deliberately skipped executions enter the failure event stream.
func TestRunFailedNotPublishedForSuccessOrSkip(t *testing.T) {
	t.Parallel()

	bus, failed := failedRuns(t)
	scheduler := schedule.New(scheduletest.New(), quiet(), func(scheduler *schedule.Builder) {
		scheduler.Events(bus)
	})

	if err := scheduler.Add(
		daily("ok", noop),
		daily("skip", func(context.Context) error {
			return schedule.ErrSkipped
		}),
	); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"ok", "skip"} {
		if err := scheduler.RunNow(t.Context(), name); err != nil {
			t.Fatal(err)
		}
	}

	if len(*failed) != 0 {
		t.Fatalf("RunFailed = %+v, want none", *failed)
	}
}

// collect returns the slice that every payload published on topic is
// appended to.
func collect[T any](t *testing.T, bus *event.Bus, topic event.Topic[T]) *[]T {
	t.Helper()

	var got []T

	if _, err := bus.Listen(topic, func(_ context.Context, payload T) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return &got
}

// TestTaskDeferredPublishedByAfter checks that relative scheduling publishes
// the returned job ID and the absolute due time computed from the test clock.
func TestTaskDeferredPublishedByAfter(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		bus := event.New()
		deferred := collect(t, bus, schedule.TaskDeferred)
		scheduler := schedule.New(scheduletest.New(), quiet(), func(scheduler *schedule.Builder) {
			scheduler.Events(bus)
		})

		remind := schedule.NewTask[reminder]("remind")

		if err := scheduler.Handle(remind, func(context.Context, reminder) error {
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		id, err := scheduler.After(t.Context(), remind, reminder{
			UserID: 42,
		}, time.Hour)

		if err != nil {
			t.Fatal(err)
		}

		want := schedule.TaskDeferredEvent{
			Name: "remind",
			ID:   id,
			At:   time.Now().Add(time.Hour),
		}

		if len(*deferred) != 1 || (*deferred)[0] != want {
			t.Fatalf("TaskDeferred = %+v, want one %+v", *deferred, want)
		}
	})
}

// TestTaskDeferredPublishedByAt checks that absolute scheduling publishes the
// caller's due time and the identifier returned by the driver.
func TestTaskDeferredPublishedByAt(t *testing.T) {
	t.Parallel()

	bus := event.New()
	deferred := collect(t, bus, schedule.TaskDeferred)
	scheduler := schedule.New(scheduletest.New(), quiet(), func(scheduler *schedule.Builder) {
		scheduler.Events(bus)
	})

	remind := schedule.NewTask[reminder]("remind")

	if err := scheduler.Handle(remind, func(context.Context, reminder) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	when := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	id, err := scheduler.At(t.Context(), remind, reminder{
		UserID: 1,
	}, when)

	if err != nil {
		t.Fatal(err)
	}

	want := schedule.TaskDeferredEvent{
		Name: "remind",
		ID:   id,
		At:   when,
	}

	if len(*deferred) != 1 || (*deferred)[0] != want {
		t.Fatalf("TaskDeferred = %+v, want one %+v", *deferred, want)
	}
}

// TestTaskDeferredNotPublishedOnFailure checks that driver and registration
// errors do not announce jobs that were never successfully deferred.
func TestTaskDeferredNotPublishedOnFailure(t *testing.T) {
	t.Parallel()

	bus := event.New()
	deferred := collect(t, bus, schedule.TaskDeferred)
	driver := scheduletest.New()
	scheduler := schedule.New(driver, quiet(), func(scheduler *schedule.Builder) {
		scheduler.Events(bus)
	})

	remind := schedule.NewTask[reminder]("remind")

	if err := scheduler.Handle(remind, func(context.Context, reminder) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	driver.FailNext(errors.New("store down"))

	if _, err := scheduler.After(t.Context(), remind, reminder{}, time.Hour); err == nil {
		t.Fatal("After succeeded, want the driver failure")
	}

	if _, err := scheduler.After(t.Context(), schedule.NewTask[reminder]("unknown"), reminder{}, time.Hour); !errors.Is(err, schedule.ErrUnknownName) {
		t.Fatalf("got %v, want ErrUnknownName", err)
	}

	if len(*deferred) != 0 {
		t.Fatalf("TaskDeferred = %+v, want none", *deferred)
	}
}

// TestTaskDeferredWithNilBusPublishesNothing checks that the default absence
// of an event bus does not prevent successful task scheduling.
func TestTaskDeferredWithNilBusPublishesNothing(t *testing.T) {
	t.Parallel()

	scheduler := schedule.New(scheduletest.New(), quiet())
	remind := schedule.NewTask[reminder]("remind")

	if err := scheduler.Handle(remind, func(context.Context, reminder) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := scheduler.After(t.Context(), remind, reminder{}, time.Hour); err != nil {
		t.Fatal(err)
	}
}

// TestTaskCanceledPublishedByCancel checks that a successful driver cancel
// produces one event carrying the requested job identifier.
func TestTaskCanceledPublishedByCancel(t *testing.T) {
	t.Parallel()

	bus := event.New()
	canceled := collect(t, bus, schedule.TaskCanceled)
	scheduler := schedule.New(scheduletest.New(), quiet(), func(scheduler *schedule.Builder) {
		scheduler.Events(bus)
	})

	if err := scheduler.Cancel(t.Context(), "job-1"); err != nil {
		t.Fatal(err)
	}

	if len(*canceled) != 1 || (*canceled)[0].ID != "job-1" {
		t.Fatalf("TaskCanceled = %+v, want one for job-1", *canceled)
	}
}

// TestTaskCanceledNotPublishedOnFailure checks that a failed driver cancel
// cannot publish a misleading successful-cancellation event.
func TestTaskCanceledNotPublishedOnFailure(t *testing.T) {
	t.Parallel()

	bus := event.New()
	canceled := collect(t, bus, schedule.TaskCanceled)
	driver := scheduletest.New()
	scheduler := schedule.New(driver, quiet(), func(scheduler *schedule.Builder) {
		scheduler.Events(bus)
	})
	driver.FailNext(errors.New("store down"))

	if err := scheduler.Cancel(t.Context(), "job-1"); err == nil {
		t.Fatal("Cancel succeeded, want the driver failure")
	}

	if len(*canceled) != 0 {
		t.Fatalf("TaskCanceled = %+v, want none", *canceled)
	}
}

// TestRunSkippedPublishedForSkippedEntry checks that intentional skips return
// success and publish the measured execution duration without a task flag.
func TestRunSkippedPublishedForSkippedEntry(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		bus := event.New()
		skipped := collect(t, bus, schedule.RunSkipped)
		scheduler := schedule.New(scheduletest.New(), quiet(), func(scheduler *schedule.Builder) {
			scheduler.Events(bus)
		})

		if err := scheduler.Add(daily("skip", func(context.Context) error {
			time.Sleep(time.Second)

			return schedule.ErrSkipped
		})); err != nil {
			t.Fatal(err)
		}

		if err := scheduler.RunNow(t.Context(), "skip"); err != nil {
			t.Fatal(err)
		}

		want := schedule.RunSkippedEvent{
			Name:     "skip",
			Duration: time.Second,
		}

		if len(*skipped) != 1 || (*skipped)[0] != want {
			t.Fatalf("RunSkipped = %+v, want one %+v", *skipped, want)
		}
	})
}

// TestRunSkippedNotPublishedForSuccessOrFailure keeps ordinary success and
// execution errors out of the intentional-skip event stream.
func TestRunSkippedNotPublishedForSuccessOrFailure(t *testing.T) {
	t.Parallel()

	bus := event.New()
	skipped := collect(t, bus, schedule.RunSkipped)
	scheduler := schedule.New(scheduletest.New(), quiet(), func(scheduler *schedule.Builder) {
		scheduler.Events(bus)
	})

	if err := scheduler.Add(
		daily("ok", noop),
		daily("bad", func(context.Context) error {
			return errors.New("boom")
		}),
	); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunNow(t.Context(), "ok"); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunNow(t.Context(), "bad"); err == nil {
		t.Fatal("RunNow succeeded, want the failure")
	}

	if len(*skipped) != 0 {
		t.Fatalf("RunSkipped = %+v, want none", *skipped)
	}
}

// TestRunSkippedWithNilBusPublishesNothing checks that intentional skipping
// remains successful when no event bus is configured.
func TestRunSkippedWithNilBusPublishesNothing(t *testing.T) {
	t.Parallel()

	scheduler := schedule.New(scheduletest.New(), quiet())

	if err := scheduler.Add(daily("skip", func(context.Context) error {
		return schedule.ErrSkipped
	})); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunNow(t.Context(), "skip"); err != nil {
		t.Fatal(err)
	}
}
