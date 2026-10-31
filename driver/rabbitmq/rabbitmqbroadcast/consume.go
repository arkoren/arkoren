package rabbitmqbroadcast

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/driver/rabbitmq/internal/amqpconn"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

// Consume receives messages through one broad AMQP subscription and routes the
// envelope to the supplied receiver only when its valid concrete name exactly
// equals the delivered routing key. Malformed and mismatched envelopes are
// silently dropped, preserving topic permissions without authenticating payloads.
// It returns when ctx ends or when the channel or connection closes.
//
// Example:
//
//	err := driver.Consume(ctx, func(ctx context.Context, message broadcast.Message) error {
//		return process(ctx, message)
//	})
func (driver *Driver) Consume(ctx context.Context, handler broadcast.Handler) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if handler == nil {
		return errors.New("nil broadcast handler")
	}

	// The consumer must outlive ctx: amqp091 cancels a context-bound consumer
	// on its own and then deadlocks Channel.Close, so Consume watches ctx itself.
	channel, deliveries, err := driver.subscribe(ctx, context.WithoutCancel(ctx), "#")

	if err != nil {
		return err
	}

	defer func() {
		// Consumption supplies the primary exit error. A brief detached cleanup
		// permits channel-only close before aborting an unresponsive owned socket.
		_ = amqpconn.CloseChannel(context.WithoutCancel(ctx), driver.abort, channel, deliveries)
	}()

	return driver.consumeDeliveries(ctx, deliveries, handler)
}

// consumeDeliveries processes native deliveries without discarding their routing
// keys. Subscription setup and cleanup remain in Consume; a closed stream returns
// ErrSubscriptionClosed unless cancellation already supplies the exit reason.
func (driver *Driver) consumeDeliveries(
	ctx context.Context,
	deliveries <-chan amqp091.Delivery,
	handler broadcast.Handler,
) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, open := <-deliveries:
			if !open {
				return cmp.Or(ctx.Err(), ErrSubscriptionClosed)
			}

			driver.deliverToHandler(func(payload []byte) {
				var message broadcast.Message

				if json.Unmarshal(payload, &message) != nil || broadcast.ValidateName(message.Name) != nil ||
					delivery.RoutingKey != message.Name {
					return
				}

				if err := handler(ctx, message); err != nil {
					driver.logger.ErrorContext(ctx, "AMQP broadcast handler failed", "name", message.Name, "error", err)
				}
			}, "#", delivery.Body)
		}
	}
}
