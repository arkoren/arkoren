package schedule

import (
	"time"

	"arkoren.dev/core/event"
)

// RunFailedEvent is the payload of [RunFailed].
// It contains execution identity and timing, never task payload bytes. Error
// causes remain shared application values and can include sensitive details.
//
//	unsubscribe, err := bus.Listen(schedule.RunFailed, func(_ context.Context, outcome schedule.RunFailedEvent) error {
//		fmt.Println(outcome.Name, outcome.Duration)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
type RunFailedEvent struct {
	// Name is the entry or task name.
	Name string

	// Task reports whether the run was a deferred task rather than an entry.
	Task bool

	// Duration is how long the run took.
	Duration time.Duration

	// Err is the error the run returned, including a recovered panic or an
	// exceeded timeout.
	Err error
}

// RunFailed is published when an entry or task run returns an error other
// than [ErrSkipped], whether the driver started it or [Scheduler.RunNow] did.
// Driver retry policy is independent of this local notification.
//
//	unsubscribe, err := bus.Listen(schedule.RunFailed, recordFailure)
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
var RunFailed = event.NewTopic[RunFailedEvent]("schedule.run.failed")

// RunSkippedEvent is the payload of [RunSkipped].
// It describes an execution that chose to skip, not a driver's overlap guard
// suppressing invocation before the facade runs.
//
//	unsubscribe, err := bus.Listen(schedule.RunSkipped, func(_ context.Context, outcome schedule.RunSkippedEvent) error {
//		fmt.Println(outcome.Name, outcome.Task)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
type RunSkippedEvent struct {
	// Name is the entry or task name.
	Name string

	// Task reports whether the run was a deferred task rather than an entry.
	Task bool

	// Duration is how long the run took before it returned [ErrSkipped].
	Duration time.Duration
}

// RunSkipped is published when an entry or task run returns [ErrSkipped],
// whether the driver started it or [Scheduler.RunNow] did. Skips are not
// failures: [RunFailed] is not published for them.
//
//	unsubscribe, err := bus.Listen(schedule.RunSkipped, recordSkip)
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
var RunSkipped = event.NewTopic[RunSkippedEvent]("schedule.run.skipped")

// TaskDeferredEvent is the payload of [TaskDeferred]. It never carries the
// task payload, which may hold personal data.
// Acceptance is not a completion or durability guarantee; those belong to the
// selected driver and the task's application effect.
//
//	unsubscribe, err := bus.Listen(schedule.TaskDeferred,
//		func(_ context.Context, outcome schedule.TaskDeferredEvent) error {
//			fmt.Println(outcome.Name, outcome.At)
//			return nil
//		})
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
type TaskDeferredEvent struct {
	// Name is the task name.
	Name string

	// ID is the job identifier the driver returned, usable with
	// [Scheduler.Cancel].
	ID string

	// At is when the job is due to run.
	At time.Time
}

// TaskDeferred is published when [Scheduler.At] or [Scheduler.After] has
// handed a job to the driver successfully. Failed calls publish nothing.
//
//	unsubscribe, err := bus.Listen(schedule.TaskDeferred, recordSubmission)
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
var TaskDeferred = event.NewTopic[TaskDeferredEvent]("schedule.task.deferred")

// TaskCanceledEvent is the payload of [TaskCanceled].
// It reports a successful cancellation request, not proof that work existed or
// that an already-running handler stopped.
//
//	unsubscribe, err := bus.Listen(schedule.TaskCanceled,
//		func(_ context.Context, outcome schedule.TaskCanceledEvent) error {
//			fmt.Println(outcome.ID)
//			return nil
//		})
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
type TaskCanceledEvent struct {
	// ID is the job identifier passed to [Scheduler.Cancel].
	ID string
}

// TaskCanceled is published when [Scheduler.Cancel] returns successfully.
// The driver contract treats an unknown or already finished identifier as
// success and does not report which case occurred, so the event means "the
// cancellation was accepted", not "a pending job was removed".
//
//	unsubscribe, err := bus.Listen(schedule.TaskCanceled, recordCancellation)
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
var TaskCanceled = event.NewTopic[TaskCanceledEvent]("schedule.task.canceled")
