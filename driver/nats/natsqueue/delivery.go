package natsqueue

import "context"

// keepAlive renews every half AckWait, serializing publication with settlement.
// The first unexpected renewal error is logged and stops renewal. Stop joins
// the worker, including when the handler panics, and is safe to call twice.
func (delivery *delivery) keepAlive(ctx context.Context) func() {
	return delivery.state.KeepAlive(delivery.driver.ackWait/2, func() error {
		return delivery.received.InProgress()
	}, func(err error) {
		delivery.driver.logger.ErrorContext(ctx, "JetStream heartbeat failed", "error", err)
	})
}

// settle delegates admission while retaining the public settlement identity.
func (delivery *delivery) settle(ctx context.Context, operation func() error) error {
	return delivery.state.Settle(ctx, ErrDeliverySettled, operation)
}
