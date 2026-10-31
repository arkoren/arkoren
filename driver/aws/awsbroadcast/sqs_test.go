package awsbroadcast

import (
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/broadcast/broadcasttest"
)

const (
	// miniStackEndpoint selects the local SQS emulator for broadcast tests.
	miniStackEndpoint = "localhost:14567"
)

// TestDriverDeliversDirectMessageWithMiniStack exercises publish and consume
// through the SQS emulator. Short mode skips the infrastructure dependency.
func TestDriverDeliversDirectMessageWithMiniStack(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)

	config, client := miniStackSQS(t, ctx)
	queueURL := miniStackQueue(t, ctx, client)

	driver, err := NewSQSFrom(sqs.NewFromConfig(config), func(consumer *SQSBuilder) {
		consumer.Queues(map[string]string{"users.created": queueURL})
		consumer.QueueURL(queueURL)
		consumer.WaitTimeSeconds(1)
	})

	if err != nil {
		t.Fatal(err)
	}

	message := broadcast.Message{
		Name:    "users.created",
		Payload: []byte(`{"id":1}`),
	}

	if err := driver.Publish(ctx, message); err != nil {
		t.Fatal(err)
	}

	consume(ctx, t, driver, message)
}

// TestDriverDeliversSNSWrapperWithMiniStack exercises SNS-to-SQS delivery
// without raw message delivery. Short mode skips the emulator dependency.
func TestDriverDeliversSNSWrapperWithMiniStack(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)

	config, client := miniStackSQS(t, ctx)
	queueURL := miniStackQueue(t, ctx, client)
	queueARN := miniStackQueueARN(t, ctx, client, queueURL)
	snsClient := sns.NewFromConfig(config)
	topic, err := snsClient.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: aws.String(miniStackName(t)),
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = snsClient.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})

	if err != nil {
		t.Fatal(err)
	}

	receiver, err := NewSQSFrom(sqs.NewFromConfig(config), func(consumer *SQSBuilder) {
		consumer.QueueURL(queueURL)
		consumer.WaitTimeSeconds(1)
	})

	if err != nil {
		t.Fatal(err)
	}

	message := broadcast.Message{
		Name:    "users.created",
		Payload: []byte(`{"id":1}`),
	}
	body, err := json.Marshal(message)

	if err != nil {
		t.Fatal(err)
	}

	if _, err := snsClient.Publish(ctx, &sns.PublishInput{
		TopicArn: topic.TopicArn,
		Message:  aws.String(string(body)),
	}); err != nil {
		t.Fatal(err)
	}

	consume(ctx, t, receiver, message)
}

// consume checks one asynchronously received envelope and waits for Consume
// to exit after cancellation. The parent context bounds the delivery wait.
func consume(ctx context.Context, t *testing.T, driver *SQSDriver, want broadcast.Message) {
	t.Helper()

	consumeCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	result := make(chan broadcast.Message, 1)
	errs := make(chan error, 1)

	go func() {
		errs <- driver.Consume(consumeCtx, func(_ context.Context, message broadcast.Message) error {
			result <- message
			cancel()

			return nil
		})
	}()

	select {
	case got := <-result:
		if got.Name != want.Name || string(got.Payload) != string(want.Payload) {
			t.Fatalf("message = %#v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	if err := <-errs; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// miniStackSQS returns native configuration and a client using local test
// credentials. Construction does not contact the emulator.
func miniStackSQS(t *testing.T, ctx context.Context) (aws.Config, *sqs.Client) {
	t.Helper()

	config := aws.Config{
		Region:       "us-east-1",
		BaseEndpoint: aws.String("http://" + miniStackEndpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("000000000000", "test", ""),
	}

	return config, sqs.NewFromConfig(config)
}

// miniStackQueue creates an isolated emulator queue and returns its URL.
// Native creation failures stop the calling integration test.
func miniStackQueue(t *testing.T, ctx context.Context, client *sqs.Client) string {
	t.Helper()

	queue, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(miniStackName(t)),
	})

	if err != nil {
		t.Fatal(err)
	}

	return aws.ToString(queue.QueueUrl)
}

// miniStackQueueARN returns the queue ARN used by SNS subscriptions.
func miniStackQueueARN(t *testing.T, ctx context.Context, client *sqs.Client, queueURL string) string {
	t.Helper()

	attributes, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})

	if err != nil {
		t.Fatal(err)
	}

	return attributes.Attributes[string(types.QueueAttributeNameQueueArn)]
}

// miniStackName derives a lowercase resource name from the test name and a
// random suffix. Failure to obtain randomness stops the calling test.
func miniStackName(t *testing.T) string {
	t.Helper()

	bytes := make([]byte, 4)

	if _, err := rand.Read(bytes); err != nil {
		t.Fatal(err)
	}

	return fmt.Sprintf("%s-%x", strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-"), bytes)
}

// TestConformanceSQS runs the broadcast conformance suite with the SQS driver
// publishing to and consuming from one queue. It needs the MiniStack gateway,
// so short mode skips it.
func TestConformanceSQS(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	broadcasttest.Run(t, func(t *testing.T) broadcasttest.Driver {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()

		config, client := miniStackSQS(t, ctx)
		queueURL := miniStackQueue(t, ctx, client)

		queues := map[string]string{}

		for _, name := range broadcasttest.Names {
			queues[name] = queueURL
		}

		driver, err := NewSQSFrom(sqs.NewFromConfig(config), func(consumer *SQSBuilder) {
			consumer.Queues(queues)
			consumer.QueueURL(queueURL)
			consumer.WaitTimeSeconds(1)
		})

		if err != nil {
			t.Fatal(err)
		}

		return broadcasttest.Driver{
			Publisher: driver,
			Consumer:  driver,
		}
	})
}

// broadcastHTTPFunc exercises native request serialization while forbidding
// external network access. Each test supplies its own deterministic response.
type broadcastHTTPFunc func(*http.Request) (*http.Response, error)

// Do executes the transport's protocol assertion on the SDK's real request.
// Blocking implementations must respect the request context.
func (broadcastHTTPFunc broadcastHTTPFunc) Do(request *http.Request) (*http.Response, error) {
	return broadcastHTTPFunc(request)
}

// TestSQSPublishSuppliesFIFOFields checks FIFO routing and the existing envelope
// through the real SDK. No SQS service, environment chain, or credentials are used.
func TestSQSPublishSuppliesFIFOFields(t *testing.T) {
	t.Parallel()

	var sent sqs.SendMessageInput
	native := sqs.NewFromConfig(aws.Config{
		Region:           "eu-west-1",
		Credentials:      credentials.NewStaticCredentialsProvider("test", "test", ""),
		RetryMaxAttempts: 1,
		HTTPClient: broadcastHTTPFunc(func(request *http.Request) (*http.Response, error) {
			if err := json.UnmarshalRead(request.Body, &sent); err != nil {
				return nil, err
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body:       io.NopCloser(strings.NewReader("{}")),
			}, nil
		}),
	})

	driver, err := NewSQSFrom(native, func(broker *SQSBuilder) {
		broker.Queues(map[string]string{"users.created": "https://sqs.example/events.fifo"})
	})

	if err != nil {
		t.Fatal(err)
	}

	message := broadcast.Message{ID: "event-1", Name: "users.created", Payload: []byte(`{"id":1}`)}

	if err := driver.Publish(context.Background(), message); err != nil {
		t.Fatal(err)
	}

	if aws.ToString(sent.MessageGroupId) != message.Name || aws.ToString(sent.MessageDeduplicationId) != message.ID {
		t.Fatalf("FIFO group/identity = %q/%q", aws.ToString(sent.MessageGroupId), aws.ToString(sent.MessageDeduplicationId))
	}

	var received broadcast.Message

	if err := json.Unmarshal([]byte(aws.ToString(sent.MessageBody)), &received); err != nil {
		t.Fatal(err)
	}

	if string(received.Payload) != `{"id":1}` || received.ID != "event-1" {
		t.Fatal("FIFO publishing changed the broadcast envelope")
	}
}

// errReceive identifies injected handler and receive failures without relying
// on error text in assertions.
var errReceive = errors.New("receive failed")

// fakeSQSClient scripts receive batches and captures sends and deletions.
// Its mutex protects request history and batch consumption during Consume.
type fakeSQSClient struct {
	// mu serializes access to request history and pending batches.
	mu sync.Mutex

	// sent retains publish requests in the order they arrived.
	sent []*sqs.SendMessageInput

	// deleted retains settlement requests for acknowledgment assertions.
	deleted []*sqs.DeleteMessageInput

	// receives holds pending batches; exhausting them returns context.Canceled.
	receives []*sqs.ReceiveMessageOutput

	// receive overrides pending batches with a native receive error when non-nil.
	receive error
}

// SendMessage records input under the mutex and returns an empty success result.
// Native SDK option callbacks are unused by this transport-free fake.
func (client *fakeSQSClient) SendMessage(_ context.Context, input *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	client.mu.Lock()
	client.sent = append(client.sent, input)
	client.mu.Unlock()

	return new(sqs.SendMessageOutput), nil
}

// ReceiveMessage returns the injected failure or consumes the next scripted
// batch. An exhausted script returns context.Canceled to end the receive loop.
func (client *fakeSQSClient) ReceiveMessage(_ context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.receive != nil {
		return nil, client.receive
	}

	if len(client.receives) == 0 {
		return nil, context.Canceled
	}

	output := client.receives[0]
	client.receives = client.receives[1:]

	return output, nil
}

// DeleteMessage records the receipt settlement under the mutex.
// It always succeeds so tests can isolate handler and decoding behavior.
func (client *fakeSQSClient) DeleteMessage(_ context.Context, input *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	client.mu.Lock()
	client.deleted = append(client.deleted, input)
	client.mu.Unlock()

	return new(sqs.DeleteMessageOutput), nil
}

// TestPublishSendsCompleteEnvelope checks queue routing and preservation of
// the source ID, name, and payload in the serialized SQS body.
func TestPublishSendsCompleteEnvelope(t *testing.T) {
	t.Parallel()

	client := new(fakeSQSClient)
	driver := testDriver(t, client)
	message := broadcast.Message{
		ID:      "source",
		Name:    "users.created",
		Payload: []byte(`{"id":1}`),
	}

	if err := driver.Publish(t.Context(), message); err != nil {
		t.Fatal(err)
	}

	if len(client.sent) != 1 {
		t.Fatalf("sent = %d", len(client.sent))
	}

	if aws.ToString(client.sent[0].QueueUrl) != "queue" {
		t.Fatal("wrong queue")
	}

	var sent broadcast.Message

	if err := json.Unmarshal([]byte(aws.ToString(client.sent[0].MessageBody)), &sent); err != nil {
		t.Fatal(err)
	}

	if sent.ID != message.ID || sent.Name != message.Name || string(sent.Payload) != string(message.Payload) {
		t.Fatalf("message = %#v", sent)
	}
}

// TestNewAllowsConsumerOnlyConfiguration verifies that a bound consumer does
// not need a publisher routing map during construction.
func TestNewAllowsConsumerOnlyConfiguration(t *testing.T) {
	t.Parallel()

	driver, err := newSQSFrom(new(fakeSQSClient), sqsSettings{
		QueueURL: "queue",
	})

	if err != nil || driver == nil {
		t.Fatalf("driver = %v, error = %v", driver, err)
	}
}

// TestPublishRejectsUnknownName checks that an unmapped envelope name returns
// ErrUnknownName instead of attempting a native send.
func TestPublishRejectsUnknownName(t *testing.T) {
	t.Parallel()

	err := testDriver(t, new(fakeSQSClient)).Publish(t.Context(), broadcast.Message{
		Name: "unknown",
	})

	if !errors.Is(err, ErrUnknownName) {
		t.Fatalf("error = %v", err)
	}
}

// TestConsumeParsesDirectMessageAndDeletesSuccess verifies broker ID fallback
// and deletion after a successful handler cancels the receive loop.
func TestConsumeParsesDirectMessageAndDeletesSuccess(t *testing.T) {
	t.Parallel()

	message := testMessage(t)
	client := &fakeSQSClient{
		receives: []*sqs.ReceiveMessageOutput{
			{
				Messages: []types.Message{delivery(message, "receipt", "broker")},
			},
		},
	}
	driver := testDriver(t, client)
	ctx, cancel := context.WithCancel(t.Context())
	var received broadcast.Message

	err := driver.Consume(ctx, func(_ context.Context, value broadcast.Message) error {
		received = value
		cancel()

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}

	if received.ID != "broker" || received.Name != "users.created" {
		t.Fatalf("received = %#v", received)
	}

	if len(client.deleted) != 1 {
		t.Fatalf("deleted = %d", len(client.deleted))
	}
}

// TestConsumeParsesSNSWrapper checks that SNS notification wrappers decode
// into broadcast envelopes and successful deliveries are deleted.
func TestConsumeParsesSNSWrapper(t *testing.T) {
	t.Parallel()

	message := testMessage(t)
	body, err := json.Marshal(struct {
		// Message carries the JSON envelope as an SNS notification string.
		Message string `json:"Message"`
	}{
		Message: string(message),
	})

	if err != nil {
		t.Fatal(err)
	}

	client := &fakeSQSClient{
		receives: []*sqs.ReceiveMessageOutput{
			{
				Messages: []types.Message{
					{
						Body:          aws.String(string(body)),
						ReceiptHandle: aws.String("receipt"),
					},
				},
			},
		},
	}
	driver := testDriver(t, client)
	ctx, cancel := context.WithCancel(t.Context())

	err = driver.Consume(ctx, func(_ context.Context, value broadcast.Message) error {
		cancel()

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}

	if len(client.deleted) != 1 {
		t.Fatal("SNS delivery was not deleted")
	}
}

// TestConsumeLeavesHandlerFailureUndeleted ensures retryable handler failures
// keep their receipt available for backend redelivery.
func TestConsumeLeavesHandlerFailureUndeleted(t *testing.T) {
	t.Parallel()

	client := &fakeSQSClient{
		receives: []*sqs.ReceiveMessageOutput{
			{
				Messages: []types.Message{delivery(testMessage(t), "receipt", "")},
			},
		},
	}
	driver := testDriver(t, client)

	err := driver.Consume(t.Context(), func(context.Context, broadcast.Message) error {
		return errReceive
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}

	if len(client.deleted) != 0 {
		t.Fatal("failed delivery was deleted")
	}
}

// TestConsumeDeletesMalformedAndSkipsMissingReceipt distinguishes a malformed
// body that can be dropped from a delivery that cannot be settled at all.
func TestConsumeDeletesMalformedAndSkipsMissingReceipt(t *testing.T) {
	t.Parallel()

	client := &fakeSQSClient{
		receives: []*sqs.ReceiveMessageOutput{
			{
				Messages: []types.Message{
					{
						Body:          aws.String("invalid"),
						ReceiptHandle: aws.String("receipt"),
					},
					{
						Body: aws.String(`{"name":"users.created"}`),
					},
				},
			},
		},
	}
	driver := testDriver(t, client)

	err := driver.Consume(t.Context(), func(context.Context, broadcast.Message) error {
		t.Fatal("handler called")

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}

	// A malformed body never parses, so redelivering it only wastes
	// receives; the message without a receipt handle cannot be deleted.
	if len(client.deleted) != 1 {
		t.Fatalf("deleted = %d, want the malformed message only", len(client.deleted))
	}
}

// TestConsumeReturnsReceiveFailure verifies native receive errors remain
// identifiable through the error returned by Consume.
func TestConsumeReturnsReceiveFailure(t *testing.T) {
	t.Parallel()

	client := &fakeSQSClient{
		receive: errReceive,
	}

	err := testDriver(t, client).Consume(t.Context(), func(context.Context, broadcast.Message) error {
		return nil
	})

	if !errors.Is(err, errReceive) {
		t.Fatalf("error = %v", err)
	}
}

// testDriver publishes a driver routing users.created to the bound test queue.
// Configuration errors fail the calling test rather than returning a nil driver.
func testDriver(t *testing.T, client *fakeSQSClient) *SQSDriver {
	t.Helper()

	driver, err := newSQSFrom(client, sqsSettings{
		Queues:   map[string]string{"users.created": "queue"},
		QueueURL: "queue",
	})

	if err != nil {
		t.Fatal(err)
	}

	return driver
}

// testMessage encodes the common users.created envelope without a source ID.
// Encoding failures stop the test; callers use the missing ID to test fallback.
func testMessage(t *testing.T) []byte {
	t.Helper()

	message, err := json.Marshal(broadcast.Message{
		Name:    "users.created",
		Payload: []byte(`{"id":1}`),
	})

	if err != nil {
		t.Fatal(err)
	}

	return message
}

// delivery builds a native SQS envelope with explicit broker identity.
// It copies body into a string and leaves all unrelated SDK fields unset.
func delivery(body []byte, receipt, id string) types.Message {
	return types.Message{
		Body:          aws.String(string(body)),
		ReceiptHandle: aws.String(receipt),
		MessageId:     aws.String(id),
	}
}
