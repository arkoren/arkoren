package queue_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/memoryqueue"
)

// newEventWorker returns a consumer of delivery handled by fn that publishes
// on bus, and the slice JobFailed payloads are appended to.
func newEventWorker(t *testing.T, delivery queue.Delivery, fn func(context.Context, email) error) (*queue.Consumer, *[]queue.JobFailedEvent) {
	t.Helper()

	bus := event.New()
	var failed []queue.JobFailedEvent

	if _, err := bus.Listen(queue.JobFailed, func(_ context.Context, payload queue.JobFailedEvent) error {
		failed = append(failed, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	consumer := queue.NewConsumer(jobConsumerDriver{
		delivery: delivery,
	}, func(consumer *queue.ConsumerBuilder) {
		consumer.Events(bus)
	})

	if err := consumer.Handle(emailJob, fn); err != nil {
		t.Fatal(err)
	}

	return consumer, &failed
}

// TestJobFailedPublishedForExhaustedJob checks terminal notifications include
// delivery identity, attempt count, and the original handler failure.
func TestJobFailedPublishedForExhaustedJob(t *testing.T) {
	t.Parallel()

	cause := errors.New("service unavailable")
	delivery := &jobDelivery{
		message: queue.Message{
			ID:       "m-1",
			Name:     "emails-send",
			Queue:    "default",
			Payload:  []byte(`{}`),
			Attempts: 3,
		},
	}
	consumer, failed := newEventWorker(t, delivery, failWith(cause))

	if err := consumer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(*failed) != 1 {
		t.Fatalf("JobFailed published %d times, want 1", len(*failed))
	}

	got := (*failed)[0]

	if got.ID != "m-1" || got.Name != "emails-send" || got.Attempts != 3 || !errors.Is(got.Err, cause) {
		t.Fatalf("JobFailed = %+v", got)
	}
}

// TestJobFailedPublishedForPermanentError verifies an explicit terminal error
// emits a failure before the ordinary attempt limit is reached.
func TestJobFailedPublishedForPermanentError(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:    "emails-send",
			Payload: []byte(`{}`),
		},
	}
	consumer, failed := newEventWorker(t, delivery, failWith(queue.Permanent(errors.New("bad template"))))

	if err := consumer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(*failed) != 1 || !errors.Is((*failed)[0].Err, queue.ErrPermanent) {
		t.Fatalf("JobFailed = %+v, want one permanent failure", *failed)
	}
}

// TestJobFailedNotPublishedForRetry checks a retryable outcome settles as a
// retry without being reported as a terminal failure.
func TestJobFailedNotPublishedForRetry(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:    "emails-send",
			Payload: []byte(`{}`),
		},
	}
	consumer, failed := newEventWorker(t, delivery, failWith(errors.New("try later")))

	if err := consumer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !delivery.retried || len(*failed) != 0 {
		t.Fatalf("retried = %t, JobFailed = %+v, want a retry and no event", delivery.retried, *failed)
	}
}

// TestJobFailedNotPublishedWhenSettlementFails verifies that the event bus
// reports confirmed outcomes, while Run preserves the broker's failure cause.
func TestJobFailedNotPublishedWhenSettlementFails(t *testing.T) {
	t.Parallel()

	failure := errors.New("broker refused terminal settlement")
	delivery := &jobDelivery{
		message: queue.Message{
			Name:    emailJob.Name(),
			Payload: []byte(`{}`),
		},
		fail: func(context.Context, error) error {
			return failure
		},
	}

	consumer, failed := newEventWorker(t, delivery, failWith(queue.Permanent(errors.New("bad template"))))

	if err := consumer.Run(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("settlement cause lost: %v", err)
	}

	if len(*failed) != 0 {
		t.Fatalf("unconfirmed JobFailed events: %+v", *failed)
	}
}

// collect returns the payloads published on topic through bus.
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

// newBusWorker returns a consumer of delivery that handles job with fn and
// publishes on bus.
func newBusWorker(t *testing.T, bus *event.Bus, delivery queue.Delivery, job queue.Job[email], fn func(context.Context, email) error) *queue.Consumer {
	t.Helper()

	consumer := queue.NewConsumer(jobConsumerDriver{
		delivery: delivery,
	}, func(consumer *queue.ConsumerBuilder) {
		consumer.Events(bus)
	})

	if err := consumer.Handle(job, fn); err != nil {
		t.Fatal(err)
	}

	return consumer
}

// TestJobRetriedPublishedWithBackoffDelay checks retry notifications carry the
// selected policy delay and preserve delivery details and the failure cause.
func TestJobRetriedPublishedWithBackoffDelay(t *testing.T) {
	t.Parallel()

	cause := errors.New("try later")
	bus := event.New()
	retried := collect(t, bus, queue.JobRetried)
	delivery := &jobDelivery{
		message: queue.Message{
			ID:       "m-1",
			Name:     "emails-send",
			Queue:    "emails",
			Payload:  []byte(`{}`),
			Attempts: 1,
		},
	}

	if err := newBusWorker(t, bus, delivery, emailJob, failWith(cause)).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := queue.JobRetriedEvent{
		ID:       "m-1",
		Name:     "emails-send",
		Queue:    "emails",
		Attempts: 1,
		Delay:    2 * time.Minute,
		Err:      cause,
	}

	if len(*retried) != 1 || (*retried)[0] != want {
		t.Fatalf("JobRetried = %+v, want one %+v", *retried, want)
	}
}

// TestJobRetriedCarriesRetryAfterDelay verifies an explicit retry delay replaces
// policy backoff in the emitted retry notification.
func TestJobRetriedCarriesRetryAfterDelay(t *testing.T) {
	t.Parallel()

	bus := event.New()
	retried := collect(t, bus, queue.JobRetried)
	delivery := &jobDelivery{
		message: queue.Message{
			Name:    "emails-send",
			Payload: []byte(`{}`),
		},
	}

	err := newBusWorker(t, bus, delivery, emailJob, failWith(queue.RetryAfter(errors.New("rate limited"), time.Hour))).Run(context.Background())

	if err != nil {
		t.Fatal(err)
	}

	if len(*retried) != 1 || (*retried)[0].Delay != time.Hour {
		t.Fatalf("JobRetried = %+v, want one with a one hour delay", *retried)
	}
}

// TestJobRetriedNotPublishedWhenExhausted checks an exhausted attempt emits only
// the terminal notification, not an additional retry notification.
func TestJobRetriedNotPublishedWhenExhausted(t *testing.T) {
	t.Parallel()

	bus := event.New()
	retried := collect(t, bus, queue.JobRetried)
	failed := collect(t, bus, queue.JobFailed)
	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "emails-send",
			Payload:  []byte(`{}`),
			Attempts: 3,
		},
	}

	if err := newBusWorker(t, bus, delivery, emailJob, failWith(errors.New("down"))).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(*retried) != 0 || len(*failed) != 1 {
		t.Fatalf("JobRetried = %+v, JobFailed = %+v, want only a failure", *retried, *failed)
	}
}

// TestJobRejectedPublished verifies explicit rejection produces its own outcome
// notification with the delivery's identity and original cause.
func TestJobRejectedPublished(t *testing.T) {
	t.Parallel()

	cause := errors.New("invalid recipient")
	bus := event.New()
	rejected := collect(t, bus, queue.JobRejected)
	failed := collect(t, bus, queue.JobFailed)
	delivery := &jobDelivery{
		message: queue.Message{
			ID:       "m-2",
			Name:     "emails-send",
			Queue:    "emails",
			Payload:  []byte(`{}`),
			Attempts: 2,
		},
	}

	if err := newBusWorker(t, bus, delivery, emailJob, failWith(queue.Reject(cause))).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(*rejected) != 1 || len(*failed) != 0 {
		t.Fatalf("JobRejected = %+v, JobFailed = %+v, want one rejection only", *rejected, *failed)
	}

	got := (*rejected)[0]

	if got.ID != "m-2" || got.Name != "emails-send" || got.Queue != "emails" || got.Attempts != 2 || !errors.Is(got.Err, cause) {
		t.Fatalf("JobRejected = %+v", got)
	}
}

// TestJobTimedOutPublishedBeforeRetry checks an execution deadline produces
// timeout details and a retry notification using virtual time.
func TestJobTimedOutPublishedBeforeRetry(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		bus := event.New()
		timedOut := collect(t, bus, queue.JobTimedOut)
		retried := collect(t, bus, queue.JobRetried)
		delivery := &jobDelivery{
			message: queue.Message{
				ID:       "m-3",
				Name:     "emails-send",
				Queue:    "emails",
				Payload:  []byte(`{}`),
				Attempts: 1,
			},
		}

		job := queue.NewJob[email]("emails-send", emailPolicy, func(job *queue.JobBuilder) {
			job.Timeout(time.Second)
		})

		err := newBusWorker(t, bus, delivery, job, func(ctx context.Context, _ email) error {
			<-ctx.Done()

			return ctx.Err()
		}).Run(context.Background())

		if err != nil {
			t.Fatal(err)
		}

		want := queue.JobTimedOutEvent{
			ID:       "m-3",
			Name:     "emails-send",
			Queue:    "emails",
			Attempts: 1,
			Timeout:  time.Second,
		}

		if len(*timedOut) != 1 || (*timedOut)[0] != want || len(*retried) != 1 {
			t.Fatalf("JobTimedOut = %+v, JobRetried = %+v, want one %+v and one retry", *timedOut, *retried, want)
		}
	})
}

// TestJobTimedOutNotPublishedForOrdinaryFailure verifies a handler error that
// occurs before the deadline is not classified as an execution timeout.
func TestJobTimedOutNotPublishedForOrdinaryFailure(t *testing.T) {
	t.Parallel()

	bus := event.New()
	timedOut := collect(t, bus, queue.JobTimedOut)
	delivery := &jobDelivery{
		message: queue.Message{
			Name:    "emails-send",
			Payload: []byte(`{}`),
		},
	}

	job := queue.NewJob[email]("emails-send", emailPolicy, func(job *queue.JobBuilder) {
		job.Timeout(time.Hour)
	})

	if err := newBusWorker(t, bus, delivery, job, failWith(errors.New("boom"))).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(*timedOut) != 0 {
		t.Fatalf("JobTimedOut = %+v, want none", *timedOut)
	}
}

// TestSuccessfulJobPublishesNothing checks acknowledgement does not emit any
// retry, rejection, failure, or timeout notification.
func TestSuccessfulJobPublishesNothing(t *testing.T) {
	t.Parallel()

	bus := event.New()
	retried := collect(t, bus, queue.JobRetried)
	rejected := collect(t, bus, queue.JobRejected)
	timedOut := collect(t, bus, queue.JobTimedOut)
	failed := collect(t, bus, queue.JobFailed)
	delivery := &jobDelivery{
		message: queue.Message{
			Name:    "emails-send",
			Payload: []byte(`{}`),
		},
	}

	job := queue.NewJob[email]("emails-send", emailPolicy, func(job *queue.JobBuilder) {
		job.Timeout(time.Hour)
	})

	if err := newBusWorker(t, bus, delivery, job, failWith(nil)).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !delivery.acknowledged || len(*retried)+len(*rejected)+len(*timedOut)+len(*failed) != 0 {
		t.Fatalf("acknowledged = %t, events = %v %v %v %v, want an ack and no events", delivery.acknowledged, *retried, *rejected, *timedOut, *failed)
	}
}

// TestJobEventsWithoutBusStillSettle verifies omitting the event bus leaves
// retry settlement functional.
func TestJobEventsWithoutBusStillSettle(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:    "emails-send",
			Payload: []byte(`{}`),
		},
	}
	consumer := newEmailWorker(t, delivery, failWith(errors.New("try later")))

	if err := consumer.Run(context.Background()); err != nil || !delivery.retried {
		t.Fatalf("err = %v, retried = %t, want a retry", err, delivery.retried)
	}
}

// TestConsumerEventsCanRestoreNil checks later callbacks can disable an earlier
// event policy without changing permanent-failure settlement.
func TestConsumerEventsCanRestoreNil(t *testing.T) {
	t.Parallel()

	bus := event.New()

	if _, err := bus.Listen(queue.JobFailed, func(context.Context, queue.JobFailedEvent) error {
		t.Error("disabled events bus received a failure")

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	delivery := &jobDelivery{
		message: queue.Message{
			Name:    emailJob.Name(),
			Payload: []byte(`{}`),
		},
	}
	consumer := queue.NewConsumer(jobConsumerDriver{
		delivery: delivery,
	}, func(consumer *queue.ConsumerBuilder) {
		consumer.Events(bus)
	}, func(consumer *queue.ConsumerBuilder) {
		consumer.Events(nil)
	})

	if err := consumer.Handle(emailJob, failWith(queue.Permanent(errors.New("failed")))); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	if delivery.failed == nil {
		t.Fatal("failure was not settled")
	}
}

// TestShutdownRetryRetainsReportedAttemptCount characterizes the existing
// protocol: shutdown bypasses the immediate cap but does not discount the
// driver's count when an ordinary failure arrives on a later delivery.
func TestShutdownRetryRetainsReportedAttemptCount(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryqueue.New()
		job := queue.NewJob[string]("send", func(job *queue.JobBuilder) {
			job.Retry(2)
		})

		first := queue.NewConsumer(driver)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		if err := first.Handle(job, func(ctx context.Context, _ string) error {
			cancel()

			return ctx.Err()
		}); err != nil {
			t.Fatal(err)
		}

		if err := queue.NewDispatcher(driver).Dispatch(ctx, job, "hello"); err != nil {
			t.Fatal(err)
		}

		if err := first.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("first Run = %v", err)
		}

		ctx, stop := context.WithCancel(t.Context())
		t.Cleanup(stop)

		bus := event.New()
		failed := make(chan queue.JobFailedEvent, 1)

		if _, err := bus.Listen(queue.JobFailed, func(_ context.Context, outcome queue.JobFailedEvent) error {
			failed <- outcome
			stop()

			return nil
		}); err != nil {
			t.Fatal(err)
		}

		second := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
			consumer.Events(bus)
		})

		cause := errors.New("ordinary failure")

		if err := second.Handle(job, func(context.Context, string) error {
			return cause
		}); err != nil {
			t.Fatal(err)
		}

		if err := second.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("second Run = %v", err)
		}

		if outcome := <-failed; outcome.Attempts != 2 || !errors.Is(outcome.Err, cause) {
			t.Fatalf("terminal outcome = %+v, want ordinary failure at delivery 2", outcome)
		}
	})
}
