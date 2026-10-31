package awsbroadcast

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"arkoren.dev/core/service/broadcast"
)

// ErrInvalidConfig reports invalid driver settings.
// Construction fails before a driver can be used; no queue is provisioned.
//
// Example:
//
//	if errors.Is(err, awsbroadcast.ErrInvalidConfig) {
//		return fmt.Errorf("configure broadcast routing and polling: %w", err)
//	}
var ErrInvalidConfig = errors.New("invalid AWS broadcast configuration")

// ErrUnknownName reports a name without a configured queue.
// SNS uses the same sentinel for a name without a configured topic mapping.
//
// Example:
//
//	if errors.Is(err, awsbroadcast.ErrUnknownName) {
//		return fmt.Errorf("add a destination for the broadcast name: %w", err)
//	}
var ErrUnknownName = errors.New("unknown broadcast name")

// emptyPollDelay prevents a tight receive loop when a backend returns an empty
// batch immediately instead of honoring SQS long polling.
const emptyPollDelay = 10 * time.Millisecond

// sqsSettings holds private queue mappings and long-poll settings until driver
// publication. Construction drafts and published drivers own separate maps.
type sqsSettings struct {
	// Queues maps broadcast names to SQS queue URLs.
	Queues map[string]string
	// QueueURL binds a consumer to one queue.
	QueueURL string
	// WaitTimeSeconds controls long polling and defaults to 20.
	WaitTimeSeconds int32
	// MaxMessages controls batch size and defaults to 1.
	MaxMessages int32
	// Logger records dropped messages and handler failures. Nil uses
	// slog.Default.
	Logger *slog.Logger
}

// sqsClient is the native SDK surface used for publishing and consuming.
// Keeping the surface narrow allows transport-free tests with scripted batches.
type sqsClient interface {
	// SendMessage publishes a message to an SQS queue.
	SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	// ReceiveMessage reserves a batch of messages from an SQS queue.
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	// DeleteMessage acknowledges a received message by its receipt handle.
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// SQSDriver implements [broadcast.PublisherDriver] and [broadcast.ConsumerDriver] for SQS.
// One bound queue supplies deliveries; publisher mappings are copied at construction.
//
// Example:
//
//	driver, err := awsbroadcast.NewSQSFrom(client, func(broker *awsbroadcast.SQSBuilder) {
//		broker.QueueURL("https://sqs.eu-west-1.amazonaws.com/123456789012/events")
//	})
//	if err != nil {
//		return err
//	}
//	consumer := broadcast.NewConsumer(driver)
//	_ = consumer
type SQSDriver struct {
	// client is the borrowed SDK client used for queue operations.
	client sqsClient

	// queues owns the published mapping from broadcast names to queue URLs.
	queues map[string]string

	// queueURL identifies the one queue polled by Consume.
	queueURL string

	// waitTime controls long polling and maxMessages bounds each receive batch.
	// Both values have been normalized and validated before publication.
	waitTime, maxMessages int32

	// logger records discarded messages, handler errors, and deletion failures.
	logger *slog.Logger
}

var _ broadcast.PublisherDriver = (*SQSDriver)(nil)
var _ broadcast.ConsumerDriver = (*SQSDriver)(nil)

// NewSQS creates the driver with credentials and region from the default AWS
// configuration chain (environment, shared config, instance role).
// Callbacks run directly once, synchronously in order on one fresh draft before
// configuration loading. Nil entries panic; nil/empty slice expansion omits
// edits. Publication copies routing maps, not loggers or native clients.
//
// Example:
//
//	driver, err := awsbroadcast.NewSQS(ctx, func(broker *awsbroadcast.SQSBuilder) {
//		broker.QueueURL("https://sqs.eu-west-1.amazonaws.com/123456789012/events")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
func NewSQS(ctx context.Context, configure ...SQSBuilderFunc) (*SQSDriver, error) {
	config := configuredSQS(configure...)

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx)

	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}

	return newSQSFrom(sqs.NewFromConfig(awsConfig), config)
}

// NewSQSFrom creates an SQS driver using client. The caller retains ownership
// of client. All SQSBuilder settings apply, with the same callback and snapshot
// rules as NewSQS. Nil callbacks panic even when client is invalid.
//
// Example:
//
//	driver, err := awsbroadcast.NewSQSFrom(sqs.NewFromConfig(cfg), func(broker *awsbroadcast.SQSBuilder) {
//		broker.QueueURL("queue-url")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
func NewSQSFrom(client *sqs.Client, configure ...SQSBuilderFunc) (*SQSDriver, error) {
	config := configuredSQS(configure...)

	if client == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}

	return newSQSFrom(client, config)
}

// newSQSFrom validates and publishes completed settings for a borrowed client.
// Routing is detached here as well as at input capture to isolate retained drafts.
func newSQSFrom(sqsClient sqsClient, config sqsSettings) (*SQSDriver, error) {
	if sqsClient == nil {
		return nil, fmt.Errorf("%w: client is required", ErrInvalidConfig)
	}

	if config.WaitTimeSeconds == 0 {
		config.WaitTimeSeconds = 20
	}

	if config.MaxMessages == 0 {
		config.MaxMessages = 1
	}

	if err := validateSQSSettings(config); err != nil {
		return nil, err
	}

	return &SQSDriver{
		client:      sqsClient,
		queues:      maps.Clone(config.Queues),
		queueURL:    config.QueueURL,
		waitTime:    config.WaitTimeSeconds,
		maxMessages: config.MaxMessages,
		logger:      cmp.Or(config.Logger, slog.Default()),
	}, nil
}

// validateSQSSettings validates completed routing and normalized polling limits.
// It neither copies maps nor contacts SQS; all errors wrap ErrInvalidConfig.
func validateSQSSettings(config sqsSettings) error {
	if len(config.Queues) == 0 && strings.TrimSpace(config.QueueURL) == "" {
		return fmt.Errorf("%w: queue mapping or consumer queue URL is required", ErrInvalidConfig)
	}

	waitInvalid := config.WaitTimeSeconds < 1 || config.WaitTimeSeconds > 20
	batchInvalid := config.MaxMessages < 1 || config.MaxMessages > 10

	if waitInvalid || batchInvalid {
		return fmt.Errorf("%w: invalid polling limits", ErrInvalidConfig)
	}

	for name, queueURL := range config.Queues {
		if broadcast.ValidateName(name) != nil || strings.TrimSpace(queueURL) == "" {
			return fmt.Errorf("%w: queue mappings are required", ErrInvalidConfig)
		}
	}

	return nil
}

// Publish sends the complete envelope to the queue configured for its name.
// FIFO destinations group by broadcast name and deduplicate by Message.ID;
// an omitted ID gets a fresh native deduplication ID for this send operation.
//
// Example:
//
//	message := broadcast.Message{ID: "event-1", Name: "users.created", Payload: []byte(`{"id":1}`)}
//	if err := driver.Publish(ctx, message); err != nil {
//		return err
//	}
func (driver *SQSDriver) Publish(ctx context.Context, message broadcast.Message) error {
	if ctx == nil {
		return errors.New("nil broadcast context")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	if err := broadcast.ValidateName(message.Name); err != nil {
		return err
	}

	queueURL, ok := driver.queues[message.Name]

	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownName, message.Name)
	}

	body, err := json.Marshal(message)

	if err != nil {
		return err
	}

	input := &sqs.SendMessageInput{
		QueueUrl:    aws.String(queueURL),
		MessageBody: aws.String(string(body)),
	}

	if strings.HasSuffix(queueURL, ".fifo") {
		input.MessageGroupId = aws.String(message.Name)
		input.MessageDeduplicationId = aws.String(cmp.Or(message.ID, rand.Text()))
	}

	_, err = driver.client.SendMessage(ctx, input)

	return err
}

// Consume long-polls the bound queue and deletes only successful deliveries.
// Permanent handler failures and malformed envelopes are also dropped; ordinary
// failures remain available for native redelivery. Handlers must honor ctx for
// prompt shutdown; deletion after handling uses a detached ten-second budget.
//
// Example:
//
//	err := driver.Consume(ctx, func(ctx context.Context, message broadcast.Message) error {
//		_ = message.Name
//		return nil
//	})
//	if err != nil && !errors.Is(err, context.Canceled) {
//		return err
//	}
func (driver *SQSDriver) Consume(ctx context.Context, handler broadcast.Handler) error {
	if ctx == nil {
		return errors.New("nil broadcast context")
	}

	if handler == nil {
		return errors.New("nil broadcast handler")
	}

	if strings.TrimSpace(driver.queueURL) == "" {
		return fmt.Errorf("%w: consumer queue URL is required", ErrInvalidConfig)
	}

	for {
		output, err := driver.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(driver.queueURL),
			WaitTimeSeconds:     driver.waitTime,
			MaxNumberOfMessages: driver.maxMessages,
		})

		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			return fmt.Errorf("receive broadcast messages: %w", err)
		}

		driver.handleMessages(ctx, output.Messages, handler)

		if len(output.Messages) == 0 {
			if err := waitForNextPoll(ctx); err != nil {
				return err
			}
		}
	}
}

// handleMessages processes a received batch in order on the caller's goroutine.
// Individual delivery failures are logged by handleMessage rather than returned.
func (driver *SQSDriver) handleMessages(ctx context.Context, messages []types.Message, handler broadcast.Handler) {
	for _, received := range messages {
		driver.handleMessage(ctx, received, handler)
	}
}

// handleMessage decodes one delivery and deletes it after success or permanent
// failure. Missing receipts are skipped; malformed bodies are logged and dropped.
func (driver *SQSDriver) handleMessage(ctx context.Context, received types.Message, handler broadcast.Handler) {
	receipt := value(received.ReceiptHandle)

	if receipt == "" {
		driver.logger.ErrorContext(ctx, "discard broadcast message without receipt handle")

		return
	}

	message, err := parseMessage(received)

	if err != nil {
		driver.logger.ErrorContext(ctx, "discard invalid broadcast message", "error", err)
		driver.delete(ctx, receipt)

		return
	}

	if err := handler(ctx, message); err != nil {
		driver.logger.ErrorContext(ctx, "broadcast handler failed", "name", message.Name, "error", err)

		// Redelivery cannot fix a permanent failure, so drop the message.
		if !errors.Is(err, broadcast.ErrPermanent) {
			return
		}
	}

	driver.delete(ctx, receipt)
}

// delete removes a handled message. It runs detached from ctx so messages
// handled during shutdown are still deleted instead of redelivered.
func (driver *SQSDriver) delete(ctx context.Context, receipt string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	input := &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(driver.queueURL),
		ReceiptHandle: aws.String(receipt),
	}

	if _, err := driver.client.DeleteMessage(ctx, input); err != nil {
		driver.logger.ErrorContext(ctx, "delete broadcast message", "error", err)
	}
}

// parseMessage accepts direct broadcast JSON or an SNS Message wrapper.
// A missing source ID falls back to the SQS ID; invalid names and bodies fail.
func parseMessage(received types.Message) (broadcast.Message, error) {
	body := value(received.Body)

	if body == "" {
		return broadcast.Message{}, errors.New("missing SQS body")
	}

	var message broadcast.Message

	if err := json.Unmarshal([]byte(body), &message); err == nil {
		if valid, validErr := validMessage(message, value(received.MessageId)); validErr == nil {
			return valid, nil
		}
	}

	var wrapper struct {
		// Message contains the original broadcast JSON in an SNS notification.
		Message string `json:"Message"`
	}

	if err := json.Unmarshal([]byte(body), &wrapper); err != nil || wrapper.Message == "" {
		return broadcast.Message{}, errors.New("invalid SQS broadcast body")
	}

	if err := json.Unmarshal([]byte(wrapper.Message), &message); err != nil {
		return broadcast.Message{}, err
	}

	return validMessage(message, value(received.MessageId))
}

// validMessage checks the envelope name before filling a missing source ID.
// It returns a modified value without mutating the caller's envelope.
func validMessage(message broadcast.Message, deliveryID string) (broadcast.Message, error) {
	if err := broadcast.ValidateName(message.Name); err != nil {
		return broadcast.Message{}, err
	}

	return fillID(message, deliveryID), nil
}

// fillID supplies the broker ID only when the source omitted its own ID.
// The envelope is passed by value; its payload remains shared and unchanged.
func fillID(message broadcast.Message, deliveryID string) broadcast.Message {
	if message.ID == "" {
		message.ID = deliveryID
	}

	return message
}

// value reads an optional native SDK string, treating nil as an empty value.
// This keeps missing SQS envelope fields on the normal validation path.
func value(input *string) string {
	if input == nil {
		return ""
	}

	return *input
}

// waitForNextPoll pauses after an empty batch without delaying cancellation.
// It returns ctx.Err when cancellation wins, otherwise nil after emptyPollDelay.
func waitForNextPoll(ctx context.Context) error {
	timer := time.NewTimer(emptyPollDelay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// MessagingSystem returns "aws_sqs", the messaging.system telemetry value that
// the queue and broadcast facades report for this driver.
//
// Example:
//
//	var driver awsbroadcast.SQSDriver
//	name := driver.MessagingSystem()
//	_ = name
func (driver *SQSDriver) MessagingSystem() string {
	return "aws_sqs"
}

// Client returns the borrowed native SQS client, or nil for an SDK-shaped test
// double. Configure native policy before sharing the driver; it is not copied.
//
// Example:
//
//	if native := driver.Client(); native != nil {
//		_ = native.Options().Region
//	}
func (driver *SQSDriver) Client() *sqs.Client {
	client, _ := driver.client.(*sqs.Client)

	return client
}
