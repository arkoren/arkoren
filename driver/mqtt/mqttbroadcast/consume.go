package mqttbroadcast

import (
	"context"

	"github.com/eclipse/paho.golang/paho"

	"arkoren.dev/core/service/broadcast"
)

// Consume binds each decoded concrete envelope name to the literal delivered
// MQTT topic. Malformed or mismatched envelopes are dropped. It rejects
// pre-canceled contexts before registration and releases its subscription after
// cancellation. Handler errors do not alter native acknowledgment policy;
// already-running callbacks must cooperate with ctx.
//
// Example:
//
//	return driver.Consume(ctx, handle)
func (driver *Driver) Consume(ctx context.Context, handler broadcast.Handler) error {
	return driver.broker.Consume(ctx, handler)
}

// HandlePublish routes a native publication and waits for admitted matching
// callbacks. With automatic acknowledgments, QoS 1 acknowledgment follows their
// completion; borrowed manual acknowledgment policy remains unchanged. Panics
// and handler errors do not request retries. The 1024-delivery limit blocks
// admission rather than dropping work. Stop drains but does not cancel handlers.
// NewFrom callers must install this callback before creating their manager.
//
// Example:
//
//	config.ClientConfig.OnPublishReceived = append(config.ClientConfig.OnPublishReceived, driver.HandlePublish)
func (driver *Driver) HandlePublish(publication paho.PublishReceived) (bool, error) {
	return driver.broker.HandlePublish(publication)
}
