package redisbroadcast

import (
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"arkoren.dev/core/service/broadcast"

	"github.com/redis/go-redis/v9"
)

// rawHandler receives uninterpreted Redis payload bytes through [Driver.Subscribe].
// It has no channel identity; envelope validation belongs to [Driver.Consume].
type rawHandler = func([]byte)

// Driver implements [broadcast.PublisherDriver] and
// [broadcast.ConsumerDriver] using Redis Pub/Sub.
// It uses Redis pattern subscriptions and verifies each delivery against the
// portable event-pattern grammar. Consume additionally binds the envelope name
// to the actual channel suffix before handing it to the broadcast consumer.
// Prefixes are routing namespaces, not authorization boundaries; raw Subscribe
// requires non-overlapping namespaces to avoid cross-application deliveries.
//
// Example:
//
//	driver := redisbroadcast.New(func(broker *redisbroadcast.Builder) {
//		broker.Prefix("billing:")
//	})
//	publisher := broadcast.NewPublisher(driver)
//	if err := publisher.Publish(ctx, broadcast.NewTopic[string]("invoice.paid"), "invoice-123"); err != nil {
//		return err
//	}
type Driver struct {
	// client is the Redis connection used for event operations.
	client *redis.Client

	// prefix namespaces every channel this driver publishes and subscribes to.
	prefix string

	// owned reports whether Stop manages client's lifecycle.
	owned bool

	// wg tracks active subscription goroutines.
	wg sync.WaitGroup

	// logger records recovered handler panics.
	logger *slog.Logger
}

var _ broadcast.PublisherDriver = (*Driver)(nil)
var _ broadcast.ConsumerDriver = (*Driver)(nil)

// Client returns the exact native client used by this broker. Native publishing
// bypasses envelope encoding, namespace validation, and facade telemetry. Stop
// closes New's client, but a client borrowed through NewFrom remains caller-owned.
//
// Example:
//
//	driver := redisbroadcast.New()
//	stats := driver.Client().PoolStats()
//	_ = stats.TotalConns
func (driver *Driver) Client() *redis.Client {
	return driver.client
}

// Config is a diagnostic Redis broadcast record. Formatting and slog redact
// Password. It is not accepted by constructors; use Builder settings instead.
// Native TLS objects remain shared values under their existing ownership.
//
// Example:
//
//	record := redisbroadcast.Config{
//		Prefix:   "billing:",
//		Password: "example",
//	}
//	slog.Info("Redis broadcast configuration", "config", record)
type Config struct {
	// Network is the network type passed to the Redis client.
	// New defaults to "tcp"; "unix" uses Addr as a socket path.
	//
	// Example:
	//
	//	record := redisbroadcast.Config{Network: "tcp"}
	//	_ = record.Network
	Network string

	// Addr is the Redis server address. New defaults to "localhost:6379".
	//
	// Example:
	//
	//	record := redisbroadcast.Config{Addr: "events.internal:6379"}
	//	_ = record.Addr
	Addr string

	// Username is the optional ACL username. New defaults to an empty username.
	//
	// Example:
	//
	//	record := redisbroadcast.Config{Username: "billing"}
	//	_ = record.Username
	Username string

	// Password is the optional ACL password. Empty sends no password. Formatting
	// the complete record redacts it; reading this field returns the secret.
	//
	// Example:
	//
	//	record := redisbroadcast.Config{Password: "example"}
	//	_ = record.String() // Contains [REDACTED], not the password.
	Password string

	// DB is the Redis connection's logical database number; New defaults to zero.
	// Pub/Sub ignores it: it does not isolate channels or subscription delivery.
	//
	// Example:
	//
	//	record := redisbroadcast.Config{DB: 0}
	//	_ = record.DB
	DB int

	// TLS enables TLS for the connection when non-nil, for example a
	// *tls.Config with MinVersion and RootCAs set. The default, nil, connects
	// without TLS. [New] passes it to the client as is; [NewFrom] ignores it
	// because the supplied client carries its own.
	//
	// Example:
	//
	//	record := redisbroadcast.Config{TLS: &tls.Config{MinVersion: tls.VersionTLS12}}
	//	_ = record.TLS.MinVersion
	TLS *tls.Config

	// Prefix is prepended literally to channel names, including Redis glob
	// metacharacters. It defaults to "arkoren." when empty. Consume rejects
	// envelopes whose names differ from the actual channel suffix; Subscribe
	// delivers raw bytes and requires non-overlapping application namespaces.
	// Redis Pub/Sub ignores DB. Use broker channel ACLs or separate instances
	// for authorization, not prefixes alone. Prefixes "billing:" and "shipping:"
	// select disjoint namespaces.
	//
	// Example:
	//
	//	record := redisbroadcast.Config{Prefix: "billing:"}
	//	_ = record.Prefix
	Prefix string

	// Logger records recovered handler panics. Nil uses slog.Default.
	// The logger remains shared; construction does not clone it.
	//
	// Example:
	//
	//	record := redisbroadcast.Config{Logger: slog.Default()}
	//	_ = record.Logger
	Logger *slog.Logger
}

// defaultConfig returns the default Redis event broker configuration.
func defaultConfig() Config {
	return Config{
		Network: "tcp",
		Addr:    "localhost:6379",
		Prefix:  "arkoren.",
	}
}

// New creates a Driver with the given configuration. It performs no network
// I/O: [Driver.Start] connects to Redis.
// Callbacks run once in order on a fresh draft; nil entries panic.
// Empty Network, Addr, and Prefix select "tcp", "localhost:6379", and "arkoren.";
// database zero, empty ACL credentials, and no TLS are the remaining defaults.
// The created client honors operation deadlines during ordinary command I/O.
// TLS and loggers remain shared, and Stop closes this owned native client.
//
// Example:
//
//	driver := redisbroadcast.New(func(broker *redisbroadcast.Builder) {
//		broker.Prefix("billing:")
//	})
//	_ = driver.Client()
func New(configure ...BuilderFunc) *Driver {
	config := configureDriver(configure...)
	config = config.withDefaults()

	client := redis.NewClient(&redis.Options{
		Network:               config.Network,
		Addr:                  config.Addr,
		Username:              config.Username,
		Password:              config.Password,
		DB:                    config.DB,
		TLSConfig:             config.TLS,
		ContextTimeoutEnabled: true,
	})

	return &Driver{
		client: client,
		owned:  true,
		prefix: config.Prefix,
		logger: brokerLogger(config.Logger),
	}
}

// withDefaults fills empty configuration fields with broker defaults.
func (config Config) withDefaults() Config {
	if config.Network == "" {
		config.Network = defaultConfig().Network
	}

	if config.Addr == "" {
		config.Addr = defaultConfig().Addr
	}

	if config.Prefix == "" {
		config.Prefix = defaultConfig().Prefix
	}

	return config
}

// NewFrom wraps an existing redis.Client as a
// Driver, allowing reuse of a pre-configured client. The caller retains
// ownership of client. An empty prefix uses the default.
//
// Only Builder.Prefix and Builder.Logger apply. Callbacks still run directly
// once in order; connection settings are ignored without mutating client.
// Set native ContextTimeoutEnabled before construction if operation deadlines
// must bound ordinary command socket I/O. Stop waits for subscriptions but never
// closes this borrowed client; stop consumers before its owner closes it.
//
// Example:
//
//	client := redis.NewClient(&redis.Options{
//		Addr: "localhost:6379",
//	})
//	driver := redisbroadcast.NewFrom(client, func(broker *redisbroadcast.Builder) {
//		broker.Prefix("billing:")
//	})
//	_ = driver.Client()
func NewFrom(client *redis.Client, configure ...BuilderFunc) *Driver {
	config := configureDriver(configure...)

	return &Driver{
		client: client,
		prefix: cmp.Or(config.Prefix, defaultConfig().Prefix),
		logger: brokerLogger(config.Logger),
	}
}

// Publish validates message.Name, JSON-encodes the complete broadcast envelope,
// and publishes it to Prefix+message.Name. Name, payload bytes, and metadata are
// carried in that envelope; raw Subscribe handlers receive the encoded envelope,
// not just message.Payload. Native encoding and transport failures are returned.
// An already-canceled context takes precedence over name validation and sends
// nothing, preserving the caller's cancellation error.
//
// Example:
//
//	if err := driver.Publish(ctx, broadcast.Message{
//		Name: "invoice.paid", Payload: []byte(`"invoice-123"`),
//	}); err != nil {
//		return err
//	}
func (driver *Driver) Publish(ctx context.Context, message broadcast.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := broadcast.ValidateName(message.Name); err != nil {
		return err
	}

	body, err := json.Marshal(message)

	if err != nil {
		return err
	}

	return driver.client.Publish(ctx, driver.prefix+message.Name, body).Err()
}

// Consume receives envelopes until ctx ends and returns its context error.
// It drops malformed envelopes and envelopes whose Name differs from the
// literal channel suffix, before core routing or telemetry sees them. Valid
// envelopes retain their payload and metadata. Handler errors are not retried;
// handler panics are recovered and logged, and consumption continues.
// A nil handler or failed Redis subscription setup returns an error.
// Cancellation releases this subscription.
//
// Example:
//
//	err := driver.Consume(ctx, func(ctx context.Context, message broadcast.Message) error {
//		slog.InfoContext(ctx, "broadcast received", "name", message.Name)
//		return nil
//	})
//	if !errors.Is(err, context.Canceled) {
//		return err
//	}
func (driver *Driver) Consume(ctx context.Context, handler broadcast.Handler) error {
	if handler == nil {
		return errors.New("nil broadcast handler")
	}

	unsubscribe, err := driver.subscribe(ctx, "#", func(name string, payload []byte) {
		var message broadcast.Message

		if json.Unmarshal(payload, &message) != nil || message.Name != name {
			return
		}

		// Pub/Sub has no acknowledgement or retry mechanism for handler errors.
		_ = handler(ctx, message)
	})

	if err != nil {
		return err
	}

	defer func() {
		// Cancellation has already ended consumption; still release the socket.
		_ = unsubscribe()
	}()

	<-ctx.Done()

	return ctx.Err()
}

// Subscribe registers a raw handler for the portable event pattern and returns
// a function that closes the subscription. ctx controls setup; the caller must
// invoke that function to release the subscription. Handler panics are recovered
// and logged without ending the subscription. Invalid patterns and failed Redis
// subscription setup return errors without retaining the subscription.
//
// Prefix matching is literal, but raw payloads cannot identify which application
// owns an overlapping channel. Use non-overlapping prefixes for raw subscribers;
// unlike Consume, Subscribe does not decode or validate envelope names. Neither
// API authorizes publishers: use Redis channel ACLs or separate instances when
// publishers are not mutually trusted.
//
// Example:
//
//	unsubscribe, err := driver.Subscribe(ctx, "users.*", func(payload []byte) {
//		slog.Info("raw broadcast received", "bytes", len(payload))
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
func (driver *Driver) Subscribe(
	ctx context.Context,
	event string,
	handler rawHandler,
) (func() error, error) {
	return driver.subscribe(ctx, event, func(_ string, payload []byte) {
		handler(payload)
	})
}

// subscribe retains the actual channel suffix through filtering and callback
// dispatch. This lets Consume validate routing identity without changing the
// public raw callback. Prefix glob characters are escaped only for PSUBSCRIBE;
// published channel names and literal matching retain their original bytes.
func (driver *Driver) subscribe(
	ctx context.Context,
	event string,
	handler func(string, []byte),
) (func() error, error) {
	if err := broadcast.ValidatePattern(event); err != nil {
		return nil, err
	}

	subscription := driver.client.PSubscribe(ctx, redisPattern(driver.prefix, event))

	if _, err := subscription.Receive(ctx); err != nil {
		return nil, errors.Join(err, subscription.Close())
	}

	driver.wg.Go(func() {
		driver.receive(subscription, event, handler)
	})

	return func() error {
		return subscription.Close()
	}, nil
}

// receive accepts only channels with the driver's literal prefix and a suffix
// matching the portable event pattern. It retains that suffix for the callback,
// rather than trusting a routing name supplied inside the payload.
func (driver *Driver) receive(
	subscription *redis.PubSub,
	event string,
	handler func(string, []byte),
) {
	for message := range subscription.Channel() {
		name, ok := strings.CutPrefix(message.Channel, driver.prefix)

		if ok && broadcast.Match(event, name) {
			driver.deliverToHandler(handler, event, name, []byte(message.Payload))
		}
	}
}

// brokerLogger returns the supplied shared logger or slog.Default for nil. The
// fallback reports recovered handler panics rather than discarding their records.
func brokerLogger(value *slog.Logger) *slog.Logger {
	if value == nil {
		return slog.Default()
	}

	return value
}

// Ping verifies Redis connectivity without publishing a message. New-created
// clients honor operation deadlines; NewFrom keeps its native timeout policy.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	return driver.client.Ping(ctx).Err()
}

// Start verifies connectivity so a misconfigured Redis fails fast. The
// client connects lazily, so [New] performs no IO.
// It does not subscribe; consumer Run or Subscribe owns subscription setup.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context) error {
	return driver.Ping(ctx)
}

// Stop closes the Redis client only when the broker created it, then waits for
// all active subscription goroutines to finish. The client is closed even when
// ctx is already expired. Stop returns ctx.Err() when ctx ends before the
// active subscriptions drain, and the client's close error otherwise.
// Borrowed clients remain open: end Consume contexts and release raw Subscribe
// registrations before stopping a NewFrom driver.
//
// Example:
//
//	if err := driver.Stop(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Stop(ctx context.Context) error {
	var err error

	if driver.owned {
		err = driver.client.Close()
	}

	drained := make(chan struct{})
	go func() {
		driver.wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		return err
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
}

// MessagingSystem returns "redis", the messaging.system telemetry value that
// the queue and broadcast facades report for this driver.
// It is a bounded protocol identifier, independent of channel names and prefixes.
//
// Example:
//
//	driver := redisbroadcast.New()
//	_ = driver.MessagingSystem() // "redis"
func (driver *Driver) MessagingSystem() string {
	return "redis"
}
