package broker

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/eclipse/paho.golang/paho"

	"arkoren.dev/core/service/broadcast"
)

// TestConsumeBindsEnvelopeToMQTTTopic drives the native publish callback after
// Consume registers its broad subscription. The final aligned envelope proves
// rejection is not caused by an absent consumer or by dropping all traffic.
func TestConsumeBindsEnvelopeToMQTTTopic(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := newMQTTBroker(1, nil)
		driver.subscriptions["#"] = true

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var received []broadcast.Message
		done := make(chan error, 1)

		go func() {
			done <- driver.Consume(ctx, func(_ context.Context, message broadcast.Message) error {
				received = append(received, message)

				return nil
			})
		}()

		synctest.Wait()

		for _, payload := range []string{
			`{"name":"payments.approved","payload":"Mg=="}`,
			`{"name":"sensors.*","payload":"Mg=="}`,
			`{"name":"sensors.#","payload":"Mg=="}`,
			`{"name":"sensors/device1/temperature","payload":"Mg=="}`,
			`{"name":"","payload":"Mg=="}`,
			`{"name":`,
		} {
			driver.route(&paho.Publish{
				Topic:   "sensors/device1/temperature",
				Payload: []byte(payload),
			})
		}

		// Even matching transport strings must not admit wildcard or invalid names.
		driver.route(&paho.Publish{
			Topic:   "sensors/+",
			Payload: []byte(`{"name":"sensors.*"}`),
		})
		driver.route(&paho.Publish{
			Topic:   "sensors/#",
			Payload: []byte(`{"name":"sensors.#"}`),
		})
		driver.route(&paho.Publish{
			Topic:   "sensors//temperature",
			Payload: []byte(`{"name":"sensors..temperature"}`),
		})
		driver.route(&paho.Publish{
			Topic:   "sensors/device1/temperature",
			Payload: []byte(`{"name":"sensors.device1.temperature","payload":"MQ=="}`),
		})

		cancel()
		<-done

		if len(received) != 1 || received[0].Name != "sensors.device1.temperature" || string(received[0].Payload) != "1" {
			t.Fatalf("received envelopes = %+v; want only aligned sensor envelope", received)
		}
	})
}

// TestRawSubscribePreservesFanout keeps raw payload delivery independent of the
// envelope checks. Overlapping multilevel and single-level subscriptions still
// fan out, and removing one shared-topic handler leaves the other registered.
func TestRawSubscribePreservesFanout(t *testing.T) {
	t.Parallel()

	driver := newMQTTBroker(1, nil)
	driver.subscriptions["sensors/#"] = true
	driver.subscriptions["sensors/+/temperature"] = true
	var first, second, wildcard int

	unsubscribe, err := driver.Subscribe(context.Background(), "sensors.#", func(payload []byte) {
		if string(payload) != "not an envelope" {
			t.Errorf("raw payload = %q", payload)
		}

		first++
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Subscribe(context.Background(), "sensors.#", func(_ []byte) {
		second++
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Subscribe(context.Background(), "sensors.*.temperature", func(_ []byte) {
		wildcard++
	}); err != nil {
		t.Fatal(err)
	}

	driver.route(&paho.Publish{
		Topic:   "sensors/device1/temperature",
		Payload: []byte("not an envelope"),
	})

	if err := unsubscribe(); err != nil {
		t.Fatal(err)
	}

	driver.route(&paho.Publish{
		Topic:   "sensors/device1/temperature",
		Payload: []byte("not an envelope"),
	})

	if first != 1 || second != 2 || wildcard != 2 {
		t.Fatalf("raw handler calls = %d, %d, %d; want 1, 2, 2", first, second, wildcard)
	}
}
