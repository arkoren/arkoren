package rabbitmqqueue_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
	"arkoren.dev/driver/rabbitmq/internal/amqptest"
	"arkoren.dev/driver/rabbitmq/rabbitmqqueue"
)

// amqpTestURL selects the disposable broker used only by integration tests.
const amqpTestURL = "amqp://arkoren:integration@127.0.0.1:5672/"

// TestConformance runs the queue conformance suite against a RabbitMQ broker.
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
		driver, err := rabbitmqqueue.New(
			amqpTestURL,
			map[string]rabbitmqqueue.Queue{
				"conformance":       {Name: "conformance." + name + ".main", RoutingKey: "main"},
				"conformance-other": {Name: "conformance." + name + ".other", RoutingKey: "other"},
			},
			func(consumer *rabbitmqqueue.Builder) {
				consumer.Exchange("conformance." + name)
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

// ExampleNew demonstrates positional topology and optional tuning without
// contacting a broker. Connection acquisition belongs to the later Start call.
func ExampleNew() {
	queues := map[string]rabbitmqqueue.Queue{
		"emails": {Name: "jobs.emails", RoutingKey: "emails"},
	}

	driver, err := rabbitmqqueue.New("amqp://localhost/", queues, func(broker *rabbitmqqueue.Builder) {
		broker.Prefetch(16)
		broker.RetryDelay(5 * time.Second)
		broker.MaxRetryDelay(time.Hour)
		broker.FailureQueue("jobs.failed")
	})

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(driver.MessagingSystem(), driver.Connection() == nil)
	// Output: rabbitmq true
}

// TestRetryReturnDoesNotAcknowledgeSource models a lost retry destination.
// A returned but positively confirmed transfer must close the consumer channel
// without emitting basic.ack for the source delivery.
func TestRetryReturnDoesNotAcknowledgeSource(t *testing.T) {
	t.Parallel()

	settlement := make(chan uint32, 1)
	url := amqptest.New(t, func(peer *amqptest.Peer) {
		peer.Handshake()
		peer.Deliver(peer.Setup(), []byte(`{"name":"work","payload":"e30="}`))
		peer.ReturnPublication(peer.Setup(), 1)
		frame := peer.Read()
		settlement <- frame.Method
		peer.Send(1, frame.Channel, []byte{0, 20, 0, 41})
		peer.Read()
	})
	driver, err := rabbitmqqueue.New(url, map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	if err := driver.Start(ctx); err != nil {
		t.Fatal(err)
	}

	err = driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Retry(ctx, time.Second)
	})

	if !errors.Is(err, rabbitmqqueue.ErrPublishNotConfirmed) {
		t.Fatalf("Consume = %v, want retry transfer failure", err)
	}

	if got := <-settlement; got != 0x00140028 {
		t.Fatalf("source method = %08x, want channel.close, not basic.ack", got)
	}
}

// TestFailureReturnDoesNotAcknowledgeSource models deletion of the archive
// queue after startup. A failed archive must never complete the original job.
func TestFailureReturnDoesNotAcknowledgeSource(t *testing.T) {
	t.Parallel()

	settlement := make(chan uint32, 1)
	url := amqptest.New(t, func(peer *amqptest.Peer) {
		peer.Handshake()
		peer.Deliver(peer.Setup(), []byte(`{"name":"work","payload":"e30="}`))
		peer.ReturnPublication(peer.Setup(), 1)
		frame := peer.Read()
		settlement <- frame.Method
		peer.Send(1, frame.Channel, []byte{0, 20, 0, 41})
		peer.Read()
	})
	queues := map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}}
	driver, err := rabbitmqqueue.New(url, queues, func(broker *rabbitmqqueue.Builder) {
		broker.FailureQueue("failed")
	})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	if err := driver.Start(ctx); err != nil {
		t.Fatal(err)
	}

	err = driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Fail(ctx, errors.New("cannot process"))
	})

	if !errors.Is(err, rabbitmqqueue.ErrPublishNotConfirmed) {
		t.Fatalf("Consume = %v, want archive transfer failure", err)
	}

	if got := <-settlement; got != 0x00140028 {
		t.Fatalf("source method = %08x, want channel.close, not basic.ack", got)
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
	driver, err := rabbitmqqueue.New(url, map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}})

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

// TestNewRequiresPositionalQueues checks that trusted routing, which has no
// safe default, is supplied independently of optional tuning callbacks.
func TestNewRequiresPositionalQueues(t *testing.T) {
	t.Parallel()

	queues := map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}}
	driver, err := rabbitmqqueue.New("amqp://localhost/", queues)

	if err != nil {
		t.Fatal(err)
	}

	delete(queues, "work")
	err = driver.Dispatch(t.Context(), queue.Message{Name: "work", Queue: "work"})

	if !errors.Is(err, rabbitmqqueue.ErrNotStarted) {
		t.Fatalf("Dispatch = %v, want detached routing and ErrNotStarted", err)
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

		for range 5 {
			peer.ReplySetup(peer.Read())
		}

		peer.Read()
		close(entered)
		<-release
	})
	driver, err := rabbitmqqueue.New(url, map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}})

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
// close replies. Stop must still close its owned connection before attempting
// a channel.close handshake that can wait forever for an unresponsive peer.
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
	driver, err := rabbitmqqueue.New(url, map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	var group sync.WaitGroup
	group.Go(func() {
		_ = driver.Dispatch(ctx, queue.Message{Name: "work", Queue: "work", Payload: []byte(`{}`)})
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
		t.Fatal("Stop blocked on publisher channel cleanup")
	}
}

// TestDeliveryMessageDetachesMetadata exercises the public delivery accessor.
// Handler edits must not change later reads or the envelope used for retry.
func TestDeliveryMessageDetachesMetadata(t *testing.T) {
	t.Parallel()

	url := amqptest.New(t, func(peer *amqptest.Peer) {
		peer.Handshake()
		frame := peer.Setup()
		peer.Deliver(frame, []byte(`{"name":"work","payload":"e30=","metadata":{"traceparent":"original"}}`))
		frame = peer.Read()
		peer.Send(1, frame.Channel, []byte{0, 20, 0, 41})
		peer.Read()
	})
	driver, err := rabbitmqqueue.New(url, map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	if err := driver.Start(ctx); err != nil {
		t.Fatal(err)
	}

	done := errors.New("done")
	err = driver.Consume(ctx, "work", func(_ context.Context, delivery queue.Delivery) error {
		message := delivery.Message()
		message.Metadata["traceparent"] = "changed"

		if got := delivery.Message().Metadata["traceparent"]; got != "original" {
			t.Errorf("second Message metadata = %q, want original", got)
		}

		return done
	})

	if !errors.Is(err, done) {
		t.Fatalf("Consume = %v, want test completion", err)
	}
}

// TestConsumeReturnsHandlerError makes a prefetch-one handler fail without
// settlement. Consume must exit and close its channel, allowing redelivery
// instead of idling forever with the only delivery reserved.
func TestConsumeReturnsHandlerError(t *testing.T) {
	t.Parallel()

	url := amqptest.New(t, func(peer *amqptest.Peer) {
		peer.Handshake()
		frame := peer.Setup()
		peer.Deliver(frame, []byte(`{"name":"work","payload":"e30="}`))
		frame = peer.Read()
		peer.Send(1, frame.Channel, []byte{0, 20, 0, 41})
		peer.Read()
	})
	driver, err := rabbitmqqueue.New(url, map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	if err := driver.Start(ctx); err != nil {
		t.Fatal(err)
	}

	want := errors.New("settlement failed")
	err = driver.Consume(ctx, "work", func(context.Context, queue.Delivery) error {
		return want
	})

	if !errors.Is(err, want) {
		t.Fatalf("Consume = %v, want handler error", err)
	}
}

// TestDispatchRejectsReturnedPublish sends basic.return before a positive
// publisher confirm. Broker acceptance alone must not report routed work.
func TestDispatchRejectsReturnedPublish(t *testing.T) {
	t.Parallel()

	mandatory := make(chan bool, 1)
	url := amqptest.New(t, func(peer *amqptest.Peer) {
		peer.Handshake()
		frame := peer.Setup()
		mandatory <- frame.Payload[len(frame.Payload)-1]&1 != 0
		peer.ReturnPublication(frame, 1)
		peer.Read() // Keep the socket alive until the test closes the driver.
	})
	driver, err := rabbitmqqueue.New(url, map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	if err := driver.Start(ctx); err != nil {
		t.Fatal(err)
	}

	err = driver.Dispatch(ctx, queue.Message{ID: "job-1", Name: "work", Queue: "work", Payload: []byte(`{}`)})

	if !errors.Is(err, rabbitmqqueue.ErrPublishNotConfirmed) {
		t.Errorf("Dispatch = %v, want ErrPublishNotConfirmed", err)
	}

	if !<-mandatory {
		t.Error("work publication omitted the mandatory flag")
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
	driver, err := rabbitmqqueue.New(url, map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}})

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

// TestNewRejectsPrefetchOverflow prevents the SDK's uint16 conversion from
// turning a configured finite delivery bound into unlimited broker prefetch.
func TestNewRejectsPrefetchOverflow(t *testing.T) {
	t.Parallel()

	queues := map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}}

	_, err := rabbitmqqueue.New("amqp://localhost/", queues, func(consumer *rabbitmqqueue.Builder) {
		consumer.Prefetch(65536)
	})

	if !errors.Is(err, rabbitmqqueue.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestNewRejectsDefaultRetryBeyondMaximum keeps Retry(0) usable after a
// successful construction; the normalized default must obey the same limit.
func TestNewRejectsDefaultRetryBeyondMaximum(t *testing.T) {
	t.Parallel()

	queues := map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}}

	_, err := rabbitmqqueue.New("amqp://localhost/", queues, func(consumer *rabbitmqqueue.Builder) {
		consumer.MaxRetryDelay(time.Millisecond)
	})

	if !errors.Is(err, rabbitmqqueue.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestNewRejectsSharedPhysicalQueues prevents a logical queue consumer from
// executing another queue's jobs through an aliased broker queue.
func TestNewRejectsSharedPhysicalQueues(t *testing.T) {
	t.Parallel()

	_, err := rabbitmqqueue.New("amqp://localhost/", map[string]rabbitmqqueue.Queue{
		"first":  {Name: "jobs", RoutingKey: "first"},
		"second": {Name: "jobs", RoutingKey: "second"},
	})

	if !errors.Is(err, rabbitmqqueue.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestNewRejectsSharedRoutingKeys prevents a direct-exchange dispatch from
// copying one logical job into multiple competing-consumer queues.
func TestNewRejectsSharedRoutingKeys(t *testing.T) {
	t.Parallel()

	_, err := rabbitmqqueue.New("amqp://localhost/", map[string]rabbitmqqueue.Queue{
		"first":  {Name: "first", RoutingKey: "work"},
		"second": {Name: "second", RoutingKey: "work"},
	})

	if !errors.Is(err, rabbitmqqueue.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestNewRejectsFailureWorkLoop prevents failure envelopes from returning to
// a queue consumed as portable jobs, producing an unbounded failure loop.
func TestNewRejectsFailureWorkLoop(t *testing.T) {
	t.Parallel()

	queues := map[string]rabbitmqqueue.Queue{"work": {Name: "jobs", RoutingKey: "work"}}

	_, err := rabbitmqqueue.New("amqp://localhost/", queues, func(consumer *rabbitmqqueue.Builder) {
		consumer.FailureQueue("jobs")
	})

	if !errors.Is(err, rabbitmqqueue.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}
