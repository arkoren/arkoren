package queuetest_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
)

// ExampleFake_Drain runs a delayed retry without waiting for its scheduled time.
func ExampleFake_Drain() {
	ctx := context.Background()
	job := queue.NewJob[string]("greet")

	fake := queuetest.New()
	consumer := queue.NewConsumer(fake)
	attempts := 0

	if err := consumer.Handle(job, func(context.Context, string) error {
		attempts++

		if attempts == 1 {
			return queue.RetryAfter(fmt.Errorf("later"), time.Hour)
		}

		return nil
	}); err != nil {
		fmt.Println(err)

		return
	}

	if err := queue.NewDispatcher(fake).Dispatch(ctx, job, "world"); err != nil {
		fmt.Println(err)

		return
	}

	if err := fake.Drain(ctx, consumer); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println("attempts:", attempts, "pending:", len(fake.Pending()))

	if err := fake.Drain(ctx, consumer, queuetest.WithScheduled()); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println("attempts:", attempts, "pending:", len(fake.Pending()))
	// Output:
	// attempts: 1 pending: 1
	// attempts: 2 pending: 0
}

// TestDrainRunsHandlers verifies Drain runs the real consumer, including
// jobs dispatched by handlers and immediate retries.
func TestDrainRunsHandlers(t *testing.T) {
	t.Parallel()

	fake, dispatcher, consumer := newPair()
	ctx := context.Background()
	var handled []string
	attempts := 0

	err := consumer.Handle(emailJob, func(ctx context.Context, payload email) error {
		handled = append(handled, payload.To)

		if payload.To == "retry" {
			attempts++

			if attempts == 1 {
				return errors.New("transient")
			}

			return dispatcher.Dispatch(ctx, emailJob, email{
				To: "chained",
			})
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, to := range []string{"first", "retry"} {
		if err := dispatcher.Dispatch(ctx, emailJob, email{
			To: to,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := fake.Drain(ctx, consumer); err != nil {
		t.Fatal(err)
	}

	want := []string{"first", "retry", "retry", "chained"}

	if fmt.Sprint(handled) != fmt.Sprint(want) {
		t.Errorf("handled = %v, want %v", handled, want)
	}

	if pending := fake.Pending(); len(pending) != 0 {
		t.Errorf("pending = %v, want none", pending)
	}
}

// TestDrainScheduled verifies delayed retries wait for WithScheduled.
func TestDrainScheduled(t *testing.T) {
	t.Parallel()

	fake, dispatcher, consumer := newPair()
	ctx := context.Background()
	calls := 0

	err := consumer.Handle(emailJob, func(context.Context, email) error {
		calls++

		if calls == 1 {
			return queue.RetryAfter(errors.New("later"), time.Hour)
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := dispatcher.Dispatch(ctx, emailJob, email{}); err != nil {
		t.Fatal(err)
	}

	if err := fake.Drain(ctx, consumer); err != nil {
		t.Fatal(err)
	}

	if calls != 1 || len(fake.Pending()) != 1 {
		t.Fatalf("after default drain: calls = %d, pending = %d, want 1 and 1", calls, len(fake.Pending()))
	}

	if err := fake.Drain(ctx, consumer, queuetest.WithScheduled()); err != nil {
		t.Fatal(err)
	}

	if calls != 2 || len(fake.Pending()) != 0 {
		t.Fatalf("after scheduled drain: calls = %d, pending = %d, want 2 and 0", calls, len(fake.Pending()))
	}
}

// TestDrainGuardsAgainstEndlessRetries verifies the attempt limit.
func TestDrainGuardsAgainstEndlessRetries(t *testing.T) {
	t.Parallel()

	fake, dispatcher, consumer := newPair()
	ctx := context.Background()
	calls := 0

	err := consumer.Handle(emailJob, func(context.Context, email) error {
		calls++

		return errors.New("always")
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := dispatcher.Dispatch(ctx, emailJob, email{}); err != nil {
		t.Fatal(err)
	}

	err = fake.Drain(ctx, consumer, queuetest.WithMaxAttempts(5))

	if !errors.Is(err, queuetest.ErrDrainLimit) {
		t.Fatalf("Drain = %v, want ErrDrainLimit", err)
	}

	if calls != 5 {
		t.Errorf("calls = %d, want 5", calls)
	}
}

// TestDrainRecordsTerminalOutcomes verifies Rejected and Failed.
func TestDrainRecordsTerminalOutcomes(t *testing.T) {
	t.Parallel()

	fake, dispatcher, consumer := newPair()
	ctx := context.Background()

	err := consumer.Handle(emailJob, func(_ context.Context, payload email) error {
		if payload.To == "reject" {
			return queue.Reject(errors.New("no"))
		}

		return queue.Permanent(errors.New("bad"))
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, to := range []string{"reject", "fail"} {
		if err := dispatcher.Dispatch(ctx, emailJob, email{
			To: to,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := fake.Drain(ctx, consumer); err != nil {
		t.Fatal(err)
	}

	if len(fake.Rejected()) != 1 || len(fake.Failed()) != 1 {
		t.Errorf("rejected = %d, failed = %d, want 1 and 1", len(fake.Rejected()), len(fake.Failed()))
	}
}

// TestDrainRejectsForeignConsumer verifies Drain refuses other consumers.
func TestDrainRejectsForeignConsumer(t *testing.T) {
	t.Parallel()

	fake := queuetest.New()
	consumer := queue.NewConsumer(queuetest.New())

	if err := fake.Drain(context.Background(), consumer); !errors.Is(err, queuetest.ErrForeignConsumer) {
		t.Fatalf("Drain = %v, want ErrForeignConsumer", err)
	}
}

// TestConsumeDeliversDelayedRetryWhenDue verifies live consumption on virtual time.
func TestConsumeDeliversDelayedRetryWhenDue(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		fake, dispatcher, consumer := newPair()
		ctx, cancel := context.WithCancel(context.Background())
		started := time.Now()
		var elapsed time.Duration

		err := consumer.Handle(emailJob, func(context.Context, email) error {
			if elapsed = time.Since(started); elapsed == 0 {
				return queue.RetryAfter(errors.New("later"), time.Minute)
			}

			return nil
		})

		if err != nil {
			t.Fatal(err)
		}

		if err := dispatcher.Dispatch(ctx, emailJob, email{}); err != nil {
			t.Fatal(err)
		}

		done := make(chan error, 1)

		go func() {
			done <- consumer.Run(ctx)
		}()

		time.Sleep(2 * time.Minute)
		synctest.Wait()
		cancel()
		<-done

		if elapsed != time.Minute || len(fake.Pending()) != 0 {
			t.Errorf("retry ran after %s with %d pending, want 1m and none", elapsed, len(fake.Pending()))
		}
	})
}

// TestDrainFollowsDispatchesAcrossQueues checks draining follows a handler's
// newly dispatched work even when it targets another registered queue.
func TestDrainFollowsDispatchesAcrossQueues(t *testing.T) {
	t.Parallel()

	fake, dispatcher, consumer := newPair()
	ctx := context.Background()
	first := queue.NewJob[email]("first-job", func(job *queue.JobBuilder) {
		job.OnQueue("one")
	})

	second := queue.NewJob[email]("second-job", func(job *queue.JobBuilder) {
		job.OnQueue("two")
	})

	var reached atomic.Bool

	err := consumer.Handle(first, func(ctx context.Context, payload email) error {
		return dispatcher.Dispatch(ctx, second, payload)
	})

	if err != nil {
		t.Fatal(err)
	}

	err = consumer.Handle(second, func(context.Context, email) error {
		reached.Store(true)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := dispatcher.Dispatch(ctx, first, email{
		To: "a",
	}); err != nil {
		t.Fatal(err)
	}

	if err := fake.Drain(ctx, consumer); err != nil || !reached.Load() {
		t.Fatalf("Drain = %v, second job reached = %t", err, reached.Load())
	}
}
