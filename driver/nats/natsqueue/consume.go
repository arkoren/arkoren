package natsqueue

import (
	"context"

	"arkoren.dev/core/service/queue"
)

// Consume pulls jobs until cancellation or native failure. Handlers own
// settlement; cancellation stops fetching and leaves unhandled jobs unsettled.
// Renewal continues through handler drain, and stopped workers are joined.
//
// Example:
//
//	err := driver.Consume(ctx, "emails", func(ctx context.Context, item queue.Delivery) error {
//		return item.Acknowledge(ctx)
//	})
//	return err
func (driver *Driver) Consume(ctx context.Context, jobQueue string, handler queue.DeliveryHandler) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return driver.consume(ctx, jobQueue, handler)
}
