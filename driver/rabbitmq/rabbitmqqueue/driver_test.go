package rabbitmqqueue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/queue"

	"github.com/rabbitmq/amqp091-go"
)

// TestNormalizeConfigAppliesDefaults verifies default AMQP driver settings.
func TestNormalizeConfigAppliesDefaults(t *testing.T) {
	t.Parallel()

	config, err := normalizeConfig(settings{
		Queues: map[string]Queue{
			"work": {
				Name:       "jobs.work",
				RoutingKey: "work",
			},
		},
	})

	if err != nil {
		t.Fatal(err)
	}

	assertDefaultAMQPConfig(t, config)
}

// TestNormalizeConfigRejectsMissingQueueRoutingKey verifies queue validation.
func TestNormalizeConfigRejectsMissingQueueRoutingKey(t *testing.T) {
	t.Parallel()

	_, err := normalizeConfig(settings{
		Queues: map[string]Queue{
			"work": {
				Name: "jobs.work",
			},
		},
	})

	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidConfig)
	}
}

// TestNewRequiresURL verifies connection URL validation.
func TestNewRequiresURL(t *testing.T) {
	t.Parallel()

	_, err := New(" ", map[string]Queue{
		"work": {
			Name:       "jobs.work",
			RoutingKey: "work",
		},
	})

	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidConfig)
	}
}

// TestNewFromRejectsConfiguredAutomaticRecovery checks the constructor without
// network I/O. Refusing the connection before returning a driver also prevents
// callers from reaching unsafe delivery paths without calling Start first.
func TestNewFromRejectsConfiguredAutomaticRecovery(t *testing.T) {
	t.Parallel()

	connection := &amqp091.Connection{
		Config: amqp091.Config{
			Recovery: &amqp091.Recovery{
				ReconnectionConfig: &amqp091.ReconnectionConfig{
					MaxRetryCount: 1,
				},
			},
		},
	}

	driver, err := NewFrom(connection, map[string]Queue{
		"work": {
			Name:       "jobs.work",
			RoutingKey: "work",
		},
	})

	if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "automatic recovery") {
		t.Fatalf("error = %v, want ErrInvalidConfig naming automatic recovery", err)
	}

	if driver != nil {
		t.Fatal("rejected connection returned a usable driver")
	}
}

// TestNewFromRejectsNilConnection keeps nil-connection validation distinct from
// the automatic-recovery restriction, with otherwise valid queue settings.
func TestNewFromRejectsNilConnection(t *testing.T) {
	t.Parallel()

	_, err := NewFrom(nil, map[string]Queue{
		"work": {
			Name:       "jobs.work",
			RoutingKey: "work",
		},
	})

	if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "nil connection") {
		t.Fatalf("error = %v, want ErrInvalidConfig naming nil connection", err)
	}
}

// TestNewFromRejectsInvalidConfig preserves topology validation for borrowed
// connections. A valid non-recovering connection does not make missing queues
// acceptable, and no driver is returned on that configuration error.
func TestNewFromRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	driver, err := NewFrom(&amqp091.Connection{}, nil)

	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}

	if driver != nil {
		t.Fatal("invalid configuration returned a usable driver")
	}
}

// TestNewFromAcceptsDisabledRecovery distinguishes configured recovery from
// enabled recovery: an explicit zero retry count disables the SDK feature and
// remains compatible with a borrowed, non-recovering connection.
func TestNewFromAcceptsDisabledRecovery(t *testing.T) {
	t.Parallel()

	connection := &amqp091.Connection{
		Config: amqp091.Config{
			Recovery: &amqp091.Recovery{
				ReconnectionConfig: &amqp091.ReconnectionConfig{
					MaxRetryCount: 0,
				},
			},
		},
	}

	driver, err := NewFrom(connection, map[string]Queue{
		"work": {
			Name:       "jobs.work",
			RoutingKey: "work",
		},
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.Connection() != connection {
		t.Fatal("driver did not retain the caller's non-recovering connection")
	}
}

// TestDeliveryMessageUsesAMQPMessageIDAndAttemptHeader verifies that delivery
// metadata comes from the broker.
func TestDeliveryMessageUsesAMQPMessageIDAndAttemptHeader(t *testing.T) {
	t.Parallel()

	message, err := deliveryMessage("work", amqp091.Delivery{
		Body:      []byte(`{"name":"email","queue":"untrusted","payload":"e30="}`),
		MessageId: "amqp-id",
		Headers: amqp091.Table{
			attemptHeader: int32(3),
		},
	})

	if err != nil {
		t.Fatal(err)
	}

	assertAMQPBrokerMessage(t, message)
}

// TestDeliveryMessageAddsAttemptForBrokerRedelivery verifies redelivery attempt
// counting.
func TestDeliveryMessageAddsAttemptForBrokerRedelivery(t *testing.T) {
	t.Parallel()

	message, err := deliveryMessage("work", amqp091.Delivery{
		Body:        []byte(`{"name":"email","payload":"e30="}`),
		Redelivered: true,
	})

	if err != nil {
		t.Fatal(err)
	}

	if message.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", message.Attempts)
	}
}

// TestDeliveryMessageRejectsMalformedBody verifies invalid payload rejection.
func TestDeliveryMessageRejectsMalformedBody(t *testing.T) {
	t.Parallel()

	_, err := deliveryMessage("work", amqp091.Delivery{
		Body: []byte("not JSON"),
	})

	if !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidDelivery)
	}
}

// TestPublishingCreatesPersistentInitialAttempt verifies persistent message properties.
func TestPublishingCreatesPersistentInitialAttempt(t *testing.T) {
	t.Parallel()

	published := publishing("message-1", []byte("body"), 1)

	assertPersistentAMQPPublishing(t, published)
}

// TestRetryQueueNameUsesRoundedMilliseconds verifies AMQP TTL rounding in queue
// names.
func TestRetryQueueNameUsesRoundedMilliseconds(t *testing.T) {
	t.Parallel()

	consumer := retryQueueName("jobs.work", 1500*time.Millisecond+time.Nanosecond)

	if consumer != "jobs.work.retry.1501" {
		t.Fatalf("queue = %q, want jobs.work.retry.1501", consumer)
	}
}

// TestDelayedPublishingIncrementsAttemptAndSetsTTL verifies delayed message
// properties.
func TestDelayedPublishingIncrementsAttemptAndSetsTTL(t *testing.T) {
	t.Parallel()

	published := delayedPublishing("message-1", []byte("body"), 2, 1500*time.Millisecond+time.Nanosecond)

	if published.Expiration != "1501" {
		t.Fatalf("expiration = %q, want 1501", published.Expiration)
	}

	if published.Headers[attemptHeader] != int32(2) {
		t.Fatalf("attempt header = %v, want 2", published.Headers[attemptHeader])
	}

	if len(published.Headers) != 1 {
		t.Fatalf("headers = %v, want only the attempt header", published.Headers)
	}
}

// TestRetryQueueArgumentsDeadLetterToWorkExchange verifies retry dead-letter routing.
func TestRetryQueueArgumentsDeadLetterToWorkExchange(t *testing.T) {
	t.Parallel()

	arguments := retryQueueArguments("arkoren:jobs", "work", time.Minute)

	if arguments["x-expires"] != int64(2*time.Minute/time.Millisecond+time.Hour/time.Millisecond) {
		t.Fatalf("x-expires = %v", arguments["x-expires"])
	}

	if arguments["x-dead-letter-exchange"] != "arkoren:jobs" {
		t.Fatalf("dead-letter exchange = %v, want arkoren:jobs", arguments["x-dead-letter-exchange"])
	}

	if arguments["x-dead-letter-routing-key"] != "work" {
		t.Fatalf("dead-letter routing key = %v, want work", arguments["x-dead-letter-routing-key"])
	}
}

// TestAttemptsFallsBackToInitialDelivery verifies the initial attempt default.
func TestAttemptsFallsBackToInitialDelivery(t *testing.T) {
	t.Parallel()

	if got := attempts(nil, false); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

// TestDeliverySettlementRejectsSecondOperation verifies single-settlement enforcement.
func TestDeliverySettlementRejectsSecondOperation(t *testing.T) {
	t.Parallel()

	delivery := &delivery{}
	called := 0

	err := delivery.settle(t.Context(), func() error {
		called++

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if called != 1 {
		t.Fatalf("calls = %d, want 1", called)
	}

	if err := delivery.settle(t.Context(), func() error {
		return nil
	}); !errors.Is(err, ErrDeliverySettled) {
		t.Fatalf("error = %v, want %v", err, ErrDeliverySettled)
	}
}

// TestDeliverySettlementAllowsRetryAfterOperationFailure verifies recovery after
// a failed settlement.
func TestDeliverySettlementAllowsRetryAfterOperationFailure(t *testing.T) {
	t.Parallel()

	delivery := &delivery{}

	err := delivery.settle(t.Context(), func() error {
		return errors.New("broker unavailable")
	})

	if err == nil {
		t.Fatal("expected an error")
	}

	if err := delivery.settle(t.Context(), func() error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestDeliverySettlementHonorsCanceledContext verifies canceled settlement rejection.
func TestDeliverySettlementHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	delivery := &delivery{}

	err := delivery.settle(ctx, func() error {
		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want %v", err, context.Canceled)
	}
}

// TestDeliveryMessagePayloadCanBeHandledByConsumer verifies consumer-compatible
// payload decoding.
func TestDeliveryMessagePayloadCanBeHandledByConsumer(t *testing.T) {
	t.Parallel()

	message, err := deliveryMessage("work", amqp091.Delivery{
		Body: []byte(`{"name":"email","payload":"eyJpZCI6MX0="}`),
	})

	if err != nil {
		t.Fatal(err)
	}

	want := queue.Message{
		Name:     "email",
		Queue:    "work",
		Payload:  []byte(`{"id":1}`),
		Attempts: 1,
	}

	assertAMQPConsumerMessage(t, message, want)
}

const (
	// rabbitMQURL is the fixed RabbitMQ endpoint supplied by family Compose.
	rabbitMQURL = "amqp://arkoren:integration@localhost:5672/"

	// rabbitMQOperationTimeout bounds a RabbitMQ integration-test operation.
	rabbitMQOperationTimeout = 10 * time.Second
)

// TestNewFromRejectsUnsafeAutomaticRecovery uses a real SDK connection with
// default automatic recovery. Rejection must leave its configuration intact
// and the caller able to open channels on the same live connection.
func TestNewFromRejectsUnsafeAutomaticRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	connection, err := amqp091.DialConfig(rabbitMQURL, amqp091.Config{
		Dial:     amqp091.DefaultDial(rabbitMQOperationTimeout),
		Recovery: &amqp091.Recovery{},
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})

	if !connection.IsRecoveryEnabled() {
		t.Fatal("test requires a recovery-enabled SDK connection")
	}

	recovery := connection.Config.Recovery
	reconnection := *recovery.ReconnectionConfig

	driver, err := NewFrom(connection, map[string]Queue{
		"work": {
			Name:       testRabbitMQResourceName(t),
			RoutingKey: "work",
		},
	})

	if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), "automatic recovery") {
		t.Fatalf("error = %v, want ErrInvalidConfig naming automatic recovery", err)
	}

	if driver != nil {
		t.Fatal("rejected connection returned a usable driver")
	}

	if connection.IsClosed() || !connection.IsRecoveryEnabled() {
		t.Fatal("rejection closed the caller connection or disabled its recovery")
	}

	if connection.Config.Recovery != recovery || *recovery.ReconnectionConfig != reconnection {
		t.Fatal("rejection changed the caller's recovery configuration")
	}

	channel, err := connection.Channel()

	if err != nil {
		t.Fatalf("caller connection unusable after rejection: %v", err)
	}

	if err := channel.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestNewFromPreservesCallerConnection exercises the non-recovering connection
// through startup, dispatch, acknowledgement, cancellation, and stop. Operations
// cannot bypass Start, and even an expired Stop context leaves ownership with
// the caller rather than closing its connection.
func TestNewFromPreservesCallerConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), rabbitMQOperationTimeout)
	t.Cleanup(cancel)
	connection := newRabbitMQConnection(t, rabbitMQURL)
	resourceName := testRabbitMQResourceName(t)
	config := settings{
		Exchange: resourceName + ".exchange",
		Queues: map[string]Queue{
			"work": {
				Name:       resourceName + ".queue",
				RoutingKey: "work",
			},
		},
	}

	driver, err := NewFrom(connection, config.Queues, func(consumer *Builder) {
		consumer.Exchange(config.Exchange)
		consumer.Prefetch(config.Prefetch)
		consumer.RetryDelay(config.RetryDelay)
		consumer.MaxRetryDelay(config.MaxRetryDelay)
		consumer.FailureQueue(config.FailureQueue)
		consumer.Logger(config.Logger)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		stoppedCtx, stopCancel := context.WithCancel(context.Background())
		stopCancel()

		if err := driver.Stop(stoppedCtx); err != nil {
			t.Error(err)
		}

		if connection.IsClosed() {
			t.Error("Stop closed the caller's connection")
		}

		channel, err := connection.Channel()

		if err != nil {
			t.Errorf("caller connection unusable after Stop: %v", err)

			return
		}

		if err := channel.Close(); err != nil {
			t.Error(err)
		}
	})
	message := rabbitMQIntegrationMessage(resourceName, []byte(`{"value":"caller-owned"}`))

	if err := driver.Dispatch(ctx, message); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("Dispatch before Start = %v, want ErrNotStarted", err)
	}

	err = driver.Consume(ctx, "work", func(context.Context, queue.Delivery) error {
		return nil
	})

	if !errors.Is(err, ErrNotStarted) {
		t.Fatalf("Consume before Start = %v, want ErrNotStarted", err)
	}

	if err := driver.Ping(ctx); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("Ping before Start = %v, want ErrNotStarted", err)
	}

	if err := driver.Start(ctx); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		deleteRabbitMQResources(t, driver, config)
	})

	if err := driver.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	consumeCtx, consumeCancel := context.WithCancel(ctx)
	consumeResults, handlerResults, workers := startAcknowledgingRabbitMQConsumer(
		consumeCtx, driver, message, consumeCancel,
	)
	t.Cleanup(func() {
		consumeCancel()
		workers.Wait()
	})

	if err := driver.Dispatch(ctx, message); err != nil {
		t.Fatal(err)
	}

	waitForRabbitMQHandler(t, ctx, handlerResults)
	workers.Wait()

	assertRabbitMQConsumeCanceled(t, <-consumeResults)
	assertRabbitMQQueueEmpty(t, newRabbitMQInspectionChannel(t, rabbitMQURL), config.Queues["work"].Name)
}

// TestDriverDispatchPersistsConfirmedMessage verifies confirmed persistent dispatch.
func TestDriverDispatchPersistsConfirmedMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), rabbitMQOperationTimeout)
	t.Cleanup(cancel)
	driver, url, resourceName, queueName := newRabbitMQIntegrationDriver(t)
	message := rabbitMQIntegrationMessage(resourceName, []byte(`{"value":"persistent"}`))

	if err := driver.Dispatch(ctx, message); err != nil {
		t.Fatal(err)
	}

	channel := newRabbitMQInspectionChannel(t, url)
	delivered := rabbitMQGetDelivery(t, channel, queueName)

	if err := delivered.Ack(false); err != nil {
		t.Fatal(err)
	}

	assertPersistentRabbitMQDelivery(t, delivered, message)
}

// TestDriverConsumeAcknowledgesMessage verifies consumer acknowledgement behavior.
func TestDriverConsumeAcknowledgesMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), rabbitMQOperationTimeout)
	t.Cleanup(cancel)
	driver, url, resourceName, queueName := newRabbitMQIntegrationDriver(t)
	message := rabbitMQIntegrationMessage(resourceName, []byte(`{"value":"acknowledged"}`))
	consumeCtx, consumeCancel := context.WithCancel(ctx)
	t.Cleanup(consumeCancel)
	consumeResults, handlerResults, workers := startAcknowledgingRabbitMQConsumer(
		consumeCtx, driver, message, consumeCancel,
	)
	t.Cleanup(func() {
		consumeCancel()
		workers.Wait()
	})

	if err := driver.Dispatch(ctx, message); err != nil {
		t.Fatal(err)
	}

	waitForRabbitMQHandler(t, ctx, handlerResults)
	workers.Wait()

	assertRabbitMQConsumeCanceled(t, <-consumeResults)
	assertRabbitMQQueueEmpty(t, newRabbitMQInspectionChannel(t, url), queueName)
}

// newRabbitMQDriver creates a test-local driver for the shared RabbitMQ endpoint.
func newRabbitMQDriver(t *testing.T, config settings) (*Driver, string) {
	t.Helper()

	driver, err := New(rabbitMQURL, config.Queues, func(consumer *Builder) {
		consumer.Exchange(config.Exchange)
		consumer.Prefetch(config.Prefetch)
		consumer.RetryDelay(config.RetryDelay)
		consumer.MaxRetryDelay(config.MaxRetryDelay)
		consumer.FailureQueue(config.FailureQueue)
		consumer.Logger(config.Logger)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		closeRabbitMQDriver(t, driver)
	})
	t.Cleanup(func() {
		deleteRabbitMQResources(t, driver, config)
	})

	return driver, rabbitMQURL
}

// newRabbitMQConnection creates an inspection connection with a bounded dial.
func newRabbitMQConnection(t *testing.T, url string) *amqp091.Connection {
	t.Helper()

	connection, err := amqp091.DialConfig(url, amqp091.Config{
		Dial: amqp091.DefaultDial(rabbitMQOperationTimeout),
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})

	return connection
}

// testRabbitMQResourceName returns an AMQP-safe name unique to the current test.
func testRabbitMQResourceName(t *testing.T) string {
	t.Helper()

	bytes := make([]byte, 6)

	if _, err := rand.Read(bytes); err != nil {
		t.Fatal(err)
	}

	var name strings.Builder

	for _, character := range strings.ToLower(t.Name()) {
		if rabbitMQResourceCharacter(character) {
			name.WriteRune(character)
			continue
		}

		name.WriteByte('-')
	}

	return "arkoren-" + strings.Trim(name.String(), "-") + "-" + hex.EncodeToString(bytes)
}

// newRabbitMQIntegrationDriver configures an isolated driver and resource names.
func newRabbitMQIntegrationDriver(t *testing.T) (*Driver, string, string, string) {
	resourceName := testRabbitMQResourceName(t)
	queueName := resourceName + ".queue"
	driver, url := newRabbitMQDriver(t, settings{
		Exchange: resourceName + ".exchange",
		Queues: map[string]Queue{
			"work": {
				Name:       queueName,
				RoutingKey: resourceName + ".routing",
			},
		},
	})

	return driver, url, resourceName, queueName
}

// rabbitMQIntegrationMessage creates the message dispatched by an integration test.
func rabbitMQIntegrationMessage(resourceName string, payload []byte) queue.Message {
	return queue.Message{
		ID:      resourceName + ".message",
		Name:    "integration",
		Queue:   "work",
		Payload: payload,
	}
}

// newRabbitMQInspectionChannel opens an inspection channel and schedules its closure.
func newRabbitMQInspectionChannel(t *testing.T, url string) *amqp091.Channel {
	connection := newRabbitMQConnection(t, url)
	channel, err := connection.Channel()

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := channel.Close(); err != nil {
			t.Error(err)
		}
	})

	return channel
}

// rabbitMQGetDelivery gets a required delivery from queueName without requeueing it.
func rabbitMQGetDelivery(t *testing.T, channel *amqp091.Channel, queueName string) amqp091.Delivery {
	delivered, ok, err := channel.Get(queueName, false)

	if err != nil {
		t.Fatal(err)
	}

	if !ok {
		t.Fatal("expected dispatched message")
	}

	return delivered
}

// assertPersistentRabbitMQDelivery verifies the persistent dispatch envelope.
func assertPersistentRabbitMQDelivery(t *testing.T, delivered amqp091.Delivery, message queue.Message) {
	if delivered.DeliveryMode != amqp091.Persistent {
		t.Fatalf("delivery mode = %d, want %d", delivered.DeliveryMode, amqp091.Persistent)
	}

	if delivered.MessageId != message.ID {
		t.Fatalf("message ID = %q, want %q", delivered.MessageId, message.ID)
	}
}

// startAcknowledgingRabbitMQConsumer starts a consumer that acknowledges message.
func startAcknowledgingRabbitMQConsumer(
	ctx context.Context,
	driver *Driver,
	message queue.Message,
	cancel context.CancelFunc,
) (<-chan error, <-chan error, *sync.WaitGroup) {
	handlerResults := make(chan error, 1)
	consumeResults := make(chan error, 1)
	workers := &sync.WaitGroup{}
	workers.Add(1)
	go func() {
		defer workers.Done()

		consumeResults <- driver.Consume(ctx, "work", rabbitMQAcknowledgingHandler(message, handlerResults, cancel))
	}()

	return consumeResults, handlerResults, workers
}

// rabbitMQAcknowledgingHandler returns a handler that checks and acknowledges message.
func rabbitMQAcknowledgingHandler(
	message queue.Message,
	results chan<- error,
	cancel context.CancelFunc,
) func(context.Context, queue.Delivery) error {
	return func(ctx context.Context, delivery queue.Delivery) error {
		received := delivery.Message()

		if !rabbitMQMessagesEqual(received, message) {
			results <- fmt.Errorf("message = %#v, want %#v", received, message)
			cancel()

			return nil
		}

		results <- delivery.Acknowledge(ctx)
		cancel()

		return nil
	}
}

// rabbitMQMessagesEqual reports whether two messages have equal integration fields.
func rabbitMQMessagesEqual(got, want queue.Message) bool {
	return got.ID == want.ID && got.Name == want.Name && got.Queue == want.Queue && string(got.Payload) == string(want.Payload)
}

// waitForRabbitMQHandler waits for a handler result or the operation deadline.
func waitForRabbitMQHandler(t *testing.T, ctx context.Context, results <-chan error) {
	select {
	case err := <-results:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatalf("wait for acknowledged delivery: %v", ctx.Err())
	}
}

// assertRabbitMQConsumeCanceled verifies Consume stopped after the handler canceled it.
func assertRabbitMQConsumeCanceled(t *testing.T, err error) {
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("consume error = %v, want %v", err, context.Canceled)
	}
}

// assertRabbitMQQueueEmpty verifies queueName has no queued messages.
func assertRabbitMQQueueEmpty(t *testing.T, channel *amqp091.Channel, queueName string) {
	consumer, err := channel.QueueDeclarePassive(queueName, true, false, false, false, nil)

	if err != nil {
		t.Fatal(err)
	}

	if consumer.Messages != 0 {
		t.Fatalf("queued messages = %d, want 0", consumer.Messages)
	}
}

// closeRabbitMQDriver closes driver unless it was already closed during cleanup.
func closeRabbitMQDriver(t *testing.T, driver *Driver) {
	if err := driver.Stop(context.Background()); err != nil && !errors.Is(err, ErrDriverClosed) {
		t.Error(err)
	}
}

// deleteRabbitMQResources removes the driver's test queues and exchange.
func deleteRabbitMQResources(t *testing.T, driver *Driver, config settings) {
	for _, consumer := range config.Queues {
		deleteRabbitMQQueue(t, driver, consumer.Name)
	}

	deleteRabbitMQExchange(t, driver, config.Exchange)
}

// deleteRabbitMQQueue removes a queue unless its channel has already closed.
func deleteRabbitMQQueue(t *testing.T, driver *Driver, queueName string) {
	if _, err := driver.publisher.QueueDelete(queueName, false, false, false); err != nil && !errors.Is(err, amqp091.ErrClosed) {
		t.Errorf("delete RabbitMQ queue %q: %v", queueName, err)
	}
}

// deleteRabbitMQExchange removes an exchange unless its channel has already closed.
func deleteRabbitMQExchange(t *testing.T, driver *Driver, exchangeName string) {
	if err := driver.publisher.ExchangeDelete(exchangeName, false, false); err != nil && !errors.Is(err, amqp091.ErrClosed) {
		t.Errorf("delete RabbitMQ exchange %q: %v", exchangeName, err)
	}
}

// rabbitMQResourceCharacter reports whether character is valid in an AMQP resource name.
func rabbitMQResourceCharacter(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

// assertDefaultAMQPConfig verifies the defaults assigned to a valid AMQP configuration.
func assertDefaultAMQPConfig(t *testing.T, config settings) {
	if config.Exchange != DefaultExchange {
		t.Fatalf("exchange = %q, want %q", config.Exchange, DefaultExchange)
	}

	if config.Prefetch != defaultPrefetch {
		t.Fatalf("prefetch = %d, want %d", config.Prefetch, defaultPrefetch)
	}

	if config.RetryDelay != defaultRetryDelay {
		t.Fatalf("retry delay = %s, want %s", config.RetryDelay, defaultRetryDelay)
	}
}

// assertAMQPBrokerMessage verifies delivery metadata populated from the broker.
func assertAMQPBrokerMessage(t *testing.T, message queue.Message) {
	if message.Queue != "work" {
		t.Fatalf("queue = %q, want work", message.Queue)
	}

	if message.ID != "amqp-id" {
		t.Fatalf("ID = %q, want amqp-id", message.ID)
	}

	if message.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", message.Attempts)
	}
}

// assertPersistentAMQPPublishing verifies persistent properties for an initial attempt.
func assertPersistentAMQPPublishing(t *testing.T, published amqp091.Publishing) {
	assertPersistentAMQPProperties(t, published)
	assertInitialAMQPMessageProperties(t, published)
}

// assertPersistentAMQPProperties verifies the persistence and content-type properties.
func assertPersistentAMQPProperties(t *testing.T, published amqp091.Publishing) {
	if published.DeliveryMode != amqp091.Persistent {
		t.Fatalf("delivery mode = %d, want %d", published.DeliveryMode, amqp091.Persistent)
	}

	if published.ContentType != "application/json" {
		t.Fatalf("content type = %q, want application/json", published.ContentType)
	}
}

// assertInitialAMQPMessageProperties verifies the identifier and initial attempt header.
func assertInitialAMQPMessageProperties(t *testing.T, published amqp091.Publishing) {
	if published.MessageId != "message-1" {
		t.Fatalf("message ID = %q, want message-1", published.MessageId)
	}

	if published.Headers[attemptHeader] != int32(1) {
		t.Fatalf("attempt header = %v, want 1", published.Headers[attemptHeader])
	}
}

// assertAMQPConsumerMessage verifies the fields decoded for consumer handling.
func assertAMQPConsumerMessage(t *testing.T, got, want queue.Message) {
	assertAMQPConsumerMessageIdentity(t, got, want)
	assertAMQPConsumerMessageContent(t, got, want)
}

// assertAMQPConsumerMessageIdentity verifies the job name and queue.
func assertAMQPConsumerMessageIdentity(t *testing.T, got, want queue.Message) {
	if got.Name != want.Name {
		t.Fatalf("message name = %q, want %q", got.Name, want.Name)
	}

	if got.Queue != want.Queue {
		t.Fatalf("message queue = %q, want %q", got.Queue, want.Queue)
	}
}

// assertAMQPConsumerMessageContent verifies the payload and attempts.
func assertAMQPConsumerMessageContent(t *testing.T, got, want queue.Message) {
	if string(got.Payload) != string(want.Payload) {
		t.Fatalf("message payload = %q, want %q", got.Payload, want.Payload)
	}

	if got.Attempts != want.Attempts {
		t.Fatalf("message attempts = %d, want %d", got.Attempts, want.Attempts)
	}
}

// TestAttemptsCountsQuorumRedeliveries verifies that broker redeliveries
// contribute to attempts even when the original attempt header is unchanged.
func TestAttemptsCountsQuorumRedeliveries(t *testing.T) {
	t.Parallel()

	headers := amqp091.Table{attemptHeader: int32(1), "x-delivery-count": int64(3)}

	if got := attempts(headers, true); got != 4 {
		t.Fatalf("attempts = %d, want 4", got)
	}
}

// TestDispatchBeforeStartReturnsErrNotStarted verifies that an unstarted
// driver refuses to publish.
func TestDispatchBeforeStartReturnsErrNotStarted(t *testing.T) {
	driver, err := New("amqp://guest:guest@localhost:1/", map[string]Queue{
		"work": {
			Name:       "jobs.work",
			RoutingKey: "work",
		},
	})

	if err != nil {
		t.Fatal(err)
	}

	err = driver.Dispatch(t.Context(), queue.Message{
		ID:    "1",
		Name:  "x",
		Queue: "work",
	})

	if !errors.Is(err, ErrNotStarted) {
		t.Fatalf("got %v, want ErrNotStarted", err)
	}
}

// TestPingBeforeStartReturnsErrNotStarted verifies Ping before Start.
func TestPingBeforeStartReturnsErrNotStarted(t *testing.T) {
	driver, err := New("amqp://guest:guest@localhost:1/", map[string]Queue{
		"work": {
			Name:       "jobs.work",
			RoutingKey: "work",
		},
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Ping(t.Context()); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("got %v, want ErrNotStarted", err)
	}
}

// TestRetryBucketRoundsUpAndBoundsQueues verifies that jittered delays share
// queues and are never shortened.
func TestRetryBucketRoundsUpAndBoundsQueues(t *testing.T) {
	t.Parallel()

	cases := map[time.Duration]time.Duration{
		time.Nanosecond:                   time.Second,
		1500 * time.Millisecond:           2 * time.Second,
		time.Minute:                       time.Minute,
		time.Minute + time.Millisecond:    time.Minute + 15*time.Second,
		time.Hour + time.Millisecond:      time.Hour + 5*time.Minute,
		24*time.Hour + time.Millisecond:   25 * time.Hour,
		math.MaxUint32 * time.Millisecond: math.MaxUint32 * time.Millisecond,
	}

	for delay, want := range cases {
		if got := retryBucket(delay); got != want {
			t.Fatalf("retryBucket(%s) = %s, want %s", delay, got, want)
		}
	}

	queues := map[string]bool{}

	for jitter := range 500 {
		queues[retryQueueName("jobs", retryBucket(10*time.Second+time.Duration(jitter+1)*time.Millisecond))] = true
	}

	if len(queues) > 1 {
		t.Fatalf("%d queues for delays within one second", len(queues))
	}
}
