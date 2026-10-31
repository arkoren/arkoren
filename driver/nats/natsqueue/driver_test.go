package natsqueue

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/queue"

	"github.com/nats-io/nats.go"
)

// TestNormalizeConfigAppliesDefaults verifies the default consumer settings.
func TestNormalizeConfigAppliesDefaults(t *testing.T) {
	t.Parallel()

	config, err := normalizeConfig(settings{
		Stream: "JOBS",
		Queues: map[string]Queue{
			"work": {
				Subject: "jobs.work",
				Durable: "work",
			},
		},
	})

	if err != nil {
		t.Fatal(err)
	}

	assertDefaultJetStreamConfig(t, config)
}

// TestNormalizeConfigRejectsMissingDurable verifies durable names are required.
func TestNormalizeConfigRejectsMissingDurable(t *testing.T) {
	t.Parallel()

	_, err := normalizeConfig(settings{
		Stream: "JOBS",
		Queues: map[string]Queue{
			"work": {
				Subject: "jobs.work",
			},
		},
	})

	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidConfig)
	}
}

// TestNormalizeConfigRejectsDuplicateSubject verifies queue subjects are unique.
func TestNormalizeConfigRejectsDuplicateSubject(t *testing.T) {
	t.Parallel()

	_, err := normalizeConfig(settings{
		Stream: "JOBS",
		Queues: map[string]Queue{
			"first": {
				Subject: "jobs.work",
				Durable: "first",
			},
			"second": {
				Subject: "jobs.work",
				Durable: "second",
			},
		},
	})

	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidConfig)
	}
}

// TestValidateConsumerRejectsPushConsumer verifies push consumers are rejected.
func TestValidateConsumerRejectsPushConsumer(t *testing.T) {
	t.Parallel()

	err := validateConsumer(settings{
		Batch:         1,
		MaxAckPending: 1,
		AckWait:       time.Second,
	}, Queue{
		Subject: "jobs.work",
		Durable: "work",
	}, nats.ConsumerConfig{
		Durable:        "work",
		DeliverSubject: "deliver.work",
		AckPolicy:      nats.AckExplicitPolicy,
		FilterSubject:  "jobs.work",
	})

	if !errors.Is(err, ErrResourcesNotProvisioned) {
		t.Fatalf("error = %v, want %v", err, ErrResourcesNotProvisioned)
	}
}

// TestValidateStreamRejectsMissingQueueSubject verifies stream subjects are required.
func TestValidateStreamRejectsMissingQueueSubject(t *testing.T) {
	t.Parallel()

	err := validateStream(settings{
		Stream: "JOBS",
		Queues: map[string]Queue{
			"work": {
				Subject: "jobs.work",
				Durable: "work",
			},
		},
	}, nats.StreamConfig{
		Subjects: []string{
			"jobs.other",
		},
	})

	if !errors.Is(err, ErrResourcesNotProvisioned) {
		t.Fatalf("error = %v, want %v", err, ErrResourcesNotProvisioned)
	}
}

// TestMarshalMessagePreservesFullEnvelope verifies every job message field survives encoding.
func TestMarshalMessagePreservesFullEnvelope(t *testing.T) {
	t.Parallel()

	want := queue.Message{
		ID:       "job-1",
		Name:     "email",
		Queue:    "work",
		Payload:  []byte(`{"id":1}`),
		Attempts: 9,
	}

	body, err := marshalMessage(want)

	if err != nil {
		t.Fatal(err)
	}

	var got queue.Message

	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}

	if !jetStreamEnvelopeEqual(got, want) {
		t.Fatalf("message = %#v, want %#v", got, want)
	}
}

// TestNewMessageIDIsOpaqueAndNonEmpty verifies generated identifiers are nonempty Base64 values.
func TestNewMessageIDIsOpaqueAndNonEmpty(t *testing.T) {
	t.Parallel()

	id, err := newMessageID()

	if err != nil {
		t.Fatal(err)
	}

	if id == "" {
		t.Fatal("message ID is empty")
	}

	_, err = base64.RawURLEncoding.DecodeString(id)

	if err != nil {
		t.Fatal(err)
	}
}

// TestDecodeMessageRejectsMalformedEnvelope verifies malformed message bodies are rejected.
func TestDecodeMessageRejectsMalformedEnvelope(t *testing.T) {
	t.Parallel()

	_, err := decodeMessage("work", &nats.Msg{
		Data: []byte("not JSON"),
	})

	if !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidDelivery)
	}
}

// TestDeliveryRetryRejectsNegativeDelay verifies negative retry delays are rejected.
func TestDeliveryRetryRejectsNegativeDelay(t *testing.T) {
	t.Parallel()

	delivery := newDelivery(&Driver{}, &nats.Msg{}, queue.Message{})

	err := delivery.Retry(t.Context(), -time.Second)

	if !errors.Is(err, ErrUnsupportedRetryDelay) {
		t.Fatalf("error = %v, want %v", err, ErrUnsupportedRetryDelay)
	}
}

// TestDeliverySettlementRejectsSecondOperation verifies a delivery cannot settle twice.
func TestDeliverySettlementRejectsSecondOperation(t *testing.T) {
	t.Parallel()

	delivery := newDelivery(&Driver{}, &nats.Msg{}, queue.Message{})

	if delivery == nil {
		t.Fatal("delivery is nil")
	}

	if err := delivery.settle(t.Context(), func() error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err := delivery.settle(t.Context(), func() error {
		return nil
	})

	if !errors.Is(err, ErrDeliverySettled) {
		t.Fatalf("error = %v, want %v", err, ErrDeliverySettled)
	}
}

// TestFailureMessageIncludesSafeEnvelope verifies failure messages retain the safe envelope.
func TestFailureMessageIncludesSafeEnvelope(t *testing.T) {
	t.Parallel()

	message := failureMessage{
		Message: queue.Message{
			ID:      "job-1",
			Queue:   "work",
			Payload: []byte{},
		},
		Error:      errorText(errors.New("failed")),
		RawMessage: "not JSON",
	}

	body, err := json.Marshal(message)

	if err != nil {
		t.Fatal(err)
	}

	var got failureMessage

	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}

	if !failureMessagesEqual(got, message) {
		t.Fatalf("failure message = %#v, want %#v", got, message)
	}
}

const (
	// jetStreamTestEndpoint is the fixed NATS endpoint supplied by family Compose.
	jetStreamTestEndpoint = "localhost:4222"
	// jetStreamTestQueue identifies the queue used by integration tests.
	jetStreamTestQueue = "work"
	// jetStreamTestOperationTimeout limits individual integration test operations.
	jetStreamTestOperationTimeout = 10 * time.Second
)

// jetStreamTestResources identifies the isolated JetStream resources for one test.
type jetStreamTestResources struct {
	// stream is the test-specific JetStream stream name.
	stream string

	// durable is the test-specific durable consumer name.
	durable string

	// subject is the test-specific job subject.
	subject string

	// messageID is the test-specific job message identifier.
	messageID string
}

// TestDriverProvisionsStreamAndConsumer verifies configured resources are provisioned.
func TestDriverProvisionsStreamAndConsumer(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), jetStreamTestOperationTimeout)
	t.Cleanup(cancel)
	_, inspector, resources := newJetStreamTestDriver(t, ctx, time.Second, 2)
	stream, err := inspector.JetStream()

	if err != nil {
		t.Fatal(err)
	}

	assertJetStreamResourcesProvisioned(t, ctx, stream, resources)
}

// TestDriverDispatchesAndAcknowledges verifies acknowledged jobs are delivered once.
func TestDriverDispatchesAndAcknowledges(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), jetStreamTestOperationTimeout)
	t.Cleanup(cancel)
	driver, _, resources := newJetStreamTestDriver(t, ctx, jetStreamTestOperationTimeout, 2)
	consumeCtx, consumeCancel := context.WithCancel(ctx)
	t.Cleanup(consumeCancel)
	consumeResults, received, consumers := startAcknowledgingJetStreamConsumer(consumeCtx, driver, consumeCancel)
	t.Cleanup(func() {
		consumeCancel()
		consumers.Wait()
	})
	want := jetStreamIntegrationMessage(resources, []byte(`{"value":"acknowledged"}`))

	if err := driver.Dispatch(ctx, want); err != nil {
		t.Fatal(err)
	}

	assertJetStreamMessage(t, ctx, received, want)
	consumers.Wait()
	assertJetStreamConsumeCanceled(t, <-consumeResults)
}

// TestDriverRetryRedeliversExactlyOnce verifies retried jobs are redelivered once.
func TestDriverRetryRedeliversExactlyOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), jetStreamTestOperationTimeout)
	t.Cleanup(cancel)
	driver, _, resources := newJetStreamTestDriver(t, ctx, jetStreamTestOperationTimeout, 2)
	consumeCtx, consumeCancel := context.WithCancel(ctx)
	t.Cleanup(consumeCancel)
	consumeResults, attempts, consumers := startRetryingJetStreamConsumer(consumeCtx, driver, consumeCancel)
	t.Cleanup(func() {
		consumeCancel()
		consumers.Wait()
	})

	if err := driver.Dispatch(ctx, jetStreamIntegrationMessage(resources, []byte(`{"value":"retry"}`))); err != nil {
		t.Fatal(err)
	}

	assertJetStreamAttempt(t, ctx, attempts, 1, "first delivery")
	assertJetStreamAttempt(t, ctx, attempts, 2, "redelivery")
	consumers.Wait()
	assertJetStreamConsumeCanceled(t, <-consumeResults)
}

// newJetStreamTestDriver creates a test-local driver for the shared server.
func newJetStreamTestDriver(
	t *testing.T,
	operationContext context.Context,
	ackWait time.Duration,
	maxDeliver int,
) (*Driver, *nats.Conn, jetStreamTestResources) {
	t.Helper()

	resources := newJetStreamTestResources(t)
	driver := newJetStreamDriver(t, resources, ackWait, maxDeliver)
	inspector := newJetStreamInspector(t)
	stream, err := inspector.JetStream()

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		deleteJetStreamStream(t, stream, resources.stream)
	})

	return driver, inspector, resources
}

// newJetStreamTestResources creates unique NATS resource names for one test.
func newJetStreamTestResources(t *testing.T) jetStreamTestResources {
	t.Helper()

	suffix := jetStreamTestSuffix(t)

	return jetStreamTestResources{
		stream:    fmt.Sprintf("stream-%s", suffix),
		durable:   fmt.Sprintf("consumer-%s", suffix),
		subject:   fmt.Sprintf("jobs.%s", suffix),
		messageID: fmt.Sprintf("message-%s", suffix),
	}
}

// jetStreamTestSuffix returns an ASCII-safe, random test-specific NATS suffix.
func jetStreamTestSuffix(t *testing.T) string {
	t.Helper()

	value := make([]byte, 8)

	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}

	name := strings.Map(jetStreamTestCharacter, t.Name())

	return fmt.Sprintf("integration-%s-%s", strings.Trim(name, "-"), hex.EncodeToString(value))
}

// assertJetStreamResourcesProvisioned verifies the configured stream and consumer.
func assertJetStreamResourcesProvisioned(
	t *testing.T,
	ctx context.Context,
	stream nats.JetStreamContext,
	resources jetStreamTestResources,
) {
	streamInfo := jetStreamStreamInfo(t, ctx, stream, resources.stream)

	assertJetStreamSubject(t, streamInfo, resources.subject)

	consumerInfo := jetStreamConsumerInfo(t, ctx, stream, resources)

	assertJetStreamConsumerConfig(t, consumerInfo, resources.subject)
}

// jetStreamStreamInfo returns stream metadata or fails the test.
func jetStreamStreamInfo(t *testing.T, ctx context.Context, stream nats.JetStreamContext, streamName string) *nats.StreamInfo {
	streamInfo, err := stream.StreamInfo(streamName, nats.Context(ctx))

	if err != nil {
		t.Fatal(err)
	}

	return streamInfo
}

// assertJetStreamSubject verifies streamInfo contains only the expected subject.
func assertJetStreamSubject(t *testing.T, streamInfo *nats.StreamInfo, subject string) {
	if len(streamInfo.Config.Subjects) != 1 || streamInfo.Config.Subjects[0] != subject {
		t.Fatalf("stream subjects = %v, want [%q]", streamInfo.Config.Subjects, subject)
	}
}

// jetStreamConsumerInfo returns consumer metadata or fails the test.
func jetStreamConsumerInfo(
	t *testing.T,
	ctx context.Context,
	stream nats.JetStreamContext,
	resources jetStreamTestResources,
) *nats.ConsumerInfo {
	consumerInfo, err := stream.ConsumerInfo(resources.stream, resources.durable, nats.Context(ctx))

	if err != nil {
		t.Fatal(err)
	}

	return consumerInfo
}

// assertJetStreamConsumerConfig verifies the expected consumer filter and acknowledgement policy.
func assertJetStreamConsumerConfig(t *testing.T, consumerInfo *nats.ConsumerInfo, subject string) {
	if consumerInfo.Config.FilterSubject != subject {
		t.Fatalf("consumer filter subject = %q, want %q", consumerInfo.Config.FilterSubject, subject)
	}

	if consumerInfo.Config.AckPolicy != nats.AckExplicitPolicy {
		t.Fatalf("consumer acknowledgement policy = %v, want %v", consumerInfo.Config.AckPolicy, nats.AckExplicitPolicy)
	}
}

// startAcknowledgingJetStreamConsumer starts a consumer that acknowledges one delivery.
func startAcknowledgingJetStreamConsumer(
	ctx context.Context,
	driver *Driver,
	cancel context.CancelFunc,
) (<-chan error, <-chan queue.Message, *sync.WaitGroup) {
	received := make(chan queue.Message, 1)
	consumeResults := make(chan error, 1)
	consumers := &sync.WaitGroup{}
	consumers.Add(1)
	go func() {
		defer consumers.Done()

		consumeResults <- driver.Consume(ctx, jetStreamTestQueue, acknowledgingJetStreamHandler(received, cancel))
	}()

	return consumeResults, received, consumers
}

// acknowledgingJetStreamHandler returns a handler that acknowledges and records a delivery.
func acknowledgingJetStreamHandler(
	received chan<- queue.Message,
	cancel context.CancelFunc,
) func(context.Context, queue.Delivery) error {
	return func(ctx context.Context, delivery queue.Delivery) error {
		message := delivery.Message()

		if err := delivery.Acknowledge(ctx); err != nil {
			return err
		}

		received <- message
		cancel()

		return nil
	}
}

// jetStreamIntegrationMessage creates a message for a JetStream integration scenario.
func jetStreamIntegrationMessage(resources jetStreamTestResources, payload []byte) queue.Message {
	return queue.Message{
		ID:      resources.messageID,
		Name:    "integration",
		Queue:   jetStreamTestQueue,
		Payload: payload,
	}
}

// assertJetStreamMessage waits for and compares a delivered message.
func assertJetStreamMessage(t *testing.T, ctx context.Context, received <-chan queue.Message, want queue.Message) {
	select {
	case got := <-received:
		if !jetStreamMessagesEqual(got, want) {
			t.Fatalf("message = %#v, want %#v", got, want)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// jetStreamMessagesEqual reports whether messages have matching delivery fields.
func jetStreamMessagesEqual(got, want queue.Message) bool {
	return got.ID == want.ID && got.Name == want.Name && got.Queue == want.Queue && string(got.Payload) == string(want.Payload)
}

// startRetryingJetStreamConsumer starts a consumer that retries its first delivery.
func startRetryingJetStreamConsumer(
	ctx context.Context,
	driver *Driver,
	cancel context.CancelFunc,
) (<-chan error, <-chan int, *sync.WaitGroup) {
	attempts := make(chan int, 2)
	consumeResults := make(chan error, 1)
	consumers := &sync.WaitGroup{}
	consumers.Add(1)
	go func() {
		defer consumers.Done()

		consumeResults <- driver.Consume(ctx, jetStreamTestQueue, retryingJetStreamHandler(attempts, cancel))
	}()

	return consumeResults, attempts, consumers
}

// retryingJetStreamHandler returns a handler that retries once before acknowledging.
func retryingJetStreamHandler(attempts chan<- int, cancel context.CancelFunc) func(context.Context, queue.Delivery) error {
	return func(ctx context.Context, delivery queue.Delivery) error {
		attempt := delivery.Message().Attempts
		attempts <- attempt

		if attempt == 1 {
			return delivery.Retry(ctx, 0)
		}

		if attempt != 2 {
			return fmt.Errorf("unexpected delivery attempt %d", attempt)
		}

		if err := delivery.Acknowledge(ctx); err != nil {
			return err
		}

		cancel()

		return nil
	}
}

// assertJetStreamAttempt waits for an expected delivery attempt.
func assertJetStreamAttempt(t *testing.T, ctx context.Context, attempts <-chan int, want int, description string) {
	select {
	case got := <-attempts:
		if got != want {
			t.Fatalf("%s attempt = %d, want %d", description, got, want)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// assertJetStreamConsumeCanceled verifies Consume stopped after the handler canceled it.
func assertJetStreamConsumeCanceled(t *testing.T, err error) {
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("consume error = %v, want %v", err, context.Canceled)
	}
}

// newJetStreamDriver creates and closes a driver for resources.
func newJetStreamDriver(t *testing.T, resources jetStreamTestResources, ackWait time.Duration, maxDeliver int) *Driver {
	driver, err := New(resources.stream, map[string]Queue{
		jetStreamTestQueue: {Subject: resources.subject, Durable: resources.durable},
	}, func(consumer *Builder) {
		consumer.URLs([]string{fmt.Sprintf("nats://%s", jetStreamTestEndpoint)}...)
		consumer.Provision(true)
		consumer.Batch(1)
		consumer.MaxAckPending(1)
		consumer.AckWait(ackWait)
		consumer.MaxDeliver(maxDeliver)
		consumer.Options([]nats.Option{nats.Timeout(jetStreamTestOperationTimeout)}...)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		closeJetStreamDriver(t, driver)
	})

	return driver
}

// closeJetStreamDriver reports driver cleanup failures.
func closeJetStreamDriver(t *testing.T, driver *Driver) {
	if err := driver.Stop(context.Background()); err != nil {
		t.Errorf("close JetStream driver: %v", err)
	}
}

// newJetStreamInspector opens an independent connection for inspection and cleanup.
func newJetStreamInspector(t *testing.T) *nats.Conn {
	inspector, err := nats.Connect(fmt.Sprintf("nats://%s", jetStreamTestEndpoint), nats.Timeout(jetStreamTestOperationTimeout))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(inspector.Close)

	return inspector
}

// deleteJetStreamStream removes a test stream unless it is already absent.
func deleteJetStreamStream(t *testing.T, stream nats.JetStreamContext, streamName string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), jetStreamTestOperationTimeout)
	defer cancel()

	err := stream.DeleteStream(streamName, nats.Context(ctx))

	if err != nil && !errors.Is(err, nats.ErrStreamNotFound) {
		t.Errorf("delete JetStream stream: %v", err)
	}
}

// jetStreamTestCharacter converts a test-name character into a NATS-safe character.
func jetStreamTestCharacter(character rune) rune {
	if jetStreamLowercaseOrDigit(character) {
		return character
	}

	if jetStreamUppercase(character) {
		return character + ('a' - 'A')
	}

	return '-'
}

// jetStreamLowercaseOrDigit reports whether character is lowercase ASCII or a digit.
func jetStreamLowercaseOrDigit(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

// jetStreamUppercase reports whether character is uppercase ASCII.
func jetStreamUppercase(character rune) bool {
	return character >= 'A' && character <= 'Z'
}

// assertDefaultJetStreamConfig verifies the default consumer settings.
func assertDefaultJetStreamConfig(t *testing.T, config settings) {
	if config.Batch != 1 {
		t.Fatalf("batch = %d, want 1", config.Batch)
	}

	if config.MaxAckPending != 1 {
		t.Fatalf("max ack pending = %d, want 1", config.MaxAckPending)
	}

	if config.AckWait != 30*time.Second {
		t.Fatalf("ack wait = %s, want 30s", config.AckWait)
	}
}

// jetStreamEnvelopeEqual reports whether all message envelope fields match.
func jetStreamEnvelopeEqual(got, want queue.Message) bool {
	return jetStreamMessagesEqual(got, want) && got.Attempts == want.Attempts
}

// failureMessagesEqual reports whether two failure-message envelopes match.
func failureMessagesEqual(got, want failureMessage) bool {
	return failureMessageMetadataEqual(got, want) && failureMessageJobEqual(got.Message, want.Message)
}

// failureMessageMetadataEqual reports whether failure error and raw-message fields match.
func failureMessageMetadataEqual(got, want failureMessage) bool {
	return got.Error == want.Error && got.RawMessage == want.RawMessage
}

// failureMessageJobEqual reports whether safe failure-message job fields match.
func failureMessageJobEqual(got, want queue.Message) bool {
	return got.ID == want.ID && got.Queue == want.Queue && string(got.Payload) == string(want.Payload)
}

// TestDispatchBeforeStartReturnsErrNotStarted verifies that an unstarted
// driver refuses to dispatch.
func TestDispatchBeforeStartReturnsErrNotStarted(t *testing.T) {
	driver, err := New("s", map[string]Queue{
		"work": {Subject: "s.work", Durable: "work"},
	}, func(consumer *Builder) {
		consumer.URLs([]string{"nats://localhost:1"}...)
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
