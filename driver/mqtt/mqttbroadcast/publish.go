package mqttbroadcast

import (
	"context"

	"arkoren.dev/core/service/broadcast"
)

// Publish encodes the complete broadcast envelope and publishes under its
// concrete MQTT topic. Cancellation precedes validation and native work. QoS 1
// and 2 wait for protocol completion and reject negative reason codes even when
// native error is nil. Success does not confirm application processing; timeout
// does not prove nondelivery. QoS 0 retains its native at-most-once policy.
//
// Example:
//
//	return driver.Publish(ctx, broadcast.Message{Name: "users.created"})
func (driver *Driver) Publish(ctx context.Context, message broadcast.Message) error {
	return driver.broker.Publish(ctx, message)
}
