package rabbitmqbroadcast

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/driver/rabbitmq/internal/amqpconn"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

// rawHandler receives a complete native body without envelope validation.
// Driver.Publish encodes that body as a JSON broadcast.Message, not only Payload.
type rawHandler = func([]byte)

// ErrSubscriptionClosed reports that the broker closed a subscription, for
// example because the connection dropped. Consume returns it so the caller
// can reconnect instead of waiting forever on a dead subscription.
var ErrSubscriptionClosed = errors.New("AMQP subscription closed")

// ErrInvalidConfig reports that [New] received an invalid argument, such as a
// blank connection URL. Test for it with [errors.Is].
var ErrInvalidConfig = errors.New("invalid AMQP broadcast driver configuration")

// ErrNotStarted reports that the driver was used before Start.
var ErrNotStarted = errors.New("AMQP broadcast driver is not started")

// ErrAlreadyStarted reports that Start was called more than once.
var ErrAlreadyStarted = errors.New("AMQP broadcast driver is already started")

// Driver implements [broadcast.PublisherDriver] and
// [broadcast.ConsumerDriver] using RabbitMQ's
// AMQP protocol for publish/subscribe messaging. It uses a topic
// exchange to broadcast events to all subscribed consumers, with
// each subscriber receiving messages in their own exclusive queue.
//
// The broker maintains a single connection with one channel for
// publishing and creates individual channels for each subscriber,
// following RabbitMQ best practices for concurrent access.
//
// Wildcard patterns: '*' matches a single dot-separated word and '#'
// matches zero or more words, following AMQP topic exchange semantics.
// Queues are exclusive, ephemeral, and automatically acknowledged; publication
// confirms broker acceptance, not subscriber processing or offline retention.
//
// Example:
//
//	driver, err := rabbitmqbroadcast.New(url)
//	if err != nil {
//		return err
//	}
//	_ = broadcast.NewPublisher(driver)
type Driver struct {
	// conn is the shared AMQP connection used for all operations.
	conn *amqp091.Connection

	// pubCh is the dedicated channel for publishing messages.
	pubCh *amqp091.Channel

	// exchange is the name of the topic exchange where events are published.
	exchange string

	// url is the connection string [New] dials; empty when the connection is
	// caller-owned.
	url string

	// owned reports whether Stop must close conn.
	owned bool
	// abort closes the owned socket without a broker RPC; nil for NewFrom.
	abort func() error

	// config holds the settings used by Start to connect.
	config settings

	// mu protects conn, pubCh, and concurrent use of the publish channel.
	mu sync.Mutex
	// publishGate serializes publisher-channel use with cancelable admission,
	// separately from lifecycle state and Stop's connection release.
	publishGate chan struct{}
	// closed rejects further operations once Stop has released the driver.
	closed bool

	// logger records handler errors and recovered panics without attaching bodies.
	logger *slog.Logger
}

var _ broadcast.PublisherDriver = (*Driver)(nil)
var _ broadcast.ConsumerDriver = (*Driver)(nil)

// Connection returns the shared native connection without transferring ownership.
// New returns nil until successful Start; NewFrom exposes the supplied connection
// immediately. The pointer remains available after Stop; an owned connection is
// then closed, while a borrowed connection still belongs to its caller.
// Native operations bypass the driver's envelope and lifecycle protections.
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

	return driver.conn
}

// settings configures the optional settings of a new Driver. The AMQP
// connection URL is not a field: it is the required first argument of [New].
type settings struct {
	// Exchange is the name of the topic exchange to use for events.
	// If empty, DefaultAMQPExchange is used.
	Exchange string

	// Logger records handler errors and recovered panics. Nil uses slog.Default().
	Logger *slog.Logger
}

// DefaultAMQPExchange is the default name for the topic exchange
// used by Driver when no custom exchange is specified.
const DefaultAMQPExchange = "arkoren:events"

// defaultConfig returns the default AMQP broker configuration.
func defaultConfig() settings {
	return settings{
		Exchange: DefaultAMQPExchange,
	}
}

// New returns a Driver that owns its future connection. url is the AMQP
// connection string in the format amqp://username:password@host:port/vhost.
// It has no safe default, so it is a required positional argument rather than
// a [settings] field.
//
// WARNING: url typically contains credentials and is kept as a plain string
// for the lifetime of the driver. Use TLS (amqps://) to protect them in
// transit, load the URL from the environment or a secret manager rather than
// hard-coding it, and prefer short-lived credentials where the broker
// supports them.
//
// New returns an error wrapping [ErrInvalidConfig] that names URL when url is
// blank. It performs no network IO: call [Driver.Start] to dial RabbitMQ,
// open the publish channel, and declare the exchange. If no exchange name is
// specified, DefaultAMQPExchange is used.
//
// Example:
//
//	driver, err := rabbitmqbroadcast.New("amqp://guest:guest@localhost:5672/", func(broker *rabbitmqbroadcast.Builder) {
//		broker.Exchange("myapp:events")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver.Connection()
func New(url string, configure ...BuilderFunc) (*Driver, error) {
	config := configureDriver(configure...)

	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("%w: URL is required", ErrInvalidConfig)
	}

	config.Exchange = amqpExchange(config)

	driver := newDriver(nil, config)
	driver.url = url

	return driver, nil
}

// NewFrom returns a Driver that uses an existing AMQP connection. Start opens
// the publish channel and declares the topic exchange, which is idempotent when
// the exchange already exists with matching configuration. The caller retains
// ownership of conn.
// Callbacks run once in order before validation; nil entries panic.
//
// Example:
//
//	driver, err := rabbitmqbroadcast.NewFrom(conn, func(broker *rabbitmqbroadcast.Builder) {
//		broker.Exchange("billing.events")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver.Connection()
func NewFrom(conn *amqp091.Connection, configure ...BuilderFunc) (*Driver, error) {
	config := configureDriver(configure...)

	if conn == nil {
		return nil, errors.New("nil AMQP connection")
	}

	return newDriver(conn, config), nil
}

// newDriver publishes detached policy around an optional borrowed connection.
// Without a supplied connection, Start acquires one owned socket.
func newDriver(conn *amqp091.Connection, config settings) *Driver {
	return &Driver{
		conn:        conn,
		exchange:    amqpExchange(config),
		owned:       conn == nil,
		config:      config,
		logger:      brokerLogger(config.Logger),
		publishGate: make(chan struct{}, 1),
	}
}

// Start dials RabbitMQ when the driver owns its connection, opens the
// publisher channel in confirm mode, and declares the event exchange.
// Cancellation covers owned TLS/AMQP negotiation and topology RPCs by closing
// the socket. Borrowed RPCs require a responsive peer or caller-managed close.
// Serialize Start and Stop through the application lifecycle.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return fmt.Errorf("start broadcasts: %w", err)
//	}
func (driver *Driver) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.closed {
		return ErrNotStarted
	}

	if driver.pubCh != nil {
		return ErrAlreadyStarted
	}

	conn := driver.conn
	dialed := false

	if conn == nil {
		var err error
		conn, driver.abort, err = amqpconn.Dial(ctx, driver.url)

		if err != nil {
			return err
		}

		dialed = true
	}

	var pubCh *amqp091.Channel
	err := amqpconn.Run(ctx, driver.abort, func() error {
		var err error
		pubCh, err = openPublisherChannel(conn, driver.exchange)

		return err
	})

	if err != nil {
		if dialed {
			// Startup already failed; closing the new connection is best-effort cleanup.
			_ = amqpconn.Close(ctx, conn, driver.abort)
		}

		return err
	}

	driver.conn = conn
	driver.pubCh = pubCh

	return nil
}

// connection returns the AMQP connection or ErrNotStarted.
func (driver *Driver) connection() (*amqp091.Connection, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.closed || driver.pubCh == nil {
		return nil, ErrNotStarted
	}

	return driver.conn, nil
}

// amqpExchange returns the configured exchange or the default exchange.
func amqpExchange(config settings) string {
	if config.Exchange == "" {
		return DefaultAMQPExchange
	}

	return config.Exchange
}

// openPublisherChannel opens a channel and declares the event exchange.
func openPublisherChannel(conn *amqp091.Connection, exchange string) (*amqp091.Channel, error) {
	pubCh, err := conn.Channel()

	if err != nil {
		return nil, err
	}

	// Confirm mode makes Publish report whether the broker accepted the message.
	if err := pubCh.Confirm(false); err != nil {
		// Preserve the confirmation setup failure while releasing the channel.
		_ = pubCh.Close()

		return nil, err
	}

	if err := declareAMQPExchange(pubCh, exchange); err != nil {
		// Close is best-effort: the connection is being abandoned due to
		// the exchange declaration failure above.
		_ = pubCh.Close()

		return nil, err
	}

	return pubCh, nil
}

// declareAMQPExchange declares the durable topic exchange used for events.
func declareAMQPExchange(channel *amqp091.Channel, exchange string) error {
	return channel.ExchangeDeclare(exchange, "topic", true, false, false, false, nil)
}

// Publish JSON-encodes the complete broadcast envelope and waits for broker
// confirmation. It does not require a subscriber: no active binding is a valid
// fan-out outcome, unlike a work-queue dispatch. Cancellation can leave broker
// acceptance unknown. Subscriber processing and persistence are not confirmed.
//
// Example:
//
//	err := driver.Publish(ctx, broadcast.Message{Name: "orders.created", Payload: []byte(`{}`)})
//	if err != nil {
//		return err
//	}
func (driver *Driver) Publish(ctx context.Context, message broadcast.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := broadcast.ValidateName(message.Name); err != nil {
		return err
	}

	payload, err := json.Marshal(message)

	if err != nil {
		return err
	}

	select {
	case driver.publishGate <- struct{}{}:
		defer func() { <-driver.publishGate }()
	case <-ctx.Done():
		return ctx.Err()
	}

	if _, err := driver.connection(); err != nil {
		return err
	}

	err = driver.publish(ctx, message.Name, payload)

	if err == nil {
		return nil
	}

	return driver.reopenAndPublish(ctx, message.Name, payload, err)
}

// publish sends payload through the current publisher channel and waits for
// the broker's confirmation.
func (driver *Driver) publish(ctx context.Context, event string, payload []byte) error {
	var confirmation *amqp091.DeferredConfirmation
	err := amqpconn.Run(ctx, driver.abort, func() error {
		var err error
		confirmation, err = driver.pubCh.PublishWithDeferredConfirmWithContext(
			ctx, driver.exchange, event, false, false, eventPublishing(payload),
		)

		return err
	})

	if err != nil {
		return err
	}

	acked, err := confirmation.WaitContext(ctx)

	if err != nil {
		return err
	}

	if !acked {
		return errors.New("AMQP broker rejected the message")
	}

	return nil
}

// eventPublishing creates an AMQP event payload.
func eventPublishing(payload []byte) amqp091.Publishing {
	return amqp091.Publishing{
		ContentType: "application/json",
		Body:        payload,
	}
}

// reopenAndPublish replaces the failed publisher channel and retries the event.
func (driver *Driver) reopenAndPublish(
	ctx context.Context,
	event string,
	payload []byte,
	publishErr error,
) error {
	// A canceled context is not a channel failure; reopening would only
	// leak another channel.
	if ctx.Err() != nil {
		return publishErr
	}

	var channel *amqp091.Channel
	err := amqpconn.Run(ctx, driver.abort, func() error {
		var err error
		channel, err = openPublisherChannel(driver.conn, driver.exchange)

		return err
	})

	if err != nil {
		return errors.Join(publishErr, err)
	}

	driver.mu.Lock()

	if driver.closed {
		driver.mu.Unlock()
		_ = channel.Close() // Stop won the race with publisher replacement.

		return ErrNotStarted
	}

	previous := driver.pubCh
	driver.pubCh = channel
	driver.mu.Unlock()

	// The failed channel is being replaced; closing it is best-effort cleanup.
	_ = amqpconn.Run(ctx, driver.abort, previous.Close)

	return driver.publish(ctx, event, payload)
}

// Subscribe registers a handler to receive events with the given
// name. Each subscription creates its own exclusive, auto-delete
// queue that is bound to the topic exchange with the event name
// as the routing key. Messages are automatically acknowledged.
//
// The handler receives messages in a separate goroutine and will
// continue processing until the context is canceled or the
// returned unsubscribe function is called. The handler receives the complete
// JSON-encoded broadcast.Message, not just its application Payload. No envelope
// validation is performed here; use broadcast.NewConsumer for validated typed
// decoding. A nil handler is programmer misuse when delivery starts.
//
// Setup failure returns a nil unsubscribe function and the error. Unsubscribe
// waits for the current raw callback; it cannot cancel application code blocked
// inside that callback. A borrowed channel-close RPC requires a responsive peer.
//
// Example:
//
//	unsubscribe, err := driver.Subscribe(ctx, "orders.*", func(body []byte) {
//		var envelope broadcast.Message
//		if err := json.Unmarshal(body, &envelope); err != nil {
//			return
//		}
//		_ = envelope.Name
//	})
//	if err != nil {
//		return err
//	}
//	return unsubscribe()
func (driver *Driver) Subscribe(
	ctx context.Context,
	event string,
	handler rawHandler,
) (func() error, error) {
	if err := broadcast.ValidatePattern(event); err != nil {
		return nil, err
	}

	channel, deliveries, err := driver.subscribe(ctx, ctx, event)

	if err != nil {
		return nil, err
	}

	return driver.subscription(channel, deliveries, event, handler), nil
}

// subscribe configures an exclusive subscription under setup cancellation.
// lifetime separately controls SDK consumer cancellation: Consume owns its
// teardown, whereas raw Subscribe retains the caller's context-bound consumer.
func (driver *Driver) subscribe(ctx, lifetime context.Context, event string) (*amqp091.Channel, <-chan amqp091.Delivery, error) {
	conn, err := driver.connection()

	if err != nil {
		return nil, nil, err
	}

	var channel *amqp091.Channel
	var deliveries <-chan amqp091.Delivery
	err = amqpconn.Run(ctx, driver.abort, func() error {
		var err error
		channel, err = conn.Channel()

		if err != nil {
			return err
		}

		queue, err := driver.subscriptionQueue(channel, event)

		if err != nil {
			return err
		}

		deliveries, err = consumeAMQPSubscription(lifetime, channel, queue)

		if err != nil {
			_ = channel.Close() // Preserve the setup error while releasing the unused channel.
		}

		return err
	})

	return channel, deliveries, err
}

// subscriptionQueue declares and binds an exclusive queue for event.
func (driver *Driver) subscriptionQueue(channel *amqp091.Channel, event string) (string, error) {
	queue, err := channel.QueueDeclare("", false, true, true, false, nil)

	if err != nil {
		// Close is best-effort: the channel is being abandoned due to
		// the queue declaration failure above.
		_ = channel.Close()

		return "", err
	}

	if err := channel.QueueBind(queue.Name, event, driver.exchange, false, nil); err != nil {
		// Close is best-effort: the channel is being abandoned due to
		// the queue bind failure above.
		_ = channel.Close()

		return "", err
	}

	return queue.Name, nil
}

// consumeAMQPSubscription starts an auto-acknowledging exclusive consumer.
func consumeAMQPSubscription(
	ctx context.Context,
	channel *amqp091.Channel,
	queue string,
) (<-chan amqp091.Delivery, error) {
	return channel.ConsumeWithContext(ctx, queue, "", true, true, false, false, nil)
}

// subscription starts delivery processing and returns its unsubscribe function.
func (driver *Driver) subscription(
	channel *amqp091.Channel,
	deliveries <-chan amqp091.Delivery,
	event string,
	handler rawHandler,
) func() error {
	var group sync.WaitGroup
	group.Go(func() {
		driver.receive(deliveries, event, handler)
	})

	return func() error {
		defer group.Wait()

		return channel.Close()
	}
}

// receive passes each AMQP delivery to handler.
func (driver *Driver) receive(deliveries <-chan amqp091.Delivery, event string, handler rawHandler) {
	for delivery := range deliveries {
		driver.deliverToHandler(handler, event, delivery.Body)
	}
}

// deliverToHandler invokes handler while recovering and logging panics.
func (driver *Driver) deliverToHandler(handler rawHandler, event string, payload []byte) {
	defer driver.recoverHandlerPanic(event)

	handler(payload)
}

// recoverHandlerPanic logs a recovered AMQP event handler panic.
func (driver *Driver) recoverHandlerPanic(event string) {
	if recovered := recover(); recovered != nil {
		driver.logger.Error("panic in AMQP event handler", "event", event, "panic", fmt.Sprint(recovered)) //nolint:sloglint // AMQP delivery callbacks carry no context
	}
}

// brokerLogger retains an explicit logger or resolves slog.Default() once at
// construction. Nil does not silence handler failures or recovered panics.
func brokerLogger(value *slog.Logger) *slog.Logger {
	if value == nil {
		return slog.Default()
	}

	return value
}

// Ping opens and closes a fresh native channel without publishing an event.
// Cancellation interrupts an owned connection and every channel sharing it.
// With NewFrom, native RPCs require a responsive peer or caller-managed socket
// deadlines/connection close: the driver never closes a borrowed connection.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return fmt.Errorf("RabbitMQ readiness: %w", err)
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	conn, err := driver.connection()

	if err != nil {
		return err
	}

	return amqpconn.Run(ctx, driver.abort, func() error {
		ch, err := conn.Channel()

		if err != nil {
			return err
		}

		return ch.Close()
	})
}

// Stop closes admission and releases an owned connection before any
// publisher-channel close handshake. It does not wait for handler execution or
// broker confirmations. Graceful close permits at most five seconds or ctx's
// earlier end; cancellation still closes the socket and returns the context
// error. NewFrom closes only the publisher channel, whose non-cancelable SDK
// close RPC requires a responsive peer or caller-managed connection close.
// Repeated Stop calls return nil; the driver cannot be restarted afterward.
// Serialize Stop with Start.
//
// Example:
//
//	if err := driver.Stop(ctx); err != nil {
//		return fmt.Errorf("release broadcasts: %w", err)
//	}
func (driver *Driver) Stop(ctx context.Context) error {
	driver.mu.Lock()

	if driver.closed {
		driver.mu.Unlock()

		return nil
	}

	driver.closed = true
	conn := driver.conn
	pubCh := driver.pubCh
	abort := driver.abort
	driver.mu.Unlock()

	if driver.owned && conn != nil {
		return amqpconn.Close(ctx, conn, abort)
	}

	if pubCh != nil {
		return pubCh.Close()
	}

	return nil
}

// MessagingSystem returns "rabbitmq", the messaging.system telemetry value that
// the queue and broadcast facades report for this driver.
func (driver *Driver) MessagingSystem() string {
	return "rabbitmq"
}
