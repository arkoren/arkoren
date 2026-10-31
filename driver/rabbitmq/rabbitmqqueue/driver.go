package rabbitmqqueue

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/driver/rabbitmq/internal/amqpconn"
	receipt "arkoren.dev/driver/rabbitmq/rabbitmqqueue/internal/delivery"
	"arkoren.dev/driver/rabbitmq/rabbitmqqueue/internal/topology"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

const (
	// DefaultExchange is the durable direct exchange used for Arkoren jobs.
	DefaultExchange = "arkoren:jobs"
	// defaultPrefetch limits each consumer to one unsettled message.
	defaultPrefetch = 1
	// defaultRetryDelay postpones retries by one second when no delay is supplied.
	defaultRetryDelay = time.Second
	// attemptHeader stores the delivery attempt count in AMQP message headers.
	attemptHeader = "arkoren-attempt"
)

var (
	// ErrInvalidConfig is returned when connection settings or [settings] cannot
	// safely define a job driver. Constructors wrap it with the invalid setting;
	// callers can use errors.Is to distinguish configuration from network failures.
	//
	// Example:
	//
	// 	if errors.Is(err, rabbitmqqueue.ErrInvalidConfig) {
	// 		return fmt.Errorf("configure RabbitMQ jobs: %w", err)
	// 	}
	ErrInvalidConfig = errors.New("invalid AMQP job driver configuration")

	// ErrUnknownQueue is returned when an application queue has no configured topology.
	ErrUnknownQueue = errors.New("unknown job queue")

	// ErrInvalidHandler is returned when Consume is given a nil handler.
	ErrInvalidHandler = errors.New("invalid job delivery handler")

	// ErrInvalidDelivery is returned when an AMQP delivery cannot be settled or decoded.
	ErrInvalidDelivery = errors.New("invalid AMQP job delivery")

	// ErrDeliverySettled is returned when a delivery is settled more than once.
	ErrDeliverySettled = errors.New("job delivery already settled")

	// ErrDriverClosed is returned when an operation is attempted after Stop.
	ErrDriverClosed = errors.New("AMQP job driver is closed")

	// ErrNotStarted is returned when an operation is attempted before Start.
	ErrNotStarted = errors.New("AMQP job driver is not started")

	// ErrAlreadyStarted is returned when Start is called more than once.
	ErrAlreadyStarted = errors.New("AMQP job driver is already started")

	// ErrPublishNotConfirmed reports a negative confirmation or an unroutable
	// mandatory publication. The source delivery of a retry or failure transfer
	// remains unsettled; a positive confirmation alone is not routing evidence.
	//
	// Example:
	//
	//	if errors.Is(err, rabbitmqqueue.ErrPublishNotConfirmed) {
	//		return fmt.Errorf("check RabbitMQ bindings: %w", err)
	//	}
	ErrPublishNotConfirmed = errors.New("AMQP job publish was not confirmed")

	// ErrUnsupportedRetryDelay is returned when a retry delay exceeds the configured bound.
	ErrUnsupportedRetryDelay = errors.New("unsupported AMQP job retry delay")
)

// Queue maps a logical application queue to a fixed broker queue and routing key.
// Neither value is read from untrusted job message data. Both must be nonblank
// and unique within the map supplied to New or NewFrom.
//
// Example:
//
//	queues := map[string]rabbitmqqueue.Queue{"emails": {Name: "jobs.emails", RoutingKey: "emails"}}
//	driver, err := rabbitmqqueue.New(url, queues)
//	if err != nil {
//		return err
//	}
//	_ = driver
type Queue struct {
	// Name is the durable RabbitMQ queue name.
	// Example: Queue{Name: "jobs.emails", RoutingKey: "emails"}.
	Name string

	// RoutingKey binds Name to the exchange set with Builder.Exchange.
	// For example, Queue{Name: "jobs.email", RoutingKey: "email"} binds that
	// broker queue with the application routing key "email".
	RoutingKey string
}

// settings configures a durable RabbitMQ job driver.
type settings struct {
	// Exchange is a durable direct exchange. An empty value uses [DefaultExchange].
	Exchange string

	// Queues maps trusted logical application queue names to RabbitMQ topology.
	Queues map[string]Queue

	// Prefetch is the number of unacknowledged messages per Consume invocation.
	// A zero value uses one.
	Prefetch int

	// RetryDelay is used by Retry when its delay is zero. A zero value uses one second.
	RetryDelay time.Duration

	// MaxRetryDelay bounds Retry delays and therefore the number of retry queues that
	// a caller can create. A zero value permits RabbitMQ's maximum TTL, about 49 days.
	MaxRetryDelay time.Duration

	// FailureQueue receives durable failure envelopes before the original job is
	// acknowledged. When empty, Fail terminally rejects the original delivery; any
	// resulting dead-lettering depends on broker policy.
	FailureQueue string

	// Logger records delivery failures. Nil selects slog.Default().
	Logger *slog.Logger
}

// defaultConfig returns the default AMQP job driver configuration.
func defaultConfig() settings {
	return settings{
		Exchange:   DefaultExchange,
		Prefetch:   defaultPrefetch,
		RetryDelay: defaultRetryDelay,
	}
}

// Driver implements [queue.DispatcherDriver] and [queue.ConsumerDriver]
// with durable RabbitMQ work queues. Dispatch returns only after RabbitMQ publisher
// confirms the persistent publish. It does not confirm that a worker processed it.
//
// Each Consume call opens its own channel, so multiple consumers safely compete for
// messages on the same durable queue.
// Broker queue types, retention, replication, and protected dead-letter forwarding
// remain operational prerequisites, not guarantees established by this type.
//
// Example:
//
//	driver, err := rabbitmqqueue.New(url, map[string]rabbitmqqueue.Queue{
//		"emails": {Name: "jobs.emails", RoutingKey: "emails"},
//	})
//	if err != nil {
//		return err
//	}
//	_ = queue.NewDispatcher(driver)
type Driver struct {
	// connection is shared by publishing and per-consumer channels.
	connection *amqp091.Connection
	// publisher is the reusable, confirmation-enabled publishing channel.
	publisher *amqp091.Channel
	// config contains the completed, detached construction settings.
	config settings
	// url is retained for the deferred connection opened by Start.
	url string
	// owned determines whether Stop closes the native connection.
	owned bool
	// abort closes the owned socket without a broker RPC; nil for NewFrom.
	abort func() error

	// mu protects connection lifecycle state, never broker confirmation waits.
	mu sync.Mutex
	// publishGate permits one in-flight publish so basic.return, which has no
	// delivery tag, can be paired with its corresponding confirmation.
	publishGate chan struct{}
	// returns buffers the sole outstanding mandatory publication's return.
	returns <-chan amqp091.Return
	// pending retains a canceled caller's confirmation until the next publish
	// can drain its outcome; a late return must never fail a different job.
	pending *amqp091.DeferredConfirmation
	// closed prevents operations after Stop releases driver resources.
	closed bool
	// logger reports handler and settlement errors without attaching job bodies.
	logger *slog.Logger
}

var _ queue.DispatcherDriver = (*Driver)(nil)
var _ queue.ConsumerDriver = (*Driver)(nil)

// Connection returns the shared native connection without transferring ownership.
// New returns nil until successful Start; NewFrom exposes its supplied connection
// immediately. The pointer remains available after Stop; an owned connection is
// then closed, while a borrowed connection still belongs to its caller.
// Native operations bypass the driver's routing and lifecycle protections.
//
// Example:
//
//	connection := driver.Connection()
//	if connection != nil && connection.IsClosed() {
//		return errors.New("RabbitMQ connection is closed")
//	}
func (driver *Driver) Connection() *amqp091.Connection {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	return driver.connection
}

// New rejects a blank url, validates queues and optional policy, and returns a
// driver that owns its future connection. url is the AMQP connection string, for example
// amqp://user:password@host:5672/vhost; it has no safe default, so it is a
// required positional argument. queues is a nonempty trusted routing map with
// unique broker queue names and routing keys, copied before callbacks run and
// again at publication. It has no environment fallback or default topology.
// The URL typically carries credentials: prefer amqps:// and load it from a
// secret manager.
//
// New returns an error wrapping [ErrInvalidConfig] that names URL when url is
// blank, and one for invalid completed settings. Callbacks run once in order
// before validation; nil entries panic. It performs no network IO:
// call [Driver.Start] to dial RabbitMQ, declare the durable topology, and
// enable publisher confirms. [Driver.Stop] releases the
// connection.
//
// Example:
//
//	driver, err := rabbitmqqueue.New("amqp://guest:guest@localhost:5672/", map[string]rabbitmqqueue.Queue{
//		"default": {Name: "jobs", RoutingKey: "jobs"},
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func New(url string, queues map[string]Queue, configure ...BuilderFunc) (*Driver, error) {
	queues = maps.Clone(queues)
	config := configureDriver(configure...)
	config.Queues = queues

	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("%w: URL is required", ErrInvalidConfig)
	}

	config, err := normalizeConfig(config)

	if err != nil {
		return nil, err
	}

	driver := newDriver(config, nil)
	driver.url = url

	return driver, nil
}

// NewFrom returns a driver that uses connection. The caller retains ownership of
// connection: [Driver.Stop] closes only the driver's publish channel, not the
// supplied connection. The caller must keep it open for the driver's lifetime.
// queues is the required trusted routing map, with the same ownership and
// validation rules as New. It cannot be supplied through an optional callback.
// All builder settings apply as with [New]. Callbacks run once in order before
// validation; nil entries panic. Construction performs no network I/O; the
// topology is declared by [Driver.Start], which does not dial.
//
// NewFrom returns nil and an error wrapping [ErrInvalidConfig] for a nil
// connection, invalid configuration, or enabled SDK automatic recovery. Recovery
// reuses channel objects and delivery tags, allowing an old delivery to settle a
// different job after reconnection. Rejection neither closes connection nor
// changes its recovery configuration. The caller must not enable recovery or
// reconnect this connection in place while the driver uses it. Instead, cancel
// and wait for consumers, stop the driver, then create a new driver with [New]
// or a fresh non-recovering connection. Keep job handlers idempotent because
// reconnecting can still cause redelivery.
//
// Example:
//
//	driver, err := rabbitmqqueue.NewFrom(connection, map[string]rabbitmqqueue.Queue{
//		"emails": {Name: "app.emails", RoutingKey: "emails"},
//	})
//	if err != nil {
//		return err
//	}
//	return driver.Start(ctx)
func NewFrom(connection *amqp091.Connection, queues map[string]Queue, configure ...BuilderFunc) (*Driver, error) {
	queues = maps.Clone(queues)
	config := configureDriver(configure...)
	config.Queues = queues
	config, err := normalizeConfig(config)

	if err != nil {
		return nil, err
	}

	if connection == nil {
		return nil, fmt.Errorf("%w: nil connection", ErrInvalidConfig)
	}

	if connection.IsRecoveryEnabled() {
		return nil, fmt.Errorf("%w: connection automatic recovery is unsupported", ErrInvalidConfig)
	}

	return newDriver(config, connection), nil
}

// newDriver publishes detached routing and policy around an optional borrowed
// connection. Without a supplied connection, Start acquires one owned socket.
func newDriver(config settings, connection *amqp091.Connection) *Driver {
	return &Driver{
		connection:  connection,
		config:      config,
		owned:       connection == nil,
		logger:      driverLogger(config.Logger),
		publishGate: make(chan struct{}, 1),
	}
}

// Start dials RabbitMQ when the driver owns its connection, opens the publisher
// channel, declares the configured topology, and enables publisher confirms.
// Cancellation during owned setup closes the socket. Borrowed setup requires a
// responsive peer or caller-managed connection close; the SDK RPCs lack context.
// Serialize Start and Stop through the application lifecycle.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return fmt.Errorf("start job transport: %w", err)
//	}
func (driver *Driver) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.closed {
		return ErrDriverClosed
	}

	if driver.publisher != nil {
		return ErrAlreadyStarted
	}

	connection := driver.connection
	dialed := false

	if connection == nil {
		var err error
		connection, driver.abort, err = amqpconn.Dial(ctx, driver.url)

		if err != nil {
			return err
		}

		dialed = true
	}

	var publisher *amqp091.Channel
	err := amqpconn.Run(ctx, driver.abort, func() error {
		var err error
		publisher, err = openPublisher(connection, driver.config)

		return err
	})

	if err != nil {
		if dialed {
			// Startup already failed; closing the new connection is best-effort cleanup.
			_ = amqpconn.Close(ctx, connection, driver.abort)
		}

		return err
	}

	driver.connection = connection
	driver.publisher = publisher
	driver.returns = publisher.NotifyReturn(make(chan amqp091.Return, 1))

	return nil
}

// openPublisher opens the publisher channel, declares topology, and enables
// confirms.
func openPublisher(connection *amqp091.Connection, config settings) (*amqp091.Channel, error) {
	publisher, err := connection.Channel()

	if err != nil {
		return nil, fmt.Errorf("open AMQP publisher channel: %w", err)
	}

	if err := declareTopology(publisher, config); err != nil {
		// Preserve the topology failure while releasing the unusable channel.
		_ = publisher.Close()

		return nil, err
	}

	if err := publisher.Confirm(false); err != nil {
		// Preserve the confirmation setup failure while releasing the channel.
		_ = publisher.Close()

		return nil, fmt.Errorf("enable AMQP publisher confirms: %w", err)
	}

	return publisher, nil
}

// Dispatch serializes message as a [queue.Message] and persistently publishes
// it to the configured routing key. Message.Queue must identify a configured
// logical queue; only the trusted map selects physical broker destinations.
// A successful mandatory publish confirms acceptance by a queue, not processing
// or later TTL dead-letter forwarding. Cancellation can leave acceptance unknown.
//
// Example:
//
//	err := driver.Dispatch(ctx, queue.Message{ID: "job-1", Name: "email", Queue: "emails", Payload: []byte(`{}`)})
//	if err != nil {
//		return err
//	}
func (driver *Driver) Dispatch(ctx context.Context, message queue.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	consumer, err := driver.queue(message.Queue)

	if err != nil {
		return err
	}

	message.Attempts = 0
	body, err := json.Marshal(message)

	if err != nil {
		return fmt.Errorf("marshal job message: %w", err)
	}

	return driver.publish(ctx, driver.config.Exchange, consumer.RoutingKey, publishing(message.ID, body, 1))
}

// openConsumer owns setup until a complete delivery stream is available.
// Cancellation interrupts only an owned socket during blocking SDK RPCs;
// after setup, ordinary Consume cancellation closes just its consumer channel.
func (driver *Driver) openConsumer(ctx context.Context, name string) (*amqp091.Channel, <-chan amqp091.Delivery, error) {
	consumer, err := driver.queue(name)

	if err != nil {
		return nil, nil, err
	}

	driver.mu.Lock()
	abort := driver.abort
	driver.mu.Unlock()

	var channel *amqp091.Channel
	var deliveries <-chan amqp091.Delivery
	err = amqpconn.Run(ctx, abort, func() error {
		var err error
		channel, err = driver.consumerChannel()

		if err != nil {
			return err
		}

		deliveries, err = driver.consumerDeliveries(ctx, channel, consumer)

		if err != nil {
			_ = channel.Close() // Preserve the setup failure while releasing the unused channel.
		}

		return err
	})

	return channel, deliveries, err
}

// consumerDeliveries configures quality of service and starts delivery.
func (driver *Driver) consumerDeliveries(
	ctx context.Context,
	channel *amqp091.Channel,
	consumer Queue,
) (<-chan amqp091.Delivery, error) {
	if err := channel.Qos(driver.config.Prefetch, 0, false); err != nil {
		return nil, fmt.Errorf("set AMQP job QoS: %w", err)
	}

	// Cancellation is owned by consumeDeliveries. The SDK's context-triggered
	// consumer cancel can race Channel.Close and deadlock its cleanup.
	deliveries, err := channel.ConsumeWithContext(context.WithoutCancel(ctx), consumer.Name, "", false, false, false, false, nil)

	if err != nil {
		return nil, fmt.Errorf("consume AMQP job queue: %w", err)
	}

	return deliveries, nil
}

// consumeDeliveries dispatches messages until the delivery channel closes.
func (driver *Driver) consumeDeliveries(
	ctx context.Context,
	consumer string,
	deliveries <-chan amqp091.Delivery,
	handler queue.DeliveryHandler,
) error {
	for {
		received, err := receiveDelivery(ctx, deliveries)

		if err != nil {
			return err
		}

		if err := driver.handle(ctx, consumer, received, handler); err != nil {
			return err
		}
	}
}

// receiveDelivery owns the cancelable SDK-stream boundary. A closed stream is
// a transport failure unless cancellation already supplies the caller's reason.
func receiveDelivery(ctx context.Context, deliveries <-chan amqp091.Delivery) (amqp091.Delivery, error) {
	select {
	case <-ctx.Done():
		return amqp091.Delivery{}, ctx.Err()
	case received, open := <-deliveries:
		if !open {
			return amqp091.Delivery{}, cmp.Or(ctx.Err(), errors.New("AMQP job delivery channel closed"))
		}

		return received, nil
	}
}

// handle converts and delivers one AMQP message to handler.
func (driver *Driver) handle(ctx context.Context, queueName string, received amqp091.Delivery, handler queue.DeliveryHandler) error {
	message, err := deliveryMessage(queueName, received)
	delivery := &delivery{
		driver:   driver,
		received: received,
		message:  message,
	}

	if err != nil {
		delivery.message.ID = received.MessageId

		return delivery.Fail(ctx, err)
	}

	if err := handler(ctx, delivery); err != nil {
		driver.logger.ErrorContext(ctx, "AMQP job delivery handler failed", "error", err, "queue", queueName, "job_id", message.ID)

		return err
	}

	return nil
}

// driverLogger retains an explicit logger or resolves slog.Default() once at
// construction. It does not silence delivery failures when Logger is nil.
func driverLogger(value *slog.Logger) *slog.Logger {
	if value == nil {
		return slog.Default()
	}

	return value
}

// queue returns the configured broker topology for name.
func (driver *Driver) queue(name string) (Queue, error) {
	consumer, ok := driver.config.Queues[name]

	if !ok {
		return Queue{}, fmt.Errorf("%w: %s", ErrUnknownQueue, name)
	}

	return consumer, nil
}

// consumerChannel opens a consumer channel unless the driver is closed.
func (driver *Driver) consumerChannel() (*amqp091.Channel, error) {
	driver.mu.Lock()

	if driver.closed {
		driver.mu.Unlock()

		return nil, ErrDriverClosed
	}

	if driver.publisher == nil {
		driver.mu.Unlock()

		return nil, ErrNotStarted
	}

	connection := driver.connection
	driver.mu.Unlock()

	channel, err := connection.Channel()

	if err != nil {
		return nil, fmt.Errorf("open AMQP consumer channel: %w", err)
	}

	return channel, nil
}

// publish pairs a mandatory return with its confirmation. A canceled wait stays
// pending until the next caller drains it, preserving correlation without
// changing wire identities. No lifecycle mutex is held during broker I/O.
func (driver *Driver) publish(ctx context.Context, exchange, routingKey string, message amqp091.Publishing) error {
	select {
	case driver.publishGate <- struct{}{}:
		defer func() { <-driver.publishGate }()
	case <-ctx.Done():
		return ctx.Err()
	}

	if driver.pending != nil {
		if _, err := driver.pending.WaitContext(ctx); err != nil {
			return err
		}

		driver.drainReturn()
		driver.pending = nil
	}

	confirmation, err := driver.send(ctx, exchange, routingKey, message)

	if err != nil {
		return err
	}

	driver.pending = confirmation
	acked, err := confirmation.WaitContext(ctx)

	if err != nil {
		return err
	}

	driver.pending = nil
	returned := driver.drainReturn()

	if !acked || returned {
		return ErrPublishNotConfirmed
	}

	return nil
}

// drainReturn consumes the outstanding publication's basic.return, if any.
// RabbitMQ sends it before basic.ack; the SDK dispatches returns synchronously
// before completing deferred confirmations, so no extra timing wait is needed.
func (driver *Driver) drainReturn() bool {
	select {
	case _, open := <-driver.returns:
		return open
	default:
		return false
	}
}

// send writes one mandatory publication while publishGate is held. Context
// cancellation interrupts an owned socket during a blocked write; confirmation
// waiting occurs separately so a canceled wait need not destroy the connection.
func (driver *Driver) send(ctx context.Context, exchange, routingKey string, message amqp091.Publishing) (*amqp091.DeferredConfirmation, error) {
	driver.mu.Lock()

	if driver.closed {
		driver.mu.Unlock()

		return nil, ErrDriverClosed
	}

	if driver.publisher == nil {
		driver.mu.Unlock()

		return nil, ErrNotStarted
	}

	publisher := driver.publisher
	abort := driver.abort
	driver.mu.Unlock()

	var confirmation *amqp091.DeferredConfirmation
	err := amqpconn.Run(ctx, abort, func() error {
		var err error
		confirmation, err = publisher.PublishWithDeferredConfirmWithContext(ctx, exchange, routingKey, true, false, message)

		return err
	})

	if err != nil {
		return nil, fmt.Errorf("publish AMQP job: %w", err)
	}

	return confirmation, nil
}

// Ping opens and closes a fresh channel without publishing data. Cancellation
// interrupts an owned connection, including its other channels. With NewFrom,
// the caller must bound native RPCs through transport deadlines or connection
// close; this method never closes a borrowed connection to enforce ctx.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return fmt.Errorf("RabbitMQ readiness: %w", err)
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	driver.mu.Lock()
	abort := driver.abort
	driver.mu.Unlock()

	return amqpconn.Run(ctx, abort, func() error {
		channel, err := driver.consumerChannel()

		if err != nil {
			return err
		}

		return channel.Close()
	})
}

// Stop prevents further operations and releases the owned connection directly,
// without first waiting for a publisher-channel close or a pending confirmation.
// Graceful connection close permits at most five seconds or ctx's earlier end;
// cancellation, including an already-expired ctx, still closes the owned socket
// and returns the context error. It does not wait for job handlers.
// NewFrom closes only the publisher channel; the caller must bound an
// unresponsive borrowed channel-close RPC. Cancel and join consumers first.
// A second Stop returns ErrDriverClosed. Serialize Stop with Start.
//
// Example:
//
//	if err := driver.Stop(ctx); err != nil {
//		return fmt.Errorf("release RabbitMQ jobs: %w", err)
//	}
func (driver *Driver) Stop(ctx context.Context) error {
	driver.mu.Lock()

	if driver.closed {
		driver.mu.Unlock()

		return ErrDriverClosed
	}

	driver.closed = true
	publisher := driver.publisher
	connection := driver.connection
	owned := driver.owned
	abort := driver.abort
	driver.mu.Unlock()

	if owned && connection != nil {
		return amqpconn.Close(ctx, connection, abort)
	}

	if publisher != nil {
		return publisher.Close()
	}

	return nil
}

// normalizeConfig applies defaults and validates durable AMQP topology settings.
func normalizeConfig(config settings) (settings, error) {
	return normalizeTopologyConfig(defaultAMQPConfig(config))
}

// defaultAMQPConfig applies default exchange, prefetch, and retry delay values.
func defaultAMQPConfig(config settings) settings {
	if config.Exchange == "" {
		config.Exchange = DefaultExchange
	}

	if config.Prefetch == 0 {
		config.Prefetch = defaultPrefetch
	}

	if config.RetryDelay == 0 {
		config.RetryDelay = defaultRetryDelay
	}

	return config
}

// normalizeTopologyConfig validates topology settings before queue normalization.
func normalizeTopologyConfig(config settings) (settings, error) {
	if !validAMQPTopology(config) {
		return emptyDriverConfig(), fmt.Errorf("%w: exchange and queues are required", ErrInvalidConfig)
	}

	if config.Prefetch < 1 || config.Prefetch > math.MaxUint16 {
		return emptyDriverConfig(), fmt.Errorf("%w: prefetch must be between 1 and 65535", ErrInvalidConfig)
	}

	return normalizeRetryConfig(config)
}

// validAMQPTopology reports whether exchange and configured queues are present.
func validAMQPTopology(config settings) bool {
	if strings.TrimSpace(config.Exchange) == "" {
		return false
	}

	return len(config.Queues) > 0
}

// normalizeRetryConfig validates retry settings before copying queue configuration.
func normalizeRetryConfig(config settings) (settings, error) {
	if !validRetryDelay(config.RetryDelay) {
		return emptyDriverConfig(), fmt.Errorf("%w: invalid retry delay", ErrInvalidConfig)
	}

	if !validMaxRetryDelay(config.MaxRetryDelay) {
		return emptyDriverConfig(), fmt.Errorf("%w: invalid maximum retry delay", ErrInvalidConfig)
	}

	if config.MaxRetryDelay > 0 && config.RetryDelay > config.MaxRetryDelay {
		return emptyDriverConfig(), fmt.Errorf("%w: retry delay exceeds maximum retry delay", ErrInvalidConfig)
	}

	return normalizeAMQPQueues(config)
}

// validRetryDelay reports whether delay fits AMQP message expiration limits.
func validRetryDelay(delay time.Duration) bool {
	if delay < 0 {
		return false
	}

	return retryMilliseconds(delay) <= math.MaxUint32
}

// validMaxRetryDelay reports whether a configured maximum delay is representable.
func validMaxRetryDelay(delay time.Duration) bool {
	if delay < 0 {
		return false
	}

	if delay == 0 {
		return true
	}

	return retryMilliseconds(delay) <= math.MaxUint32
}

// normalizeAMQPQueues validates and copies queue configuration.
func normalizeAMQPQueues(config settings) (settings, error) {
	queues, err := copyAMQPQueues(config.Queues)

	if err != nil {
		return emptyDriverConfig(), err
	}

	config.Queues = queues

	for _, consumer := range queues {
		if consumer.Name == config.FailureQueue {
			return emptyDriverConfig(), fmt.Errorf("%w: failure queue must not be a work queue", ErrInvalidConfig)
		}
	}

	return config, nil
}

// copyAMQPQueues returns an independent copy of valid queue configuration.
func copyAMQPQueues(source map[string]Queue) (map[string]Queue, error) {
	queues := make(map[string]Queue, len(source))
	names := make(map[string]bool, len(source))
	keys := make(map[string]bool, len(source))

	for logical, consumer := range source {
		if !validAMQPQueue(logical, consumer) {
			return nil, fmt.Errorf("%w: logical queue, queue name, and routing key are required", ErrInvalidConfig)
		}

		if names[consumer.Name] || keys[consumer.RoutingKey] {
			return nil, fmt.Errorf("%w: queue names and routing keys must be unique", ErrInvalidConfig)
		}

		names[consumer.Name] = true
		keys[consumer.RoutingKey] = true
		queues[logical] = consumer
	}

	return queues, nil
}

// validAMQPQueue reports whether every queue identifier is non-blank.
func validAMQPQueue(logical string, consumer Queue) bool {
	if strings.TrimSpace(logical) == "" {
		return false
	}

	if strings.TrimSpace(consumer.Name) == "" {
		return false
	}

	return strings.TrimSpace(consumer.RoutingKey) != ""
}

// emptyDriverConfig returns an intentionally empty configuration after failure.
func emptyDriverConfig() settings {
	return settings{}
}

// declareTopology declares the exchange, queues, and bindings in config.
func declareTopology(channel *amqp091.Channel, config settings) error {
	if err := declareAMQPJobExchange(channel, config.Exchange); err != nil {
		return err
	}

	if err := declareAMQPJobQueues(channel, config); err != nil {
		return err
	}

	return declareAMQPFailureQueue(channel, config.FailureQueue)
}

// declareAMQPJobExchange declares the durable direct job exchange.
func declareAMQPJobExchange(channel *amqp091.Channel, exchange string) error {
	return topology.Exchange(channel, exchange)
}

// declareAMQPJobQueues declares and binds every configured job queue.
func declareAMQPJobQueues(channel *amqp091.Channel, config settings) error {
	for _, consumer := range config.Queues {
		if err := declareAMQPJobQueue(channel, config.Exchange, consumer); err != nil {
			return err
		}
	}

	return nil
}

// declareAMQPJobQueue declares and binds one durable job queue.
func declareAMQPJobQueue(channel *amqp091.Channel, exchange string, consumer Queue) error {
	return topology.Queue(channel, exchange, consumer.Name, consumer.RoutingKey)
}

// declareAMQPFailureQueue declares the optional durable failure queue.
func declareAMQPFailureQueue(channel *amqp091.Channel, failureQueue string) error {
	return topology.Failure(channel, failureQueue)
}

// deliveryMessage decodes received and supplies broker-controlled job metadata.
func deliveryMessage(queueName string, received amqp091.Delivery) (queue.Message, error) {
	var message queue.Message

	if err := json.Unmarshal(received.Body, &message); err != nil {
		return queue.Message{
			Queue: queueName,
		}, fmt.Errorf("%w: unmarshal job message: %w", ErrInvalidDelivery, err)
	}

	message.Queue = queueName
	message.ID = cmp.Or(received.MessageId, message.ID)
	message.Attempts = attempts(received.Headers, received.Redelivered)

	return message, nil
}

// attempts derives the current attempt count from AMQP delivery metadata.
// Quorum queues count every redelivery, including after a worker crash, in
// x-delivery-count; classic queues only flag a redelivery, which counts once.
func attempts(headers amqp091.Table, redelivered bool) int {
	attempt := max(headerAttempt(headers), 1)

	if count := deliveryCount(headers); count > 0 {
		return attempt + count
	}

	if redelivered {
		attempt++
	}

	return attempt
}

// deliveryCount reads the quorum-queue x-delivery-count header.
func deliveryCount(headers amqp091.Table) int {
	switch count := headers["x-delivery-count"].(type) {
	case int32:
		return int(count)
	case int64:
		return int64Attempt(count)
	}

	return 0
}

// headerAttempt reads a valid positive attempt count from headers.
func headerAttempt(headers amqp091.Table) int {
	value, ok := headers[attemptHeader]

	if !ok {
		return 0
	}

	switch attempt := value.(type) {
	case int32:
		return int(attempt)
	case int64:
		return int64Attempt(attempt)
	case string:
		return stringAttempt(attempt)
	}

	return 0
}

// int64Attempt converts a valid AMQP int64 attempt count.
func int64Attempt(value int64) int {
	if value <= 0 {
		return 0
	}

	if value > math.MaxInt {
		return 0
	}

	return int(value)
}

// stringAttempt converts a valid AMQP string attempt count.
func stringAttempt(value string) int {
	parsed, err := strconv.Atoi(value)

	if err != nil {
		return 0
	}

	if parsed <= 0 {
		return 0
	}

	return parsed
}

// publishing creates a persistent AMQP message for an initial job attempt.
func publishing(id string, body []byte, attempt int) amqp091.Publishing {
	return amqp091.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp091.Persistent,
		MessageId:    id,
		Headers: amqp091.Table{
			attemptHeader: int32(attempt),
		},
		Body: slices.Clone(body),
	}
}

// delivery adapts one AMQP message to the [queue.Delivery] settlement contract.
type delivery struct {
	// driver publishes retries and failures using the configured topology.
	driver *Driver
	// received retains native acknowledgement and delivery metadata.
	received amqp091.Delivery
	// message is the decoded portable envelope returned by Message.
	message queue.Message
	// state owns settlement admission independently of publisher state.
	state receipt.State
}

// Message returns the portable envelope with independent payload and metadata
// containers. Handler edits cannot change a later retry or failure archive.
func (delivery *delivery) Message() queue.Message {
	message := delivery.message
	message.Payload = slices.Clone(message.Payload)
	message.Metadata = maps.Clone(message.Metadata)

	return message
}

// Acknowledge confirms successful processing of the delivery.
func (delivery *delivery) Acknowledge(ctx context.Context) error {
	return delivery.settle(ctx, func() error {
		return delivery.received.Ack(false)
	})
}

// Retry requeues the delivery immediately or through a durable delayed retry queue.
func (delivery *delivery) Retry(ctx context.Context, delay time.Duration) error {
	delay = delivery.retryDelay(delay)

	if !delivery.validRetryDelay(delay) {
		return fmt.Errorf("%w: %s", ErrUnsupportedRetryDelay, delay)
	}

	if delay == 0 {
		return delivery.settle(ctx, delivery.requeue)
	}

	return delivery.settle(ctx, func() error {
		return delivery.delayedRetryAndAcknowledge(ctx, delay)
	})
}

// retryDelay replaces an omitted delay with the driver default.
func (delivery *delivery) retryDelay(delay time.Duration) time.Duration {
	if delay == 0 {
		return delivery.driver.config.RetryDelay
	}

	return delay
}

// validRetryDelay reports whether delay satisfies configured and AMQP limits.
func (delivery *delivery) validRetryDelay(delay time.Duration) bool {
	if delay < 0 {
		return false
	}

	if delivery.exceedsMaximumRetryDelay(delay) {
		return false
	}

	return retryMilliseconds(delay) <= math.MaxUint32
}

// exceedsMaximumRetryDelay reports whether delay exceeds the configured maximum.
func (delivery *delivery) exceedsMaximumRetryDelay(delay time.Duration) bool {
	if delivery.driver.config.MaxRetryDelay == 0 {
		return false
	}

	return delay > delivery.driver.config.MaxRetryDelay
}

// requeue negatively acknowledges the current delivery for immediate redelivery.
func (delivery *delivery) requeue() error {
	return delivery.received.Nack(false, true)
}

// delayedRetryAndAcknowledge publishes a retry before acknowledging its source.
func (delivery *delivery) delayedRetryAndAcknowledge(ctx context.Context, delay time.Duration) error {
	return receipt.Transfer(func() error {
		return delivery.delayedRetry(ctx, delay)
	}, func() error {
		return delivery.received.Ack(false)
	})
}

// Reject terminally rejects the delivery without requeueing it.
func (delivery *delivery) Reject(ctx context.Context) error {
	return delivery.settle(ctx, func() error {
		return delivery.received.Reject(false)
	})
}

// Fail records the failure when configured, then terminally settles the delivery.
func (delivery *delivery) Fail(ctx context.Context, err error) error {
	return delivery.settle(ctx, func() error {
		if delivery.driver.config.FailureQueue == "" {
			return delivery.received.Reject(false)
		}

		body, marshalErr := json.Marshal(failureMessage{
			Message:    delivery.message,
			Error:      errorText(err),
			RawMessage: string(delivery.received.Body),
		})

		if marshalErr != nil {
			return fmt.Errorf("marshal AMQP job failure: %w", marshalErr)
		}

		return receipt.Transfer(func() error {
			if publishErr := delivery.driver.publish(ctx, "", delivery.driver.config.FailureQueue, publishing(delivery.message.ID, body, delivery.message.Attempts)); publishErr != nil {
				return fmt.Errorf("publish AMQP job failure: %w", publishErr)
			}

			return nil
		}, func() error {
			return delivery.received.Ack(false)
		})
	})
}

// delayedRetry publishes the next attempt to a durable TTL queue, checking both
// mandatory routing and confirmation. Its caller acknowledges the original only
// after success. This protects the client-side handoff, not subsequent broker
// dead-letter forwarding; see the package's conditional durability requirements.
func (delivery *delivery) delayedRetry(ctx context.Context, delay time.Duration) error {
	consumer, err := delivery.driver.queue(delivery.message.Queue)

	if err != nil {
		return err
	}

	return delivery.driver.publishDelayed(ctx, consumer, delivery.message, delay)
}

// settle executes one delivery settlement operation at most once.
func (delivery *delivery) settle(ctx context.Context, operation func() error) error {
	return delivery.state.Settle(ctx, ErrDeliverySettled, operation)
}

// retryQueue names a TTL retry queue and carries its declaration arguments.
type retryQueue struct {
	// name identifies the durable queue for one rounded retry delay.
	name string
	// arguments route expired messages back to the work exchange.
	arguments amqp091.Table
}

// publishDelayed declares a retry queue and publishes the next delayed attempt.
// delay is rounded up with [retryBucket] so the set of retry queues stays small.
func (driver *Driver) publishDelayed(ctx context.Context, consumer Queue, message queue.Message, delay time.Duration) error {
	delay = retryBucket(delay)
	body, err := json.Marshal(message)

	if err != nil {
		return fmt.Errorf("marshal retry job message: %w", err)
	}

	retry := &retryQueue{
		name:      retryQueueName(consumer.Name, delay),
		arguments: retryQueueArguments(driver.config.Exchange, consumer.RoutingKey, delay),
	}

	if err := driver.declareRetry(ctx, retry); err != nil {
		return err
	}

	return driver.publish(ctx, "", retry.name, delayedPublishing(message.ID, body, message.Attempts+1, delay))
}

// declareRetry serializes retry topology RPCs with the publishing channel's
// other operations. Its gate can be acquired with a canceled context without
// waiting for an unrelated publisher; identities and declaration arguments stay
// unchanged from existing retry queues.
func (driver *Driver) declareRetry(ctx context.Context, retry *retryQueue) error {
	select {
	case driver.publishGate <- struct{}{}:
		defer func() { <-driver.publishGate }()
	case <-ctx.Done():
		return ctx.Err()
	}

	driver.mu.Lock()
	publisher := driver.publisher
	abort := driver.abort
	closed := driver.closed
	driver.mu.Unlock()

	if closed {
		return ErrDriverClosed
	}

	if publisher == nil {
		return ErrNotStarted
	}

	return amqpconn.Run(ctx, abort, func() error {
		_, err := publisher.QueueDeclare(retry.name, true, false, false, false, retry.arguments)

		return err
	})
}

// retryBucket rounds delay up to a bucket boundary: whole seconds below a
// minute, 15 seconds below an hour, 5 minutes below a day, and hours beyond,
// capped at the AMQP TTL limit. Each distinct delay gets its own broker queue,
// so bucketing bounds the queue count even for jittered backoff functions.
// Rounding is always upward because a retry delay is a lower bound.
func retryBucket(delay time.Duration) time.Duration {
	return topology.Bucket(delay)
}

// retryQueueName returns the durable retry queue name for delay.
func retryQueueName(consumer string, delay time.Duration) string {
	return topology.Name(consumer, delay)
}

// retryQueueArguments returns dead-letter settings that route expired retries
// to work. Each distinct delay gets its own queue, so x-expires deletes queues
// that stop being used instead of letting them accumulate.
func retryQueueArguments(exchange, routingKey string, delay time.Duration) amqp091.Table {
	return topology.Arguments(exchange, routingKey, delay)
}

// delayedPublishing creates an AMQP message that expires after delay. It
// starts from a fresh [publishing] and so never carries over headers that the
// broker added to the received delivery (x-delivery-count, x-death); queue
// metadata and trace propagation travel in the message body, not in headers.
func delayedPublishing(id string, body []byte, attempt int, delay time.Duration) amqp091.Publishing {
	message := publishing(id, body, attempt)
	message.Expiration = strconv.FormatInt(retryMilliseconds(delay), 10)

	return message
}

// retryMilliseconds rounds delay up to an AMQP-compatible TTL in milliseconds.
func retryMilliseconds(delay time.Duration) int64 {
	return topology.Milliseconds(delay)
}

// failureMessage is the durable envelope sent to a configured failure queue.
type failureMessage struct {
	// Message preserves the portable envelope when decoding succeeded.
	Message queue.Message `json:"message"`
	// Error records the handler or decoding failure for inspection.
	Error string `json:"error"`
	// RawMessage retains the original body, including for successfully decoded
	// envelopes. Failure archives therefore contain application data twice.
	RawMessage string `json:"raw_message,omitzero"`
}

// errorText returns a stable failure description for a job error.
func errorText(err error) string {
	if err == nil {
		return "job failed"
	}

	return err.Error()
}

// MessagingSystem returns "rabbitmq", the messaging.system telemetry value that
// the queue and broadcast facades report for this driver.
func (driver *Driver) MessagingSystem() string {
	return "rabbitmq"
}
