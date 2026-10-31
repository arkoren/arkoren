package rabbitmqqueue

import (
	"context"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/driver/rabbitmq/internal/amqpconn"
)

// Consume receives manually acknowledged jobs until cancellation or a broker or
// handler error. A handler error closes the consumer channel and is returned to
// the caller; unsettled jobs can then be redelivered instead of occupying all
// prefetch slots. The handler is responsible for settling accepted deliveries.
//
// Example:
//
//	err := driver.Consume(ctx, "work", func(ctx context.Context, item queue.Delivery) error {
//		return item.Acknowledge(ctx)
//	})
//	return err
func (driver *Driver) Consume(ctx context.Context, name string, handler queue.DeliveryHandler) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if handler == nil {
		return ErrInvalidHandler
	}

	channel, deliveries, err := driver.openConsumer(ctx, name)

	if err != nil {
		return err
	}

	defer func() {
		// Preserve the consumption error while releasing its channel. Normal
		// cancellation permits a brief channel-only close before socket abort.
		_ = amqpconn.CloseChannel(context.WithoutCancel(ctx), driver.abort, channel, deliveries)
	}()

	return driver.consumeDeliveries(ctx, name, deliveries, handler)
}
