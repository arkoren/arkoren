package natsbroadcast

import (
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/driver/nats/internal/natsconfig"
	"arkoren.dev/driver/nats/internal/natsconn"

	"github.com/nats-io/nats.go"
)

// rawHandler receives application-owned raw payload processing callbacks.
// Unlike envelope consumption, raw subscriptions do not interpret the bytes.
type rawHandler = func([]byte)

const (
	// DefaultNATSURL is the default connection URL for NATS server.
	// It points to a local NATS server running on the standard port.
	//
	// Example:
	//
	//	policy := func(broker *natsbroadcast.Builder) {
	//		broker.URLs(natsbroadcast.DefaultNATSURL)
	//	}
	//	_, err := natsbroadcast.New(policy)
	//	if err != nil {
	//		return err
	//	}
	DefaultNATSURL = nats.DefaultURL

	// DefaultNATSMaxReconnects is the default maximum number of reconnect
	// attempts.
	// A value of -1 allows unlimited reconnection attempts.
	// Zero also selects this default; use Builder.NoReconnect to disable retries.
	//
	// Example:
	//
	//	policy := func(broker *natsbroadcast.Builder) {
	//		broker.MaxReconnects(natsbroadcast.DefaultNATSMaxReconnects)
	//	}
	//	_, err := natsbroadcast.New(policy)
	//	if err != nil {
	//		return err
	//	}
	DefaultNATSMaxReconnects = -1

	// DefaultNATSReconnectWait is the default time to wait between reconnect
	// attempts.
	// This provides a reasonable backoff when the NATS server is temporarily
	// unavailable.
	// Zero restores this wait; negative values are rejected by New.
	//
	// Example:
	//
	//	policy := func(broker *natsbroadcast.Builder) {
	//		broker.ReconnectWait(natsbroadcast.DefaultNATSReconnectWait)
	//	}
	//	_, err := natsbroadcast.New(policy)
	//	if err != nil {
	//		return err
	//	}
	DefaultNATSReconnectWait = 2 * time.Second
)

// Driver implements [broadcast.PublisherDriver] and
// [broadcast.ConsumerDriver] using NATS messaging
// system.
// It provides a lightweight, high-performance event broker with built-in
// fan-out support and wildcard subscriptions.
// NATS handles message routing natively, making this implementation simpler
// than brokers that require manual handler tracking.
//
// Wildcard patterns: '*' matches a single dot-separated token (NATS native).
// '#' is translated to NATS subscriptions that together match zero or more
// tokens and must be the last token.
// Construct with New or NewFrom; the zero Driver is not ready for operations.
//
// Example:
//
//	driver, err := natsbroadcast.New()
//	if err != nil {
//		return err
//	}
//	compose.Cleanup(driver.Stop)
//	compose.Startup(driver.Start)
type Driver struct {
	// mu protects conn.
	mu sync.RWMutex

	// urls is the joined server list Start connects to.
	urls string

	// connectOptions are the options Start passes to nats.Connect.
	connectOptions []nats.Option

	// conn is the underlying NATS connection, nil until Start.
	// It handles all communication with the NATS server including publishing,
	// subscribing, and maintaining the connection lifecycle.
	conn *nats.Conn
	// owned reports whether Stop manages conn's lifecycle.
	owned bool

	// prefix namespaces every subject.
	prefix string

	// queueGroup, when set, load-balances messages across group members.
	queueGroup string
	// logger records recovered handler panics.
	logger *slog.Logger
}

// ErrNotStarted reports that the driver was used before Start.
// It is returned without touching the network when no connection is installed.
//
// Example:
//
//	if errors.Is(err, natsbroadcast.ErrNotStarted) {
//		return fmt.Errorf("start broadcast driver first: %w", err)
//	}
var ErrNotStarted = errors.New("NATS broadcast driver is not started")

// ErrAlreadyStarted reports that Start was called more than once.
// Borrowed drivers instead accept repeated Start calls without reconnecting.
//
// Example:
//
//	if errors.Is(err, natsbroadcast.ErrAlreadyStarted) {
//		return fmt.Errorf("duplicate startup registration: %w", err)
//	}
var ErrAlreadyStarted = errors.New("NATS broadcast driver is already started")

// ErrInvalidConfig identifies invalid constructor settings or a nil borrowed
// connection. Errors wrap this identity without including credential inputs.
//
// Example:
//
//	_, err := natsbroadcast.NewFrom(nil)
//	if errors.Is(err, natsbroadcast.ErrInvalidConfig) {
//		return err
//	}
var ErrInvalidConfig = errors.New("invalid NATS broadcast driver configuration")

var _ broadcast.PublisherDriver = (*Driver)(nil)
var _ broadcast.ConsumerDriver = (*Driver)(nil)

// Config is a diagnostic NATS connection record, not a constructor input; use
// Builder settings for connection, authentication, and routing policy. [Config.String],
// [Config.GoString], [Config.LogValue], and fmt verbs dispatched to [Config.Format]
// hide URL userinfo, Password, Token, and NKeySeed without changing the record.
// URL entries with parse errors or no authority hostname, including schemeless
// entries, are replaced wholesale. This affects diagnostics, not native dialing.
// Other fields and native objects are not recursively sanitized.
//
// The fields still contain raw values. Reading them, serializing the record, or
// converting it to a method-free type does not apply redaction. Go also bypasses
// all formatting methods for invalid %p on a Config value and invalid %w on a
// value or pointer: these format errors can expose fields even without a type
// conversion. For externally chosen fmt formats, pass record.String(), not record.
//
// Example:
//
//	record := natsbroadcast.Config{
//		Name:  "billing",
//		Token: "example",
//	}
//	slog.Info("NATS configuration", "config", record)
//
// WARNING: Credential fields (Username, Password, Token,
// NKeySeed) are stored as plain strings in memory for the
// lifetime of this struct. Callers should:
//  1. Always use TLS (set TLS) to protect credentials
//     in transit.
//  2. Load credentials from environment variables or a secret
//     manager rather than hard-coding them.
//  3. Prefer short-lived credentials or NKey/JWT auth via
//     CredentialsFile where supported.
type Config struct {
	// URLs is a list of NATS server URLs to connect to.
	// Multiple URLs enable automatic failover in clustered deployments.
	// If empty, defaults to DefaultNATSURL.
	// Redacted diagnostics detach this slice and hide all parsed userinfo. Entries
	// without an authority hostname or with parse errors are hidden completely,
	// including schemeless entries that remain valid for native connections.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{URLs: []string{natsbroadcast.DefaultNATSURL}}
	//	slog.Info("connection settings", "config", record)
	URLs []string

	// Name identifies this client connection in NATS server logs and
	// monitoring.
	// Useful for debugging and tracing connection issues.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{Name: "billing"}
	//	slog.Info("connection settings", "config", record)
	Name string

	// MaxReconnects is the maximum number of reconnection attempts. Zero
	// uses [DefaultNATSMaxReconnects] and -1 allows unlimited reconnects.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{MaxReconnects: 10}
	//	slog.Info("reconnect policy", "config", record)
	MaxReconnects int

	// NoReconnect disables reconnection entirely.
	// It takes precedence over MaxReconnects and ReconnectWait.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{NoReconnect: true}
	//	slog.Info("reconnect policy", "config", record)
	NoReconnect bool

	// ReconnectWait is the time to wait between reconnection attempts.
	// Defaults to DefaultNATSReconnectWait (2 seconds).
	//
	// Example:
	//
	//	record := natsbroadcast.Config{ReconnectWait: time.Second}
	//	slog.Info("reconnect policy", "config", record)
	ReconnectWait time.Duration

	// Timeout bounds native connection establishment, not Publish or Ping.
	// Zero uses the native default; negative values are invalid in New.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{Timeout: time.Second}
	//	slog.Info("connection settings", "config", record)
	Timeout time.Duration

	// Username is the username for basic authentication.
	// Used in combination with Password when the NATS server requires auth.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{Username: "billing"}
	//	slog.Info("authentication settings", "config", record)
	Username string

	// Password is the password for basic authentication.
	// Used in combination with Username when the NATS server requires auth.
	// Config's redacted diagnostics hide nonempty passwords; the field retains
	// plaintext. See Config for fmt paths that bypass its diagnostic methods.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{Password: os.Getenv("NATS_PASSWORD")}
	//	slog.Info("authentication settings", "config", record)
	Password string

	// Token is a bearer token for token-based authentication.
	// Alternative to username/password authentication.
	// Config's redacted diagnostics hide nonempty values, not erase them from
	// memory. See Config for fmt paths that bypass its diagnostic methods.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{Token: os.Getenv("NATS_TOKEN")}
	//	slog.Info("authentication settings", "config", record)
	Token string

	// NKeySeed is the seed for NKey authentication.
	// NKey provides cryptographic authentication without transmitting secrets.
	// This is encoded user seed material, never a filesystem path. Config's
	// redacted diagnostics hide the seed, but the string remains in memory for
	// nonce signing. See Config for fmt paths that bypass its diagnostic methods.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{NKeySeed: os.Getenv("NATS_NKEY_SEED")}
	//	slog.Info("authentication settings", "config", record)
	NKeySeed string

	// CredentialsFile is the path to a credentials file containing JWT and
	// NKey.
	// This is the recommended authentication method for production
	// deployments.
	// The native SDK loads the file during connection/authentication in Start.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{CredentialsFile: "/run/secrets/nats.creds"}
	//	slog.Info("authentication settings", "config", record)
	CredentialsFile string

	// RootCAs is a list of paths to root CA certificate files.
	// Used to verify the NATS server's certificate when using TLS.
	// The native SDK loads these files during Start, not New.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{RootCAs: []string{"/run/secrets/root.pem"}}
	//	slog.Info("TLS settings", "config", record)
	RootCAs []string

	// Prefix namespaces subjects, so Consume receives only Arkoren
	// broadcasts instead of every subject in the account. Defaults to
	// "arkoren".
	// It must be a literal NATS subject; native spelling is preserved.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{Prefix: "billing"}
	//	slog.Info("routing settings", "config", record)
	Prefix string

	// QueueGroup, when set, makes the replicas of one service share each
	// message instead of all receiving it: NATS delivers it to one member
	// of the group. Leave it empty for fan-out to every consumer.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{QueueGroup: "billing-workers"}
	//	slog.Info("routing settings", "config", record)
	QueueGroup string

	// TLS enables TLS encryption for the NATS connection.
	// Native configurations remain shared; configure them before concurrent use.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{TLS: &tls.Config{MinVersion: tls.VersionTLS12}}
	//	slog.Info("TLS settings", "config", record)
	TLS *tls.Config

	// Logger records recovered handler panics. Nil uses slog.Default.
	// The logger is shared and also receives envelope-handler failures.
	//
	// Example:
	//
	//	record := natsbroadcast.Config{Logger: slog.New(slog.DiscardHandler)}
	//	_ = record.Logger
	Logger *slog.Logger
}

// defaultConfig returns the default NATS broker configuration.
func defaultConfig() Config {
	return Config{
		URLs: []string{
			DefaultNATSURL,
		},
		MaxReconnects: DefaultNATSMaxReconnects,
		ReconnectWait: DefaultNATSReconnectWait,
	}
}

// Start connects to the NATS server. The connection attempt runs in the
// background so that ctx cancellation abandons it. Drivers created with
// [NewFrom] already hold a connection, so Start does nothing for them.
// Cancellation returns the context error and closes any connection obtained
// after cancellation. Native dialing may finish in the background.
//
// Example:
//
//	compose.Startup(driver.Start)
func (driver *Driver) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.conn != nil {
		if driver.owned {
			return ErrAlreadyStarted
		}

		return nil
	}

	conn, err := connect(ctx, driver.urls, driver.connectOptions)

	if err != nil {
		return err
	}

	driver.conn = conn

	return nil
}

// connect runs nats.Connect and abandons it when ctx ends first.
func connect(ctx context.Context, urls string, opts []nats.Option) (*nats.Conn, error) {
	type result struct {
		// conn is the connection to return or close after cancellation.
		conn *nats.Conn
		// err records the native connection failure, if any.
		err error
	}

	results := make(chan result, 1)
	go func() {
		conn, err := nats.Connect(urls, opts...)
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
		return outcome.conn, outcome.err
	}
}

// connection returns the NATS connection or ErrNotStarted.
func (driver *Driver) connection() (*nats.Conn, error) {
	driver.mu.RLock()
	defer driver.mu.RUnlock()

	if driver.conn == nil {
		return nil, ErrNotStarted
	}

	return driver.conn, nil
}

// natsURLs returns configured URLs or the default broker URL.
func natsURLs(urls []string) []string {
	if len(urls) == 0 {
		return []string{DefaultNATSURL}
	}

	return urls
}

// NewFrom creates a new NATS broker from an existing connection.
// This is useful when you need full control over connection creation or want
// to share a connection across multiple components. The caller retains
// ownership of conn. Only the Prefix, QueueGroup, and Logger settings are
// used. A nil connection returns ErrInvalidConfig; no borrowed driver dials.
// Callbacks run once in order and connection settings do not mutate conn.
//
// Example:
//
//	driver, err := natsbroadcast.NewFrom(conn, func(broker *natsbroadcast.Builder) {
//		broker.QueueGroup("billing-workers")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver.Connection()
func NewFrom(conn *nats.Conn, configure ...BuilderFunc) (*Driver, error) {
	config := configureDriver(configure...)

	if conn == nil {
		return nil, fmt.Errorf("%w: nil NATS connection", ErrInvalidConfig)
	}

	if err := validateRouting(config); err != nil {
		return nil, err
	}

	return &Driver{
		conn:       conn,
		prefix:     cmp.Or(config.Prefix, defaultPrefix),
		queueGroup: config.QueueGroup,
		logger:     eventLogger(config.Logger),
	}, nil
}

// validateConfig checks only ordinary settings, without evaluating native hooks
// or loading credential files. Borrowed constructors call validateRouting alone.
func validateConfig(config Config) error {
	if err := natsconfig.URLs(config.URLs); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}

	if config.Timeout < 0 || config.ReconnectWait < 0 {
		return fmt.Errorf("%w: Timeout and ReconnectWait must not be negative", ErrInvalidConfig)
	}

	return validateRouting(config)
}

// validateRouting validates the settings shared by owned and borrowed brokers.
// Native subject spelling, including uppercase inbox prefixes, is preserved.
func validateRouting(config Config) error {
	if !natsconfig.Subject(cmp.Or(config.Prefix, defaultPrefix)) {
		return fmt.Errorf("%w: Prefix must be a literal NATS subject", ErrInvalidConfig)
	}

	if strings.ContainsAny(config.QueueGroup, " \t\r\n") {
		return fmt.Errorf("%w: QueueGroup contains invalid native characters", ErrInvalidConfig)
	}

	return nil
}

// defaultPrefix namespaces subjects when Config.Prefix is empty.
const defaultPrefix = "arkoren"

// Publish JSON-encodes the complete broadcast envelope under prefix + name.
// It rejects invalid names and already-canceled contexts before native access.
// Native acceptance does not imply subscriber processing or retained delivery.
//
// Example:
//
//	if err := driver.Publish(ctx, broadcast.Message{Name: "users.created", Payload: []byte(`{}`)}); err != nil {
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

	conn, err := driver.connection()

	if err != nil {
		return err
	}

	return conn.Publish(driver.prefix+"."+message.Name, body)
}

// Subscribe registers a handler for events matching the given pattern.
// The event pattern supports portable wildcards:
//   - "*" matches a single token (e.g., "users.*.created")
//   - "#" matches zero or more trailing tokens (e.g., "users.#")
//
// The "#" wildcard is translated to an exact NATS subscription plus a '>'
// subscription because NATS '>' does not match zero tokens.
// Without QueueGroup, subscribers receive fan-out copies; a configured group
// instead load-balances each subject's messages among its members.
//
// Returns an unsubscribe function that removes this specific handler.
// The context is used only for the subscription setup, not for the handler
// lifecycle.
// Unsubscribe releases native registrations; callbacks receive borrowed bytes
// and must copy bytes they retain or mutate.
//
// Example:
//
//	unsubscribe, err := driver.Subscribe(ctx, "users.#", func(payload []byte) {
//		processRaw(payload)
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := broadcast.ValidatePattern(event); err != nil {
		return nil, err
	}

	conn, err := driver.connection()

	if err != nil {
		return nil, err
	}

	subjects := natsSubjects(event)

	for index, subject := range subjects {
		subjects[index] = driver.prefix + "." + subject
	}

	subs, err := driver.subscribeSubjects(conn, subjects, func(message *nats.Msg) {
		handler(message.Data)
	})

	if err != nil {
		return nil, err
	}

	return natsUnsubscribe(subs), nil
}

// subscribeSubjects creates a subscription for each translated subject. The
// callback retains the delivered subject, rather than the subscription pattern,
// so envelope consumers can validate transport identity before forwarding.
func (driver *Driver) subscribeSubjects(conn *nats.Conn, subjects []string, handler nats.MsgHandler) ([]*nats.Subscription, error) {
	subs := make([]*nats.Subscription, 0, len(subjects))

	for _, subject := range subjects {
		sub, err := driver.subscribeSubject(conn, subject, handler)

		if err != nil {
			cleanupNATSSubscriptions(subs)

			return nil, err
		}

		subs = append(subs, sub)
	}

	return subs, nil
}

// subscribeSubject registers a handler for one NATS subject or wildcard pattern.
// Queue subscriptions and ordinary subscriptions share identity preservation
// and panic isolation through messageHandler.
func (driver *Driver) subscribeSubject(conn *nats.Conn, subject string, handler nats.MsgHandler) (*nats.Subscription, error) {
	if driver.queueGroup != "" {
		return conn.QueueSubscribe(subject, driver.queueGroup, driver.messageHandler(handler))
	}

	return conn.Subscribe(subject, driver.messageHandler(handler))
}

// messageHandler isolates application panics while preserving the actual NATS
// message. The subscription pattern must never substitute for message.Subject:
// broad consumers need the exact broker-delivered identity for envelope checks.
func (driver *Driver) messageHandler(handler nats.MsgHandler) nats.MsgHandler {
	return func(message *nats.Msg) {
		defer driver.recoverHandlerPanic(message.Subject)

		handler(message)
	}
}

// recoverHandlerPanic logs a recovered NATS event handler panic.
func (driver *Driver) recoverHandlerPanic(subject string) {
	if recovered := recover(); recovered != nil {
		driver.logger.Error("panic in NATS event handler", "subject", subject, "panic", fmt.Sprint(recovered)) //nolint:sloglint // NATS subscription callbacks carry no context
	}
}

// natsUnsubscribe returns a function that unsubscribes every subscription.
func natsUnsubscribe(subs []*nats.Subscription) func() error {
	return func() error {
		return unsubscribeNATSSubscriptions(subs)
	}
}

// cleanupNATSSubscriptions unsubscribes subscriptions while ignoring cleanup errors.
func cleanupNATSSubscriptions(subs []*nats.Subscription) {
	for _, sub := range subs {
		// Setup already failed; preserve that failure while releasing earlier subscriptions.
		_ = sub.Unsubscribe()
	}
}

// unsubscribeNATSSubscriptions unsubscribes every subscription and joins errors.
func unsubscribeNATSSubscriptions(subs []*nats.Subscription) error {
	var errs []error

	for _, sub := range subs {
		errs = append(errs, sub.Unsubscribe())
	}

	return errors.Join(errs...)
}

// eventLogger returns the shared configured logger or slog.Default for nil.
func eventLogger(value *slog.Logger) *slog.Logger {
	if value == nil {
		return slog.Default()
	}

	return value
}

// Ping verifies that the NATS connection is still alive.
// It flushes a native ping with ctx; without a deadline it adds the native
// default timeout. Unstarted drivers return ErrNotStarted without I/O.
//
// Example:
//
//	compose.Check("nats", driver.Ping)
func (driver *Driver) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	conn, err := driver.connection()

	if err != nil {
		return err
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, nats.DefaultTimeout)
		defer cancel()
	}

	return conn.FlushWithContext(ctx)
}

// Stop drains and closes the NATS connection only when the broker created it.
// A broker created with [NewFrom] leaves its caller-owned connection
// open. Stop waits for native drain completion, including pending subscription
// callbacks, until ctx ends or the native drain timeout closes the connection.
// Cancellation forces Close and returns the context error. Forced close does
// not wait for application callbacks that ignore cancellation.
// A native drain timeout remains identifiable as nats.ErrDrainTimeout.
//
// Example:
//
//	compose.Cleanup(driver.Stop)
func (driver *Driver) Stop(ctx context.Context) error {
	driver.mu.RLock()
	conn := driver.conn
	driver.mu.RUnlock()

	if !driver.owned || conn == nil {
		return nil
	}

	return natsconn.Drain(ctx, conn)
}

// convertSubject converts event patterns to NATS subject format.
// It replaces the multi-level wildcard "#" with NATS's ">" wildcard.
// Single-level wildcards "*" are already compatible with NATS.
func convertSubject(event string) string {
	return strings.ReplaceAll(event, "#", ">")
}

// natsSubjects translates a portable pattern to one or more NATS subjects.
func natsSubjects(pattern string) []string {
	if pattern == "#" {
		return []string{
			">",
		}
	}

	if !strings.HasSuffix(pattern, "#") {
		return []string{
			convertSubject(pattern),
		}
	}

	prefix := strings.TrimSuffix(pattern, ".#")

	return []string{
		prefix, prefix + ".>",
	}
}

// MessagingSystem returns "nats", the messaging.system telemetry value that
// the queue and broadcast facades report for this driver.
// Calling it performs no I/O and does not enable telemetry by itself.
//
// Example:
//
//	slog.Info("broadcast transport", "system", driver.MessagingSystem())
func (driver *Driver) MessagingSystem() string {
	return "nats"
}
