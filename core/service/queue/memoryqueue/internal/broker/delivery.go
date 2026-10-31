package broker

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"arkoren.dev/core/service/queue"
)

// consumeNext admits and handles one delivery or waits until work may exist.
// Reservation errors end consumption before any queued message is removed.
func (driver *Broker) consumeNext(ctx context.Context, name string, handler queue.DeliveryHandler) error {
	message, wake, due, err := driver.reserve(ctx, name)

	if err != nil {
		return err
	}

	if message == nil {
		return driver.wait(ctx, wake, due)
	}

	return driver.handle(ctx, handler, &delivery{
		driver:  driver,
		message: *message,
	})
}

// handle runs handler for delivery and fails the delivery when the handler
// errors, panics, or leaves it unsettled. The returned error ends Consume.
func (driver *Broker) handle(ctx context.Context, handler queue.DeliveryHandler, delivery *delivery) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("job delivery handler panicked: %v", recovered)
			driver.Logger.ErrorContext(ctx, "job delivery handler panicked", "error", recovered, "job_id", delivery.message.ID, "queue", delivery.message.Queue)
		}

		if err != nil {
			driver.Logger.ErrorContext(ctx, "job delivery handler failed", "error", err, "job_id", delivery.message.ID, "queue", delivery.message.Queue)

			// Keep the handler failure as the result; the delivery may already be settled.
			_ = delivery.Fail(context.Background(), err)
		}

		if !delivery.settled() {
			err = ErrDeliveryUnsettled
			driver.Logger.ErrorContext(ctx, "job delivery handler did not settle delivery", "job_id", delivery.message.ID, "queue", delivery.message.Queue)

			// Consume reports ErrDeliveryUnsettled; failing the delivery is best-effort cleanup.
			_ = delivery.Fail(context.Background(), err)
		}
	}()

	return handler(ctx, delivery)
}

// delivery is one reserved message, settled at most once.
type delivery struct {
	// driver requeues retried messages.
	driver *Broker

	// message is the reserved message.
	message queue.Message

	// mu guards done and settling.
	mu sync.Mutex

	// done reports a completed settlement.
	done bool

	// settling reports a settlement in progress.
	settling bool
}

// Message returns a copy of the delivered message.
func (delivery *delivery) Message() queue.Message {
	delivery.mu.Lock()
	defer delivery.mu.Unlock()

	message := delivery.message
	message.Payload = slices.Clone(message.Payload)
	message.Metadata = maps.Clone(message.Metadata)

	return message
}

// Acknowledge settles the delivery as done.
func (delivery *delivery) Acknowledge(ctx context.Context) error {
	return delivery.settle(ctx, func() error {
		return nil
	})
}

// Retry requeues the message after delay.
func (delivery *delivery) Retry(ctx context.Context, delay time.Duration) error {
	return delivery.settle(ctx, func() error {
		return delivery.driver.retry(ctx, delivery.message, delay)
	})
}

// Reject drops the message.
func (delivery *delivery) Reject(ctx context.Context) error {
	return delivery.settle(ctx, func() error {
		return nil
	})
}

// Fail logs err and drops the message.
func (delivery *delivery) Fail(ctx context.Context, err error) error {
	return delivery.settle(ctx, func() error {
		if err != nil {
			delivery.driver.Logger.ErrorContext(ctx, "job delivery failed", "error", err, "job_id", delivery.message.ID, "queue", delivery.message.Queue)
		}

		return nil
	})
}

// settle runs operation as the one settlement of the delivery. A failed
// operation leaves the delivery open for another attempt.
func (delivery *delivery) settle(ctx context.Context, operation func() error) error {
	if err := delivery.startSettlement(ctx); err != nil {
		return err
	}

	err := operation()
	delivery.finishSettlement(err == nil)

	return err
}

// startSettlement records an in-progress settlement when the delivery is available.
func (delivery *delivery) startSettlement(ctx context.Context) error {
	delivery.mu.Lock()
	defer delivery.mu.Unlock()

	if delivery.done || delivery.settling {
		return ErrDeliverySettled
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	delivery.settling = true

	return nil
}

// finishSettlement ends the in-progress settlement, completing the delivery
// when succeeded is true.
func (delivery *delivery) finishSettlement(succeeded bool) {
	delivery.mu.Lock()
	defer delivery.mu.Unlock()

	delivery.settling = false
	delivery.done = succeeded
}

// settled reports whether the delivery has been settled.
func (delivery *delivery) settled() bool {
	delivery.mu.Lock()
	defer delivery.mu.Unlock()

	return delivery.done
}
