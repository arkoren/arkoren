package cloudflarequeue

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
	"github.com/cloudflare/cloudflare-go/v7/queues"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/driver/cloudflare/cloudflarequeue/internal/delivery"
)

const (
	// defaultBatchSize is the number of messages pulled per request when
	// [Builder.BatchSize] is zero. One keeps a pulled message from waiting
	// behind the handlers of its batch while its lease runs down.
	defaultBatchSize = 1

	// maxBatchSize is the most messages Cloudflare returns per pull.
	maxBatchSize = 100

	// defaultVisibilityTimeout is the lease length when
	// [Builder.VisibilityTimeout] is zero, matching Cloudflare's default.
	defaultVisibilityTimeout = 30 * time.Second

	// maxVisibilityTimeout is the longest lease Cloudflare grants.
	maxVisibilityTimeout = 12 * time.Hour

	// defaultPollInterval is the wait after an empty pull when
	// [Builder.PollInterval] is zero.
	defaultPollInterval = time.Second

	// defaultMaxPollInterval caps the wait between empty pulls when
	// [Builder.MaxPollInterval] is zero.
	defaultMaxPollInterval = 10 * time.Second
)

var (
	// ErrUnknownQueue is returned when a queue name has no configured
	// Cloudflare queue ID.
	// Routing is fixed at construction; unknown names cause no vendor request.
	//
	// Example:
	//
	//	err := driver.Dispatch(ctx, queue.Message{Queue: "not-configured", Name: "send-email"})
	//	if errors.Is(err, cloudflarequeue.ErrUnknownQueue) {
	//		// Correct the application's queue routing before retrying.
	//	}
	ErrUnknownQueue = errors.New("unknown job queue")

	// ErrInvalidConfig is returned when the driver configuration is invalid.
	// Constructors wrap it with the rejected setting and perform no requests.
	//
	// Example:
	//
	//	_, err := cloudflarequeue.New("", "account-id", map[string]string{"emails": "queue-id"})
	//	if errors.Is(err, cloudflarequeue.ErrInvalidConfig) {
	//		// Load the required API token before starting the worker.
	//	}
	ErrInvalidConfig = errors.New("invalid Cloudflare Queues job driver configuration")

	// ErrInvalidHandler is returned when Consume is given a nil handler.
	// Validation happens before polling; an already canceled context takes priority.
	//
	// Example:
	//
	//	err := driver.Consume(ctx, "emails", nil)
	//	if errors.Is(err, cloudflarequeue.ErrInvalidHandler) {
	//		// Supply a delivery handler; no message was pulled.
	//	}
	ErrInvalidHandler = errors.New("invalid job delivery handler")

	// ErrDeliverySettled is returned when a delivery is settled more than once.
	// A concurrent settlement in progress also returns this error without issuing
	// another request. Failed native settlements do not mark the delivery settled.
	//
	// Example:
	//
	//	if err := delivery.Acknowledge(ctx); err != nil {
	//		return err
	//	}
	//	if err := delivery.Acknowledge(ctx); errors.Is(err, cloudflarequeue.ErrDeliverySettled) {
	//		// This delivery was already completed; do not settle it again.
	//	}
	ErrDeliverySettled = errors.New("job delivery already settled")
)

// settings holds the optional settings of a Cloudflare Queues [Driver]. The API
// token, account ID, and queue map are positional arguments of [New] and
// [NewFrom], not fields here.
type settings struct {
	// Endpoint overrides the Cloudflare API base URL. The default is empty,
	// which uses https://api.cloudflare.com/client/v4/. [NewFrom] ignores it,
	// because the caller's client already has its endpoint.
	Endpoint string

	// BatchSize is the number of messages requested per pull, from 1 to 100.
	// The handlers of one batch run one after another, so a large batch can
	// outlast the leases of its later messages. Zero uses 1.
	BatchSize int

	// VisibilityTimeout is how long a pulled message stays leased to this
	// consumer, from 1ms to 12 hours. Cloudflare cannot extend a lease, so a
	// handler (and the handlers before it in the batch) must finish within it
	// or the message is delivered again. Zero uses 30 seconds.
	VisibilityTimeout time.Duration

	// PollInterval is the wait after an empty pull. It doubles on each
	// further empty pull up to MaxPollInterval and resets after a message
	// arrives. Zero uses 1 second.
	PollInterval time.Duration

	// MaxPollInterval caps the wait between empty pulls. A value below
	// PollInterval is raised to it. Zero uses 10 seconds.
	MaxPollInterval time.Duration

	// Logger records delivery failures. The default is nil, which uses
	// [slog.Default].
	Logger *slog.Logger
}

// Driver implements [queue.DispatcherDriver] and [queue.ConsumerDriver] using
// Cloudflare Queues over its REST API: messages are pushed with the send
// endpoint and consumed as an HTTP pull consumer.
//
// Delivery is at least once. A message that is not settled before its lease
// ends is delivered again; see [Builder.VisibilityTimeout].
// Construction captures routing before callbacks. Native clients and loggers are
// shared dependencies: configure them before use. The driver opens no background
// worker, owns no closeable connection, and has no Start or Stop method; callers
// own Consume's context and native transport lifecycle. Do not copy Driver values.
//
// Example:
//
//	driver, err := cloudflarequeue.New(token, accountID, map[string]string{"emails": "queue-id"})
//	if err != nil {
//		return err
//	}
//	consumer := queue.NewConsumer(driver)
//	_ = consumer // Register jobs and run it with the application's worker context.
type Driver struct {
	// client calls the Cloudflare API.
	client *cloudflare.Client

	// accountID identifies the Cloudflare account that owns the queues.
	accountID string

	// queueIDs maps application queue names to Cloudflare queue IDs.
	queueIDs map[string]string

	// batchSize is the number of messages requested per pull.
	batchSize int

	// visibility is the lease length requested on every pull.
	visibility time.Duration

	// pollInterval is the first wait after an empty pull.
	pollInterval time.Duration

	// maxPollInterval caps the wait between empty pulls.
	maxPollInterval time.Duration

	// logger records delivery failures.
	logger *slog.Logger
}

var _ queue.DispatcherDriver = (*Driver)(nil)
var _ queue.ConsumerDriver = (*Driver)(nil)

// Client returns the underlying Cloudflare API client.
// It is the same pointer supplied to NewFrom; neither method transfers ownership
// or closes its transports. Configure it before concurrent use. Native calls
// bypass the driver's routing and settlement checks, and native diagnostics are
// not sanitized by this package.
//
// Example:
//
//	native := cloudflare.NewClient(option.WithAPIToken(token))
//	driver, err := cloudflarequeue.NewFrom(native, accountID, routes)
//	if err != nil {
//		return err
//	}
//	_ = driver.Client() == native // Identity and ownership are preserved.
func (driver *Driver) Client() *cloudflare.Client {
	return driver.client
}

// New creates a driver that authenticates with apiToken, an API token with
// Queues read and write permissions.
//
// accountID is the Cloudflare account ID. queues maps application queue names
// to Cloudflare queue IDs; every queue to be consumed needs an HTTP pull
// consumer. All three are required: an empty token, account ID, or queue map,
// or an entry with a blank name or ID, returns an error wrapping
// [ErrInvalidConfig]. The map is copied before callbacks, so caller edits made
// during a callback or afterwards do not affect routing. Callers must not mutate
// it concurrently with input capture. configure callbacks run directly once, synchronously in
// order on one fresh draft. Nil entries panic; expanded nil/empty slices supply
// no callbacks. Retained drafts cannot change the driver. New makes no request.
//
// Example:
//
//	driver, err := cloudflarequeue.New(os.Getenv("CLOUDFLARE_API_TOKEN"),
//		os.Getenv("CLOUDFLARE_ACCOUNT_ID"),
//		map[string]string{"emails": "6b8f3a0c2d1e4f5a8b7c9d0e1f2a3b4c"},
//		func(consumer *cloudflarequeue.Builder) {
//			consumer.BatchSize(10)
//		},
//	)
//	if err != nil {
//		return err
//	}
//	_ = driver
func New(apiToken, accountID string, queueIDs map[string]string, configure ...BuilderFunc) (*Driver, error) {
	queueIDs = maps.Clone(queueIDs)
	config := configured(configure...)

	if apiToken == "" {
		return nil, fmt.Errorf("%w: API token is required", ErrInvalidConfig)
	}

	clientOptions := []option.RequestOption{option.WithAPIToken(apiToken)}

	if config.Endpoint != "" {
		clientOptions = append(clientOptions, option.WithBaseURL(config.Endpoint))
	}

	return newDriver(cloudflare.NewClient(clientOptions...), accountID, queueIDs, config)
}

// NewFrom creates a driver that calls the Cloudflare API through client. The
// caller retains ownership of client and its credentials.
//
// accountID and queues are required and validated and copied exactly as in
// [New]; a nil client also returns an error wrapping [ErrInvalidConfig]. All
// Builder settings apply except [Builder.Endpoint]. Callbacks follow New's
// ordered once-only policy, including nil-entry panics before validation.
// Routing is captured before those callbacks. Loggers and the native client stay
// shared; the driver neither closes nor recursively sanitizes their contents.
//
// Example:
//
//	client := cloudflare.NewClient(option.WithAPIToken(token))
//	driver, err := cloudflarequeue.NewFrom(client, accountID, map[string]string{
//		"emails": "6b8f3a0c2d1e4f5a8b7c9d0e1f2a3b4c",
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
func NewFrom(client *cloudflare.Client, accountID string, queueIDs map[string]string, configure ...BuilderFunc) (*Driver, error) {
	queueIDs = maps.Clone(queueIDs)
	config := configured(configure...)

	if client == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidConfig)
	}

	return newDriver(client, accountID, queueIDs, config)
}

// newDriver validates the settings shared by New and NewFrom and applies
// defaults. It takes ownership of the routing snapshot already detached by its
// callers before their callbacks; no caller can mutate that private map.
func newDriver(client *cloudflare.Client, accountID string, queueIDs map[string]string, config settings) (*Driver, error) {
	if err := validateRouting(accountID, queueIDs); err != nil {
		return nil, err
	}

	batchSize := cmp.Or(config.BatchSize, defaultBatchSize)

	if batchSize < 1 || batchSize > maxBatchSize {
		return nil, fmt.Errorf("%w: batch size must be between 1 and %d", ErrInvalidConfig, maxBatchSize)
	}

	visibility := cmp.Or(config.VisibilityTimeout, defaultVisibilityTimeout)

	if visibility < time.Millisecond || visibility > maxVisibilityTimeout {
		return nil, fmt.Errorf("%w: visibility timeout must be between 1ms and %s", ErrInvalidConfig, maxVisibilityTimeout)
	}

	pollInterval := cmp.Or(config.PollInterval, defaultPollInterval)

	if pollInterval < 0 || config.MaxPollInterval < 0 {
		return nil, fmt.Errorf("%w: poll intervals must not be negative", ErrInvalidConfig)
	}

	return &Driver{
		client:          client,
		accountID:       accountID,
		queueIDs:        queueIDs,
		batchSize:       batchSize,
		visibility:      visibility,
		pollInterval:    pollInterval,
		maxPollInterval: max(cmp.Or(config.MaxPollInterval, defaultMaxPollInterval), pollInterval),
		logger:          cmp.Or(config.Logger, slog.Default()),
	}, nil
}

// validateRouting rejects incomplete account and queue routing before polling
// settings are normalized. It reads the map without taking ownership or doing I/O.
func validateRouting(accountID string, queueIDs map[string]string) error {
	if strings.TrimSpace(accountID) == "" {
		return fmt.Errorf("%w: account ID is required", ErrInvalidConfig)
	}

	if len(queueIDs) == 0 {
		return fmt.Errorf("%w: no queues", ErrInvalidConfig)
	}

	for name, id := range queueIDs {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(id) == "" {
			return fmt.Errorf("%w: queue name and ID are required", ErrInvalidConfig)
		}
	}

	return nil
}

// queueID returns the Cloudflare queue ID configured for the queue name.
func (driver *Driver) queueID(name string) (string, error) {
	id, ok := driver.queueIDs[name]

	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownQueue, name)
	}

	return id, nil
}

// Dispatch JSON-encodes message and pushes it as a text message to the
// Cloudflare queue configured for message.Queue. The envelope carries
// Metadata, so it needs no Cloudflare-specific header. It returns an error
// wrapping [ErrUnknownQueue] when the queue has no configured ID and the
// context error when ctx is already canceled.
//
// Cloudflare limits a message to 128 KB including the envelope, whose Payload
// is base64 encoded.
// Native request errors retain their original causes; this is not a delivery
// receipt or proof that a consumer processed the message.
//
// Example:
//
//	message := queue.Message{Queue: "emails", Name: "send-email", Payload: []byte(`{"user_id":42}`)}
//	if err := driver.Dispatch(ctx, message); err != nil {
//		return err
//	}
func (driver *Driver) Dispatch(ctx context.Context, message queue.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	queueID, err := driver.queueID(message.Queue)

	if err != nil {
		return err
	}

	body, err := json.Marshal(message)

	if err != nil {
		return fmt.Errorf("marshal job message: %w", err)
	}

	_, err = driver.client.Queues.Messages.Push(ctx, queueID, queues.MessagePushParams{
		AccountID: cloudflare.F(driver.accountID),
		Body: queues.MessagePushParamsBodyMqQueueMessageText{
			Body:        cloudflare.F(string(body)),
			ContentType: cloudflare.F(queues.MessagePushParamsBodyMqQueueMessageTextContentTypeText),
		},
	})

	if err != nil {
		return fmt.Errorf("send job message: %w", err)
	}

	return nil
}

// Consume pulls messages from the named queue and passes each to handler
// until ctx is canceled or a pull fails. Cloudflare answers a pull at once
// even when the queue is empty, so Consume waits [Builder.PollInterval],
// doubling up to [Builder.MaxPollInterval], between empty pulls.
//
// Handlers run one after another within a batch. A handler error is logged and
// leaves the delivery unsettled: Cloudflare delivers it again when its lease
// ends. Cancellation returns the context error. A failed pull returns an error
// after the Cloudflare client has exhausted its own retries; the caller (for
// example [queue.Consumer.Run]) decides whether to start Consume again.
// Cancellation is checked before every handler, including remaining messages
// in an already-pulled batch; unused leases expire normally. No background handler
// goroutines are started: a running synchronous handler must return cooperatively.
// Native pull errors and context identity remain inspectable.
//
// Example:
//
//	return driver.Consume(ctx, "emails", func(ctx context.Context, delivery queue.Delivery) error {
//		message := delivery.Message()
//		_ = message // Process it before acknowledging.
//		return delivery.Acknowledge(ctx)
//	})
func (driver *Driver) Consume(ctx context.Context, queueName string, handler queue.DeliveryHandler) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if handler == nil {
		return ErrInvalidHandler
	}

	queueID, err := driver.queueID(queueName)

	if err != nil {
		return err
	}

	for {
		messages, err := driver.pull(ctx, queueID)

		if err != nil {
			return err
		}

		if err := driver.handleBatch(ctx, queueName, messages, handler); err != nil {
			return err
		}
	}
}

// pull waits for a nonempty native batch, applying empty-poll backoff locally.
// Each successful batch resets that backoff. Context identity and native causes
// are retained on errors; no delivery handler runs inside this network boundary.
func (driver *Driver) pull(ctx context.Context, queueID string) ([]queues.MessagePullResponseMessage, error) {
	wait := driver.pollInterval

	for {
		pulled, err := driver.client.Queues.Messages.Pull(ctx, queueID, queues.MessagePullParams{
			AccountID:           cloudflare.F(driver.accountID),
			BatchSize:           cloudflare.F(float64(driver.batchSize)),
			VisibilityTimeoutMs: cloudflare.F(float64(driver.visibility.Milliseconds())),
		})

		if err != nil {
			return nil, fmt.Errorf("pull job messages: %w", errors.Join(err, ctx.Err()))
		}

		if len(pulled.Messages) != 0 {
			return pulled.Messages, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}

		wait = min(wait*2, driver.maxPollInterval)
	}
}

// handleBatch begins each handler only while the consumer context is active.
// Remaining leases are left untouched on shutdown so the broker can redeliver.
// queueName has already been validated against the immutable routing map.
func (driver *Driver) handleBatch(ctx context.Context, queueName string, messages []queues.MessagePullResponseMessage, handler queue.DeliveryHandler) error {
	for _, received := range messages {
		if err := ctx.Err(); err != nil {
			return err
		}

		driver.handle(ctx, queueName, received, handler)
	}

	return nil
}

// handle decodes received into a delivery and passes it to handler, or fails
// the message when its body is not a job envelope. A message without a lease
// ID cannot be settled and is skipped. queueName is an already validated route;
// its physical queue ID is read from the driver's immutable routing snapshot.
func (driver *Driver) handle(ctx context.Context, queueName string, received queues.MessagePullResponseMessage, handler queue.DeliveryHandler) {
	if received.LeaseID == "" {
		driver.logger.ErrorContext(ctx, "Cloudflare job delivery has no lease ID", "queue", queueName, "message_id", received.ID)

		return
	}

	message := queue.Message{
		ID:    received.ID,
		Queue: queueName,
	}

	if err := json.Unmarshal([]byte(received.Body), &message); err != nil || message.Name == "" {
		driver.logger.ErrorContext(ctx, "invalid Cloudflare job message", "queue", queueName, "message_id", received.ID, "error", err)

		leased := delivery.New(driver.client, driver.accountID, driver.queueIDs[queueName], received.LeaseID, message, driver.logger, ErrDeliverySettled)

		if err := leased.Fail(ctx, errors.New("invalid job envelope")); err != nil {
			driver.logger.ErrorContext(ctx, "fail invalid Cloudflare job message", "queue", queueName, "message_id", received.ID, "error", err)
		}

		return
	}

	// The configured queue and Cloudflare's identity are authoritative over
	// anything the envelope claims.
	message.Queue = queueName
	message.ID = received.ID
	message.Attempts = int(received.Attempts)

	leased := delivery.New(driver.client, driver.accountID, driver.queueIDs[queueName], received.LeaseID, message, driver.logger, ErrDeliverySettled)

	if err := handler(ctx, leased); err != nil {
		driver.logger.ErrorContext(ctx, "Cloudflare job delivery handler failed", "queue", queueName, "job_id", received.ID, "error", err)
	}
}

// MessagingSystem returns "cloudflare_queues", the messaging.system telemetry
// value that the queue facades report for this driver.
//
// Example:
//
//	var driver cloudflarequeue.Driver
//	name := driver.MessagingSystem()
//	_ = name
func (driver *Driver) MessagingSystem() string {
	return "cloudflare_queues"
}
