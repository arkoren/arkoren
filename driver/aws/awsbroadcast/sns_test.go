package awsbroadcast

import (
	"context"
	"encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/broadcast/broadcasttest"
)

// TestSNSConstructorRejectsUnpublishableName checks routing at the exported
// conversion seam. The native client is constructed without resolving AWS state.
func TestSNSConstructorRejectsUnpublishableName(t *testing.T) {
	t.Parallel()

	_, err := NewSNSFrom(sns.New(sns.Options{}), map[string]string{
		"users..created": "arn:aws:sns:eu-west-1:123456789012:users-created",
	})

	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewSNSFrom error = %v, want ErrInvalidConfig", err)
	}
}

// TestConformanceSNSToSQS runs the broadcast conformance suite with the SNS
// driver publishing to a topic and the SQS driver consuming the queue
// subscribed to it. It needs the MiniStack gateway, so short mode skips it.
func TestConformanceSNSToSQS(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	broadcasttest.Run(t, func(t *testing.T) broadcasttest.Driver {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()

		config, client := miniStackSQS(t, ctx)
		queueURL := miniStackQueue(t, ctx, client)
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
			Endpoint: aws.String(miniStackQueueARN(t, ctx, client, queueURL)),
		})

		if err != nil {
			t.Fatal(err)
		}

		topics := map[string]string{}

		for _, name := range broadcasttest.Names {
			topics[name] = aws.ToString(topic.TopicArn)
		}

		publisher, err := NewSNSFrom(snsClient, topics)

		if err != nil {
			t.Fatal(err)
		}

		consumer, err := NewSQSFrom(sqs.NewFromConfig(config), func(consumer *SQSBuilder) {
			consumer.QueueURL(queueURL)
			consumer.WaitTimeSeconds(1)
		})

		if err != nil {
			t.Fatal(err)
		}

		return broadcasttest.Driver{
			Publisher: publisher,
			Consumer:  consumer,
		}
	})
}

// errPublish identifies the injected SNS failure so tests can check error
// identity through the driver's publish path.
var errPublish = errors.New("publish failed")

// fakeSNSClient records the most recent publish request without contacting SNS.
// Tests configure err before use; this fake is not shared across goroutines.
type fakeSNSClient struct {
	// input retains the request passed to Publish for envelope assertions.
	input *sns.PublishInput

	// err is returned unchanged by Publish; nil simulates successful delivery.
	err error
}

// Publish captures input and returns the configured error with an empty result.
// It does not run native SDK option callbacks or perform network I/O.
func (client *fakeSNSClient) Publish(_ context.Context, input *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	client.input = input

	return new(sns.PublishOutput), client.err
}

// TestPublishSendsCompleteEnvelopeToConfiguredTopic checks both topic routing
// and preservation of the source ID, name, and payload in the SNS body.
func TestPublishSendsCompleteEnvelopeToConfiguredTopic(t *testing.T) {
	t.Parallel()

	client := new(fakeSNSClient)
	driver, err := newSNSFrom(client, map[string]string{"users.created": "topic"})

	if err != nil {
		t.Fatal(err)
	}

	message := broadcast.Message{
		ID:      "source",
		Name:    "users.created",
		Payload: []byte(`{"id":1}`),
	}

	if err := driver.Publish(t.Context(), message); err != nil {
		t.Fatal(err)
	}

	if aws.ToString(client.input.TopicArn) != "topic" {
		t.Fatal("wrong topic")
	}

	var published broadcast.Message

	if err := json.Unmarshal([]byte(aws.ToString(client.input.Message)), &published); err != nil {
		t.Fatal(err)
	}

	if published.ID != message.ID || published.Name != message.Name || string(published.Payload) != string(message.Payload) {
		t.Fatalf("message = %#v", published)
	}
}

// TestPublishReturnsClientError verifies that callers can identify the native
// publish failure with errors.Is rather than parsing the error message.
func TestPublishReturnsClientError(t *testing.T) {
	t.Parallel()

	client := &fakeSNSClient{
		err: errPublish,
	}
	driver, err := newSNSFrom(client, map[string]string{"users.created": "topic"})

	if err != nil {
		t.Fatal(err)
	}

	err = driver.Publish(context.Background(), broadcast.Message{
		Name: "users.created",
	})

	if !errors.Is(err, errPublish) {
		t.Fatalf("error = %v", err)
	}
}

// TestPublishRejectsCanceledContext checks cancellation before publishing so
// an already canceled operation does not reach the native client.
func TestPublishRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	driver, err := newSNSFrom(new(fakeSNSClient), map[string]string{"users.created": "topic"})

	if err != nil {
		t.Fatal(err)
	}

	err = driver.Publish(ctx, broadcast.Message{
		Name: "users.created",
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
