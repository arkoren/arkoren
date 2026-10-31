package rabbitmqbroadcast

import (
	"context"
	"errors"
	"testing"

	amqp091 "github.com/rabbitmq/amqp091-go"

	"arkoren.dev/core/service/broadcast"
)

// TestConsumeDeliveriesBindsEnvelopeToRoutingKey feeds native deliveries through
// the Consume loop, including names that match their key but are not concrete
// broadcast names. Only the aligned valid envelope may reach the handler.
func TestConsumeDeliveriesBindsEnvelopeToRoutingKey(t *testing.T) {
	t.Parallel()

	driver := newDriver(nil, defaultConfig())
	deliveries := make(chan amqp091.Delivery, 8)

	for _, delivery := range []amqp091.Delivery{
		{
			RoutingKey: "sensors.device1.temperature",
			Body:       []byte(`{"name":"payments.approved","payload":"Mg=="}`),
		},
		{
			RoutingKey: "sensors.*",
			Body:       []byte(`{"name":"sensors.*","payload":"Mg=="}`),
		},
		{
			RoutingKey: "sensors.#",
			Body:       []byte(`{"name":"sensors.#","payload":"Mg=="}`),
		},
		{
			RoutingKey: "sensors..temperature",
			Body:       []byte(`{"name":"sensors..temperature","payload":"Mg=="}`),
		},
		{
			RoutingKey: "",
			Body:       []byte(`{"name":"","payload":"Mg=="}`),
		},
		{
			RoutingKey: "payments.approved",
			Body:       []byte(`{"name":`),
		},
		{
			RoutingKey: "payments.approved",
			Body:       []byte(`{"name":"payments.approved","payload":"MQ=="}`),
		},
	} {
		deliveries <- delivery
	}

	close(deliveries)
	var received []broadcast.Message

	err := driver.consumeDeliveries(context.Background(), deliveries, func(_ context.Context, message broadcast.Message) error {
		received = append(received, message)

		return nil
	})

	if !errors.Is(err, ErrSubscriptionClosed) {
		t.Fatalf("closed stream error = %v", err)
	}

	if len(received) != 1 || received[0].Name != "payments.approved" || string(received[0].Payload) != "1" {
		t.Fatalf("received envelopes = %+v; want only aligned payment envelope", received)
	}
}
