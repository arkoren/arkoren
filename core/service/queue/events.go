package queue

import (
	"time"

	"arkoren.dev/core/event"
)

// JobFailedEvent is the payload of [JobFailed].
// It reports an accepted terminal settlement without carrying job contents.
// Error causes remain shared application values, not sanitized wire messages.
//
//	unsubscribe, err := bus.Listen(queue.JobFailed, func(_ context.Context, outcome queue.JobFailedEvent) error {
//		fmt.Println(outcome.Name, outcome.Attempts)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
type JobFailedEvent struct {
	// ID is the message identifier.
	ID string

	// Name is the job name.
	Name string

	// Queue is the queue the message was consumed from.
	Queue string

	// Attempts is the driver's reported delivery count, or zero when unavailable.
	Attempts int

	// Err is the reason: the job's error, a [Permanent] error, a payload
	// decode error, or [ErrUnknownName].
	Err error
}

// JobFailed is published when a [Consumer] fails a delivery for good: the job
// returned a [Permanent] error or exhausted its retries, the payload did not
// decode, or no handler took the name within the unknown-name attempts.
// Retries and rejections publish [JobRetried] and [JobRejected] instead.
// A failed terminal settlement publishes nothing; the broker outcome is not
// confirmed, and the settlement error is returned through Consumer.Run.
//
//	unsubscribe, err := bus.Listen(queue.JobFailed, recordFailure)
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
var JobFailed = event.NewTopic[JobFailedEvent]("queue.job.failed")

// JobRetriedEvent is the payload of [JobRetried].
// Delay is a request to the driver, not proof of an exact future execution time.
//
//	unsubscribe, err := bus.Listen(queue.JobRetried, func(_ context.Context, outcome queue.JobRetriedEvent) error {
//		fmt.Println(outcome.Name, outcome.Delay)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
type JobRetriedEvent struct {
	// ID is the message identifier.
	ID string

	// Name is the job name.
	Name string

	// Queue is the queue the message was consumed from.
	Queue string

	// Attempts is the driver's reported delivery count, or zero when unavailable.
	Attempts int

	// Delay is how long the driver was asked to wait before redelivering:
	// the [RetryAfter] delay, the job's backoff, or zero.
	Delay time.Duration

	// Err is the job error that caused the retry.
	Err error
}

// JobRetried is published when a [Consumer] hands a failed delivery back to
// the driver for another attempt and the driver accepted it. A timed-out
// attempt that is retried publishes [JobTimedOut] and then JobRetried. It is
// not published when the retries are exhausted ([JobFailed] is), when the
// consumer stops mid-job and releases the delivery, or for the driver's own
// redelivery of an unknown name.
//
//	unsubscribe, err := bus.Listen(queue.JobRetried, recordRetry)
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
var JobRetried = event.NewTopic[JobRetriedEvent]("queue.job.retried")

// JobRejectedEvent is the payload of [JobRejected].
// It describes the facade's accepted rejection, not a broker's dead-letter state.
//
//	unsubscribe, err := bus.Listen(queue.JobRejected, func(_ context.Context, outcome queue.JobRejectedEvent) error {
//		fmt.Println(outcome.Name, outcome.Attempts)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
type JobRejectedEvent struct {
	// ID is the message identifier.
	ID string

	// Name is the job name.
	Name string

	// Queue is the queue the message was consumed from.
	Queue string

	// Attempts is the driver's reported delivery count, or zero when unavailable.
	Attempts int

	// Err is the [Reject] error the job returned.
	Err error
}

// JobRejected is published when a job returns a [Reject] error and the driver
// accepted the rejection, which drops the message without retrying and
// without failing it. Rejection is a deliberate outcome, so [JobFailed] is
// not published as well.
//
//	unsubscribe, err := bus.Listen(queue.JobRejected, recordRejection)
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
var JobRejected = event.NewTopic[JobRejectedEvent]("queue.job.rejected")

// JobTimedOutEvent is the payload of [JobTimedOut].
// Timeout describes the elapsed attempt deadline, before settlement is attempted.
//
//	unsubscribe, err := bus.Listen(queue.JobTimedOut, func(_ context.Context, outcome queue.JobTimedOutEvent) error {
//		fmt.Println(outcome.Name, outcome.Timeout)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
type JobTimedOutEvent struct {
	// ID is the message identifier.
	ID string

	// Name is the job name.
	Name string

	// Queue is the queue the message was consumed from.
	Queue string

	// Attempts is the driver's reported delivery count, or zero when unavailable.
	Attempts int

	// Timeout is the job's per-attempt limit that elapsed.
	Timeout time.Duration
}

// JobTimedOut is published when an attempt returned an error after the job's
// [JobBuilder.Timeout] elapsed. It describes the attempt and is published before the
// attempt is settled. Accepted settlement is followed by [JobRetried] when the job retries
// or by [JobFailed] when it is out of attempts (or by [JobRejected] if the
// job turned the error into a rejection). An attempt that finishes
// successfully after its deadline publishes nothing.
// A settlement error can prevent any subsequent settlement notification.
//
//	unsubscribe, err := bus.Listen(queue.JobTimedOut, recordTimeout)
//	if err != nil {
//		return err
//	}
//	compose.Defer(unsubscribe)
var JobTimedOut = event.NewTopic[JobTimedOutEvent]("queue.job.timed-out")
