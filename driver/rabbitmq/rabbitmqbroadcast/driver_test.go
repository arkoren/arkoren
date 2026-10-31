package rabbitmqbroadcast_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/broadcast/broadcasttest"
	"arkoren.dev/driver/rabbitmq/internal/amqptest"
	"arkoren.dev/driver/rabbitmq/rabbitmqbroadcast"
)

// amqpTestURL selects the disposable broker used only by integration tests.
const amqpTestURL = "amqp://arkoren:integration@127.0.0.1:5672/"

// TestConformance runs the broadcast conformance suite against a RabbitMQ broker.
func TestConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	broadcasttest.Run(t, func(t *testing.T) broadcasttest.Driver {
		id := make([]byte, 6)

		if _, err := rand.Read(id); err != nil {
			t.Fatal(err)
		}

		driver, err := rabbitmqbroadcast.New(amqpTestURL, func(broker *rabbitmqbroadcast.Builder) {
			broker.Exchange("conformance." + hex.EncodeToString(id))
		})

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

		return broadcasttest.Driver{
			Publisher: driver,
			Consumer:  driver,
		}
	})
}

// TestNativePublishCannotForgeEnvelopeName exercises actual AMQP routing keys
// through Consume and the typed core consumer. A positive readiness barrier and
// an ordered final marker ensure a rejected publication was really observed.
func TestNativePublishCannotForgeEnvelopeName(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	exchange := "security." + rand.Text()
	driver, err := rabbitmqbroadcast.New(amqpTestURL, func(broker *rabbitmqbroadcast.Builder) {
		broker.Exchange(exchange)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = driver.Stop(context.Background())
	})

	channel, err := driver.Connection().Channel()

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = channel.ExchangeDelete(exchange, false, false)
		_ = channel.Close()
	})

	consumer := broadcast.NewConsumer(driver)
	received := make(chan int, 32)

	if _, err := consumer.Listen(broadcast.NewTopic[int]("payments.approved"), func(_ context.Context, value int) error {
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

	publish := func(key, body string) {
		t.Helper()

		if err := channel.PublishWithContext(ctx, exchange, key, false, false, amqp091.Publishing{
			Body: []byte(body),
		}); err != nil {
			t.Fatal(err)
		}
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	t.Cleanup(ticker.Stop)
	ready := false

	for !ready {
		publish("payments.approved", `{"name":"payments.approved","payload":"MA=="}`)

		select {
		case <-received:
			ready = true
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("consumer never became ready")
		}
	}

	publish("sensors.device1.temperature", `{"name":"payments.approved","payload":"Mg=="}`)
	publish("payments.approved", `{"name":"payments.approved","payload":"MQ=="}`)

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

// TestCanceledConsumePrecedesHandlerValidation checks the core contract's
// boundary rule without starting or contacting a broker.
func TestCanceledConsumePrecedesHandlerValidation(t *testing.T) {
	t.Parallel()

	driver, err := rabbitmqbroadcast.New("amqp://localhost/")

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Consume(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume = %v, want context.Canceled", err)
	}
}

// TestCanceledPublishPrecedesNameValidation checks the same boundary rule for
// publication: cancellation is observed before attempting any serialization.
func TestCanceledPublishPrecedesNameValidation(t *testing.T) {
	t.Parallel()

	driver, err := rabbitmqbroadcast.New("amqp://localhost/")

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := driver.Publish(ctx, broadcast.Message{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish = %v, want context.Canceled", err)
	}
}

// TestStartCancellationInterruptsTopology withholds the first channel.open-ok
// after negotiation. Start's context must cover broker setup, not just dialing.
func TestStartCancellationInterruptsTopology(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	url := amqptest.New(t, func(peer *amqptest.Peer) {
		peer.Handshake()
		peer.Read()
		close(entered)
		<-release
	})
	driver, err := rabbitmqbroadcast.New(url)

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	var group sync.WaitGroup
	results := make(chan error, 1)
	group.Go(func() {
		results <- driver.Start(ctx)
	})
	t.Cleanup(func() {
		cancel()
		close(release)
		group.Wait()
	})

	<-entered
	cancel()

	select {
	case err := <-results:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start ignored cancellation during topology setup")
	}
}

// TestPingCancellationInterruptsChannelOpen withholds channel.open-ok after
// successful startup. Cancellation must interrupt the owned SDK RPC itself.
func TestPingCancellationInterruptsChannelOpen(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	url := amqptest.New(t, func(peer *amqptest.Peer) {
		peer.Handshake()

		for range 3 {
			peer.ReplySetup(peer.Read())
		}

		peer.Read()
		close(entered)
		<-release
	})
	driver, err := rabbitmqbroadcast.New(url)

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	var group sync.WaitGroup
	results := make(chan error, 1)
	group.Go(func() {
		results <- driver.Ping(ctx)
	})
	t.Cleanup(func() {
		cancel()
		close(release)
		group.Wait()
	})

	<-entered
	cancel()

	select {
	case err := <-results:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Ping = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Ping ignored cancellation during channel.open")
	}
}

// TestExpiredStopReleasesUnconfirmedPublish stalls the confirmation and all
// close replies. Stop must still close its owned connection without waiting
// behind the publisher's lifecycle lock or a channel.close RPC.
func TestExpiredStopReleasesUnconfirmedPublish(t *testing.T) {
	t.Parallel()

	published := make(chan struct{})
	release := make(chan struct{})
	url := amqptest.New(t, func(peer *amqptest.Peer) {
		peer.Handshake()
		peer.Setup()
		peer.Read()
		peer.Read()
		close(published)
		<-release
	})
	driver, err := rabbitmqbroadcast.New(url)

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	var group sync.WaitGroup
	group.Go(func() {
		_ = driver.Publish(ctx, broadcast.Message{Name: "work.ready", Payload: []byte(`{}`)})
	})
	t.Cleanup(func() {
		cancel()
		close(release)
		group.Wait()
	})

	<-published
	stopped, stop := context.WithCancel(t.Context())
	stop()
	results := make(chan error, 1)
	group.Go(func() {
		results <- driver.Stop(stopped)
	})

	select {
	case <-results:
		if !driver.Connection().IsClosed() {
			t.Fatal("Stop returned with its owned connection open")
		}
	case <-time.After(time.Second):
		t.Fatal("Stop blocked behind an unconfirmed publication")
	}
}

// TestStartCancellationInterruptsHandshake stalls after TCP acceptance. Cancel
// must interrupt AMQP negotiation, not just the earlier TCP connection attempt.
func TestStartCancellationInterruptsHandshake(t *testing.T) {
	t.Parallel()

	accepted := make(chan struct{})
	release := make(chan struct{})
	url := amqptest.New(t, func(*amqptest.Peer) {
		close(accepted)
		<-release
	})
	driver, err := rabbitmqbroadcast.New(url)

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	var group sync.WaitGroup
	results := make(chan error, 1)
	group.Go(func() {
		results <- driver.Start(ctx)
	})
	t.Cleanup(func() {
		cancel()
		close(release)
		group.Wait()
	})

	<-accepted
	cancel()

	select {
	case err := <-results:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start ignored cancellation during AMQP handshake")
	}
}
