package awsqueue

import (
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
)

// TestConformance runs the queue conformance suite against SQS emulation. It
// needs the MiniStack gateway, so short mode skips it.
func TestConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	queuetest.Run(t, func(t *testing.T) queuetest.Driver {
		_, awsConfig, _, queueURL := newMiniStackSQS(t)
		_, _, _, otherURL := newMiniStackSQS(t)
		driver, err := NewFrom(sqs.NewFromConfig(awsConfig), map[string]string{"conformance": queueURL, "conformance-other": otherURL}, func(consumer *Builder) {
			consumer.WaitTimeSeconds(1)
		})

		if err != nil {
			t.Fatal(err)
		}

		return queuetest.Driver{
			Dispatcher: driver,
			Consumer:   driver,
		}
	})
}

// TestDeliveryCapturesOwnedPayload verifies that retaining and mutating the
// source envelope cannot alter delivery-owned payload bytes.
func TestDeliveryCapturesOwnedPayload(t *testing.T) {
	t.Parallel()

	message := queue.Message{Payload: []byte("original")}
	delivery := newDelivery(nil, "queue-url", types.Message{ReceiptHandle: aws.String("receipt")}, message)

	message.Payload[0] = 'X'

	if got := string(delivery.Message().Payload); got != "original" {
		t.Fatalf("captured payload = %q, want original", got)
	}
}

// TestDeliveryCapturesOwnedMetadata verifies that retaining and mutating the
// source map cannot alter delivery-owned metadata.
func TestDeliveryCapturesOwnedMetadata(t *testing.T) {
	t.Parallel()

	message := queue.Message{Metadata: map[string]string{"key": "original"}}
	delivery := newDelivery(nil, "queue-url", types.Message{ReceiptHandle: aws.String("receipt")}, message)

	message.Metadata["key"] = "changed"

	if got := delivery.Message().Metadata["key"]; got != "original" {
		t.Fatalf("captured metadata = %q, want original", got)
	}
}

// TestDeliveryMessagePreservesNilContainers keeps absent payload and metadata
// absent through both capture and repeated snapshot publication.
func TestDeliveryMessagePreservesNilContainers(t *testing.T) {
	t.Parallel()

	delivery := newDelivery(nil, "queue-url", types.Message{ReceiptHandle: aws.String("receipt")}, queue.Message{})

	message := delivery.Message()

	if message.Payload != nil || message.Metadata != nil {
		t.Fatalf("message containers = %v, %v; want nil", message.Payload, message.Metadata)
	}
}

// TestDeliveryMessageConcurrentSnapshots lets callers mutate independent maps
// while other callers publish snapshots from the same immutable delivery.
func TestDeliveryMessageConcurrentSnapshots(t *testing.T) {
	t.Parallel()

	delivery := newDelivery(nil, "queue-url", types.Message{ReceiptHandle: aws.String("receipt")}, queue.Message{
		Metadata: map[string]string{"key": "original"},
	})
	var wait sync.WaitGroup

	for range 8 {
		wait.Go(func() {
			message := delivery.Message()

			if message.Metadata["key"] != "original" {
				t.Error("snapshot observed another caller's mutation")
			}

			message.Metadata["key"] = "changed"
		})
	}

	wait.Wait()
}

// checks provides fatal assertions for driver tests.
type checks struct{}

// check is the shared assertion helper for driver tests.
var check checks

// NoError fails t when err is non-nil.
func (checks) NoError(t testing.TB, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}

// ErrorIs fails t unless err matches target.
func (checks) ErrorIs(t testing.TB, err, target error) {
	t.Helper()

	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

// Equal fails t unless want and got are deeply equal.
func (checks) Equal(t testing.TB, want, got any) {
	t.Helper()

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// Len fails t unless value has want elements.
func (checks) Len(t testing.TB, value any, want int) {
	t.Helper()

	if got := reflect.ValueOf(value).Len(); got != want {
		t.Fatalf("length = %d, want %d", got, want)
	}
}

// Empty fails t unless value has no elements.
func (checks) Empty(t testing.TB, value any) {
	t.Helper()

	if reflect.ValueOf(value).Len() != 0 {
		t.Fatalf("value = %#v, want empty", value)
	}
}

// NotEmpty fails t when value has no elements.
func (checks) NotEmpty(t testing.TB, value any) {
	t.Helper()

	if reflect.ValueOf(value).Len() == 0 {
		t.Fatal("value is empty")
	}
}

// channelHandler is a slog.Handler that sends each record message to errors.
type channelHandler struct {
	// errors carries record messages to the test's handler-failure assertions.
	errors chan string
}

// Enabled accepts every level.
func (channelHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

// Handle sends the record message.
func (handler channelHandler) Handle(_ context.Context, record slog.Record) error {
	handler.errors <- record.Message

	return nil
}

// WithAttrs returns the handler unchanged.
func (handler channelHandler) WithAttrs([]slog.Attr) slog.Handler {
	return handler
}

// WithGroup returns the handler unchanged.
func (handler channelHandler) WithGroup(string) slog.Handler {
	return handler
}

const (
	// miniStackEndpoint is the Compose MiniStack gateway endpoint.
	miniStackEndpoint = "localhost:14568"

	// miniStackRegion is the static AWS region used by the test client.
	miniStackRegion = "us-east-1"

	// miniStackAccessKeyID is the non-secret access key used by MiniStack.
	miniStackAccessKeyID = "000000000000"

	// miniStackSecretAccessKey is the non-secret secret key used by MiniStack.
	miniStackSecretAccessKey = "test"

	// miniStackOperationTimeout bounds setup and all test service operations.
	miniStackOperationTimeout = time.Minute

	// miniStackCleanupTimeout bounds resource cleanup after a test.
	miniStackCleanupTimeout = time.Minute
)

// TestDriverDispatchConsumeAcknowledgesMiniStackMessage verifies dispatch,
// acknowledgement, and post-acknowledgement absence against real SQS emulation.
func TestDriverDispatchConsumeAcknowledgesMiniStackMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx, awsConfig, client, queueURL := newMiniStackSQS(t)
	driver, err := NewFrom(sqs.NewFromConfig(awsConfig), map[string]string{"work": queueURL}, func(consumer *Builder) {
		consumer.WaitTimeSeconds(1)
	})

	check.NoError(t, err)

	message := queue.Message{
		Name:    "email",
		Queue:   "work",
		Payload: []byte(`{"id":1}`),
	}

	check.NoError(t, driver.Dispatch(ctx, message))

	consumeCtx, stopConsume := context.WithCancel(ctx)
	t.Cleanup(stopConsume)
	consumed, received := startAcknowledgingMiniStackConsumer(consumeCtx, driver, stopConsume)

	assertMiniStackMessage(t, message, <-received)
	check.ErrorIs(t, <-consumed, context.Canceled)
	assertMiniStackQueueEmpty(t, ctx, client, queueURL)
}

// TestDriverConsumesSNSMiniStackMessage verifies standard SNS-to-SQS delivery
// preserves the queue envelope consumed by an SQS worker.
func TestDriverConsumesSNSMiniStackMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	ctx, awsConfig, _, queueURL := newMiniStackSQS(t)
	driver, err := NewFrom(sqs.NewFromConfig(awsConfig), map[string]string{"work": queueURL}, func(consumer *Builder) {
		consumer.WaitTimeSeconds(1)
	})

	check.NoError(t, err)

	topicARN := newMiniStackTopic(t, ctx, awsConfig)
	subscribeMiniStackQueue(t, ctx, awsConfig, topicARN, queueURL)

	message := queue.Message{
		ID:      "publisher-id",
		Name:    "email",
		Queue:   "work",
		Payload: []byte(`{"id":1}`),
	}
	body, err := json.Marshal(message)

	check.NoError(t, err)

	_, err = sns.NewFromConfig(awsConfig).Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(topicARN),
		Message:  aws.String(string(body)),
	})

	check.NoError(t, err)

	consumeCtx, stopConsume := context.WithCancel(ctx)
	t.Cleanup(stopConsume)
	consumed, received := startAcknowledgingMiniStackConsumer(consumeCtx, driver, stopConsume)

	assertMiniStackMessage(t, message, <-received)
	check.ErrorIs(t, <-consumed, context.Canceled)
}

// miniStackAWSConfig creates a fresh endpoint-configured AWS client configuration.
// Static test credentials avoid the ambient production credential chain.
func miniStackAWSConfig(endpoint string) aws.Config {
	return aws.Config{
		Region:       miniStackRegion,
		BaseEndpoint: aws.String(fmt.Sprintf("http://%s", endpoint)),
		Credentials: credentials.NewStaticCredentialsProvider(
			miniStackAccessKeyID,
			miniStackSecretAccessKey,
			"",
		),
	}
}

// newMiniStackQueueName creates an SQS-safe queue name unique to one test run.
// Failure to obtain its random suffix stops the calling integration test.
func newMiniStackQueueName(t *testing.T) string {
	t.Helper()

	randomBytes := make([]byte, 8)

	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatal(err)
	}

	return fmt.Sprintf("%s-%x", strings.Map(miniStackQueueCharacter, t.Name()), randomBytes)
}

// TestDriverDispatchSendsJSONMessageToMappedQueue verifies dispatch encodes a
// message and sends it to its configured SQS URL.
func TestDriverDispatchSendsJSONMessageToMappedQueue(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	driver := newDriver(t, client)
	message := queue.Message{
		Name:    "email",
		Queue:   "work",
		Payload: []byte(`{"id":1}`),
	}

	err := driver.Dispatch(t.Context(), message)

	check.NoError(t, err)
	check.Len(t, client.sendInputs(), 1)

	input := client.sendInputs()[0]

	check.Equal(t, "https://sqs.example/work", aws.ToString(input.QueueUrl))

	var sent queue.Message

	check.NoError(t, json.Unmarshal([]byte(aws.ToString(input.MessageBody)), &sent))
	check.Equal(t, message, sent)
}

// TestDriverConsumeReceivesAndAcknowledgesDelivery verifies consuming a message
// exposes delivery data and acknowledges it.
func TestDriverConsumeReceivesAndAcknowledgesDelivery(t *testing.T) {
	t.Parallel()

	client := receivedSQSClient(t)
	driver := newDriver(t, client)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	consumed, handled := startAcknowledgingSQSConsumer(ctx, driver)
	message := <-handled
	cancel()

	check.ErrorIs(t, <-consumed, context.Canceled)
	assertAcknowledgedSQSMessage(t, client, message)
}

// TestDriverDecodesSNSNotification verifies SNS-to-SQS bodies use the same envelope.
func TestDriverDecodesSNSNotification(t *testing.T) {
	t.Parallel()

	body, err := json.Marshal(queue.Message{
		Name:    "email",
		Queue:   "untrusted",
		Payload: []byte(`{"id":1}`),
	})

	check.NoError(t, err)

	wrapper, err := json.Marshal(struct {
		// Message models the JSON string carried by an SNS notification.
		Message string `json:"Message"`
	}{
		Message: string(body),
	})

	check.NoError(t, err)

	message, err := parseMessage("work", types.Message{
		Body:       aws.String(string(wrapper)),
		MessageId:  aws.String("message-1"),
		Attributes: map[string]string{"ApproximateReceiveCount": "2"},
	})

	check.NoError(t, err)
	check.Equal(t, "work", message.Queue)
	check.Equal(t, "message-1", message.ID)
	check.Equal(t, 2, message.Attempts)
}

// TestDriverDecodesDirectMessageWithBrokerMetadata verifies direct SQS delivery
// uses the configured queue and broker-controlled metadata.
func TestDriverDecodesDirectMessageWithBrokerMetadata(t *testing.T) {
	t.Parallel()

	body, err := json.Marshal(queue.Message{
		ID:       "untrusted",
		Name:     "email",
		Queue:    "untrusted",
		Payload:  []byte(`{"id":1}`),
		Attempts: 99,
	})

	check.NoError(t, err)

	message, err := parseMessage("work", types.Message{
		Body:       aws.String(string(body)),
		MessageId:  aws.String("sqs-1"),
		Attributes: map[string]string{"ApproximateReceiveCount": "3"},
	})

	check.NoError(t, err)
	check.Equal(t, "work", message.Queue)
	check.Equal(t, "sqs-1", message.ID)
	check.Equal(t, 3, message.Attempts)
}

// TestDriverRejectsMalformedSNSNotification verifies a malformed SNS wrapper is
// not accepted as a direct queue message.
func TestDriverRejectsMalformedSNSNotification(t *testing.T) {
	t.Parallel()

	_, err := parseMessage("work", types.Message{
		Body: aws.String(`{"Message":"not JSON"}`),
	})

	if err == nil {
		t.Fatal("malformed SNS notification was accepted")
	}
}

// TestDriverRejectsSNSNotificationWithMalformedEnvelope verifies a syntactically
// valid SNS notification still requires a valid queue envelope.
func TestDriverRejectsSNSNotificationWithMalformedEnvelope(t *testing.T) {
	t.Parallel()

	_, err := parseMessage("work", types.Message{
		Body: aws.String(`{"Message":"{}"}`),
	})

	if err == nil {
		t.Fatal("SNS notification without a queue envelope was accepted")
	}
}

// TestDriverRejectsMalformedDirectMessage verifies invalid direct queue bodies
// remain malformed deliveries.
func TestDriverRejectsMalformedDirectMessage(t *testing.T) {
	t.Parallel()

	_, err := parseMessage("work", types.Message{
		Body: aws.String("not JSON"),
	})

	if err == nil {
		t.Fatal("malformed direct message was accepted")
	}
}

// TestDeliveryRetryChangesVisibility verifies retry rounds its delay up to SQS
// visibility-timeout seconds.
func TestDeliveryRetryChangesVisibility(t *testing.T) {
	t.Parallel()

	client := receivedClient(t)
	driver := newDriver(t, client)

	err := driver.Consume(t.Context(), "work", func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Retry(ctx, 1500*time.Millisecond)
	})

	check.ErrorIs(t, err, context.Canceled)
	check.Len(t, client.visibilityInputs(), 1)
	check.Equal(t, int32(2), client.visibilityInputs()[0].VisibilityTimeout)
}

// TestDeliveryRetryWithZeroDelayMakesMessageImmediatelyVisible verifies retry
// accepts an immediate visibility timeout.
func TestDeliveryRetryWithZeroDelayMakesMessageImmediatelyVisible(t *testing.T) {
	t.Parallel()

	client := receivedClient(t)
	driver := newDriver(t, client)

	err := driver.Consume(t.Context(), "work", func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Retry(ctx, 0)
	})

	check.ErrorIs(t, err, context.Canceled)
	check.Len(t, client.visibilityInputs(), 1)
	check.Equal(t, int32(0), client.visibilityInputs()[0].VisibilityTimeout)
}

// TestDeliveryRejectLeavesMessageForRedrive verifies rejecting keeps the
// message so the queue's redrive policy can dead-letter it.
func TestDeliveryRejectLeavesMessageForRedrive(t *testing.T) {
	t.Parallel()

	client := receivedClient(t)
	driver := newDriver(t, client)

	err := driver.Consume(t.Context(), "work", func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Reject(ctx)
	})

	check.ErrorIs(t, err, context.Canceled)
	check.Len(t, client.deleteInputs(), 0)
}

// TestDriverConsumeLeavesDeliveryWhenHandlerReturnsError verifies handler
// failures leave a delivery unsettled for redelivery.
func TestDriverConsumeLeavesDeliveryWhenHandlerReturnsError(t *testing.T) {
	t.Parallel()

	client := receivedClient(t)
	driver := newDriver(t, client)

	err := driver.Consume(t.Context(), "work", func(context.Context, queue.Delivery) error {
		return errors.New("handler failed")
	})

	check.ErrorIs(t, err, context.Canceled)
	check.Empty(t, client.deleteInputs())
	check.Empty(t, client.visibilityInputs())
}

// TestDriverLogsHandlerFailureToConfiguredLogger verifies handler failures are
// logged through the configured logger.
func TestDriverLogsHandlerFailureToConfiguredLogger(t *testing.T) {
	t.Parallel()

	logs := make(chan string, 1)
	client := receivedClient(t)
	config := configured()
	config.Logger = slog.New(channelHandler{
		errors: logs,
	})

	driver, err := newFrom(client, map[string]string{"work": "https://sqs.example/work"}, config)

	check.NoError(t, err)

	err = driver.Consume(t.Context(), "work", func(context.Context, queue.Delivery) error {
		return errors.New("unexpected")
	})

	check.ErrorIs(t, err, context.Canceled)
	check.Equal(t, "SQS job delivery handler failed", <-logs)
}

// TestDeliveryFailPublishesFailureThenDeletesMessage verifies failure reporting
// precedes deletion when a failure queue is configured.
func TestDeliveryFailPublishesFailureThenDeletesMessage(t *testing.T) {
	t.Parallel()

	client := receivedClient(t)
	driver := newFailureDriver(t, client)

	err := driver.Consume(t.Context(), "work", func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Fail(ctx, errors.New("failed"))
	})

	check.ErrorIs(t, err, context.Canceled)
	check.Len(t, client.sendInputs(), 1)
	check.Equal(t, "https://sqs.example/failures", aws.ToString(client.sendInputs()[0].QueueUrl))
	check.Len(t, client.deleteInputs(), 1)
}

// TestDeliveryFailWithoutFailureQueueDeletesMessage verifies failing a delivery
// deletes it when no failure queue is configured.
func TestDeliveryFailWithoutFailureQueueDeletesMessage(t *testing.T) {
	t.Parallel()

	client := receivedClient(t)
	driver := newDriver(t, client)

	err := driver.Consume(t.Context(), "work", func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Fail(ctx, errors.New("failed"))
	})

	check.ErrorIs(t, err, context.Canceled)
	check.Empty(t, client.sendInputs())
	check.Len(t, client.deleteInputs(), 1)
}

// TestDeliveryFailLeavesMessageWhenFailurePublishingFails verifies an unreported
// failure is left for redelivery.
func TestDeliveryFailLeavesMessageWhenFailurePublishingFails(t *testing.T) {
	t.Parallel()

	client := receivedClient(t)
	client.sendErr = errors.New("unavailable")
	driver := newFailureDriver(t, client)

	err := driver.Consume(t.Context(), "work", func(ctx context.Context, delivery queue.Delivery) error {
		return delivery.Fail(ctx, errors.New("failed"))
	})

	check.ErrorIs(t, err, context.Canceled)
	check.Empty(t, client.deleteInputs())
}

// TestDriverConsumeFailsMalformedMessageAndDeletesIt verifies malformed messages
// are reported to the failure queue and deleted.
func TestDriverConsumeFailsMalformedMessageAndDeletesIt(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		receiveOutputs: []*sqs.ReceiveMessageOutput{
			{
				Messages: []types.Message{
					{
						Body:          aws.String("not json"),
						ReceiptHandle: aws.String("receipt-1"),
					},
				},
			},
		},
	}
	driver := newFailureDriver(t, client)

	err := driver.Consume(t.Context(), "work", func(context.Context, queue.Delivery) error {
		return errors.New("handler should not be called")
	})

	check.ErrorIs(t, err, context.Canceled)
	check.Len(t, client.sendInputs(), 1)
	check.Len(t, client.deleteInputs(), 1)
}

// TestDriverRejectsUnknownQueueAndInvalidConfiguration verifies invalid setup and
// unknown queue names return their sentinel errors.
func TestDriverRejectsUnknownQueueAndInvalidConfiguration(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}

	_, err := New(t.Context(), nil)

	check.ErrorIs(t, err, ErrInvalidConfig)

	driver := newDriver(t, client)

	check.ErrorIs(t, driver.Dispatch(t.Context(), queue.Message{
		Queue: "unknown",
	}), ErrUnknownQueue)
	check.ErrorIs(t, driver.Consume(t.Context(), "unknown", func(context.Context, queue.Delivery) error {
		return nil
	}), ErrUnknownQueue)
}

// TestDriverRejectsNilClient verifies a constructed driver always has a usable client.
func TestDriverRejectsNilClient(t *testing.T) {
	t.Parallel()

	_, err := newFrom(nil, map[string]string{"work": "https://sqs.example/work"}, settings{})

	check.ErrorIs(t, err, ErrInvalidConfig)
}

// TestDriverConsumeReturnsCanceledContext verifies Consume returns a context that
// was already canceled.
func TestDriverConsumeReturnsCanceledContext(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, &fakeClient{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := driver.Consume(ctx, "work", func(context.Context, queue.Delivery) error {
		return nil
	})

	check.ErrorIs(t, err, context.Canceled)
}

// newDriver creates a configured Driver backed by client.
func newDriver(t *testing.T, client *fakeClient) *Driver {
	t.Helper()

	config := configured()
	queues := map[string]string{
		"work": "https://sqs.example/work",
	}

	driver, err := newFrom(client, queues, config)

	check.NoError(t, err)

	return driver
}

// newFailureDriver creates a driver that reports failures through its failure
// queue.
func newFailureDriver(t *testing.T, client *fakeClient) *Driver {
	t.Helper()

	config := configured()
	queues := map[string]string{
		"work": "https://sqs.example/work",
	}
	config.FailureQueueURL = "https://sqs.example/failures"

	driver, err := newFrom(client, queues, config)

	check.NoError(t, err)

	return driver
}

// receivedClient creates a client with one valid received job message.
func receivedClient(t *testing.T) *fakeClient {
	t.Helper()

	body, err := json.Marshal(queue.Message{
		Name:    "email",
		Payload: []byte(`{"id":1}`),
	})

	check.NoError(t, err)

	return &fakeClient{
		receiveOutputs: []*sqs.ReceiveMessageOutput{
			{
				Messages: []types.Message{
					{
						Body:          aws.String(string(body)),
						ReceiptHandle: aws.String("receipt-1"),
					},
				},
			},
		},
	}
}

// fakeClient records SQS requests and returns configured fake responses.
type fakeClient struct {
	// mu protects recorded requests and configured receive responses.
	mu sync.Mutex
	// receiveOutputs supplies queued responses to ReceiveMessage.
	receiveOutputs []*sqs.ReceiveMessageOutput
	// receive records ReceiveMessage inputs.
	receive []*sqs.ReceiveMessageInput
	// send records SendMessage inputs.
	send []*sqs.SendMessageInput
	// deleted records DeleteMessage inputs.
	deleted []*sqs.DeleteMessageInput
	// visibility records ChangeMessageVisibility inputs.
	visibility []*sqs.ChangeMessageVisibilityInput
	// sendErr is returned by SendMessage when non-nil.
	sendErr error
}

// SendMessage records input and returns the configured send result.
func (client *fakeClient) SendMessage(_ context.Context, input *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.send = append(client.send, input)

	return &sqs.SendMessageOutput{}, client.sendErr
}

// ReceiveMessage records input and returns its next configured response.
func (client *fakeClient) ReceiveMessage(_ context.Context, input *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	client.mu.Lock()
	client.receive = append(client.receive, input)

	if len(client.receiveOutputs) > 0 {
		output := client.receiveOutputs[0]
		client.receiveOutputs = client.receiveOutputs[1:]
		client.mu.Unlock()

		return output, nil
	}

	client.mu.Unlock()

	return nil, context.Canceled
}

// DeleteMessage records input and reports successful deletion.
func (client *fakeClient) DeleteMessage(_ context.Context, input *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.deleted = append(client.deleted, input)

	return &sqs.DeleteMessageOutput{}, nil
}

// ChangeMessageVisibility records input and reports a successful update.
func (client *fakeClient) ChangeMessageVisibility(_ context.Context, input *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.visibility = append(client.visibility, input)

	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

// receiveInputs returns a snapshot of recorded ReceiveMessage inputs.
func (client *fakeClient) receiveInputs() []*sqs.ReceiveMessageInput {
	client.mu.Lock()
	defer client.mu.Unlock()

	return append([]*sqs.ReceiveMessageInput(nil), client.receive...)
}

// sendInputs returns a snapshot of recorded SendMessage inputs.
func (client *fakeClient) sendInputs() []*sqs.SendMessageInput {
	client.mu.Lock()
	defer client.mu.Unlock()

	return append([]*sqs.SendMessageInput(nil), client.send...)
}

// deleteInputs returns a snapshot of recorded DeleteMessage inputs.
func (client *fakeClient) deleteInputs() []*sqs.DeleteMessageInput {
	client.mu.Lock()
	defer client.mu.Unlock()

	return append([]*sqs.DeleteMessageInput(nil), client.deleted...)
}

// visibilityInputs returns a snapshot of recorded visibility-update inputs.
func (client *fakeClient) visibilityInputs() []*sqs.ChangeMessageVisibilityInput {
	client.mu.Lock()
	defer client.mu.Unlock()

	return append([]*sqs.ChangeMessageVisibilityInput(nil), client.visibility...)
}

// newMiniStackSQS starts MiniStack and creates a queue for an integration test.
func newMiniStackSQS(t *testing.T) (context.Context, aws.Config, *sqs.Client, string) {
	ctx, cancel := context.WithTimeout(t.Context(), miniStackOperationTimeout)
	t.Cleanup(cancel)
	awsConfig := miniStackAWSConfig(miniStackEndpoint)
	client := sqs.NewFromConfig(awsConfig)
	queueURL := createMiniStackQueue(t, ctx, client)
	t.Cleanup(func() {
		deleteMiniStackQueue(t, client, queueURL)
	})

	return ctx, awsConfig, client, queueURL
}

// createMiniStackQueue creates a test queue and returns its URL.
func createMiniStackQueue(t *testing.T, ctx context.Context, client *sqs.Client) string {
	consumer, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(newMiniStackQueueName(t)),
	})

	check.NoError(t, err)

	return aws.ToString(consumer.QueueUrl)
}

// newMiniStackTopic creates an SNS topic for one integration test.
func newMiniStackTopic(t *testing.T, ctx context.Context, config aws.Config) string {
	t.Helper()

	client := sns.NewFromConfig(config)
	topic, err := client.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: aws.String(newMiniStackQueueName(t)),
	})

	check.NoError(t, err)

	t.Cleanup(func() {
		deleteMiniStackTopic(t, client, aws.ToString(topic.TopicArn))
	})

	return aws.ToString(topic.TopicArn)
}

// deleteMiniStackTopic removes an SNS topic during test cleanup.
func deleteMiniStackTopic(t *testing.T, client *sns.Client, topicARN string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), miniStackCleanupTimeout)
	defer cancel()
	_, err := client.DeleteTopic(ctx, &sns.DeleteTopicInput{
		TopicArn: aws.String(topicARN),
	})

	if err != nil {
		t.Errorf("delete MiniStack topic: %v", err)
	}
}

// subscribeMiniStackQueue subscribes an SQS queue to an SNS topic.
func subscribeMiniStackQueue(t *testing.T, ctx context.Context, config aws.Config, topicARN, queueURL string) {
	t.Helper()

	consumer := sqs.NewFromConfig(config)
	attributes, err := consumer.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})

	check.NoError(t, err)

	_, err = sns.NewFromConfig(config).Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(topicARN),
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(attributes.Attributes["QueueArn"]),
	})

	check.NoError(t, err)
}

// deleteMiniStackQueue removes a MiniStack test queue during cleanup.
func deleteMiniStackQueue(t *testing.T, client *sqs.Client, queueURL string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), miniStackCleanupTimeout)
	defer cancel()
	_, err := client.DeleteQueue(ctx, &sqs.DeleteQueueInput{
		QueueUrl: aws.String(queueURL),
	})

	if err != nil {
		t.Errorf("delete MiniStack queue: %v", err)
	}
}

// startAcknowledgingMiniStackConsumer starts a consumer that records and acknowledges a message.
func startAcknowledgingMiniStackConsumer(
	ctx context.Context,
	driver *Driver,
	stop context.CancelFunc,
) (<-chan error, <-chan queue.Message) {
	consumed := make(chan error, 1)
	received := make(chan queue.Message, 1)

	go func() {
		consumed <- driver.Consume(ctx, "work", func(handlerCtx context.Context, delivery queue.Delivery) error {
			received <- delivery.Message()
			err := delivery.Acknowledge(handlerCtx)
			stop()

			return err
		})
	}()

	return consumed, received
}

// assertMiniStackMessage verifies that a dispatched message was received unchanged.
func assertMiniStackMessage(t *testing.T, want, got queue.Message) {
	check.Equal(t, want.Name, got.Name)
	check.Equal(t, want.Queue, got.Queue)
	check.Equal(t, want.Payload, got.Payload)
}

// assertMiniStackQueueEmpty verifies the queue has no remaining messages.
func assertMiniStackQueueEmpty(t *testing.T, ctx context.Context, client *sqs.Client, queueURL string) {
	remaining, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(queueURL),
		WaitTimeSeconds:     0,
		MaxNumberOfMessages: 1,
	})

	check.NoError(t, err)
	check.Empty(t, remaining.Messages)
}

// receivedSQSClient creates a client with a received message that has a second attempt.
func receivedSQSClient(t *testing.T) *fakeClient {
	body, err := json.Marshal(queue.Message{
		Name:    "email",
		Queue:   "untrusted",
		Payload: []byte(`{"id":1}`),
	})

	check.NoError(t, err)

	return &fakeClient{
		receiveOutputs: []*sqs.ReceiveMessageOutput{
			{
				Messages: []types.Message{
					{
						Body:          aws.String(string(body)),
						MessageId:     aws.String("message-1"),
						ReceiptHandle: aws.String("receipt-1"),
						Attributes:    map[string]string{"ApproximateReceiveCount": "2"},
					},
				},
			},
		},
	}
}

// startAcknowledgingSQSConsumer acknowledges one message before reporting it.
// The caller may cancel consumption once handled receives; reporting earlier
// would race that cancellation against acknowledgement's context check.
func startAcknowledgingSQSConsumer(ctx context.Context, driver *Driver) (<-chan error, <-chan queue.Message) {
	consumed := make(chan error, 1)
	handled := make(chan queue.Message, 1)

	go func() {
		consumed <- driver.Consume(ctx, "work", func(ctx context.Context, delivery queue.Delivery) error {
			err := delivery.Acknowledge(ctx)
			handled <- delivery.Message()

			return err
		})
	}()

	return consumed, handled
}

// assertAcknowledgedSQSMessage verifies the received message and SQS requests.
func assertAcknowledgedSQSMessage(t *testing.T, client *fakeClient, message queue.Message) {
	check.NotEmpty(t, client.receiveInputs())
	check.Equal(t, "work", message.Queue)
	check.Equal(t, "message-1", message.ID)
	check.Equal(t, 2, message.Attempts)
	check.Len(t, client.deleteInputs(), 1)
	check.Equal(t, "receipt-1", aws.ToString(client.deleteInputs()[0].ReceiptHandle))

	receive := client.receiveInputs()[0]

	check.Equal(t, "https://sqs.example/work", aws.ToString(receive.QueueUrl))
	check.Equal(t, int32(20), receive.WaitTimeSeconds)
	check.Equal(t, int32(1), receive.MaxNumberOfMessages)
	check.Equal(t, []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount}, receive.MessageSystemAttributeNames)
}

// miniStackQueueCharacter converts a test-name character into an SQS-safe character.
func miniStackQueueCharacter(character rune) rune {
	if miniStackLowercaseOrDigit(character) {
		return character
	}

	if miniStackUppercase(character) {
		return character + ('a' - 'A')
	}

	return '-'
}

// miniStackLowercaseOrDigit reports whether character is lowercase ASCII or a digit.
func miniStackLowercaseOrDigit(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

// miniStackUppercase reports whether character is uppercase ASCII.
func miniStackUppercase(character rune) bool {
	return character >= 'A' && character <= 'Z'
}

// TestHandleMessagesExtendsVisibilityOfQueuedBatchMessages verifies that a
// message waiting behind a slow handler keeps its visibility timeout extended,
// so another worker cannot receive and run it concurrently.
func TestHandleMessagesExtendsVisibilityOfQueuedBatchMessages(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		client := &fakeClient{}
		driver := newDriver(t, client)
		body, err := json.Marshal(queue.Message{
			Name: "email",
		})

		check.NoError(t, err)

		messages := []types.Message{
			{
				Body:          aws.String(string(body)),
				ReceiptHandle: aws.String("receipt-1"),
			},
			{
				Body:          aws.String(string(body)),
				ReceiptHandle: aws.String("receipt-2"),
			},
		}

		handled := 0
		driver.handleMessages(t.Context(), "work", "https://sqs.example/work", messages,
			func(ctx context.Context, delivery queue.Delivery) error {
				handled++

				if handled == 1 {
					time.Sleep(3 * driver.visibility)
				}

				return delivery.Acknowledge(ctx)
			})

		extended := map[string]bool{}

		for _, input := range client.visibilityInputs() {
			extended[aws.ToString(input.ReceiptHandle)] = true
		}

		if !extended["receipt-2"] {
			t.Fatalf("extended receipts = %v, want receipt-2 extended while queued", extended)
		}
	})
}
