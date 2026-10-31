package queue_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/memoryqueue"
)

// TestHandleCapturesPublishedWorkerPolicy proves retained draft edits after
// registration cannot alter retry attempts or the backoff function used at run.
func TestHandleCapturesPublishedWorkerPolicy(t *testing.T) {
	t.Parallel()

	var draft *queue.JobBuilder
	definition := queue.NewJob[email]("email", emailPolicy, func(job *queue.JobBuilder) {
		draft = job
	})

	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "email",
			Payload:  []byte(`{}`),
			Attempts: 2,
		},
	}
	consumer := queue.NewConsumer(jobConsumerDriver{
		delivery: delivery,
	})

	if err := consumer.Handle(definition, failWith(errors.New("retry"))); err != nil {
		t.Fatal(err)
	}

	draft.Retry(1).BackoffFunc(nil)

	if err := consumer.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !delivery.retried || delivery.retryDelay != 3*time.Minute {
		t.Fatalf("settlement = %+v", delivery)
	}
}

// TestHandleCapturesPublishedTimeout uses virtual time to prove a retained
// draft cannot remove the attempt timeout after registration.
func TestHandleCapturesPublishedTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var draft *queue.JobBuilder
		definition := queue.NewJob[email]("email", func(job *queue.JobBuilder) {
			draft = job
			job.Timeout(time.Second).Retry(1)
		})

		delivery := &jobDelivery{
			message: queue.Message{
				Name:     "email",
				Payload:  []byte(`{}`),
				Attempts: 1,
			},
		}
		consumer := queue.NewConsumer(jobConsumerDriver{
			delivery: delivery,
		})

		if err := consumer.Handle(definition, func(ctx context.Context, _ email) error {
			<-ctx.Done()

			return ctx.Err()
		}); err != nil {
			t.Fatal(err)
		}

		draft.Timeout(0)

		if err := consumer.Run(t.Context()); err != nil {
			t.Fatal(err)
		}

		if !errors.Is(delivery.failed, context.DeadlineExceeded) {
			t.Fatalf("failure = %v", delivery.failed)
		}
	})
}

// email is the payload of the email job.
type email struct {
	// ID identifies the payload for JSON serialization.
	ID string `json:"id"`
}

// emailJob is the job under test, with a linear backoff policy.
var emailJob = queue.NewJob[email]("emails-send", emailPolicy)

// emailPolicy is reusable worker policy; timeout variants reconstruct a fresh
// definition with this policy followed by a local override.
func emailPolicy(job *queue.JobBuilder) {
	job.OnQueue("emails").Retry(3).BackoffFunc(func(attempt int) time.Duration {
		return time.Duration(attempt) * time.Minute
	})
}

// newEmailWorker returns a consumer whose email handler is fn.
func newEmailWorker(t *testing.T, delivery queue.Delivery, fn func(context.Context, email) error) *queue.Consumer {
	t.Helper()

	consumer := queue.NewConsumer(jobConsumerDriver{
		delivery: delivery,
	})

	if err := consumer.Handle(emailJob, fn); err != nil {
		t.Fatal(err)
	}

	return consumer
}

// failWith returns a handler that always returns err.
func failWith(err error) func(context.Context, email) error {
	return func(context.Context, email) error {
		return err
	}
}

// jobConsumerDriver delivers one fixed message synchronously to isolate facade
// routing and settlement from transport scheduling.
type jobConsumerDriver struct {
	// delivery supplies the message and records the consumer's settlement.
	delivery queue.Delivery
}

// Consume delivers the configured message to the handler.
func (jobConsumerDriver jobConsumerDriver) Consume(ctx context.Context, _ string, handler queue.DeliveryHandler) error {
	return handler(ctx, jobConsumerDriver.delivery)
}

// jobDelivery records settlement outcomes for a single synchronous delivery.
// Every settlement rejects expired contexts, allowing tests to detect a
// settlement budget that was incorrectly spent executing the job.
type jobDelivery struct {
	// message is returned unchanged for consumer routing and payload decoding.
	message queue.Message

	// acknowledge overrides normal acknowledgement recording when non-nil.
	// Tests can block on its context to observe the settlement deadline; its
	// error is returned without changing acknowledged.
	acknowledge func(context.Context) error

	// acknowledged records a successful acknowledgement without an override.
	acknowledged bool

	// retried records whether Retry accepted a live settlement context.
	retried bool

	// retryDelay retains the delay requested by the consumer without waiting.
	retryDelay time.Duration

	// rejected records whether Reject accepted a live settlement context.
	rejected bool

	// failed retains the cause passed to Fail after its context check succeeds.
	failed error

	// fail substitutes a broker settlement failure after context validation.
	// It lets facade tests observe a rejected terminal settlement through Run.
	fail func(context.Context, error) error
}

// Message returns the delivered job message.
func (jobDelivery *jobDelivery) Message() queue.Message {
	return jobDelivery.message
}

// Acknowledge rejects an expired context before recording acknowledgement.
// A configured acknowledge callback receives the live context instead, so a
// test can observe timeout behavior without performing broker I/O.
func (jobDelivery *jobDelivery) Acknowledge(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if jobDelivery.acknowledge != nil {
		return jobDelivery.acknowledge(ctx)
	}

	jobDelivery.acknowledged = true

	return nil
}

// Retry records a retry request and its delay only while ctx is live.
// It does not schedule another attempt; tests using the memory driver cover
// actual redelivery and backoff timing.
func (jobDelivery *jobDelivery) Retry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	jobDelivery.retried = true
	jobDelivery.retryDelay = delay

	return nil
}

// Reject records rejection only while ctx is live. An expired settlement
// context returns its error without changing the recorded outcome.
func (jobDelivery *jobDelivery) Reject(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	jobDelivery.rejected = true

	return nil
}

// Fail retains the failure cause only while ctx is live. An expired
// settlement context returns its own error without recording the job error.
func (jobDelivery *jobDelivery) Fail(ctx context.Context, err error) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if jobDelivery.fail != nil {
		return jobDelivery.fail(ctx, err)
	}

	jobDelivery.failed = err

	return nil
}

// checkJobHasError fails the test when job reports no error.
func checkJobHasError(t *testing.T, job queue.Job[email]) {
	t.Helper()

	if job.Err() == nil {
		t.Error("Err = nil")
	}
}

// checkBackoffDelays fails the test unless settling failed attempts of job
// requests exactly the retry delays in want.
func checkBackoffDelays(t *testing.T, job queue.Job[email], attempts []int, want []time.Duration) {
	t.Helper()

	var got []time.Duration

	for _, attempt := range attempts {
		got = append(got, backoffDelay(t, job, attempt))
	}

	if !slices.Equal(got, want) {
		t.Errorf("delays = %v, want %v", got, want)
	}
}

// backoffDelay settles one failed attempt of job and returns the retry delay
// the consumer requested.
func backoffDelay(t *testing.T, job queue.Job[email], attempt int) time.Duration {
	t.Helper()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:     job.Name(),
			Payload:  []byte(`{}`),
			Attempts: attempt - 1,
		},
	}
	consumer := queue.NewConsumer(jobConsumerDriver{
		delivery: delivery,
	})

	if err := consumer.Handle(job, failWith(errors.New("boom"))); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	return delivery.retryDelay
}

// TestWorkerAcknowledgesSuccessfulJob verifies decoded payloads reach the
// handler and success acknowledges without recording a failure.
func TestWorkerAcknowledgesSuccessfulJob(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:    "emails-send",
			Payload: []byte(`{"id":"email-1"}`),
		},
	}
	got := ""
	consumer := newEmailWorker(t, delivery, func(_ context.Context, payload email) error {
		got = payload.ID

		return nil
	})

	err := consumer.Run(context.Background())

	if err != nil || !delivery.acknowledged || delivery.failed != nil || got != "email-1" {
		t.Fatalf("err = %v, acknowledged = %t, failed = %v, id = %q", err, delivery.acknowledged, delivery.failed, got)
	}
}

// TestWorkerRetriesOrdinaryErrorUsingPolicyBackoff checks the next attempt's
// policy delay is used for a retryable handler failure.
func TestWorkerRetriesOrdinaryErrorUsingPolicyBackoff(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "emails-send",
			Payload:  []byte(`{}`),
			Attempts: 1,
		},
	}
	consumer := newEmailWorker(t, delivery, failWith(errors.New("service unavailable")))

	err := consumer.Run(context.Background())

	if err != nil || !delivery.retried || delivery.retryDelay != 2*time.Minute {
		t.Fatalf("err = %v, retried = %t, delay = %s", err, delivery.retried, delivery.retryDelay)
	}
}

// TestWorkerRetriesExplicitDelay verifies RetryAfter overrides the registered
// backoff policy for that delivery.
func TestWorkerRetriesExplicitDelay(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:    "emails-send",
			Payload: []byte(`{}`),
		},
	}
	consumer := newEmailWorker(t, delivery, failWith(queue.RetryAfter(errors.New("rate limited"), time.Minute)))

	err := consumer.Run(context.Background())

	if err != nil || !delivery.retried || delivery.retryDelay != time.Minute {
		t.Fatalf("err = %v, retried = %t, delay = %s", err, delivery.retried, delivery.retryDelay)
	}
}

// TestWorkerRejectsRejectedJob checks explicit rejection selects the reject
// settlement path instead of retrying or failing the job.
func TestWorkerRejectsRejectedJob(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:    "emails-send",
			Payload: []byte(`{}`),
		},
	}
	consumer := newEmailWorker(t, delivery, failWith(queue.Reject(errors.New("invalid recipient"))))

	err := consumer.Run(context.Background())

	if err != nil || !delivery.rejected {
		t.Fatalf("err = %v, rejected = %t", err, delivery.rejected)
	}
}

// TestWorkerFailsExhaustedJob verifies the last allowed attempt records the
// handler's cause as a terminal failure.
func TestWorkerFailsExhaustedJob(t *testing.T) {
	t.Parallel()

	cause := errors.New("service unavailable")
	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "emails-send",
			Payload:  []byte(`{}`),
			Attempts: 3,
		},
	}
	consumer := newEmailWorker(t, delivery, failWith(cause))

	err := consumer.Run(context.Background())

	if err != nil || !errors.Is(delivery.failed, cause) {
		t.Fatalf("err = %v, failed = %v", err, delivery.failed)
	}
}

// TestWorkerFailsUndecodablePayload checks invalid JSON fails permanently
// without invoking the handler or scheduling a retry.
func TestWorkerFailsUndecodablePayload(t *testing.T) {
	t.Parallel()

	called := false
	delivery := &jobDelivery{
		message: queue.Message{
			Name:    "emails-send",
			Payload: []byte(`"text"`),
		},
	}
	consumer := newEmailWorker(t, delivery, func(context.Context, email) error {
		called = true

		return nil
	})

	err := consumer.Run(context.Background())

	if err != nil || delivery.failed == nil || called || delivery.retried {
		t.Fatalf("err = %v, failed = %v, called = %v, retried = %v", err, delivery.failed, called, delivery.retried)
	}
}

// TestWorkerRetriesUnknownJob verifies an unregistered name below its attempt
// cap is retried to allow later deployments to provide its handler.
func TestWorkerRetriesUnknownJob(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name: "reports-generate",
		},
	}
	consumer := newEmailWorker(t, delivery, failWith(nil))

	err := consumer.Run(context.Background())

	if err != nil || !delivery.retried || delivery.failed != nil {
		t.Fatalf("err = %v, retried = %v, failed = %v", err, delivery.retried, delivery.failed)
	}
}

// TestWorkerFailsUnknownJobAtAttemptCap checks unregistered jobs become terminal
// with ErrUnknownName when the default attempt cap is reached.
func TestWorkerFailsUnknownJobAtAttemptCap(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "reports-generate",
			Attempts: 10,
		},
	}
	consumer := newEmailWorker(t, delivery, failWith(nil))

	err := consumer.Run(context.Background())

	if err != nil || delivery.retried || !errors.Is(delivery.failed, queue.ErrUnknownName) {
		t.Fatalf("err = %v, retried = %v, failed = %v", err, delivery.retried, delivery.failed)
	}
}

// TestUnknownJobRetriesDuringShutdown checks the separate unknown-name path
// still detaches settlement from a canceled delivery context. The handler must
// not run, and the unknown-name retry delay must remain unchanged.
func TestUnknownJobRetriesDuringShutdown(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name: "reports-generate",
		},
	}
	consumer := newEmailWorker(t, delivery, func(context.Context, email) error {
		t.Error("handler ran for an unknown job")

		return nil
	})

	if err := consumer.Run(ctx); err != nil || !delivery.retried || delivery.retryDelay != 30*time.Second || delivery.failed != nil {
		t.Fatalf("Run = %v, retried = %t, delay = %s, failed = %v", err, delivery.retried, delivery.retryDelay, delivery.failed)
	}
}

// TestUnknownJobFailsDuringShutdown checks an unknown job at its attempt cap
// still fails with ErrUnknownName when the delivery context is canceled.
// Shutdown must not turn that terminal outcome into a retry or settlement error.
func TestUnknownJobFailsDuringShutdown(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "reports-generate",
			Attempts: 10,
		},
	}
	consumer := newEmailWorker(t, delivery, failWith(nil))

	if err := consumer.Run(ctx); err != nil || delivery.retried || !errors.Is(delivery.failed, queue.ErrUnknownName) {
		t.Fatalf("Run = %v, retried = %t, failed = %v", err, delivery.retried, delivery.failed)
	}
}

// TestWorkerUnknownJobCapIsConfigurable verifies the consumer's configured
// unknown-name cap replaces the default terminal-attempt threshold.
func TestWorkerUnknownJobCapIsConfigurable(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "reports-generate",
			Attempts: 3,
		},
	}
	consumer := queue.NewConsumer(jobConsumerDriver{
		delivery: delivery,
	}, func(consumer *queue.ConsumerBuilder) {
		consumer.UnknownNameAttempts(3)
	})

	if err := consumer.Handle(emailJob, failWith(nil)); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Run(context.Background()); err != nil || !errors.Is(delivery.failed, queue.ErrUnknownName) {
		t.Fatalf("err = %v, failed = %v", err, delivery.failed)
	}
}

// TestWorkerRetriesUnknownJobForeverWhenCapNegative checks a negative cap
// disables terminal unknown-name settlement even at a large attempt count.
func TestWorkerRetriesUnknownJobForeverWhenCapNegative(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "reports-generate",
			Attempts: 1000,
		},
	}
	consumer := queue.NewConsumer(jobConsumerDriver{
		delivery: delivery,
	}, func(consumer *queue.ConsumerBuilder) {
		consumer.UnknownNameAttempts(-1)
	})

	if err := consumer.Handle(emailJob, failWith(nil)); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Run(context.Background()); err != nil || !delivery.retried || delivery.failed != nil {
		t.Fatalf("err = %v, retried = %v, failed = %v", err, delivery.retried, delivery.failed)
	}
}

// TestWorkerRejectsDuplicateHandler verifies a second registration for the same
// job name returns ErrDuplicateName.
func TestWorkerRejectsDuplicateHandler(t *testing.T) {
	t.Parallel()

	consumer := queue.NewConsumer(jobConsumerDriver{})

	if err := consumer.Handle(emailJob, failWith(nil)); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Handle(emailJob, failWith(nil)); !errors.Is(err, queue.ErrDuplicateName) {
		t.Fatalf("error = %v, want ErrDuplicateName", err)
	}
}

// TestWorkerRejectsNilHandler checks registration rejects a missing execution
// function before it can be invoked by a delivery.
func TestWorkerRejectsNilHandler(t *testing.T) {
	t.Parallel()

	consumer := queue.NewConsumer(jobConsumerDriver{})

	if err := consumer.Handle(emailJob, nil); err == nil {
		t.Fatal("expected error")
	}
}

// TestWorkerRetriesJobInterruptedByShutdown verifies cancellation during an
// exhausted attempt retries instead of treating shutdown as a terminal failure.
func TestWorkerRetriesJobInterruptedByShutdown(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "emails-send",
			Payload:  []byte(`{}`),
			Attempts: 3,
		},
	}
	consumer := newEmailWorker(t, delivery, func(ctx context.Context, _ email) error {
		cancel()

		return ctx.Err()
	})

	_ = consumer.Run(ctx)

	if !delivery.retried || delivery.failed != nil {
		t.Fatalf("retried = %v, failed = %v, want retry without failing", delivery.retried, delivery.failed)
	}
}

// TestWorkerRecoversHandlerPanic checks recovered execution panics follow the
// ordinary retry settlement path.
func TestWorkerRecoversHandlerPanic(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "emails-send",
			Payload:  []byte(`{}`),
			Attempts: 1,
		},
	}
	consumer := newEmailWorker(t, delivery, func(context.Context, email) error {
		panic("boom")
	})

	if err := consumer.Run(context.Background()); err != nil || !delivery.retried {
		t.Fatalf("err = %v, retried = %v", err, delivery.retried)
	}
}

// TestWorkerClampsNegativeDelay checks an explicit negative retry delay is
// normalized to zero before being passed to the delivery.
func TestWorkerClampsNegativeDelay(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:    "emails-send",
			Payload: []byte(`{}`),
		},
	}
	consumer := newEmailWorker(t, delivery, failWith(queue.RetryAfter(errors.New("later"), -time.Minute)))

	if err := consumer.Run(context.Background()); err != nil || delivery.retryDelay != 0 {
		t.Fatalf("err = %v, delay = %v, want 0", err, delivery.retryDelay)
	}
}

// TestWorkerFailsPermanentError verifies a terminal marker bypasses remaining
// retry attempts and preserves its underlying cause.
func TestWorkerFailsPermanentError(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "emails-send",
			Payload:  []byte(`{}`),
			Attempts: 1,
		},
	}
	consumer := newEmailWorker(t, delivery, failWith(queue.Permanent(errors.New("bad template"))))

	if err := consumer.Run(context.Background()); err != nil || delivery.retried || !errors.Is(delivery.failed, queue.ErrPermanent) {
		t.Fatalf("err = %v, retried = %t, failed = %v", err, delivery.retried, delivery.failed)
	}
}

// TestWorkerTimeoutCountsAsFailedAttempt checks an execution deadline uses the
// normal attempt budget and backoff rather than the shutdown retry path.
func TestWorkerTimeoutCountsAsFailedAttempt(t *testing.T) {
	t.Parallel()

	job := queue.NewJob[email]("emails-send", emailPolicy, func(job *queue.JobBuilder) {
		job.Timeout(time.Millisecond)
	})

	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "emails-send",
			Payload:  []byte(`{}`),
			Attempts: 3,
		},
	}
	consumer := queue.NewConsumer(jobConsumerDriver{
		delivery: delivery,
	})

	err := consumer.Handle(job, func(ctx context.Context, _ email) error {
		<-ctx.Done()

		return ctx.Err()
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := consumer.Run(context.Background()); err != nil || delivery.retried || delivery.failed == nil {
		t.Fatalf("err = %v, retried = %t, failed = %v, want a counted failure at the attempt limit", err, delivery.retried, delivery.failed)
	}
}

// TestLongSuccessfulJobAcknowledges checks a handler can outlast 30 seconds
// without spending its acknowledgement budget. The memory driver must accept
// that settlement and continue to the next queued job.
func TestLongSuccessfulJobAcknowledges(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryqueue.New()
		t.Cleanup(func() {
			if err := driver.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		dispatcher := queue.NewDispatcher(driver)
		consumer := queue.NewConsumer(driver)
		var handled []string

		if err := consumer.Handle(emailJob, func(_ context.Context, payload email) error {
			if payload.ID == "first" {
				time.Sleep(40 * time.Second)
			} else {
				cancel()
			}

			handled = append(handled, payload.ID)

			return nil
		}); err != nil {
			t.Fatal(err)
		}

		if err := dispatcher.Dispatch(ctx, emailJob, email{
			ID: "first",
		}); err != nil {
			t.Fatal(err)
		}

		if err := dispatcher.Dispatch(ctx, emailJob, email{
			ID: "second",
		}); err != nil {
			t.Fatal(err)
		}

		if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled after both jobs", err)
		}

		if !slices.Equal(handled, []string{"first", "second"}) {
			t.Fatalf("handled = %v, want first and second", handled)
		}
	})
}

// TestLongRetryableJobRetries checks a slow ordinary failure can still request
// redelivery through the memory driver. Fake time verifies the configured
// backoff starts after execution, without introducing a real-time wait.
func TestLongRetryableJobRetries(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryqueue.New()
		t.Cleanup(func() {
			if err := driver.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		job := queue.NewJob[email]("retry", func(job *queue.JobBuilder) {
			job.Retry(2).Backoff(2*time.Second, 1, 0)
		})

		dispatcher := queue.NewDispatcher(driver)
		consumer := queue.NewConsumer(driver)
		calls := 0

		if err := consumer.Handle(job, func(context.Context, email) error {
			calls++

			if calls == 1 {
				time.Sleep(40 * time.Second)

				return errors.New("service unavailable")
			}

			cancel()

			return nil
		}); err != nil {
			t.Fatal(err)
		}

		if err := dispatcher.Dispatch(ctx, job, email{}); err != nil {
			t.Fatal(err)
		}

		startedAt := time.Now()

		if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled after retry", err)
		}

		if calls != 2 || time.Since(startedAt) != 42*time.Second {
			t.Fatalf("calls = %d, elapsed = %s, want 2 calls in 42s", calls, time.Since(startedAt))
		}
	})
}

// TestLongPermanentJobFails checks a slow permanent failure receives a live
// settlement context and retains its cause. It must fail rather than retry,
// even though the handler ran longer than the settlement timeout.
func TestLongPermanentJobFails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cause := errors.New("invalid template")
		delivery := &jobDelivery{
			message: queue.Message{
				Name:     emailJob.Name(),
				Payload:  []byte(`{}`),
				Attempts: 1,
			},
		}
		consumer := newEmailWorker(t, delivery, func(context.Context, email) error {
			time.Sleep(40 * time.Second)

			return queue.Permanent(cause)
		})

		if err := consumer.Run(context.Background()); err != nil {
			t.Fatal(err)
		}

		if !errors.Is(delivery.failed, cause) || !errors.Is(delivery.failed, queue.ErrPermanent) || delivery.retried {
			t.Fatalf("failed = %v, retried = %t, want permanent failure", delivery.failed, delivery.retried)
		}
	})
}

// TestLongRejectedJobRejects checks an explicit rejection still settles after
// a handler outlasts 30 seconds. The context-checking delivery distinguishes
// an accepted rejection from a settlement attempted with an expired context.
func TestLongRejectedJobRejects(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		delivery := &jobDelivery{
			message: queue.Message{
				Name:    emailJob.Name(),
				Payload: []byte(`{}`),
			},
		}
		consumer := newEmailWorker(t, delivery, func(context.Context, email) error {
			time.Sleep(40 * time.Second)

			return queue.Reject(errors.New("invalid recipient"))
		})

		if err := consumer.Run(context.Background()); err != nil || !delivery.rejected {
			t.Fatalf("Run = %v, rejected = %t", err, delivery.rejected)
		}
	})
}

// TestLongExhaustedJobFails checks the attempt cap still produces terminal
// failure after a slow ordinary error. A fresh settlement budget must not
// reset or bypass the job's retry policy.
func TestLongExhaustedJobFails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cause := errors.New("service unavailable")
		delivery := &jobDelivery{
			message: queue.Message{
				Name:     emailJob.Name(),
				Payload:  []byte(`{}`),
				Attempts: 3,
			},
		}
		consumer := newEmailWorker(t, delivery, func(context.Context, email) error {
			time.Sleep(40 * time.Second)

			return cause
		})

		if err := consumer.Run(context.Background()); err != nil || !errors.Is(delivery.failed, cause) || delivery.retried {
			t.Fatalf("Run = %v, failed = %v, retried = %t", err, delivery.failed, delivery.retried)
		}
	})
}

// TestLongPanickingJobRetries checks panic recovery finishes before the
// settlement budget starts. The recovered failure must request the same
// policy backoff as an ordinary failed attempt.
func TestLongPanickingJobRetries(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		delivery := &jobDelivery{
			message: queue.Message{
				Name:     emailJob.Name(),
				Payload:  []byte(`{}`),
				Attempts: 1,
			},
		}
		consumer := newEmailWorker(t, delivery, func(context.Context, email) error {
			time.Sleep(40 * time.Second)
			panic("boom")
		})

		if err := consumer.Run(context.Background()); err != nil || !delivery.retried || delivery.retryDelay != 2*time.Minute {
			t.Fatalf("Run = %v, retried = %t, delay = %s", err, delivery.retried, delivery.retryDelay)
		}
	})
}

// TestLongTimedOutJobFails checks the per-attempt timeout can exceed the
// settlement timeout and still cause terminal failure at the attempt cap.
// Handler cancellation must remain active while settlement stays detached.
func TestLongTimedOutJobFails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		job := queue.NewJob[email]("emails-send", emailPolicy, func(job *queue.JobBuilder) {
			job.Timeout(40 * time.Second)
		})

		delivery := &jobDelivery{
			message: queue.Message{
				Name:     job.Name(),
				Payload:  []byte(`{}`),
				Attempts: 3,
			},
		}
		consumer := queue.NewConsumer(jobConsumerDriver{
			delivery: delivery,
		})

		if err := consumer.Handle(job, func(ctx context.Context, _ email) error {
			<-ctx.Done()

			return ctx.Err()
		}); err != nil {
			t.Fatal(err)
		}

		if err := consumer.Run(context.Background()); err != nil || !errors.Is(delivery.failed, context.DeadlineExceeded) || delivery.retried {
			t.Fatalf("Run = %v, failed = %v, retried = %t", err, delivery.failed, delivery.retried)
		}
	})
}

// slowInvalidPayload simulates decoding that outlasts the settlement timeout.
// Use it only inside a synctest bubble to avoid a real 40-second decoding wait.
type slowInvalidPayload struct{}

// UnmarshalJSON always rejects the payload after sleeping for 40 seconds;
// synctest makes that wait virtual in the decoding regression. This exercises
// failures before the application handler runs, which must receive the same
// fresh settlement budget as handler outcomes.
func (*slowInvalidPayload) UnmarshalJSON([]byte) error {
	time.Sleep(40 * time.Second)

	return errors.New("invalid payload")
}

// TestLongUndecodableJobFails checks decoding cannot spend the settlement
// budget before a payload is declared invalid. The handler must remain
// uncalled, and the undecodable payload must fail rather than be retried.
func TestLongUndecodableJobFails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		job := queue.NewJob[slowInvalidPayload]("decode")
		delivery := &jobDelivery{
			message: queue.Message{
				Name:    job.Name(),
				Payload: []byte(`{}`),
			},
		}
		consumer := queue.NewConsumer(jobConsumerDriver{
			delivery: delivery,
		})
		called := false

		if err := consumer.Handle(job, func(context.Context, slowInvalidPayload) error {
			called = true

			return nil
		}); err != nil {
			t.Fatal(err)
		}

		if err := consumer.Run(context.Background()); err != nil {
			t.Fatal(err)
		}

		if called || delivery.failed == nil || !strings.Contains(delivery.failed.Error(), "decode job payload") || delivery.retried {
			t.Fatalf("called = %t, failed = %v, retried = %t", called, delivery.failed, delivery.retried)
		}
	})
}

// TestShutdownRetryUsesLiveSettlementContext checks a consumer deadline after
// 40 seconds still permits an immediate retry at the job's attempt cap.
// Consumer cancellation bypasses the immediate cap and does not spend the
// detached settlement budget. A driver's later delivery count still advances.
func TestShutdownRetryUsesLiveSettlementContext(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		t.Cleanup(cancel)

		delivery := &jobDelivery{
			message: queue.Message{
				Name:     emailJob.Name(),
				Payload:  []byte(`{}`),
				Attempts: 3,
			},
		}
		consumer := newEmailWorker(t, delivery, func(ctx context.Context, _ email) error {
			<-ctx.Done()

			return ctx.Err()
		})

		if err := consumer.Run(ctx); err != nil || !delivery.retried || delivery.retryDelay != 0 || delivery.failed != nil {
			t.Fatalf("Run = %v, retried = %t, delay = %s, failed = %v", err, delivery.retried, delivery.retryDelay, delivery.failed)
		}
	})
}

// TestConsumerLateHandlerUsesActiveQueue verifies that Run snapshots queue
// discovery, not the handler map used by subsequent deliveries on those queues.
func TestConsumerLateHandlerUsesActiveQueue(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryqueue.New()
		consumer := queue.NewConsumer(driver)

		if err := consumer.Handle(queue.NewJob[string]("initial"), func(context.Context, string) error {
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		done := make(chan error, 1)

		go func() {
			done <- consumer.Run(ctx)
		}()

		synctest.Wait()

		late := queue.NewJob[string]("late")
		called := false

		if err := consumer.Handle(late, func(context.Context, string) error {
			called = true
			cancel()

			return nil
		}); err != nil {
			t.Fatal(err)
		}

		if err := queue.NewDispatcher(driver).Dispatch(ctx, late, "hello"); err != nil {
			t.Fatal(err)
		}

		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}

		if !called {
			t.Fatal("late handler did not run on the existing queue")
		}
	})
}

// TestSettlementOperationStillTimesOut checks detaching settlement does not
// remove its bound. A blocked acknowledgement must receive exactly 30 seconds
// after a slow handler finishes, then return the settlement deadline error.
func TestSettlementOperationStillTimesOut(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var elapsed time.Duration
		delivery := &jobDelivery{
			message: queue.Message{
				Name:    emailJob.Name(),
				Payload: []byte(`{}`),
			},
			acknowledge: func(ctx context.Context) error {
				startedAt := time.Now()
				<-ctx.Done()

				elapsed = time.Since(startedAt)

				return ctx.Err()
			},
		}

		consumer := newEmailWorker(t, delivery, func(context.Context, email) error {
			time.Sleep(40 * time.Second)

			return nil
		})

		if err := consumer.Run(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run = %v, want context.DeadlineExceeded", err)
		}

		if elapsed != 30*time.Second {
			t.Fatalf("settlement elapsed = %s, want 30s after handler completion", elapsed)
		}
	})
}

// TestZeroJobHandleRejectsBeforeRegistration checks that rejection neither
// invokes the handler nor leaves a registration that Run could consume.
func TestZeroJobHandleRejectsBeforeRegistration(t *testing.T) {
	t.Parallel()

	var job queue.Job[email]
	driver := &recordingDriver{
		failQueue: "",
	}
	consumer := queue.NewConsumer(driver)

	err := consumer.Handle(job, func(context.Context, email) error {
		t.Error("zero job handler ran")

		return nil
	})

	if !errors.Is(err, queue.ErrInvalidName) {
		t.Fatalf("Handle error = %v, want ErrInvalidName", err)
	}

	if err := consumer.Run(context.Background()); !errors.Is(err, queue.ErrNoQueues) {
		t.Fatalf("Run error = %v, want ErrNoQueues", err)
	}

	if len(driver.queues) != 0 {
		t.Fatalf("Consume called for %v", driver.queues)
	}
}

// marshalProbe records whether dispatch reached payload serialization.
type marshalProbe struct {
	// calls points to the serialization count owned by the test.
	calls *int
}

// MarshalJSON records encoding and returns an empty JSON object.
// For example, json.Marshal(marshalProbe{calls: &calls}) increments calls.
func (probe marshalProbe) MarshalJSON() ([]byte, error) {
	(*probe.calls)++

	return []byte("{}"), nil
}

// recordingDriver records the queues Consume is called for and fails the
// queue named failQueue.
type recordingDriver struct {
	// failQueue is the queue whose Consume fails immediately.
	failQueue string

	// mu guards queues.
	mu sync.Mutex

	// queues are the queues consumed so far.
	queues []string
}

// Consume records queue and blocks until ctx ends, unless queue is failQueue.
func (driver *recordingDriver) Consume(ctx context.Context, queueName string, _ queue.DeliveryHandler) error {
	driver.mu.Lock()

	driver.queues = append(driver.queues, queueName)
	driver.mu.Unlock()

	if queueName == driver.failQueue {
		return errors.New("consume failed")
	}

	<-ctx.Done()

	return ctx.Err()
}

// TestRunConsumesEveryHandledQueue checks the consumer starts each distinct
// registered queue and stops all of them when its context is canceled.
func TestRunConsumesEveryHandledQueue(t *testing.T) {
	t.Parallel()

	driver := &recordingDriver{}
	consumer := queue.NewConsumer(driver)

	for _, job := range []queue.Job[email]{
		queue.NewJob[email]("a", func(job *queue.JobBuilder) {
			job.OnQueue("one")
		}),
		queue.NewJob[email]("b", func(job *queue.JobBuilder) {
			job.OnQueue("one")
		}),
		queue.NewJob[email]("c", func(job *queue.JobBuilder) {
			job.OnQueue("two")
		}),
	} {
		if err := consumer.Handle(job, failWith(nil)); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := consumer.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want deadline exceeded", err)
	}

	slices.Sort(driver.queues)

	if !slices.Equal(driver.queues, []string{"one", "two"}) {
		t.Fatalf("queues = %v, want [one two]", driver.queues)
	}
}

// TestRunRestrictsToWithQueues checks configured queue restrictions exclude
// other handled queues without starting unwanted consume loops.
func TestRunRestrictsToWithQueues(t *testing.T) {
	t.Parallel()

	driver := &recordingDriver{}
	consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
		consumer.Queues("two", "absent")
	})

	jobs := []queue.Job[email]{
		queue.NewJob[email]("a", func(job *queue.JobBuilder) {
			job.OnQueue("one")
		}),
		queue.NewJob[email]("c", func(job *queue.JobBuilder) {
			job.OnQueue("two")
		}),
	}

	for _, job := range jobs {
		if err := consumer.Handle(job, failWith(nil)); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = consumer.Run(ctx)

	if !slices.Equal(driver.queues, []string{"two"}) {
		t.Fatalf("queues = %v, want [two]", driver.queues)
	}
}

// TestRunWithoutQueues verifies a consumer with no registered routes returns
// ErrNoQueues instead of starting an idle driver loop.
func TestRunWithoutQueues(t *testing.T) {
	t.Parallel()

	consumer := queue.NewConsumer(&recordingDriver{})

	if err := consumer.Run(context.Background()); !errors.Is(err, queue.ErrNoQueues) {
		t.Fatalf("Run error = %v, want ErrNoQueues", err)
	}
}

// TestRunStopsAllQueuesOnFirstFailure checks one driver's consume error cancels
// sibling queue loops and is returned to the caller.
func TestRunStopsAllQueuesOnFirstFailure(t *testing.T) {
	t.Parallel()

	driver := &recordingDriver{
		failQueue: "two",
	}
	consumer := queue.NewConsumer(driver)
	jobs := []queue.Job[email]{
		queue.NewJob[email]("a", func(job *queue.JobBuilder) {
			job.OnQueue("one")
		}),
		queue.NewJob[email]("c", func(job *queue.JobBuilder) {
			job.OnQueue("two")
		}),
	}

	for _, job := range jobs {
		if err := consumer.Handle(job, failWith(nil)); err != nil {
			t.Fatal(err)
		}
	}

	err := consumer.Run(context.Background())

	if err == nil || err.Error() != "consume failed" {
		t.Fatalf("Run error = %v, want the consume failure", err)
	}
}
