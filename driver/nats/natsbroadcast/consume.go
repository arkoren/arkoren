package natsbroadcast

import (
	"context"
	"encoding/json/v2"
	"errors"

	"github.com/nats-io/nats.go"

	"arkoren.dev/core/service/broadcast"
)

// Consume receives envelopes through a broad NATS subscription. It drops
// malformed envelopes and any envelope whose name does not exactly identify
// the delivered subject as prefix + "." + name, before invoking handler.
// This binds core routing and telemetry to the broker-authorized identity,
// including deliveries received through a wildcard subscription.
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

	conn, err := driver.connection()

	if err != nil {
		return err
	}

	subs, err := driver.subscribeSubjects(conn, []string{driver.prefix + ".>"}, func(delivered *nats.Msg) {
		var message broadcast.Message

		if json.Unmarshal(delivered.Data, &message) != nil || delivered.Subject != driver.prefix+"."+message.Name {
			return
		}

		if err := handler(ctx, message); err != nil {
			driver.logger.ErrorContext(ctx, "NATS broadcast handler failed", "name", message.Name, "error", err)
		}
	})

	if err != nil {
		return err
	}

	defer func() {
		// Cancellation remains the result even if subscription cleanup fails.
		_ = unsubscribeNATSSubscriptions(subs)
	}()

	<-ctx.Done()

	return ctx.Err()
}
