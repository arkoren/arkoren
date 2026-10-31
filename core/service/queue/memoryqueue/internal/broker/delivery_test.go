package broker_test

import (
	"context"
	"log/slog"
	"testing"
	"testing/synctest"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/memoryqueue"
)

// TestMemoryDriverCanceledDeliveryCanRetry checks an admitted delivery can
// settle with a detached context after cancellation. Its immediate retry stays
// queued for a live consumer instead of being redelivered to the canceled one.
func TestMemoryDriverCanceledDeliveryCanRetry(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryqueue.New()
		t.Cleanup(func() {
			check.NoError(t, driver.Stop(context.Background()))
		})

		check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
			ID:      "retry",
			Queue:   "work",
			Payload: []byte("payload"),
		}))

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		var attempts []int

		err := driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
			message := delivery.Message()
			attempts = append(attempts, message.Attempts)
			cancel()

			if message.Attempts == 1 {
				return delivery.Retry(context.WithoutCancel(ctx), 0)
			}

			return delivery.Acknowledge(context.WithoutCancel(ctx))
		})

		check.ErrorIs(t, err, context.Canceled)
		check.Equal(t, []int{1}, attempts)

		resumed, stop := context.WithCancel(context.Background())
		t.Cleanup(stop)

		var messages []queue.Message
		finished := make(chan error, 1)

		go func() {
			finished <- driver.Consume(resumed, "work", func(ctx context.Context, delivery queue.Delivery) error {
				messages = append(messages, delivery.Message())

				return delivery.Acknowledge(ctx)
			})
		}()

		synctest.Wait()
		stop()

		check.ErrorIs(t, <-finished, context.Canceled)
		check.Equal(t, []queue.Message{{
			ID:       "retry",
			Queue:    "work",
			Payload:  []byte("payload"),
			Attempts: 2,
		}}, messages)
	})
}

// TestMemoryDriverStoppedDeliveryCanAcknowledge checks shutdown does not revoke
// settlement of an admitted delivery. Once it settles, Consume reports shutdown
// without handling queued work that Stop has discarded.
func TestMemoryDriverStoppedDeliveryCanAcknowledge(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryqueue.New()

		for _, id := range []string{"first", "second"} {
			check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
				ID:    id,
				Queue: "work",
			}))
		}

		handled := make(chan string, 2)
		release := make(chan struct{})
		finished := make(chan error, 1)

		go func() {
			finished <- driver.Consume(context.Background(), "work", func(ctx context.Context, delivery queue.Delivery) error {
				handled <- delivery.Message().ID
				<-release

				return delivery.Acknowledge(ctx)
			})
		}()

		first := <-handled
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := driver.Stop(ctx)
		close(release)

		check.ErrorIs(t, <-finished, memoryqueue.ErrDriverClosed)
		check.ErrorIs(t, err, context.Canceled)
		check.Equal(t, "first", first)
		check.Equal(t, 0, len(handled))
		check.NoError(t, driver.Stop(context.Background()))
	})
}

// TestMemoryDriverSettlementIsExactlyOnce checks concurrent acknowledgements
// produce exactly one success and one already-settled error.
func TestMemoryDriverSettlementIsExactlyOnce(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New()

	check.NoError(t, driver.Start(context.Background()))
	t.Cleanup(func() {
		check.NoError(t, driver.Stop(context.Background()))
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	consumer, results := startSettlementConsumer(ctx, driver)

	check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
		Queue: "work",
	}))

	first, second := <-results, <-results

	check.True(t, settledExactlyOnce(first, second))
	cancel()

	check.ErrorIs(t, <-consumer, context.Canceled)
}

// TestMemoryDriverRecoversHandlerPanic verifies a handler panic becomes a
// consume error instead of escaping the driver's goroutine.
func TestMemoryDriverRecoversHandlerPanic(t *testing.T) {
	t.Parallel()

	driver := memoryqueue.New()

	check.NoError(t, driver.Start(context.Background()))
	t.Cleanup(func() {
		check.NoError(t, driver.Stop(context.Background()))
	})

	consumer := make(chan error, 1)

	go func() {
		consumer <- driver.Consume(context.Background(), "work", func(context.Context, queue.Delivery) error {
			panic("handler panic")
		})
	}()

	check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
		Queue: "work",
	}))
	check.Error(t, <-consumer)
}

// TestMemoryDriverLogsRecoveredHandlerPanicToConfiguredLogger checks recovered
// execution panics reach the configured logger as well as the consume caller.
func TestMemoryDriverLogsRecoveredHandlerPanicToConfiguredLogger(t *testing.T) {
	t.Parallel()

	logs := make(chan string, 3)
	driver := memoryqueue.New(func(consumer *memoryqueue.Builder) {
		consumer.Logger(slog.New(channelHandler{
			errors: logs,
		}))
	})

	check.NoError(t, driver.Start(context.Background()))
	t.Cleanup(func() {
		check.NoError(t, driver.Stop(context.Background()))
	})

	consumer := make(chan error, 1)

	go func() {
		consumer <- driver.Consume(context.Background(), "work", func(context.Context, queue.Delivery) error {
			panic("unexpected")
		})
	}()

	check.NoError(t, driver.Dispatch(context.Background(), queue.Message{
		Queue: "work",
	}))
	check.Error(t, <-consumer)
	check.Equal(t, "job delivery handler panicked", <-logs)
}

// startSettlementConsumer starts a consumer that acknowledges one delivery twice.
func startSettlementConsumer(
	ctx context.Context,
	driver *memoryqueue.Driver,
) (<-chan error, <-chan error) {
	consumer := make(chan error, 1)
	results := make(chan error, 2)

	go func() {
		consumer <- driver.Consume(ctx, "work", func(handlerCtx context.Context, delivery queue.Delivery) error {
			acknowledgeConcurrently(handlerCtx, delivery, results)

			return nil
		})
	}()

	return consumer, results
}
