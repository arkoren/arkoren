package natsqueue_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
	"arkoren.dev/driver/nats/natsqueue"

	"github.com/nats-io/nats.go"
)

// natsTestURL selects the disposable JetStream-enabled integration server.
const natsTestURL = "nats://127.0.0.1:4222"

// TestConformance runs the queue conformance suite against a JetStream server.
func TestConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	queuetest.Run(t, func(t *testing.T) queuetest.Driver {
		id := make([]byte, 6)

		if _, err := rand.Read(id); err != nil {
			t.Fatal(err)
		}

		name := hex.EncodeToString(id)
		driver, err := natsqueue.New(
			"conformance_"+name,
			map[string]natsqueue.Queue{
				"conformance":       {Subject: "conformance." + name + ".main", Durable: "main_" + name},
				"conformance-other": {Subject: "conformance." + name + ".other", Durable: "other_" + name},
			},
			func(consumer *natsqueue.Builder) {
				consumer.URLs(natsTestURL)
				consumer.Provision(true)
			},
		)

		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		if err := driver.Start(ctx); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			_ = driver.Stop(context.Background())
		})

		return queuetest.Driver{
			Dispatcher: driver,
			Consumer:   driver,
		}
	})
}

// TestDeliveryMessageMetadataIsDetached checks the public delivery object and
// the stored envelope, not a private delivery constructor or map implementation.
func TestDeliveryMessageMetadataIsDetached(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	driver := integrationDriver(t, time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	if err := driver.Dispatch(ctx, queue.Message{
		Name: "work", Queue: "work", Metadata: map[string]string{"source": "original"},
	}); err != nil {
		t.Fatal(err)
	}

	err := driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
		message := delivery.Message()
		message.Metadata["source"] = "changed"

		if delivery.Message().Metadata["source"] != "original" {
			t.Error("Message exposed internal metadata")
		}

		err := delivery.Acknowledge(ctx)
		cancel()

		return err
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume = %v", err)
	}
}

// TestSmallAckWaitSupportsPullRequests separates acknowledgement timing from
// pull expiry while checking a real native delivery and settlement boundary.
func TestSmallAckWaitSupportsPullRequests(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	driver := integrationDriver(t, time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	var group sync.WaitGroup
	result := make(chan error, 1)
	group.Go(func() {
		result <- driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
			err := delivery.Acknowledge(ctx)
			cancel()

			return err
		})
	})
	t.Cleanup(func() {
		cancel()
		group.Wait()
	})

	if err := driver.Dispatch(ctx, queue.Message{Name: "work", Queue: "work"}); err != nil {
		t.Fatal(err)
	}

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume = %v", err)
	}
}

// integrationDriver provisions unique subjects and a durable on the coordinator's
// isolated NATS endpoint. Short runs never connect; cleanup preserves the borrowed
// native ownership rule and removes only this test's stream.
func integrationDriver(t *testing.T, ackWait time.Duration) *natsqueue.Driver {
	t.Helper()

	conn, err := nats.Connect(natsTestURL)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(conn.Close)
	stream := strings.ReplaceAll(nats.NewInbox(), ".", "_")
	jetStream, err := conn.JetStream()

	if err != nil {
		t.Fatal(err)
	}

	driver, err := natsqueue.NewFrom(conn, stream, map[string]natsqueue.Queue{
		"work": {Subject: "jobs." + stream, Durable: "work"},
	}, func(consumer *natsqueue.Builder) {
		consumer.Provision(true)
		consumer.AckWait(ackWait)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := driver.Stop(ctx); err != nil {
			t.Error(err)
		}

		if err := jetStream.DeleteStream(stream, nats.Context(ctx)); err != nil {
			t.Error(err)
		}
	})

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	return driver
}
