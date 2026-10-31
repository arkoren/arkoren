package natsqueue

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/driver/nats/internal/natsconfig"
	"arkoren.dev/driver/nats/internal/natsconn"
	receipt "arkoren.dev/driver/nats/natsqueue/internal/delivery"
	"arkoren.dev/driver/nats/natsqueue/internal/topology"

	"github.com/nats-io/nats.go"
)

// DefaultNATSURL is the NATS server URL [New] connects to when [Builder.URLs]
// is empty. It points to a local server on the standard port.
// No connection is opened until Start.
//
// Example:
//
//	policy := func(consumer *natsqueue.Builder) {
//		consumer.URLs(natsqueue.DefaultNATSURL)
//	}
//	_, err := natsqueue.New("JOBS", queues, policy)
//	if err != nil {
//		return err
//	}
const DefaultNATSURL = nats.DefaultURL

var (
	// ErrInvalidConfig is returned when a JetStream job driver configuration is invalid.
	// Constructors wrap it before any network I/O; native input values are not normalized.
	//
	// Example:
	//
	//	_, err := natsqueue.New("", nil)
	//	if errors.Is(err, natsqueue.ErrInvalidConfig) {
	//		return err
	//	}
	ErrInvalidConfig = errors.New("invalid JetStream job driver configuration")

	// ErrUnknownQueue is returned when a queue has no configured JetStream mapping.
	// Dispatch and Consume return it before publishing or reserving a message.
	//
	// Example:
	//
	//	if errors.Is(err, natsqueue.ErrUnknownQueue) {
	//		return fmt.Errorf("configure the logical queue: %w", err)
	//	}
	ErrUnknownQueue = errors.New("unknown job queue")

	// ErrResourcesNotProvisioned identifies missing or incompatible stream/consumer
	// resources. Lookup authorization, connection, and cancellation failures retain
	// their native identity without this classification. Start never repairs existing resources.
	//
	// Example:
	//
	//	if errors.Is(err, natsqueue.ErrResourcesNotProvisioned) {
	//		return fmt.Errorf("provision compatible JetStream resources: %w", err)
	//	}
	ErrResourcesNotProvisioned = errors.New("JetStream job resources are not provisioned")

	// ErrInvalidHandler is returned when Consume is given a nil handler.
	// An already-canceled context takes precedence over handler validation.
	//
	// Example:
	//
	//	if errors.Is(err, natsqueue.ErrInvalidHandler) {
	//		return fmt.Errorf("supply a delivery handler: %w", err)
	//	}
	ErrInvalidHandler = errors.New("invalid job delivery handler")

	// ErrInvalidDelivery is returned when a JetStream message cannot be converted to a job delivery.
	// Decode and metadata causes remain wrapped; malformed envelopes follow terminal failure policy.
	//
	// Example:
	//
	//	if errors.Is(err, natsqueue.ErrInvalidDelivery) {
	//		return fmt.Errorf("inspect producer envelope format: %w", err)
	//	}
	ErrInvalidDelivery = errors.New("invalid JetStream job delivery")

	// ErrUnsupportedRetryDelay is returned when a retry delay is negative.
	// No settlement is attempted; zero selects immediate native redelivery.
	//
	// Example:
	//
	//	if errors.Is(err, natsqueue.ErrUnsupportedRetryDelay) {
	//		return fmt.Errorf("retry delay must not be negative: %w", err)
	//	}
	ErrUnsupportedRetryDelay = errors.New("unsupported JetStream job retry delay")

	// ErrDriverClosed is returned when an operation is attempted after Stop.
	// A stopped driver cannot be restarted; construct a new driver for recovery.
	//
	// Example:
	//
	//	if errors.Is(err, natsqueue.ErrDriverClosed) {
	//		return fmt.Errorf("replace the stopped driver: %w", err)
	//	}
	ErrDriverClosed = errors.New("JetStream job driver is closed")

	// ErrDeliverySettled is returned when a delivery is settled more than once.
	// Concurrent settlement admission also returns it without a second side effect.
	//
	// Example:
	//
	//	if errors.Is(err, natsqueue.ErrDeliverySettled) {
	//		return fmt.Errorf("settle each delivery once: %w", err)
	//	}
	ErrDeliverySettled = errors.New("job delivery already settled")

	// ErrNotStarted is returned when an operation is attempted before Start.
	// Native Connection access alone does not prepare a borrowed driver's resources.
	//
	// Example:
	//
	//	if errors.Is(err, natsqueue.ErrNotStarted) {
	//		return fmt.Errorf("register driver startup first: %w", err)
	//	}
	ErrNotStarted = errors.New("JetStream job driver is not started")

	// ErrAlreadyStarted is returned when Start is called more than once.
	// Failed startup may be retried; successful startup is registered only once.
	//
	// Example:
	//
	//	if errors.Is(err, natsqueue.ErrAlreadyStarted) {
	//		return fmt.Errorf("duplicate startup registration: %w", err)
	//	}
	ErrAlreadyStarted = errors.New("JetStream job driver is already started")
)

// messageIDLength is the number of random bytes in generated message IDs.
// Sixteen bytes provides 128 bits of collision-resistant entropy.
const messageIDLength = 16

// defaultAckWait is the native acknowledgement policy applied to omitted or
// explicitly zero AckWait. Pull expiry is controlled separately by fetchWait.
const defaultAckWait = 30 * time.Second

// minimumAckWait keeps the half-deadline heartbeat interval strictly positive.
// No arbitrary millisecond floor is imposed on otherwise valid native policy.
const minimumAckWait = 2 * time.Nanosecond

// Queue maps one application queue to a JetStream subject and durable pull consumer.
// Both strings are required and copied as values with the constructor's owned
// map. Distinct logical queues require distinct subjects and durable names.
//
// Example:
//
//	queues := map[string]natsqueue.Queue{
//		"emails": {Subject: "jobs.emails", Durable: "email-workers"},
//	}
//	driver, err := natsqueue.New("JOBS", queues)
//	if err != nil {
//		return err
//	}
//	_ = driver.Connection()
type Queue struct {
	// Subject receives dispatched jobs for this queue.
	// It must be a literal native subject, not a wildcard filter.
	//
	// Example:
	//
	//	mapping := natsqueue.Queue{Subject: "jobs.emails", Durable: "email-workers"}
	//	_ = mapping.Subject
	Subject string

	// Durable is the durable pull consumer name for this queue.
	// It must fit native asset-name syntax and remain stable across workers.
	//
	// Example:
	//
	//	mapping := natsqueue.Queue{Subject: "jobs.emails", Durable: "email-workers"}
	//	_ = mapping.Durable
	Durable string
}

// settings configures a JetStream-backed job driver.
type settings struct {
	// URLs are the NATS server URLs used by [New] to connect. If empty, it
	// defaults to [DefaultNATSURL], a local server on the standard port.
	// [NewFrom] ignores it because the supplied connection already carries
	// its servers.
	URLs []string

	// Stream is the JetStream stream containing all configured queue subjects.
	Stream string

	// Queues maps application queue names to subjects and durable pull consumers.
	Queues map[string]Queue

	// FailureSubject receives application-level terminal failure envelopes. An
	// empty value logs failures and acknowledges terminal deliveries.
	FailureSubject string

	// Provision creates missing stream and durable consumers. When false,
	// resources must already exist and are validated by Start.
	Provision bool

	// Batch is the number of messages each pull request asks for. Zero uses one.
	Batch int

	// MaxAckPending caps unacknowledged messages for each durable consumer. Zero uses Batch.
	MaxAckPending int

	// AckWait is the JetStream acknowledgement deadline. Zero uses 30 seconds.
	AckWait time.Duration

	// MaxDeliver caps deliveries for provisioned consumers. Zero leaves the
	// JetStream server default in effect.
	MaxDeliver int

	// Options are passed to nats.Connect, for example TLS or credentials.
	Options []nats.Option

	// Logger records delivery failures. A nil logger uses slog.Default.
	Logger *slog.Logger
}

// defaultConfig returns JetStream job driver defaults. Callers must set Stream and Queues.
func defaultConfig() settings {
	return settings{
		URLs:    []string{DefaultNATSURL},
		Batch:   1,
		AckWait: defaultAckWait,
	}
}

// Driver implements [queue.DispatcherDriver] and [queue.ConsumerDriver].
// It uses durable JetStream pull consumers, so concurrent workers safely compete for one queue's messages.
// Connection failures can still duplicate processing; keep handlers idempotent.
// The zero Driver is invalid: construct with New or NewFrom before use.
//
// Example:
//
//	driver, err := natsqueue.New("JOBS", queues)
//	if err != nil {
//		return err
//	}
//	compose.Cleanup(driver.Stop)
//	compose.Startup(driver.Start)
type Driver struct {
	// stream identifies the JetStream stream that stores queued jobs.
	stream string
	// queues maps application queue names to their JetStream settings.
	queues map[string]Queue
	// failureSubject receives terminal delivery failures when configured.
	failureSubject string
	// batch limits the number of jobs requested in each pull.
	batch int
	// ackWait is the consumer acknowledgement deadline.
	ackWait time.Duration
	// config is the normalized configuration used by Start.
	config settings
	// jetStream performs the required JetStream operations; nil until Start.
	jetStream nats.JetStreamContext
	// connection is the NATS connection used by the driver; nil until Start.
	connection *nats.Conn
	// owned reports whether the driver must drain the connection on close.
	owned bool
	// mu protects connection, jetStream, and closed.
	mu sync.Mutex
	// closed reports whether Stop has already run.
	closed bool
	// logger records delivery and settlement failures.
	logger *slog.Logger
}

var _ queue.DispatcherDriver = (*Driver)(nil)
var _ queue.ConsumerDriver = (*Driver)(nil)

// NewFrom configures a JetStream job driver using conn. The caller retains
// ownership of conn and must keep it open for the driver's lifetime.
// [Driver.Start] provisions or validates JetStream resources without dialing.
// Callbacks run once in order before validation; nil entries panic. Connection
// options configure only connections created by New, not the borrowed conn.
// A nil conn returns ErrInvalidConfig after callbacks. URL and native-option
// settings are ignored; delivery policy applies and the queue map is copied.
//
// Example:
//
//	driver, err := natsqueue.NewFrom(conn, "JOBS", map[string]natsqueue.Queue{
//		"emails": {Subject: "jobs.emails", Durable: "emails"},
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver.Connection()
func NewFrom(conn *nats.Conn, stream string, queues map[string]Queue, configure ...BuilderFunc) (*Driver, error) {
	queues = maps.Clone(queues)
	config := configureDriver(configure...)
	config.Stream = stream
	config.Queues = queues

	if conn == nil {
		return nil, fmt.Errorf("%w: nil NATS connection", ErrInvalidConfig)
	}

	config, err := normalizeConfig(config)

	if err != nil {
		return nil, err
	}

	return newDriver(conn, config), nil
}

// prepareJetStream creates the JetStream context and ensures its resources.
func prepareJetStream(ctx context.Context, conn *nats.Conn, config settings) (nats.JetStreamContext, error) {
	jetStream, err := conn.JetStream()

	if err != nil {
		return nil, fmt.Errorf("create JetStream context: %w", err)
	}

	if err := ensureResources(ctx, jetStream, config); err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return jetStream, nil
}

// connect runs nats.Connect and abandons it when ctx ends first.
func connect(ctx context.Context, config settings) (*nats.Conn, error) {
	type result struct {
		// conn is the connection to return or close after cancellation.
		conn *nats.Conn
		// err records the native connection failure, if any.
		err error
	}

	results := make(chan result, 1)
	go func() {
		conn, err := nats.Connect(strings.Join(config.URLs, ","), config.Options...)
		results <- result{
			conn: conn,
			err:  err,
		}
	}()

	select {
	case <-ctx.Done():
		go func() {
			if outcome := <-results; outcome.conn != nil {
				outcome.conn.Close()
			}
		}()

		return nil, ctx.Err()
	case outcome := <-results:
		if outcome.err != nil {
			return nil, fmt.Errorf("connect to NATS: %w", outcome.err)
		}

		return outcome.conn, nil
	}
}

// newDriver publishes completed settings around an optional native connection.
// Absence of a supplied connection selects ownership of the later Start result.
func newDriver(conn *nats.Conn, config settings) *Driver {
	return &Driver{
		stream:         config.Stream,
		queues:         config.Queues,
		failureSubject: config.FailureSubject,
		batch:          config.Batch,
		ackWait:        config.AckWait,
		config:         config,
		connection:     conn,
		owned:          conn == nil,
		logger:         driverLogger(config.Logger),
	}
}

// started returns the JetStream context or ErrNotStarted.
func (driver *Driver) started() (nats.JetStreamContext, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.closed {
		return nil, ErrDriverClosed
	}

	if driver.jetStream == nil {
		return nil, ErrNotStarted
	}

	return driver.jetStream, nil
}

// queue returns the configured JetStream topology for name.
func (driver *Driver) queue(name string) (Queue, error) {
	jobQueue, ok := driver.queues[name]

	if !ok {
		return Queue{}, fmt.Errorf("%w: %s", ErrUnknownQueue, name)
	}

	return jobQueue, nil
}

// Dispatch JSON-encodes message and publishes it to its configured JetStream subject.
// A non-empty message ID is sent as Nats-Msg-Id for JetStream deduplication.
// Empty IDs receive an opaque generated value. Logical queue mapping is trusted
// construction data; native acceptance does not mean worker completion.
//
// Example:
//
//	if err := driver.Dispatch(ctx, queue.Message{Name: "email", Queue: "emails", Payload: []byte(`{}`)}); err != nil {
//		return err
//	}
func (driver *Driver) Dispatch(ctx context.Context, message queue.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return driver.dispatch(ctx, message)
}

// dispatch prepares and publishes a message to its configured subject.
func (driver *Driver) dispatch(ctx context.Context, message queue.Message) error {
	jobQueue, err := driver.queue(message.Queue)

	if err != nil {
		return err
	}

	message, err = identifyMessage(message)

	if err != nil {
		return err
	}

	body, err := marshalMessage(message)

	if err != nil {
		return err
	}

	return driver.publishMessage(ctx, jobQueue.Subject, message.ID, body)
}

// identifyMessage generates an ID when message does not have one.
func identifyMessage(message queue.Message) (queue.Message, error) {
	if message.ID != "" {
		return message, nil
	}

	id, err := newMessageID()

	if err != nil {
		return queue.Message{}, err
	}

	message.ID = id

	return message, nil
}

// marshalMessage encodes the complete job message envelope for publication.
func marshalMessage(message queue.Message) ([]byte, error) {
	body, err := json.Marshal(message)

	if err != nil {
		return nil, fmt.Errorf("marshal job message: %w", err)
	}

	return body, nil
}

// newMessageID returns a cryptographically random, URL-safe message ID.
func newMessageID() (string, error) {
	value := make([]byte, messageIDLength)

	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate job message ID: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(value), nil
}

// publishMessage publishes a prepared message and applies its optional ID header.
func (driver *Driver) publishMessage(ctx context.Context, subject, id string, body []byte) error {
	jetStream, err := driver.started()

	if err != nil {
		return err
	}

	publication := nats.NewMsg(subject)
	publication.Data = body

	if id != "" {
		publication.Header.Set(nats.MsgIdHdr, id)
	}

	_, err = jetStream.PublishMsg(publication, nats.Context(ctx))

	if err != nil {
		return fmt.Errorf("publish JetStream job message: %w", err)
	}

	return nil
}

// consume validates its handler, binds a pull consumer, and processes messages.
func (driver *Driver) consume(ctx context.Context, jobQueue string, handler queue.DeliveryHandler) error {
	if handler == nil {
		return ErrInvalidHandler
	}

	configuredQueue, err := driver.queue(jobQueue)

	if err != nil {
		return err
	}

	subscription, err := driver.bindConsumer(ctx, configuredQueue)

	if err != nil {
		return fmt.Errorf("bind JetStream pull consumer: %w", err)
	}

	defer func() {
		// Preserve the consumption result while releasing the pull subscription.
		_ = subscription.Unsubscribe()
	}()

	return driver.consumeSubscription(ctx, jobQueue, subscription, handler)
}

// bindConsumer explicitly validates the current native consumer using ctx. The
// SDK's implicit PullSubscribe lookup ignores both SubOpt Context and default
// JetStream context, so skip only that duplicate lookup after our own succeeds.
// Bind forbids creation or overwrite. Explicit acknowledgement policy/pending
// limits populate local SDK state without changing the durable. Do not bind the
// subscription worker to ctx: accepted deliveries must remain settleable while
// application handlers finish cancellation and perform detached settlement.
func (driver *Driver) bindConsumer(ctx context.Context, configuredQueue Queue) (*nats.Subscription, error) {
	jetStream, err := driver.started()

	if err != nil {
		return nil, err
	}

	info, err := jetStream.ConsumerInfo(driver.stream, configuredQueue.Durable, nats.Context(ctx))

	if err != nil {
		return nil, fmt.Errorf("inspect consumer before binding: %w", err)
	}

	if err := validateConsumer(driver.config, configuredQueue, info.Config); err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return jetStream.PullSubscribe(configuredQueue.Subject, configuredQueue.Durable,
		nats.Bind(driver.stream, configuredQueue.Durable), nats.SkipConsumerLookup(),
		nats.AckExplicit(), nats.MaxAckPending(info.Config.MaxAckPending))
}

// consumeSubscription repeatedly fetches and handles batches from subscription.
func (driver *Driver) consumeSubscription(
	ctx context.Context,
	jobQueue string,
	subscription *nats.Subscription,
	handler queue.DeliveryHandler,
) error {
	for {
		messages, err := fetchMessages(ctx, subscription, driver.batch)

		if err != nil {
			return err
		}

		driver.handleMessages(ctx, jobQueue, messages, handler)
	}
}

// fetchWait bounds native pull requests independently of acknowledgement timing.
// Provisioning uses the same upper limit; existing consumers must leave it
// unlimited or allow at least this duration. The caller's earlier deadline wins.
const fetchWait = 10 * time.Second

// fetchMessages retrieves a batch and preserves context cancellation errors.
// An empty fetch (the pull request expired with no messages) and a fetch
// interrupted by a reconnect are normal: they return no messages, so an idle
// worker keeps polling instead of exiting.
func fetchMessages(ctx context.Context, subscription *nats.Subscription, batch int) ([]*nats.Msg, error) {
	fetchContext, cancel := context.WithTimeout(ctx, fetchWait)
	defer cancel()

	messages, err := subscription.Fetch(batch, nats.Context(fetchContext))

	if err == nil {
		return messages, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrFetchDisconnected) {
		return nil, nil
	}

	return nil, fmt.Errorf("fetch JetStream job messages: %w", err)
}

// handleMessages reserves heartbeat state for the entire fetched batch before
// invoking handlers sequentially. Settlement and renewals share each delivery's
// mutex, so no progress publication can follow a delayed negative acknowledgement.
// Connection failures can still cause redelivery; handlers must be idempotent.
func (driver *Driver) handleMessages(ctx context.Context, jobQueue string, messages []*nats.Msg, handler queue.DeliveryHandler) {
	deliveries := make([]*delivery, len(messages))
	stops := make([]func(), len(messages))
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()

	for i, received := range messages {
		deliveries[i] = newDelivery(driver, received, queue.Message{Queue: jobQueue})
		stops[i] = deliveries[i].keepAlive(ctx)
	}

	for i, delivery := range deliveries {
		if ctx.Err() != nil {
			return
		}

		driver.handle(ctx, jobQueue, delivery, handler)
		stops[i]()
	}
}

// Ping verifies that the NATS connection is alive and that the JetStream API
// answers. It returns [ErrNotStarted] before [Driver.Start] and
// [ErrDriverClosed] after [Driver.Stop]. It publishes no data.
// The native AccountInfo request respects cancellation and its native timeout.
//
// Example:
//
//	compose.Check("jetstream", driver.Ping)
func (driver *Driver) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	jetStream, err := driver.started()

	if err != nil {
		return err
	}

	if _, err := jetStream.AccountInfo(nats.Context(ctx)); err != nil {
		return fmt.Errorf("ping JetStream: %w", err)
	}

	return nil
}

// Stop drains the NATS connection only when the driver created it with
// [New]; a connection passed to [NewFrom] stays open. Drain flushes pending
// acknowledgements and publishes; Stop waits until the connection closes, subject
// to ctx and native DrainTimeout. Cancellation forces Close and returns the
// context error, even for an already-expired context. It does not join application
// workers: cancel and wait for Consume first. A second Stop is a no-op.
// Native drain-timeout failures remain identifiable as nats.ErrDrainTimeout.
//
// Example:
//
//	compose.Cleanup(driver.Stop)
func (driver *Driver) Stop(ctx context.Context) error {
	driver.mu.Lock()

	if driver.closed {
		driver.mu.Unlock()

		return nil
	}

	driver.closed = true
	connection := driver.connection
	driver.mu.Unlock()

	if !driver.owned || connection == nil {
		return nil
	}

	if err := natsconn.Drain(ctx, connection); err != nil {
		return fmt.Errorf("drain NATS connection: %w", err)
	}

	return nil
}

// handle decodes received and invokes handler or records a terminal failure.
func (driver *Driver) handle(ctx context.Context, jobQueue string, delivery *delivery, handler queue.DeliveryHandler) {
	message, err := decodeMessage(jobQueue, delivery.received)

	if err != nil {
		delivery.rawMessage = string(delivery.received.Data)

		if err := delivery.Fail(ctx, err); err != nil {
			driver.logger.ErrorContext(ctx, "failed invalid JetStream job message", "error", err, "queue", jobQueue)
		}

		return
	}

	delivery.message = message

	if err := handler(ctx, delivery); err != nil {
		driver.logger.ErrorContext(ctx, "JetStream job delivery handler failed", "error", err, "queue", jobQueue, "job_id", message.ID)
	}
}

// normalizeConfig applies defaults and validates JetStream driver settings.
func normalizeConfig(config settings) (settings, error) {
	if !natsconfig.Name(config.Stream) {
		return emptyDriverConfig(), fmt.Errorf("%w: Stream must be a native asset name", ErrInvalidConfig)
	}

	if len(config.Queues) == 0 {
		return emptyDriverConfig(), fmt.Errorf("%w: Queues are required", ErrInvalidConfig)
	}

	config = defaultJetStreamConfig(config)

	return normalizeJetStreamSettings(config)
}

// defaultJetStreamConfig applies URL, batch, and acknowledgement defaults.
func defaultJetStreamConfig(config settings) settings {
	if len(config.URLs) == 0 {
		config.URLs = []string{DefaultNATSURL}
	}

	if config.Batch == 0 {
		config.Batch = 1
	}

	if config.MaxAckPending == 0 {
		config.MaxAckPending = config.Batch
	}

	if config.AckWait == 0 {
		config.AckWait = defaultAckWait
	}

	return config
}

// normalizeJetStreamSettings validates settings before normalizing queues.
func normalizeJetStreamSettings(config settings) (settings, error) {
	if config.Batch < 1 {
		return emptyDriverConfig(), fmt.Errorf("%w: batch must be positive", ErrInvalidConfig)
	}

	if config.MaxAckPending < config.Batch {
		return emptyDriverConfig(), fmt.Errorf("%w: max ack pending must be at least batch", ErrInvalidConfig)
	}

	if config.AckWait < minimumAckWait {
		return emptyDriverConfig(), fmt.Errorf("%w: AckWait must be at least two nanoseconds", ErrInvalidConfig)
	}

	if config.MaxDeliver < 0 {
		return emptyDriverConfig(), fmt.Errorf("%w: MaxDeliver must not be negative", ErrInvalidConfig)
	}

	return normalizeJetStreamQueues(config)
}

// normalizeJetStreamQueues validates and copies configured queues.
func normalizeJetStreamQueues(config settings) (settings, error) {
	queues, err := copyJetStreamQueues(config.Queues)

	if err != nil {
		return emptyDriverConfig(), err
	}

	config.Queues = queues

	if err := validateFailureSubject(config); err != nil {
		return emptyDriverConfig(), err
	}

	return config, nil
}

// queueValidation tracks JetStream subjects and durable names during copying.
type queueValidation struct {
	// subjects records subjects already assigned to a logical queue.
	subjects map[string]struct{}
	// durables records durable names already assigned within the stream.
	durables map[string]struct{}
}

// newQueueValidation allocates duplicate-detection maps for queueCount queues.
func newQueueValidation(queueCount int) queueValidation {
	return queueValidation{
		subjects: make(map[string]struct{}, queueCount),
		durables: make(map[string]struct{}, queueCount),
	}
}

// add validates queue and records its unique subject and durable values.
func (queueValidation *queueValidation) add(name string, jobQueue Queue) error {
	if !validJetStreamQueue(name, jobQueue) {
		return fmt.Errorf("%w: queue name, subject, and durable are required", ErrInvalidConfig)
	}

	if _, ok := queueValidation.subjects[jobQueue.Subject]; ok {
		return fmt.Errorf("%w: duplicate queue subject %q", ErrInvalidConfig, jobQueue.Subject)
	}

	if _, ok := queueValidation.durables[jobQueue.Durable]; ok {
		return fmt.Errorf("%w: duplicate durable consumer %q", ErrInvalidConfig, jobQueue.Durable)
	}

	queueValidation.subjects[jobQueue.Subject] = struct{}{}
	queueValidation.durables[jobQueue.Durable] = struct{}{}

	return nil
}

// validJetStreamQueue reports whether a queue has complete identifiers.
func validJetStreamQueue(name string, jobQueue Queue) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}

	if !natsconfig.Subject(jobQueue.Subject) {
		return false
	}

	return natsconfig.Name(jobQueue.Durable)
}

// validateFailureSubject prevents terminal envelopes from feeding work consumers.
// Work subjects are literal, so equality is the complete overlap check. An empty
// failure destination intentionally selects logging followed by acknowledgement.
func validateFailureSubject(config settings) error {
	if config.FailureSubject == "" {
		return nil
	}

	if !natsconfig.Subject(config.FailureSubject) {
		return fmt.Errorf("%w: FailureSubject must be a literal NATS subject", ErrInvalidConfig)
	}

	for _, jobQueue := range config.Queues {
		if jobQueue.Subject == config.FailureSubject {
			return fmt.Errorf("%w: FailureSubject overlaps a work subject", ErrInvalidConfig)
		}
	}

	return nil
}

// copyJetStreamQueues returns a copy of valid, non-conflicting queues.
func copyJetStreamQueues(source map[string]Queue) (map[string]Queue, error) {
	queues := make(map[string]Queue, len(source))
	validation := newQueueValidation(len(source))

	for name, jobQueue := range source {
		if err := validation.add(name, jobQueue); err != nil {
			return nil, err
		}

		queues[name] = jobQueue
	}

	return queues, nil
}

// emptyDriverConfig returns an intentionally empty configuration after failure.
func emptyDriverConfig() settings {
	return settings{}
}

// ensureResources validates or provisions the configured stream and consumers.
func ensureResources(ctx context.Context, jetStream nats.JetStreamContext, config settings) error {
	return topology.Ensure(ctx, jetStream, topologyConfig(config))
}

// topologyConfig detaches native declarations without moving public Queue types.
func topologyConfig(config settings) topology.Config {
	queues := make(map[string]nats.ConsumerConfig, len(config.Queues))

	for name, jobQueue := range config.Queues {
		queues[name] = topology.Consumer(jobQueue.Durable, jobQueue.Subject, config.AckWait,
			config.MaxDeliver, config.MaxAckPending, config.Batch, fetchWait)
	}

	return topology.Config{Stream: config.Stream, Queues: queues,
		Provision: config.Provision, Missing: ErrResourcesNotProvisioned}
}

// validateStream verifies that stream includes every configured queue subject.
func validateStream(config settings, stream nats.StreamConfig) error {
	return topology.ValidateStream(topologyConfig(config), stream)
}

// validateConsumer verifies that consumer meets this driver's requirements.
func validateConsumer(config settings, jobQueue Queue, consumer nats.ConsumerConfig) error {
	expected := topology.Consumer(jobQueue.Durable, jobQueue.Subject, config.AckWait,
		config.MaxDeliver, config.MaxAckPending, config.Batch, fetchWait)

	return topology.ValidateConsumer(expected, consumer, ErrResourcesNotProvisioned)
}

// decodeMessage decodes received and applies JetStream delivery metadata.
func decodeMessage(queueName string, received *nats.Msg) (queue.Message, error) {
	message, err := decodeMessageBody(received)

	if err != nil {
		return queue.Message{}, err
	}

	attempts, err := deliveryAttempts(received)

	if err != nil {
		return queue.Message{}, err
	}

	return applyDeliveryMetadata(message, queueName, attempts, received), nil
}

// decodeMessageBody validates and decodes a JetStream message body.
func decodeMessageBody(received *nats.Msg) (queue.Message, error) {
	if received == nil {
		return queue.Message{}, fmt.Errorf("%w: nil message", ErrInvalidDelivery)
	}

	var message queue.Message

	if err := json.Unmarshal(received.Data, &message); err != nil {
		return queue.Message{}, fmt.Errorf("%w: unmarshal job message: %w", ErrInvalidDelivery, err)
	}

	return message, nil
}

// applyDeliveryMetadata applies broker-controlled queue, attempt, and ID values.
func applyDeliveryMetadata(message queue.Message, jobQueue string, attempts int, received *nats.Msg) queue.Message {
	message.Queue = jobQueue
	message.Attempts = attempts

	if message.ID == "" {
		message.ID = received.Header.Get(nats.MsgIdHdr)
	}

	return message
}

// deliveryAttempts returns the number of times received has been delivered.
func deliveryAttempts(received *nats.Msg) (int, error) {
	metadata, err := received.Metadata()

	if err != nil {
		return 0, fmt.Errorf("%w: JetStream metadata: %w", ErrInvalidDelivery, err)
	}

	if metadata.NumDelivered > uint64(^uint(0)>>1) {
		return 0, fmt.Errorf("%w: delivery attempt overflow", ErrInvalidDelivery)
	}

	return int(metadata.NumDelivered), nil
}

// delivery tracks one JetStream message and its settlement state.
type delivery struct {
	// driver owns the JetStream connection used for settlement.
	driver *Driver
	// received is the JetStream message being delivered.
	received *nats.Msg
	// message is the decoded job message exposed to the handler.
	message queue.Message
	// rawMessage retains an undecodable message body for failure reporting.
	rawMessage string
	// state owns heartbeat publication and settlement admission.
	state receipt.State
}

// newDelivery constructs a delivery for received or returns nil when it is absent.
func newDelivery(driver *Driver, received *nats.Msg, message queue.Message) *delivery {
	if received == nil {
		return nil
	}

	return &delivery{
		driver:   driver,
		received: received,
		message:  message,
	}
}

// Message returns an independent job envelope, copying Payload and Metadata.
// Mutating a returned record cannot change settlement or later Message calls.
//
// Example:
//
//	message := delivery.Message()
//	message.Metadata = nil
//	_ = message.Name
func (delivery *delivery) Message() queue.Message {
	message := delivery.message
	message.Payload = slices.Clone(message.Payload)
	message.Metadata = maps.Clone(message.Metadata)

	return message
}

// Acknowledge acknowledges this delivery to prevent redelivery.
// The native acknowledgement uses ctx; a failed operation can be attempted again.
//
// Example:
//
//	if err := delivery.Acknowledge(ctx); err != nil {
//		return err
//	}
func (delivery *delivery) Acknowledge(ctx context.Context) error {
	return delivery.settle(ctx, func() error {
		return delivery.received.Ack(nats.Context(ctx))
	})
}

// Retry negatively acknowledges this delivery for immediate or delayed redelivery.
// Zero is immediate; negative delays return ErrUnsupportedRetryDelay. Progress
// renewal is serialized before settlement so it cannot shorten a delayed retry.
//
// Example:
//
//	if err := delivery.Retry(ctx, time.Minute); err != nil {
//		return err
//	}
func (delivery *delivery) Retry(ctx context.Context, delay time.Duration) error {
	if delay < 0 {
		return fmt.Errorf("%w: %s", ErrUnsupportedRetryDelay, delay)
	}

	return delivery.settle(ctx, func() error {
		if delay == 0 {
			return delivery.received.Nak(nats.Context(ctx))
		}

		return delivery.received.NakWithDelay(delay, nats.Context(ctx))
	})
}

// Reject terminates this delivery (JetStream Term), so it is never redelivered.
// This is not a universal JetStream dead-letter operation; ctx controls the request.
//
// Example:
//
//	if err := delivery.Reject(ctx); err != nil {
//		return err
//	}
func (delivery *delivery) Reject(ctx context.Context) error {
	return delivery.settle(ctx, func() error {
		return delivery.received.Term(nats.Context(ctx))
	})
}

// Fail publishes a terminal failure when configured and acknowledges this delivery.
// Failure publication must succeed first. Without a FailureSubject it logs the
// error then acknowledges; connection loss can still make settlement uncertain.
//
// Example:
//
//	if err := delivery.Fail(ctx, errors.New("unsupported payload")); err != nil {
//		return err
//	}
func (delivery *delivery) Fail(ctx context.Context, err error) error {
	return delivery.settle(ctx, func() error {
		if err := delivery.publishFailure(ctx, err); err != nil {
			return err
		}

		return delivery.received.Ack(nats.Context(ctx))
	})
}

// publishFailure records err as a failure message when a subject is configured.
func (delivery *delivery) publishFailure(ctx context.Context, err error) error {
	if delivery.driver.failureSubject == "" {
		delivery.driver.logger.ErrorContext(ctx, "JetStream job delivery failed", "error", err, "queue", delivery.message.Queue, "job_id", delivery.message.ID)

		return nil
	}

	return delivery.publishFailureMessage(ctx, err)
}

// publishFailureMessage serializes and publishes a configured failure envelope.
func (delivery *delivery) publishFailureMessage(ctx context.Context, err error) error {
	body, err := json.Marshal(failureMessage{
		Message:    delivery.message,
		Error:      errorText(err),
		RawMessage: delivery.rawMessage,
	})

	if err != nil {
		return fmt.Errorf("marshal job failure: %w", err)
	}

	_, err = delivery.driver.jetStream.PublishMsg(delivery.failurePublication(body), nats.Context(ctx))

	if err != nil {
		return fmt.Errorf("publish job failure: %w", err)
	}

	return nil
}

// failurePublication creates a failure publication with the derived message ID.
func (delivery *delivery) failurePublication(body []byte) *nats.Msg {
	publication := nats.NewMsg(delivery.driver.failureSubject)
	publication.Data = body

	if delivery.message.ID != "" {
		publication.Header.Set(nats.MsgIdHdr, delivery.message.ID+":failure")
	}

	return publication
}

// driverLogger returns the shared configured logger or slog.Default for nil.
func driverLogger(value *slog.Logger) *slog.Logger {
	if value == nil {
		return slog.Default()
	}

	return value
}

// failureMessage contains the application-level terminal failure envelope.
type failureMessage struct {
	// Message is the job message whose processing failed.
	Message queue.Message `json:"message"`
	// Error describes the processing failure.
	Error string `json:"error"`
	// RawMessage contains the original body when it could not be decoded.
	RawMessage string `json:"raw_message,omitzero"`
}

// errorText returns err's text, or a generic description for nil. Failure
// envelopes are application data, not redacted diagnostics; restrict access.
func errorText(err error) string {
	if err == nil {
		return "job failed"
	}

	return err.Error()
}

// MessagingSystem returns "nats", the messaging.system telemetry value that
// the queue and broadcast facades report for this driver.
// It performs no I/O and enables no instrumentation by itself.
//
// Example:
//
//	slog.Info("queue transport", "system", driver.MessagingSystem())
func (driver *Driver) MessagingSystem() string {
	return "nats"
}
