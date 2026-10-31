package memoryqueue_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"testing"
	"time"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/memoryqueue"
	"arkoren.dev/core/service/queue/queuetest"
)

// TestConformance runs the queue conformance suite against the memory driver.
func TestConformance(t *testing.T) {
	t.Parallel()

	queuetest.Run(t, func(t *testing.T) queuetest.Driver {
		driver := memoryqueue.New()
		t.Cleanup(func() {
			_ = driver.Stop(context.Background())
		})

		return queuetest.Driver{
			Dispatcher: driver,
			Consumer:   driver,
		}
	}, queuetest.WithSynctest())
}

// Example adds a default delay for deliveries that request an immediate retry.
// Construction starts no consumers; this example only configures the driver.
func Example() {
	driver := memoryqueue.New(func(consumer *memoryqueue.Builder) {
		consumer.RetryDelay(time.Second)
	})

	_ = driver
}

// ExampleDriver_Consume cancels consumption after one acknowledged delivery.
// The context error describes the consumer's exit, not a settlement failure.
func ExampleDriver_Consume() {
	driver := memoryqueue.New()

	if err := driver.Dispatch(context.Background(), queue.Message{Queue: "work"}); err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
		defer cancel()

		return delivery.Acknowledge(ctx)
	})

	if !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}

	fmt.Println("consumer canceled after acknowledgment")
	// Output: consumer canceled after acknowledgment
}
