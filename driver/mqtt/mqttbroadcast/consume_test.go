package mqttbroadcast_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/driver/mqtt/mqttbroadcast"
)

// TestNativePublishCannotForgeEnvelopeName sends hostile wire JSON using the
// native SDK, bypassing Publish's name-to-topic mapping. An aligned publication
// first establishes readiness; a final marker proves later traffic still flows
// through the typed consumer after the attempted identity substitution.
func TestNativePublishCannotForgeEnvelopeName(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	driver, err := mqttbroadcast.New([]string{mqttTestURL})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(context.Background())
	})

	prefix := "security." + strings.ToLower(rand.Text()) + "."
	name := prefix + "payments.approved"
	topic := strings.ReplaceAll(name, ".", "/")
	consumer := broadcast.NewConsumer(driver)
	received := make(chan int, 32)

	if _, err := consumer.Listen(broadcast.NewTopic[int](name), func(_ context.Context, value int) error {
		received <- value

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)

	go func() {
		done <- consumer.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	publish := func(actualTopic, payload string) {
		t.Helper()

		_, err := driver.Client().Publish(ctx, &paho.Publish{
			Topic:   actualTopic,
			QoS:     1,
			Payload: []byte(fmt.Sprintf(`{"name":%q,"payload":%q}`, name, payload)),
		})

		if err != nil {
			t.Fatal(err)
		}
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	t.Cleanup(ticker.Stop)
	ready := false

	for !ready {
		publish(topic, "MA==")

		select {
		case <-received:
			ready = true
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("consumer never became ready")
		}
	}

	publish(strings.ReplaceAll(prefix, ".", "/")+"sensors/device1/temperature", "Mg==")
	publish(topic, "MQ==")

	for {
		select {
		case value := <-received:
			if value == 2 {
				t.Fatal("forged envelope invoked privileged listener")
			}

			if value == 1 {
				return
			}
		case <-ctx.Done():
			t.Fatal("aligned final publication was not delivered")
		}
	}
}
