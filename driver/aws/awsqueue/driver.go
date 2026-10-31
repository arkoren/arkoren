package awsqueue

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"arkoren.dev/core/service/queue"
	receipt "arkoren.dev/driver/aws/awsqueue/internal/delivery"
)

// maxVisibilityTimeout is SQS's inclusive visibility limit for both received
// messages and retry delays. Renewal remains subject to SQS's time-left limit.
const maxVisibilityTimeout = 12 * time.Hour

// maxHeartbeatTimeout caps one complete SDK renewal operation, including native
// retries. Shorter leases use half their configured visibility instead, so a
// stalled call cannot occupy more than one configured heartbeat interval.
const maxHeartbeatTimeout = 10 * time.Second

var (
	// ErrUnknownQueue is returned when a queue name has no configured SQS URL.
	// No message is sent or received for that unknown mapping.
	//
	// Example:
	//
	//	if errors.Is(err, awsqueue.ErrUnknownQueue) {
	//		return fmt.Errorf("configure the logical queue destination: %w", err)
	//	}
	ErrUnknownQueue = errors.New("unknown job queue")

	// ErrInvalidConfig is returned when SQS driver configuration is invalid.
	// Required routing and polling/visibility bounds are checked at construction.
	//
	// Example:
	//
	//	if errors.Is(err, awsqueue.ErrInvalidConfig) {
	//		return fmt.Errorf("correct queue construction settings: %w", err)
	//	}
	ErrInvalidConfig = errors.New("invalid SQS job driver configuration")

	// ErrUnsupportedRetryDelay is returned when an SQS visibility timeout cannot
	// represent the requested retry delay.
	// A rejected delay never settles the delivery or shortens the requested wait.
	//
	// Example:
	//
	//	if errors.Is(err, awsqueue.ErrUnsupportedRetryDelay) {
	//		return fmt.Errorf("choose a delay supported by SQS: %w", err)
	//	}
	ErrUnsupportedRetryDelay = errors.New("unsupported SQS job retry delay")

	// ErrInvalidDelivery is returned when SQS does not provide the data required
	// to settle a received delivery.
	// Missing receipt handles cannot be acknowledged by the driver.
	//
	// Example:
	//
	//	if errors.Is(err, awsqueue.ErrInvalidDelivery) {
	//		return fmt.Errorf("invalid native SQS delivery: %w", err)
	//	}
	ErrInvalidDelivery = errors.New("invalid SQS job delivery")

	// ErrInvalidHandler is returned when Consume is given a nil handler.
	// A handler must explicitly settle each accepted delivery.
	//
	// Example:
	//
	//	if errors.Is(err, awsqueue.ErrInvalidHandler) {
	//		return fmt.Errorf("supply a delivery handler before consuming: %w", err)
	//	}
	ErrInvalidHandler = errors.New("invalid job delivery handler")

	// ErrDeliverySettled is returned when a delivery is settled more than once.
	// Concurrent settlement admission also fails with this sentinel.
	//
	// Example:
	//
	//	if errors.Is(err, awsqueue.ErrDeliverySettled) {
	//		return fmt.Errorf("delivery already has a settlement owner: %w", err)
	//	}
	ErrDeliverySettled = errors.New("job delivery already settled")
)

// settings holds the optional settings of an SQS-backed job [Driver]. The
// required queue map is a positional argument of [New] and [NewFrom], not a
// field here.
type settings struct {
	// FailureQueueURL receives terminal failure reports when non-empty. The
	// default is empty, which sends no failure reports.
	FailureQueueURL string

	// WaitTimeSeconds controls SQS long polling. A zero value uses 20 seconds.
	WaitTimeSeconds int32

	// MaxMessages controls the maximum number of messages received per poll. A
	// zero value uses one message.
	MaxMessages int32

	// Logger records delivery failures. Nil selects slog.Default at construction.
	Logger *slog.Logger

	// VisibilityTimeout is the requested lease and sets the half-timeout
	// heartbeat interval. Zero uses 30 seconds; other values must be between
	// one second and 12 hours. SQS requests round fractional seconds up.
	VisibilityTimeout time.Duration
}

// defaultConfig returns SQS driver defaults.
func defaultConfig() settings {
	return settings{
		WaitTimeSeconds: 20,
		MaxMessages:     1,
	}
}

// client defines the Amazon SQS operations used by Driver.
type client interface {
	// SendMessage dispatches a job envelope to an SQS queue.
	SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	// ReceiveMessage reserves a batch of job envelopes from an SQS queue.
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	// DeleteMessage completes a delivery using its SQS receipt handle.
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	// ChangeMessageVisibility postpones redelivery of a reserved message.
	ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

// Driver implements [queue.DispatcherDriver] and [queue.ConsumerDriver] using Amazon SQS.
// Native clients are borrowed; each Consume call owns its per-delivery heartbeats.
//
// Example:
//
//	driver, err := awsqueue.NewFrom(client, map[string]string{"emails": "queue-url"})
//	if err != nil {
//		return err
//	}
//	dispatcher := queue.NewDispatcher(driver)
//	_ = dispatcher
type Driver struct {
	// client sends, receives, and settles SQS messages.
	client client
	// queues maps application queue names to SQS queue URLs.
	queues map[string]string
	// failureQueueURL receives terminal failure reports when configured.
	failureQueueURL string
	// waitTimeSeconds controls the SQS long-poll duration.
	waitTimeSeconds int32
	// visibility is the visibility timeout requested and kept alive.
	visibility time.Duration
	// maxMessages limits messages returned by each SQS receive request.
	maxMessages int32
	// logger records delivery and handler failures.
	logger *slog.Logger
}

var _ queue.DispatcherDriver = (*Driver)(nil)
var _ queue.ConsumerDriver = (*Driver)(nil)

// Client returns the underlying SQS client, or nil when the driver was
// built from a test double. Native policy remains shared; direct queue operations
// bypass this driver's envelope, lease, and settlement handling.
//
// Example:
//
//	if native := driver.Client(); native != nil {
//		_ = native.Options().Region
//	}
func (driver *Driver) Client() *sqs.Client {
	client, _ := driver.client.(*sqs.Client)

	return client
}

// New creates the driver with credentials and region from the default AWS
// configuration chain (environment, shared config, instance role).
//
// queues maps application queue names to SQS queue URLs. It is required: an
// empty map, or any entry with a blank name or URL, returns an error wrapping
// [ErrInvalidConfig]. The map is copied, so later changes by the caller do not
// affect the driver. configure callbacks adjust optional settings directly once,
// synchronously in order on one fresh draft. Nil entries panic; expanded nil or
// empty slices supply no callbacks. Retained drafts cannot change the driver.
// Queue mappings and completed settings are validated before AWS configuration
// loading. VisibilityTimeout accepts zero (30 seconds) or one second through
// 12 hours inclusive; other values wrap [ErrInvalidConfig]. Fractional seconds
// round up in SQS requests; the heartbeat uses half the configured duration.
//
// Example:
//
//	driver, err := awsqueue.New(ctx, map[string]string{
//		"emails": "https://sqs.eu-west-1.amazonaws.com/123456789012/emails",
//	}, func(consumer *awsqueue.Builder) {
//		consumer.MaxMessages(5)
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
func New(ctx context.Context, queues map[string]string, configure ...BuilderFunc) (*Driver, error) {
	queues, config, err := normalizedSQSConfig(queues, configured(configure...))

	if err != nil {
		return nil, err
	}

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx)

	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}

	return configuredDriver(sqs.NewFromConfig(awsConfig), queues, config), nil
}

// NewFrom creates an SQS job driver using client. The caller retains ownership
// of client.
//
// queues is required and validated and copied exactly as in [New]; a nil
// client also returns an error wrapping [ErrInvalidConfig]. All Builder settings
// apply, including New's visibility bounds and zero default. Validation makes
// no SQS requests and starts no goroutines. Callbacks follow New's ordered
// once-only policy, including nil panics.
// Native HTTP clients, credential providers, and middleware must honor operation
// contexts: the driver cancels and joins heartbeat I/O rather than abandoning
// goroutines when a supplied dependency ignores cancellation.
//
// Example:
//
//	driver, err := awsqueue.NewFrom(sqs.NewFromConfig(cfg), map[string]string{
//		"emails": "https://sqs.eu-west-1.amazonaws.com/123456789012/emails",
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
func NewFrom(client *sqs.Client, queues map[string]string, configure ...BuilderFunc) (*Driver, error) {
	config := configured(configure...)

	if client == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}

	return newFrom(client, queues, config)
}

// newFrom validates queues and config and builds a driver from an
// SQS-compatible client.
func newFrom(client client, queues map[string]string, config settings) (*Driver, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}

	queues, config, err := normalizedSQSConfig(queues, config)

	if err != nil {
		return nil, err
	}

	return configuredDriver(client, queues, config), nil
}

// configuredDriver publishes already validated, defaulted settings and an owned
// queue map without repeating normalization. Both construction paths use it;
// the native client and logger remain shared, but the settings draft is detached.
func configuredDriver(client client, queues map[string]string, config settings) *Driver {
	return &Driver{
		client:          client,
		queues:          queues,
		failureQueueURL: config.FailureQueueURL,
		waitTimeSeconds: config.WaitTimeSeconds,
		visibility:      config.VisibilityTimeout,
		maxMessages:     config.MaxMessages,
		logger:          driverLogger(config.Logger),
	}
}

// normalizedSQSConfig validates queues and configuration, copies queues, and
// applies SQS defaults.
func normalizedSQSConfig(source map[string]string, config settings) (map[string]string, settings, error) {
	if len(source) == 0 {
		return nil, settings{}, fmt.Errorf("%w: no queues", ErrInvalidConfig)
	}

	queues, err := copySQSQueues(source)

	if err != nil {
		return nil, settings{}, err
	}

	config = defaultSQSConfig(config)

	if err := validateSQSConfig(config); err != nil {
		return nil, settings{}, err
	}

	return queues, config, nil
}

// copySQSQueues returns an independent copy of complete queue URL mappings.
func copySQSQueues(source map[string]string) (map[string]string, error) {
	queues := make(map[string]string, len(source))

	for name, url := range source {
		if !validSQSQueue(name, url) {
			return nil, fmt.Errorf("%w: queue name and URL are required", ErrInvalidConfig)
		}

		queues[name] = url
	}

	return queues, nil
}

// validSQSQueue reports whether a queue mapping has a name and URL.
func validSQSQueue(name, url string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}

	return strings.TrimSpace(url) != ""
}

// defaultSQSConfig replaces zero polling and visibility settings with driver
// defaults. Negative and out-of-range values remain for validateSQSConfig.
func defaultSQSConfig(config settings) settings {
	if config.WaitTimeSeconds == 0 {
		config.WaitTimeSeconds = 20
	}

	if config.MaxMessages == 0 {
		config.MaxMessages = 1
	}

	config.VisibilityTimeout = cmp.Or(config.VisibilityTimeout, 30*time.Second)

	return config
}

// validateSQSConfig checks completed settings before SDK loading or driver
// publication. The one-second minimum avoids sub-second heartbeat churn and
// nonpositive ticker periods; the 12-hour maximum also bounds int32 conversion.
func validateSQSConfig(config settings) error {
	if !validSQSRange(config.WaitTimeSeconds, 1, 20) {
		return fmt.Errorf("%w: wait time must be between 1 and 20 seconds", ErrInvalidConfig)
	}

	if !validSQSRange(config.MaxMessages, 1, 10) {
		return fmt.Errorf("%w: maximum messages must be between 1 and 10", ErrInvalidConfig)
	}

	if config.VisibilityTimeout < time.Second || config.VisibilityTimeout > maxVisibilityTimeout {
		return fmt.Errorf("%w: visibility timeout must be between 1 second and 12 hours", ErrInvalidConfig)
	}

	return nil
}

// validSQSRange reports whether value lies within inclusive SQS limits.
func validSQSRange(value, minimum, maximum int32) bool {
	return value >= minimum && value <= maximum
}

// Dispatch serializes message and sends it to the configured URL for its queue.
// FIFO queues group by logical queue and deduplicate on the supplied ID, or a
// fresh ID when omitted. Success reports acceptance, not handler completion.
//
// Example:
//
//	message := queue.Message{
//		ID: "job-1", Name: "send-email", Queue: "emails", Payload: []byte(`{"id":1}`),
//	}
//	if err := driver.Dispatch(ctx, message); err != nil {
//		return err
//	}
func (driver *Driver) Dispatch(ctx context.Context, message queue.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return driver.dispatch(ctx, message)
}

// dispatch serializes and sends a job message to its configured queue.
func (driver *Driver) dispatch(ctx context.Context, message queue.Message) error {
	queueURL, err := driver.queueURL(message.Queue)

	if err != nil {
		return err
	}

	body, err := json.Marshal(message)

	if err != nil {
		return fmt.Errorf("marshal job message: %w", err)
	}

	if err := driver.sendMessage(ctx, queueURL, body, message); err != nil {
		return fmt.Errorf("send job message: %w", err)
	}

	return nil
}

// sendMessage sends a serialized job body to queueURL. FIFO queues get one
// message group per queue and deduplicate on the job's message ID. Malformed
// failure reports use the "failed" group and a fresh ID when no envelope exists.
func (driver *Driver) sendMessage(ctx context.Context, queueURL string, body []byte, message queue.Message) error {
	input := &sqs.SendMessageInput{
		MessageBody: aws.String(string(body)),
		QueueUrl:    aws.String(queueURL),
	}

	if strings.HasSuffix(queueURL, ".fifo") {
		input.MessageGroupId = aws.String(cmp.Or(message.Queue, "failed"))
		input.MessageDeduplicationId = aws.String(cmp.Or(message.ID, rand.Text()))
	}

	_, err := driver.client.SendMessage(ctx, input)

	return err
}

// handleMessages dispatches every message in an SQS polling batch. Handlers
// run one after another, so a batch of N messages can wait many handler
// durations; every message therefore has its visibility extended from receipt
// (see [delivery.keepAlive]) until its own handling ends to reduce redelivery
// while queued here. Renewal failures can still let visibility expire. Deferred
// stops also cancel and join workers never reached because a handler panicked.
func (driver *Driver) handleMessages(
	ctx context.Context,
	queueName string,
	queueURL string,
	messages []types.Message,
	handler queue.DeliveryHandler,
) {
	deliveries := make([]*delivery, len(messages))
	stops := make([]func(), len(messages))

	defer func() {
		for _, stop := range stops {
			if stop != nil {
				stop()
			}
		}
	}()

	for i, received := range messages {
		// The job message is decoded later; keepAlive needs only the receipt.
		deliveries[i] = newDelivery(driver, queueURL, received, queue.Message{})

		if deliveries[i] != nil {
			stops[i] = deliveries[i].keepAlive(ctx)
		}
	}

	for i, received := range messages {
		driver.handle(ctx, queueName, received, deliveries[i], handler)

		if stops[i] != nil {
			stops[i]()
		}
	}
}

// handle decodes received into delivery and invokes its handler, or fails the
// delivery when the body cannot be decoded. A nil delivery means received had
// no receipt handle and cannot be settled.
func (driver *Driver) handle(ctx context.Context, queueName string, received types.Message, delivery *delivery, handler queue.DeliveryHandler) {
	message, err := parseMessage(queueName, received)

	if delivery == nil {
		driver.logger.ErrorContext(ctx, "invalid SQS job delivery", "error", cmp.Or(err, ErrInvalidDelivery), "queue", queueName, "job_id", message.ID)

		return
	}

	if err != nil {
		if failureErr := delivery.fail(ctx, err, stringPointer(received.Body)); failureErr != nil {
			driver.logger.ErrorContext(ctx, "failed invalid SQS job message", "error", failureErr, "queue", queueName)
		}

		return
	}

	delivery.state.Publish(message)

	if err := handler(ctx, delivery); err != nil {
		driver.logger.ErrorContext(ctx, "SQS job delivery handler failed", "error", err, "queue", queueName, "job_id", message.ID)
	}
}

// keepAlive owns renewal until the handler returns, including while a canceled
// consumer drains its handler. The returned idempotent stop cancels pending I/O
// before joining the worker. Each extension has its own finite SDK budget;
// clients must honor request contexts for cancellation and joining to complete.
func (delivery *delivery) keepAlive(ctx context.Context) func() {
	return delivery.state.KeepAlive(ctx, delivery.driver.visibility, maxHeartbeatTimeout, func(ctx context.Context) error {
		_, err := delivery.driver.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
			QueueUrl:          aws.String(delivery.queueURL),
			ReceiptHandle:     aws.String(delivery.receiptHandle),
			VisibilityTimeout: visibilitySeconds(delivery.driver.visibility),
		})

		return err
	}, func(ctx context.Context, err error) {
		delivery.driver.logger.ErrorContext(ctx, "extend SQS visibility", "error", err, "queue_url", delivery.queueURL)
	})
}

// visibilitySeconds rounds a validated visibility timeout up to whole SQS
// seconds. Division before rounding avoids overflowing duration arithmetic;
// validateSQSConfig ensures the result fits SQS's range and int32.
func visibilitySeconds(timeout time.Duration) int32 {
	seconds := timeout / time.Second

	if timeout%time.Second != 0 {
		seconds++
	}

	return int32(seconds)
}

// queueURL returns the configured SQS URL for queue.
func (driver *Driver) queueURL(consumer string) (string, error) {
	url, ok := driver.queues[consumer]

	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownQueue, consumer)
	}

	return url, nil
}

// parseMessage decodes received into a job message for queue.
func parseMessage(queueName string, received types.Message) (queue.Message, error) {
	if received.Body == nil {
		return queue.Message{}, fmt.Errorf("%w: missing message body", ErrInvalidDelivery)
	}

	message, err := decodeEnvelope([]byte(*received.Body))

	if err != nil {
		return queue.Message{}, err
	}

	message.Queue = queueName
	message.ID = stringPointer(received.MessageId)
	message.Attempts = attempts(received.Attributes)

	return message, nil
}

// decodeEnvelope accepts direct queue envelopes and standard SNS notifications.
func decodeEnvelope(body []byte) (queue.Message, error) {
	var message queue.Message

	if err := json.Unmarshal(body, &message); err == nil && message.Name != "" {
		return message, nil
	}

	var notification struct {
		// Message contains the queue envelope encoded by the SNS publisher.
		Message string `json:"Message"`
	}

	if err := json.Unmarshal(body, &notification); err != nil || notification.Message == "" {
		return queue.Message{}, fmt.Errorf("unmarshal job message: invalid envelope")
	}

	if err := json.Unmarshal([]byte(notification.Message), &message); err != nil || message.Name == "" {
		return queue.Message{}, fmt.Errorf("unmarshal SNS job message: invalid envelope")
	}

	return message, nil
}

// attempts returns the SQS approximate receive count when it is valid.
func attempts(attributes map[string]string) int {
	count, err := strconv.Atoi(attributes["ApproximateReceiveCount"])

	if err != nil || count < 1 {
		return 0
	}

	return count
}

// stringPointer returns the string value referenced by value, or an empty string.
func stringPointer(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

// delivery owns one receipt's settlement state and immutable message snapshot.
// Its context-aware guard serializes renewal RPCs with settlement admission;
// flags then exclude renewal until settlement fails or succeeds permanently.
type delivery struct {
	// driver performs SQS operations for the delivery.
	driver *Driver
	// queueURL identifies the SQS queue that supplied the message.
	queueURL string
	// receiptHandle identifies the received message to SQS.
	receiptHandle string
	// state owns immutable message captures, renewal, and settlement admission.
	state *receipt.State
}

// newDelivery creates a delivery when received has an SQS receipt handle,
// otherwise returning nil. It captures owned payload and metadata containers so
// later changes to the supplied envelope cannot change delivery snapshots or
// failure reports. Nil containers remain nil. Each delivery gets an independent
// serialization slot for renewal and settlement; unrelated receipts never share
// a guard.
func newDelivery(driver *Driver, queueURL string, received types.Message, message queue.Message) *delivery {
	receiptHandle := stringPointer(received.ReceiptHandle)

	if receiptHandle == "" {
		return nil
	}

	return &delivery{
		driver:        driver,
		queueURL:      queueURL,
		receiptHandle: receiptHandle,
		state:         receipt.New(message, ErrDeliverySettled),
	}
}

// Message returns a detached snapshot of the delivered job message. Its payload
// and metadata may be mutated without changing later snapshots or failure
// reports. Nil containers remain nil; concurrent calls read immutable state.
//
//	func handle(ctx context.Context, received queue.Delivery) error {
//		message := received.Message()
//		fmt.Println(message.Name)
//		return received.Acknowledge(ctx)
//	}
func (delivery *delivery) Message() queue.Message {
	return delivery.state.Message()
}

// Acknowledge deletes the delivered message from SQS. The caller's context
// bounds both waiting for a pending heartbeat RPC and the deletion itself.
// Failed admission or deletion leaves the delivery available for another try.
//
// Example:
//
//	func handle(ctx context.Context, received queue.Delivery) error {
//		return received.Acknowledge(ctx)
//	}
func (delivery *delivery) Acknowledge(ctx context.Context) error {
	return delivery.state.Settle(ctx, func() error {
		return delivery.delete(ctx)
	})
}

// Retry changes the delivered message visibility for delay. Negative delays and
// delays above 12 hours return ErrUnsupportedRetryDelay without settlement.
// Positive fractional seconds round up; zero requests immediate visibility.
// SQS also limits visibility to the time remaining since this receipt's receive
// request. Consequently even a nominally valid delay, including exactly 12
// hours, can fail at the provider; that error is returned without shortening the
// delay. Waiting for an earlier heartbeat
// consumes ctx's budget; a failed wait or RPC permits another settlement.
// Successful retry disables subsequent heartbeat requests for this delivery.
//
// Example:
//
//	func handle(ctx context.Context, received queue.Delivery) error {
//		return received.Retry(ctx, 2*time.Second)
//	}
func (delivery *delivery) Retry(ctx context.Context, delay time.Duration) error {
	if delay < 0 || delay > maxVisibilityTimeout {
		return fmt.Errorf("%w: %s", ErrUnsupportedRetryDelay, delay)
	}

	return delivery.state.Settle(ctx, func() error {
		_, err := delivery.driver.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
			QueueUrl:          aws.String(delivery.queueURL),
			ReceiptHandle:     aws.String(delivery.receiptHandle),
			VisibilityTimeout: visibilitySeconds(delay),
		})

		return err
	})
}

// Reject gives the message back to SQS for dead-lettering.
//
// The message is left in the queue untouched, so the queue's redrive policy
// moves it to its dead-letter queue once maxReceiveCount is reached.
// Configure a redrive policy, or the message is retried indefinitely.
// Waiting for an earlier heartbeat honors ctx; successful rejection stops
// subsequent renewal without issuing an additional SQS request.
//
// Example:
//
//	func handle(ctx context.Context, received queue.Delivery) error {
//		return received.Reject(ctx)
//	}
func (delivery *delivery) Reject(ctx context.Context) error {
	return delivery.state.Settle(ctx, func() error {
		return nil
	})
}

// Fail reports the delivery failure and then deletes the message. The caller's
// context bounds waiting for renewal, failure publication, and deletion. A
// failed operation permits another settlement and does not disable renewal.
//
// Example:
//
//	func handle(ctx context.Context, received queue.Delivery) error {
//		return received.Fail(ctx, errors.New("invalid payload"))
//	}
func (delivery *delivery) Fail(ctx context.Context, err error) error {
	return delivery.state.Settle(ctx, func() error {
		return delivery.fail(ctx, err, "")
	})
}

// fail sends a failure report when configured and deletes the delivery.
func (delivery *delivery) fail(ctx context.Context, err error, rawMessage string) error {
	message := delivery.Message()

	if delivery.driver.failureQueueURL != "" {
		body, marshalErr := json.Marshal(failureMessage{
			Message:    message,
			Error:      errorText(err),
			RawMessage: rawMessage,
		})

		if marshalErr != nil {
			return fmt.Errorf("marshal job failure: %w", marshalErr)
		}

		if err := delivery.driver.sendMessage(ctx, delivery.driver.failureQueueURL, body, message); err != nil {
			return fmt.Errorf("send job failure: %w", err)
		}
	} else {
		delivery.driver.logger.ErrorContext(ctx, "SQS job delivery failed", "error", err, "queue", message.Queue, "job_id", message.ID)
	}

	return delivery.delete(ctx)
}

// driverLogger retains the shared logger or resolves slog.Default at construction.
func driverLogger(value *slog.Logger) *slog.Logger {
	if value == nil {
		return slog.Default()
	}

	return value
}

// delete removes the delivered message from its SQS queue.
func (delivery *delivery) delete(ctx context.Context) error {
	_, err := delivery.driver.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(delivery.queueURL),
		ReceiptHandle: aws.String(delivery.receiptHandle),
	})

	return err
}

// failureMessage is the JSON payload sent to the configured failure queue.
type failureMessage struct {
	// Message is the failed job message.
	Message queue.Message `json:"message"`
	// Error describes why the job delivery failed.
	Error string `json:"error"`
	// RawMessage contains an undecodable SQS message body when available.
	RawMessage string `json:"raw_message,omitzero"`
}

// errorText returns a failure description for err.
func errorText(err error) string {
	if err == nil {
		return "job failed"
	}

	return err.Error()
}

// MessagingSystem returns "aws_sqs", the messaging.system telemetry value that
// the queue and broadcast facades report for this driver.
//
// Example:
//
//	var driver awsqueue.Driver
//	name := driver.MessagingSystem()
//	_ = name
func (driver *Driver) MessagingSystem() string {
	return "aws_sqs"
}
