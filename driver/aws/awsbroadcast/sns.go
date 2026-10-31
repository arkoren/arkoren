package awsbroadcast

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"fmt"
	"maps"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"arkoren.dev/core/service/broadcast"
)

// snsClient publishes SNS messages for SNSDriver without owning its transport.
type snsClient interface {
	// Publish sends input to SNS.
	Publish(context.Context, *sns.PublishInput, ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// The compile-time assertion ensures SNSDriver satisfies core.Publisher.
var _ broadcast.PublisherDriver = (*SNSDriver)(nil)

// SNSDriver implements [broadcast.PublisherDriver] using Amazon SNS.
//
// SNSDriver sends the complete JSON broadcast envelope without an SNS Subject.
// Payload bytes use the envelope's base64 representation; metadata is preserved.
// Consumers may receive that envelope directly or inside an SNS notification.
//
// Example:
//
//	driver, err := awsbroadcast.NewSNSFrom(sns.NewFromConfig(cfg), map[string]string{
//		"users.created": "arn:aws:sns:eu-west-1:123456789012:users-created",
//	})
//	if err != nil {
//		return err
//	}
//	publisher := broadcast.NewPublisher(driver)
//	_ = publisher
type SNSDriver struct {
	// client publishes messages to SNS.
	client snsClient
	// topics maps event names to SNS topic ARNs.
	topics map[string]string
}

// NewSNS creates the driver with credentials and region from the default AWS
// configuration chain (environment, shared config, instance role).
//
// topics maps broadcast names to SNS topic ARNs. It is required: an empty map,
// or any entry with an invalid broadcast name or blank ARN, returns an error wrapping
// [ErrInvalidConfig]. The map is copied, so later changes by the caller do not
// affect the driver. SNS has no further settings, so there are no options and
// no config type.
//
// Example:
//
//	publisher, err := awsbroadcast.NewSNS(ctx, map[string]string{
//		"users.created": "arn:aws:sns:eu-west-1:123456789012:users-created",
//	})
func NewSNS(ctx context.Context, topics map[string]string) (*SNSDriver, error) {
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx)

	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}

	return newSNSFrom(sns.NewFromConfig(awsConfig), topics)
}

// NewSNSFrom creates an SNS broadcast publisher using client. The caller retains
// ownership of client.
//
// topics is required and validated and copied exactly as in [NewSNS]. A nil
// client returns an error wrapping [ErrInvalidConfig].
//
// Example:
//
//	publisher, err := awsbroadcast.NewSNSFrom(sns.NewFromConfig(cfg), map[string]string{
//		"users.created": "arn:aws:sns:eu-west-1:123456789012:users-created",
//	})
func NewSNSFrom(client *sns.Client, topics map[string]string) (*SNSDriver, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}

	return newSNSFrom(client, topics)
}

// newSNSFrom validates topics and creates an SNSDriver with an isolated topic map.
func newSNSFrom(client snsClient, topics map[string]string) (*SNSDriver, error) {
	if len(topics) == 0 {
		return nil, fmt.Errorf("%w: no topics", ErrInvalidConfig)
	}

	if !validTopics(topics) {
		return nil, fmt.Errorf("%w: event name and topic ARN are required", ErrInvalidConfig)
	}

	return &SNSDriver{
		client: client,
		topics: maps.Clone(topics),
	}, nil
}

// validTopics reports whether every event-to-topic mapping is complete.
func validTopics(topics map[string]string) bool {
	for event, topicARN := range topics {
		if invalidTopic(event, topicARN) {
			return false
		}
	}

	return true
}

// invalidTopic rejects names Publish cannot route and blank destination ARNs.
// AWS remains responsible for validating nonempty ARN syntax and permissions.
func invalidTopic(event, topicARN string) bool {
	return broadcast.ValidateName(event) != nil || strings.TrimSpace(topicARN) == ""
}

// Publish sends the complete envelope to the SNS topic mapped by message.Name.
// Success means SNS acceptance, not subscriber processing. FIFO topics group by
// name and use the supplied ID, or a fresh one, for native deduplication.
//
// Example:
//
//	if err := driver.Publish(ctx, broadcast.Message{Name: "users.created", Payload: []byte(`{"id":1}`)}); err != nil {
//		return err
//	}
func (driver *SNSDriver) Publish(ctx context.Context, message broadcast.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := broadcast.ValidateName(message.Name); err != nil {
		return err
	}

	topicARN, ok := driver.topics[message.Name]

	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownName, message.Name)
	}

	body, err := json.Marshal(message)

	if err != nil {
		return fmt.Errorf("marshal broadcast message: %w", err)
	}

	input := &sns.PublishInput{
		Message:  aws.String(string(body)),
		TopicArn: aws.String(topicARN),
		// The name attribute lets SNS subscription filter policies route
		// by broadcast name without parsing the body.
		MessageAttributes: map[string]types.MessageAttributeValue{
			"name": {
				DataType:    aws.String("String"),
				StringValue: aws.String(message.Name),
			},
		},
	}

	// FIFO topics require a group and a deduplication ID. Grouping by name
	// keeps each name ordered; the message ID deduplicates retries.
	if strings.HasSuffix(topicARN, ".fifo") {
		input.MessageGroupId = aws.String(message.Name)
		input.MessageDeduplicationId = aws.String(cmp.Or(message.ID, rand.Text()))
	}

	_, err = driver.client.Publish(ctx, input)

	if err != nil {
		return fmt.Errorf("publish SNS event: %w", err)
	}

	return nil
}

// MessagingSystem returns "aws_sns", the messaging.system telemetry value that
// the queue and broadcast facades report for this driver.
//
// Example:
//
//	var driver awsbroadcast.SNSDriver
//	name := driver.MessagingSystem()
//	_ = name
func (driver *SNSDriver) MessagingSystem() string {
	return "aws_sns"
}

// Client returns the borrowed native SNS client, or nil for an SDK-shaped test
// double. Native configuration and transport cleanup remain caller-owned.
//
// Example:
//
//	if native := driver.Client(); native != nil {
//		_ = native.Options().Region
//	}
func (driver *SNSDriver) Client() *sns.Client {
	client, _ := driver.client.(*sns.Client)

	return client
}
