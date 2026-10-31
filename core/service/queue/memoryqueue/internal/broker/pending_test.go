package broker_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/memoryqueue"
)

// TestMemoryDriverCanceledConsumerPreservesBacklog checks cancellation while a
// facade handler is active stops new deliveries, without undoing that handler's
// detached acknowledgment or removing the remaining messages from the queue.
func TestMemoryDriverCanceledConsumerPreservesBacklog(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryqueue.New()
		t.Cleanup(func() {
			check.NoError(t, driver.Stop(context.Background()))
		})

		for _, id := range []string{"first", "second", "third"} {
			check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
				ID:       id,
				Name:     "work",
				Queue:    "work",
				Payload:  []byte(`"` + id + `"`),
				Metadata: map[string]string{"source": "cancellation-test"},
			}))
		}

		consumer := queue.NewConsumer(driver)
		job := queue.NewJob[string]("work", func(job *queue.JobBuilder) {
			job.OnQueue("work")
		})

		handled := make(chan string, 3)
		release := make(chan struct{})

		check.NoError(t, consumer.Handle(job, func(_ context.Context, payload string) error {
			handled <- payload
			<-release

			return nil
		}))

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		finished := make(chan error, 1)

		go func() {
			finished <- consumer.Run(ctx)
		}()

		first := <-handled
		cancel()
		close(release)

		check.ErrorIs(t, <-finished, context.Canceled)
		check.Equal(t, "first", first)
		check.Equal(t, 0, len(handled))

		resumed, stop := context.WithCancel(context.Background())
		t.Cleanup(stop)

		var messages []queue.Message

		go func() {
			finished <- driver.Consume(resumed, "work", func(ctx context.Context, delivery queue.Delivery) error {
				messages = append(messages, delivery.Message())

				return delivery.Acknowledge(ctx)
			})
		}()

		synctest.Wait()
		stop()

		check.ErrorIs(t, <-finished, context.Canceled)
		check.Equal(t, []queue.Message{
			{
				ID:       "second",
				Name:     "work",
				Queue:    "work",
				Payload:  []byte(`"second"`),
				Attempts: 1,
				Metadata: map[string]string{"source": "cancellation-test"},
			},
			{
				ID:       "third",
				Name:     "work",
				Queue:    "work",
				Payload:  []byte(`"third"`),
				Attempts: 1,
				Metadata: map[string]string{"source": "cancellation-test"},
			},
		}, messages)
	})
}

// TestMemoryDriverDispatchDeliversClonedMessageWithMetadata checks dispatch
// assigns delivery fields and detaches the payload from caller mutation.
func TestMemoryDriverDispatchDeliversClonedMessageWithMetadata(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New()

	check.NoError(t, driver.Start(context.Background()))
	t.Cleanup(func() {
		check.NoError(t, driver.Stop(context.Background()))
	})

	payload := []byte(`{"id":"first"}`)
	received := make(chan queue.Message, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	consumer := make(chan error, 1)

	go func() {
		consumer <- driver.Consume(ctx, "emails", func(ctx context.Context, delivery queue.Delivery) error {
			received <- delivery.Message()

			return delivery.Acknowledge(ctx)
		})
	}()

	check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
		Name:    "email",
		Queue:   "emails",
		Payload: payload,
	}))

	payload[7] = 'X'
	message := <-received

	check.NotEmpty(t, message.ID)
	check.Equal(t, 1, message.Attempts)
	check.Equal(t, []byte(`{"id":"first"}`), message.Payload)
	cancel()

	check.ErrorIs(t, <-consumer, context.Canceled)
}

// TestMemoryDriverIsolatesQueues checks consuming one queue does not take
// messages addressed to another queue.
func TestMemoryDriverIsolatesQueues(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New()

	check.NoError(t, driver.Start(context.Background()))
	t.Cleanup(func() {
		check.NoError(t, driver.Stop(context.Background()))
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	received := make(chan string, 1)
	consumer := make(chan error, 1)

	go func() {
		consumer <- driver.Consume(ctx, "second", func(ctx context.Context, delivery queue.Delivery) error {
			received <- delivery.Message().Queue

			return delivery.Acknowledge(ctx)
		})
	}()

	check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
		Queue: "first",
	}))
	check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
		Queue: "second",
	}))
	check.Equal(t, "second", <-received)
	cancel()

	check.ErrorIs(t, <-consumer, context.Canceled)
}

// TestMemoryDriverRetryDelaysRedelivery verifies a delayed retry becomes
// ready only after its delay, without Start and on virtual time.
func TestMemoryDriverRetryDelaysRedelivery(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryqueue.New()
		ctx, cancel := context.WithCancel(context.Background())
		started := time.Now()
		var mu sync.Mutex
		var attempts []time.Duration
		seen := func() []time.Duration {
			mu.Lock()
			defer mu.Unlock()

			return slices.Clone(attempts)
		}

		consumer := make(chan error, 1)

		go func() {
			consumer <- driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
				message := delivery.Message()
				mu.Lock()

				attempts = append(attempts, time.Since(started))
				mu.Unlock()

				if message.Attempts == 1 {
					return delivery.Retry(ctx, 40*time.Millisecond)
				}

				return delivery.Acknowledge(ctx)
			})
		}()

		check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
			Queue: "work",
		}))

		synctest.Wait()

		check.Equal(t, []time.Duration{0}, seen())
		time.Sleep(40 * time.Millisecond)
		synctest.Wait()

		check.Equal(t, []time.Duration{0, 40 * time.Millisecond}, seen())
		cancel()

		check.ErrorIs(t, <-consumer, context.Canceled)
	})
}
