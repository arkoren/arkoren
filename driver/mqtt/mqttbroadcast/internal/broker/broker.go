// Package broker owns MQTT routing, subscription transitions, and lifecycle
// admission. All mutable native-manager and handler state has one owner here.
package broker

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"arkoren.dev/core/service/broadcast"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

// rawHandler receives uninterpreted bytes through the public Subscribe API.
// Envelope validation belongs to Consume, not to raw subscribers.
type rawHandler = func([]byte)

// topicHandler retains the actual MQTT publication topic alongside its bytes.
// Consume uses this transport identity before handing an envelope to core.
type topicHandler = func(string, []byte)

// defaultMaxConcurrentDeliveries bounds concurrent handler invocations.
const defaultMaxConcurrentDeliveries = 1024

// Driver implements [broadcast.PublisherDriver] and
// [broadcast.ConsumerDriver] using MQTT v5
// protocol for publish/subscribe messaging. It uses the Eclipse
// Paho Go client with automatic reconnection support. New uses clean sessions;
// NewFrom preserves the native manager's session configuration.
//
// Event names are automatically converted to MQTT topic format
// where dots become slashes and asterisks become plus signs for
// single-level wildcard matching. Hash symbols remain unchanged
// for multi-level wildcards.
//
// The broker supports fan-out messaging where multiple handlers
// can subscribe to the same topic and all will receive messages.
//
// Wildcard patterns: dots are converted to MQTT '/' separators, '*' is
// converted to MQTT '+' (single-level), and '#' matches multiple levels
// (must be the last token).
//
// New owns a clean, nonpersistent MQTT session; NewFrom borrows the caller's
// native session and its policy. Stop is terminal in both cases. The zero value
// is not usable; construct drivers with New or NewFrom. Delivery limits and
// acknowledgement behavior are described by HandlePublish and Subscribe.
//
// Example:
//
//	driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"})
//	if err != nil {
//		return err
//	}
//	publisher := broadcast.NewPublisher(driver)
//	_ = publisher // Start the driver and register Stop with its lifecycle owner.
type Driver struct {
	// lifecycle serializes route admission with Stop so no WaitGroup work is
	// added after shutdown begins waiting.
	lifecycle sync.Mutex
	// closed reports whether Stop has stopped delivery admission.
	closed bool

	// client is the autopaho connection manager with auto-reconnection. It is
	// nil until Start (or SetClient) and is guarded by lifecycle.
	client *autopaho.ConnectionManager
	// owned reports whether the driver creates client in Start and must
	// disconnect it in Stop.
	owned bool
	// clientConfig is the connection configuration Start passes to autopaho.
	clientConfig autopaho.ClientConfig
	// cancel stops the connection manager's internal context; it is set by
	// Start and called by Stop.
	cancel context.CancelFunc

	// qos is the quality of service level for publish and subscribe.
	qos byte

	// mu protects the handlers and subscriptions maps during
	// concurrent access.
	mu sync.RWMutex
	// subscriptionGate serializes broker requests with local subscription changes.
	// It is never held by delivery callbacks, which use mu only for snapshots.
	subscriptionGate chan struct{}

	// handlers stores topic-aware callbacks keyed by subscription topic and
	// handler ID. Raw subscribers and envelope consumers share these refcounts.
	handlers map[string]map[string]topicHandler

	// subscriptions tracks active MQTT broker subscriptions
	// by topic to enable proper cleanup and unsubscribe.
	subscriptions map[string]bool

	// nextID generates unique handler identifiers for
	// proper unsubscribe behavior.
	nextID atomic.Uint64

	// sem limits the number of concurrent delivery goroutines
	// to prevent resource exhaustion under high throughput.
	sem chan struct{}

	// work tracks admitted handler deliveries and reconnect restoration so Stop
	// can wait without allowing new work to race its Wait call.
	work sync.WaitGroup

	// logger records recovered handler panics and failed reconnect subscriptions.
	logger *slog.Logger
}

// ErrNotStarted reports that no connection manager is attached. New attaches
// one in Start; NewFrom(nil) requires SetClient after native hook configuration.
//
// Example:
//
//	if err := driver.Ping(ctx); errors.Is(err, mqttbroadcast.ErrNotStarted) {
//		return err // Start or attach the native manager before readiness checks.
//	}
var ErrNotStarted = errors.New("MQTT broadcast driver is not started")

// ErrAlreadyStarted reports a repeated Start on an owned, already-started
// manager. Borrowed managers have a no-op Start while the driver remains open.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		if errors.Is(err, mqttbroadcast.ErrAlreadyStarted) {
//			return err // Register startup once with the acquiring owner.
//		}
//		return err
//	}
var ErrAlreadyStarted = errors.New("MQTT broadcast driver is already started")

var _ broadcast.PublisherDriver = (*Driver)(nil)
var _ broadcast.ConsumerDriver = (*Driver)(nil)

// Config is a diagnostic record of MQTT settings: QoS level, keep
// alive, and authentication credentials. The broker URLs are not a field: they
// are the required first argument of [New].
// Formatting and slog redact Password. Config is not a constructor input;
// configure New and NewFrom through their construction Builder instead.
//
// Example:
//
//	record := mqttbroadcast.Config{
//		QoS:      1,
//		Password: "example",
//	}
//	slog.Info("MQTT configuration", "config", record)
//
// WARNING: Credential fields (Username, Password) are stored
// as plain strings in memory for the lifetime of this struct.
// Callers should:
//  1. Always use TLS (mqtts:// URLs) to protect credentials
//     in transit.
//  2. Load credentials from environment variables or a secret
//     manager rather than hard-coding them.
//  3. Consider short-lived or certificate-based credentials
//     where the broker supports them.
type Config struct {
	// QoS specifies the MQTT quality of service level: 0 (at most once),
	// 1 (at least once), or 2 (exactly once). The zero value is QoS 0;
	// New defaults to [DefaultMQTTQoS]; Builder.QoS(0) selects QoS 0.
	//
	// Example:
	//
	//	record := mqttbroadcast.Config{QoS: 0}
	//	_ = record.QoS
	QoS byte

	// Username is optional broker authentication data, empty by default.
	// Formatting preserves it; do not treat Config as safe for personal data.
	//
	// Example:
	//
	//	record := mqttbroadcast.Config{Username: "billing"}
	//	_ = record.Username
	Username string

	// Password is optional broker authentication data, empty by default.
	// Formatting redacts nonempty passwords, but direct field access does not.
	//
	// Example:
	//
	//	record := mqttbroadcast.Config{Password: "example"}
	//	_ = record.Password
	Password string

	// KeepAlive is the maximum interval between outbound MQTT control packets.
	// Zero selects 30 seconds; negative disables keepalive. Positive values must
	// be whole seconds in the range 1 second through 65535 seconds, inclusive.
	// This is the CONNECT request; a broker can override it in CONNACK.
	//
	// Example:
	//
	//	record := mqttbroadcast.Config{KeepAlive: 45 * time.Second}
	//	_ = record.KeepAlive
	KeepAlive time.Duration

	// Logger records recovered handler panics and reconnect subscription errors.
	// Nil selects slog.Default() at construction; the logger remains shared.
	//
	// Example:
	//
	//	record := mqttbroadcast.Config{Logger: slog.Default()}
	//	_ = record.Logger
	Logger *slog.Logger
}

// DefaultMQTTQoS is the default quality of service level used
// for MQTT publish and subscribe operations when not specified.
// Explicit Builder.QoS(0) selects at-most-once delivery instead.
//
// Example:
//
//	configure := func(broker *mqttbroadcast.Builder) {
//		broker.QoS(mqttbroadcast.DefaultMQTTQoS)
//	}
//	_ = configure
const DefaultMQTTQoS byte = 1

// DefaultMQTTKeepAlive is the default keep-alive interval in
// duration units used to maintain the MQTT connection. A zero builder setting
// restores this interval; a negative interval disables keepalive.
//
// Example:
//
//	configure := func(broker *mqttbroadcast.Builder) {
//		broker.KeepAlive(mqttbroadcast.DefaultMQTTKeepAlive)
//	}
//	_ = configure
const DefaultMQTTKeepAlive time.Duration = 30 * time.Second

// convertTopic converts an event name to MQTT topic format by
// replacing dots with slashes (topic separator) and asterisks
// with plus signs (single-level wildcard). Multi-level wildcards
// (hash) are left unchanged as they match MQTT conventions.
func convertTopic(event string) string {
	topic := strings.ReplaceAll(event, ".", "/")
	topic = strings.ReplaceAll(topic, "*", "+")

	return topic
}

// matchTopic checks if a message topic matches a subscription
// pattern, supporting MQTT wildcard semantics for single-level
// plus and multi-level hash wildcards.
func matchTopic(pattern, topic string) bool {
	if pattern == topic {
		return true
	}

	patternParts := strings.Split(pattern, "/")
	topicParts := strings.Split(topic, "/")

	return matchParts(patternParts, topicParts)
}

// matchParts recursively matches topic parts against pattern
// parts, handling plus for single-level and hash for multi-level
// wildcard matching according to MQTT topic filter rules.
func matchParts(pattern, topic []string) bool {
	if len(pattern) == 0 {
		return len(topic) == 0
	}

	if len(topic) == 0 {
		return pattern[0] == "#"
	}

	return matchNonEmptyParts(pattern, topic)
}

// matchNonEmptyParts matches a non-empty pattern against a non-empty topic.
func matchNonEmptyParts(pattern, topic []string) bool {
	if pattern[0] == "#" {
		return true
	}

	return matchPart(pattern, topic)
}

// matchPart matches one concrete or single-level wildcard topic part.
func matchPart(pattern, topic []string) bool {
	if pattern[0] == "+" {
		return matchParts(pattern[1:], topic[1:])
	}

	return pattern[0] == topic[0] && matchParts(pattern[1:], topic[1:])
}

// New creates a Driver that connects to the MQTT brokers at urls, using the
// construction callbacks for QoS level and authentication. Each URL has the form
// mqtt://host:port, or mqtts://host:port for TLS; several URLs enable
// automatic failover between brokers. The URLs have no safe default, so they
// are a required positional argument rather than a [Config] field.
//
// New rejects empty URLs, unsupported schemes, missing hosts, invalid ports,
// invalid QoS, URL userinfo, and unrepresentable keepalive durations without
// echoing credentials. MQTT/TCP/TLS URLs require a port and no path or query; ws/wss
// URLs may use their default ports and preserve paths and queries. No URL may
// contain a fragment. It performs no network IO: call [Driver.Start] to
// connect. The driver uses clean sessions and automatic reconnection.
// Callbacks run once in order before validation; nil entries panic.
//
// Example:
//
//	driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"}, func(broker *mqttbroadcast.Builder) {
//		broker.QoS(1)
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver.Client()
func New(urls []string, config Config) (*Driver, error) {
	urls = slices.Clone(urls)

	if len(urls) == 0 {
		return nil, errors.New("MQTT URLs are required")
	}

	broker, clientConfig, err := mqttClientConfig(urls, config)

	if err != nil {
		return nil, err
	}

	broker.clientConfig = clientConfig
	broker.owned = true

	return broker, nil
}

// Start creates the autopaho connection manager and waits until the first
// connection is established or ctx ends. The manager outlives ctx: it runs on
// an internal context that [Driver.Stop] cancels. For NewFrom, Start is a no-op;
// attach the borrowed manager before publishing or consuming.
// Start rejects an already canceled context, a stopped driver, or a repeated
// owned start. A failed connection wait cancels and retires the new manager.
//
// Example:
//
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
//	// The acquiring owner must later call driver.Stop with a cleanup context.
func (driver *Driver) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	client, err := driver.startClient(ctx)

	if err != nil || client == nil {
		return err
	}

	if err := client.AwaitConnection(ctx); err != nil {
		driver.retireClient(client)

		return err
	}

	return nil
}

// startClient publishes a newly created manager under the lifecycle lock, but
// never waits for connection while holding that lock. Paho's connection callback
// also admits work under lifecycle and must remain able to finish on cancellation.
// A borrowed driver returns nil because its native owner already started it.
func (driver *Driver) startClient(ctx context.Context) (*autopaho.ConnectionManager, error) {
	driver.lifecycle.Lock()
	defer driver.lifecycle.Unlock()

	if driver.closed {
		return nil, errors.New("mqtt broker is closed")
	}

	if !driver.owned {
		return nil, nil
	}

	if driver.client != nil {
		return nil, ErrAlreadyStarted
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	client, err := autopaho.NewConnection(runCtx, driver.clientConfig)

	if err != nil {
		cancel()

		return nil, err
	}

	driver.client = client
	driver.cancel = cancel

	return client, nil
}

// retireClient cancels an owned manager after a failed initial connection wait.
// Cancellation and native termination happen outside lifecycle, allowing an
// already-dispatched connection callback to complete rather than deadlocking.
func (driver *Driver) retireClient(client *autopaho.ConnectionManager) {
	driver.lifecycle.Lock()
	cancel := driver.cancel
	driver.lifecycle.Unlock()

	cancel()
	<-client.Done()

	driver.lifecycle.Lock()
	driver.client = nil
	driver.cancel = nil
	driver.lifecycle.Unlock()
}

// connection returns the shared native manager while the driver remains open.
// Missing managers return ErrNotStarted; stopped drivers never permit operations.
func (driver *Driver) connection() (*autopaho.ConnectionManager, error) {
	driver.lifecycle.Lock()
	defer driver.lifecycle.Unlock()

	if driver.closed {
		return nil, errors.New("mqtt broker is closed")
	}

	if driver.client == nil {
		return nil, ErrNotStarted
	}

	return driver.client, nil
}

// mqttClientConfig validates QoS, the wire keepalive interval, and broker URLs
// before publishing a fresh driver and its native configuration. It performs no
// network I/O; New marks the returned driver as owning its future manager.
func mqttClientConfig(values []string, config Config) (*Driver, autopaho.ClientConfig, error) {
	if err := validateQoS(config.QoS); err != nil {
		return nil, autopaho.ClientConfig{}, err
	}

	keepAlive, err := mqttKeepAlive(config.KeepAlive)

	if err != nil {
		return nil, autopaho.ClientConfig{}, err
	}

	urls, err := mqttURLs(values)

	if err != nil {
		return nil, autopaho.ClientConfig{}, err
	}

	broker := newMQTTBroker(config.QoS, config.Logger)

	return broker, mqttConnectionConfig(config, urls, keepAlive, broker), nil
}

// mqttKeepAlive converts the public duration policy to MQTT's unsigned seconds.
// Zero restores the default, negative disables, and positive values must fit
// exactly; rejecting fractions avoids silently changing the requested interval.
func mqttKeepAlive(value time.Duration) (uint16, error) {
	if value < 0 {
		return 0, nil
	}

	if value == 0 {
		value = DefaultMQTTKeepAlive
	}

	if value%time.Second != 0 || value > 65535*time.Second {
		return 0, errors.New("MQTT KeepAlive must be whole seconds from 1s through 65535s, " +
			"zero for default, or negative to disable")
	}

	return uint16(value / time.Second), nil
}

// validateQoS accepts only MQTT's three delivery levels. New validates at
// construction; NewFrom, whose signature has no error result, validates at use.
func validateQoS(qos byte) error {
	if qos > 2 {
		return errors.New("MQTT QoS must be 0, 1, or 2")
	}

	return nil
}

// mqttURLs parses and validates each broker address without returning its input
// in errors. net/url errors can contain credentials even when parsing failed,
// so syntax errors deliberately carry only the one-based position.
func mqttURLs(values []string) ([]*url.URL, error) {
	urls := make([]*url.URL, len(values))

	for index, value := range values {
		parsed, err := url.Parse(value)

		if err != nil {
			return nil, fmt.Errorf("invalid MQTT URL %d: malformed URL", index+1)
		}

		if err := validateMQTTURL(parsed); err != nil {
			return nil, fmt.Errorf("invalid MQTT URL %d: %w", index+1, err)
		}

		urls[index] = parsed
	}

	return urls, nil
}

// validateMQTTURL checks transport-specific syntax against pinned autopaho's
// supported schemes. TCP dialers do not infer ports or use paths and queries;
// WebSocket dialers do. Error text contains no caller-controlled URL values.
func validateMQTTURL(address *url.URL) error {
	if address.User != nil {
		return errors.New("URL credentials are not supported; use Username and Password")
	}

	switch strings.ToLower(address.Scheme) {
	case "mqtt", "tcp", "ssl", "tls", "mqtts", "mqtt+ssl", "tcps":
		if address.Path != "" || address.RawQuery != "" {
			return errors.New("TCP and TLS URLs cannot contain a path or query")
		}

		if address.Port() == "" {
			return errors.New("TCP and TLS URLs require a port")
		}
	case "ws", "wss":
		// WebSocket paths, queries, and default ports are meaningful to Paho.
	default:
		return errors.New("unsupported scheme")
	}

	if address.Fragment != "" {
		return errors.New("URL fragments are not supported")
	}

	return validateMQTTAddress(address)
}

// validateMQTTAddress requires an actual host and a usable numeric port when
// supplied. WebSocket URLs may omit the port to use their transport's default.
func validateMQTTAddress(address *url.URL) error {
	if address.Hostname() == "" {
		return errors.New("host is required")
	}

	if address.Port() == "" {
		return nil
	}

	port, err := strconv.ParseUint(address.Port(), 10, 16)

	if err != nil || port == 0 {
		return errors.New("port must be between 1 and 65535")
	}

	return nil
}

// newMQTTBroker allocates independent handler maps and admission gates without
// creating a native manager. The logger is shared, with nil resolved once here;
// New and NewFrom separately decide native ownership and attachment.
func newMQTTBroker(qos byte, value *slog.Logger) *Driver {
	return &Driver{
		qos:              qos,
		handlers:         make(map[string]map[string]topicHandler),
		subscriptions:    make(map[string]bool),
		sem:              make(chan struct{}, defaultMaxConcurrentDeliveries),
		subscriptionGate: make(chan struct{}, 1),
		logger:           brokerLogger(value),
	}
}

// mqttConnectionConfig describes New's nonpersistent session and installs its
// receive/reconnect callbacks without opening a connection. Parsed URLs belong
// to the construction path; the driver retains this private native configuration.
func mqttConnectionConfig(
	config Config,
	urls []*url.URL,
	keepAlive uint16,
	broker *Driver,
) autopaho.ClientConfig {
	clientConfig := autopaho.ClientConfig{
		ServerUrls:                    urls,
		KeepAlive:                     keepAlive,
		CleanStartOnInitialConnection: true,
		SessionExpiryInterval:         0,
		ClientID:                      "",
		OnPublishReceived:             []func(paho.PublishReceived) (bool, error){broker.HandlePublish},
		OnConnectionUp:                broker.HandleConnectionUp,
	}

	if config.Username != "" {
		clientConfig.ConnectUsername = config.Username
		clientConfig.ConnectPassword = []byte(config.Password)
	}

	return clientConfig
}

// NewFrom borrows an autopaho manager without lifecycle I/O or changing its
// session, authentication, keepalive, TLS, native loggers, or reconnect policy.
// Only Builder.QoS and Builder.Logger apply. Callbacks run directly once in
// order; nil entries panic. QoS defaults to 1; invalid levels are reported by
// Publish or Subscribe before contacting Paho because NewFrom returns no error.
// Stop clears this driver's registrations and attempts to unsubscribe its filters
// but never disconnects client. The native owner must disconnect after consumers
// and the driver stop. Avoid sharing the driver's filters with other native users.
//
// Install receive and connection hooks in ClientConfig before creating the
// manager: ConnectionManager has no setter for OnConnectionUp. Stage with nil,
// configure both HandlePublish and HandleConnectionUp, then attach the borrowed
// manager once with SetClient. Preserve existing native hooks when composing
// these callbacks; the example starts from a fresh native configuration.
//
// Example:
//
//	driver := mqttbroadcast.NewFrom(nil)
//	config := autopaho.ClientConfig{
//		ServerUrls: []*url.URL{{Scheme: "mqtt", Host: "localhost:1883"}},
//		OnConnectionUp: driver.HandleConnectionUp,
//		ClientConfig: paho.ClientConfig{
//			OnPublishReceived: []func(paho.PublishReceived) (bool, error){driver.HandlePublish},
//		},
//	}
//	manager, err := autopaho.NewConnection(ctx, config)
//	if err != nil {
//		return err
//	}
//	driver.SetClient(manager)
//	runErr := broadcast.NewConsumer(driver).Run(ctx)
//	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	defer cancel()
//	return errors.Join(runErr, driver.Stop(cleanup), manager.Disconnect(cleanup))
func NewFrom(client *autopaho.ConnectionManager, config Config) *Driver {
	driver := newMQTTBroker(config.QoS, config.Logger)
	driver.SetClient(client)

	return driver
}

// SetClient attaches the borrowed manager after configuring native callbacks.
// Use it once on a staged NewFrom(nil), before Publish, Subscribe, or Consume.
// It does not transfer ownership, install hooks, or reopen a stopped driver.
// Replacing a live manager or using SetClient on New's owned driver is unsupported.
//
// Example:
//
//	driver := mqttbroadcast.NewFrom(nil)
//	config := autopaho.ClientConfig{
//		ServerUrls: []*url.URL{{Scheme: "mqtt", Host: "localhost:1883"}},
//		OnConnectionUp: driver.HandleConnectionUp,
//		ClientConfig: paho.ClientConfig{
//			OnPublishReceived: []func(paho.PublishReceived) (bool, error){driver.HandlePublish},
//		},
//	}
//	manager, err := autopaho.NewConnection(ctx, config)
//	if err != nil {
//		return err
//	}
//	driver.SetClient(manager)
//	// The native owner disconnects manager after driver.Stop.
func (driver *Driver) SetClient(client *autopaho.ConnectionManager) {
	driver.lifecycle.Lock()
	defer driver.lifecycle.Unlock()

	driver.client = client
}

// HandlePublish is a paho OnPublishReceived callback that routes
// incoming MQTT messages to registered handlers. Callers using
// [NewFrom] must include this method in the paho
// ClientConfig.OnPublishReceived slice so that subscribed handlers
// receive messages.
//
// It returns true, nil after all admitted matching callbacks finish. With Paho's
// default automatic acknowledgements, QoS 1 acknowledgement follows callback
// completion; a native manager with manual acknowledgements retains that policy.
// Errors and recovered panics do not request broker retries. A full 1024-delivery
// limit blocks admission, so slow callbacks backpressure the native receive loop.
// Stop closes admission but does not forcibly cancel callbacks already admitted.
//
// Example:
//
//	driver := mqttbroadcast.NewFrom(nil)
//	config.ClientConfig.OnPublishReceived = append(config.ClientConfig.OnPublishReceived, driver.HandlePublish)
//	// Create config's native manager, then attach it with driver.SetClient.
func (driver *Driver) HandlePublish(pr paho.PublishReceived) (bool, error) {
	driver.route(pr.Packet)

	return true, nil
}

// HandleConnectionUp schedules restoration of active subscriptions after every
// connection, including reconnections. It returns without waiting for broker
// acknowledgements, as autopaho requires. New installs this hook automatically;
// NewFrom callers must install it before constructing the native manager. Stop
// prevents further restoration and waits for admitted restoration work until its
// context ends. Failed restoration is logged, not retried on the same connection.
//
// Example:
//
//	driver := mqttbroadcast.NewFrom(nil)
//	config := autopaho.ClientConfig{
//		OnConnectionUp: driver.HandleConnectionUp,
//	}
//	_ = config // Also wire HandlePublish and ServerUrls before NewConnection.
func (driver *Driver) HandleConnectionUp(manager *autopaho.ConnectionManager, _ *paho.Connack) {
	if !driver.admitWork(1) {
		return
	}

	go func() {
		defer driver.work.Done()

		driver.resubscribe(manager)
	}()
}

// resubscribe restores a snapshot while holding the subscription transition
// lock. An older restoration therefore cannot recreate a removed subscription.
// The timeout bounds Paho's acknowledgement wait, not arbitrary native I/O.
func (driver *Driver) resubscribe(manager *autopaho.ConnectionManager) {
	driver.subscriptionGate <- struct{}{}
	defer func() {
		<-driver.subscriptionGate
	}()

	if err := driver.open(); err != nil {
		return
	}

	driver.mu.RLock()
	options := make([]paho.SubscribeOptions, 0, len(driver.subscriptions))

	for topic := range driver.subscriptions {
		options = append(options, paho.SubscribeOptions{
			Topic: topic,
			QoS:   driver.qos,
		})
	}

	driver.mu.RUnlock()

	if len(options) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := manager.Subscribe(ctx, &paho.Subscribe{
		Subscriptions: options,
	}); err != nil {
		driver.logger.ErrorContext(ctx, "resubscribe after MQTT reconnect", "error", err)
	}
}

// route delivers an incoming MQTT message to all matching
// handlers based on topic pattern matching. This implements
// fan-out behavior where multiple handlers can receive the
// same message if they subscribed to matching patterns.
//
// Callbacks run concurrently with panic recovery, but this call waits for every
// admitted callback. Semaphore saturation backpressures dispatch and the native
// receive loop; it is not an asynchronous buffer or a drop policy.
func (driver *Driver) route(pb *paho.Publish) {
	handlers := driver.matchingHandlers(pb.Topic)

	if !driver.admitWork(len(handlers)) {
		return
	}

	driver.startRoutes(handlers, pb.Topic, pb.Payload)
}

// matchingHandlers snapshots handlers whose subscription patterns match topic.
// Callbacks run outside the map lock and receive the concrete publication topic,
// not the matching pattern, so envelope identity checks cannot use wildcards.
func (driver *Driver) matchingHandlers(topic string) []topicHandler {
	driver.mu.RLock()
	defer driver.mu.RUnlock()

	var matched []topicHandler

	for pattern, handlers := range driver.handlers {
		if matchTopic(pattern, topic) {
			matched = appendMQTTHandlers(matched, handlers)
		}
	}

	return matched
}

// appendMQTTHandlers adds every handler in source to destination without invoking
// it. The caller holds the handler-map read lock while constructing its snapshot.
func appendMQTTHandlers(destination []topicHandler, source map[string]topicHandler) []topicHandler {
	for _, handler := range source {
		destination = append(destination, handler)
	}

	return destination
}

// admitWork counts deliveries or reconnect tasks before Stop begins waiting.
// Admission and closure share lifecycle so no Add can race shutdown's Wait.
func (driver *Driver) admitWork(count int) bool {
	driver.lifecycle.Lock()
	defer driver.lifecycle.Unlock()

	if driver.closed {
		return false
	}

	driver.work.Add(count)

	return true
}

// startRoutes invokes each handler concurrently and waits for all of them. Every
// callback receives the actual topic and shared read-only payload under the same
// bounded admission policy, including callbacks registered through raw Subscribe.
func (driver *Driver) startRoutes(handlers []topicHandler, topic string, payload []byte) {
	var group sync.WaitGroup

	for _, handler := range handlers {
		driver.sem <- struct{}{}

		group.Go(func() {
			driver.routeDelivery(handler, topic, payload)
		})
	}

	group.Wait()
}

// routeDelivery passes transport identity and bytes to one admitted callback.
// It releases the route slot even if the callback panics; panic isolation remains
// per callback so a failing raw subscriber cannot stop an envelope consumer.
func (driver *Driver) routeDelivery(handler topicHandler, topic string, payload []byte) {
	defer driver.routeDone()
	defer driver.recoverRoutePanic()

	handler(topic, payload)
}

// routeDone releases one delivery slot and completes its previously admitted
// work count. Every routed callback defers it, including callbacks that panic.
func (driver *Driver) routeDone() {
	<-driver.sem
	driver.work.Done()
}

// recoverRoutePanic logs a recovered routed handler panic.
func (driver *Driver) recoverRoutePanic() {
	if recovered := recover(); recovered != nil {
		driver.logError("event handler panicked", "error", recovered)
	}
}

// brokerLogger resolves nil to slog.Default() when the driver is constructed.
// The selected logger remains shared; it does not configure Paho's own logging.
func brokerLogger(value *slog.Logger) *slog.Logger {
	if value == nil {
		return slog.Default()
	}

	return value
}

// logError records a broker error through its configured logger.
func (driver *Driver) logError(message string, args ...any) {
	brokerLogger(driver.logger).Error(message, args...) //nolint:sloglint // MQTT router callbacks carry no context
}

// Publish encodes message as a broadcast envelope and publishes it on the MQTT
// topic derived from its concrete name. It rejects an already canceled context
// before encoding or contacting Paho. QoS 1 and 2 wait for the protocol result;
// a failure reason is an error even when Paho returns no error. Success does not
// confirm application processing, and a timeout does not prove nondelivery.
//
// Example:
//
//	err := driver.Publish(ctx, broadcast.Message{
//		Name: "sensors.temperature",
//		Payload: []byte(`21`),
//	})
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

	if err := validateQoS(driver.qos); err != nil {
		return err
	}

	payload, err := json.Marshal(message)

	if err != nil {
		return err
	}

	client, err := driver.connection()

	if err != nil {
		return err
	}

	topic := convertTopic(message.Name)

	response, err := client.Publish(ctx, &paho.Publish{
		Topic:   topic,
		QoS:     driver.qos,
		Payload: payload,
		Properties: &paho.PublishProperties{
			ContentType: "application/octet-stream",
		},
	})

	if err != nil {
		return err
	}

	if response.ReasonCode >= 0x80 {
		return fmt.Errorf("MQTT publish rejected with reason code 0x%02x", response.ReasonCode)
	}

	return nil
}

// Consume receives envelopes through a broad MQTT subscription. Before invoking
// handler, it requires a valid concrete name whose dot-to-slash conversion
// exactly equals the actual MQTT topic; malformed and mismatched envelopes are
// silently dropped. This preserves broker topic ACLs, not payload authenticity.
// It returns the context error after cancellation and releases its subscription.
// A pre-canceled context performs no registration. Handler errors do not change
// acknowledgement policy; a callback already running must cooperate with ctx.
//
// Example:
//
//	err := driver.Consume(ctx, func(ctx context.Context, message broadcast.Message) error {
//		return process(ctx, message)
//	})
func (driver *Driver) Consume(ctx context.Context, handler broadcast.Handler) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if handler == nil {
		return errors.New("nil broadcast handler")
	}

	unsubscribe, err := driver.subscribe(ctx, "#", func(topic string, payload []byte) {
		var message broadcast.Message

		if json.Unmarshal(payload, &message) != nil || broadcast.ValidateName(message.Name) != nil ||
			topic != convertTopic(message.Name) {
			return
		}

		// Handler failures do not change the existing MQTT acknowledgement policy.
		_ = handler(ctx, message)
	})

	if err != nil {
		return err
	}

	defer func() {
		// Consumption has ended; unsubscription is best-effort cleanup.
		_ = unsubscribe()
	}()

	<-ctx.Done()

	return ctx.Err()
}

// Subscribe registers a handler to receive events with the given
// name or pattern. The event name is converted to MQTT topic
// format where dots become slashes and asterisks become plus
// signs for single-level wildcard matching. Hash symbols remain
// unchanged for multi-level wildcard matching.
//
// Multiple handlers can subscribe to the same topic and all will
// receive messages (fan-out). Each handler is tracked individually
// so unsubscribing one handler does not affect others.
//
// The returned unsubscribe function removes the specific handler
// and unsubscribes from the MQTT broker only when the last handler
// for the topic is removed. It is idempotent and returns the original result on
// repeated calls. Cleanup ignores registration cancellation and has a separate
// ten-second acknowledgement timeout; native writes can outlive that timeout.
// Subscription transitions serialize, including Stop and reconnect restoration.
//
// The callback receives raw bytes without envelope decoding or name validation;
// bytes are shared read-only across matching callbacks and valid only during
// callback execution. Copy before retaining or mutating them. Use Driver.Consume
// with a core consumer for validated envelopes. An already canceled ctx installs
// nothing, even when the filter has other handlers. Later cancellation alone
// does not remove this registration; call unsubscribe or Stop.
//
// Example:
//
//	unsubscribe, err := driver.Subscribe(ctx, "sensors.#", func(payload []byte) {
//		processRaw(payload)
//	})
//	if err != nil {
//		return err
//	}
//	// Keep the handler registered while notifications are needed.
//	return unsubscribe()
func (driver *Driver) Subscribe(
	ctx context.Context,
	event string,
	handler rawHandler,
) (func() error, error) {
	return driver.subscribe(ctx, event, func(_ string, payload []byte) {
		handler(payload)
	})
}

// subscribe registers a topic-aware callback using the same subscription and
// handler refcounts as Subscribe. Keeping transport identity here lets Consume
// validate envelopes without changing raw delivery or reconnect behavior.
func (driver *Driver) subscribe(
	ctx context.Context,
	event string,
	handler topicHandler,
) (func() error, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case driver.subscriptionGate <- struct{}{}:
	}

	defer func() {
		<-driver.subscriptionGate
	}()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := driver.open(); err != nil {
		return nil, err
	}

	if err := broadcast.ValidatePattern(event); err != nil {
		return nil, err
	}

	if err := validateQoS(driver.qos); err != nil {
		return nil, err
	}

	topic := convertTopic(event)
	handlerID := strconv.FormatUint(driver.nextID.Add(1), 10)

	if !driver.subscribeHandler(topic, handlerID, handler) {
		return driver.unsubscribeFunc(ctx, topic, handlerID), nil
	}

	if err := driver.subscribeTopic(ctx, topic); err != nil {
		return nil, err
	}

	driver.registerSubscribedHandler(topic, handlerID, handler)

	return driver.unsubscribeFunc(ctx, topic, handlerID), nil
}

// open reports whether the broker remains available for subscriptions.
func (driver *Driver) open() error {
	driver.lifecycle.Lock()
	defer driver.lifecycle.Unlock()

	if driver.closed {
		return errors.New("mqtt broker is closed")
	}

	return nil
}

// subscribeHandler registers a topic-aware callback when topic is already
// subscribed, returning false to avoid a duplicate broker subscription. A true
// result leaves registration to registerSubscribedHandler after broker setup.
func (driver *Driver) subscribeHandler(topic, handlerID string, handler topicHandler) bool {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	if !driver.subscriptions[topic] {
		return true
	}

	// Already subscribed at MQTT level; just append the handler.
	driver.handlersForTopic(topic)[handlerID] = handler

	return false
}

// subscribeTopic performs the first MQTT-level subscription for topic.
func (driver *Driver) subscribeTopic(ctx context.Context, topic string) error {
	client, err := driver.connection()

	if err != nil {
		return err
	}

	// First subscriber for this pattern: subscribe at the MQTT
	// broker level before registering the handler so that on
	// failure the handler is never visible to route().
	_, err = client.Subscribe(ctx, &paho.Subscribe{
		Subscriptions: []paho.SubscribeOptions{
			{
				Topic: topic,
				QoS:   driver.qos,
			},
		},
	})

	return err
}

// registerSubscribedHandler records an acknowledged subscription and callback.
// The caller holds subscriptionGate across the broker request and this mutation;
// route snapshots take only mu and never block waiting for acknowledgements.
func (driver *Driver) registerSubscribedHandler(topic, handlerID string, handler topicHandler) {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	driver.subscriptions[topic] = true
	driver.handlersForTopic(topic)[handlerID] = handler
}

// handlersForTopic returns the mutable callback map for topic, allocating it on
// first registration. The caller must hold the handler-map write lock.
func (driver *Driver) handlersForTopic(topic string) map[string]topicHandler {
	if driver.handlers[topic] == nil {
		driver.handlers[topic] = make(map[string]topicHandler)
	}

	return driver.handlers[topic]
}

// unsubscribeFunc returns an idempotent release of one registration. The final
// removal and broker request share the subscription transition lock so an older
// UNSUBSCRIBE cannot erase a replacement subscription. Repeated calls return the
// original result without touching newer registrations.
func (driver *Driver) unsubscribeFunc(
	ctx context.Context,
	topic string,
	handlerID string,
) func() error {
	return sync.OnceValue(func() error {
		driver.subscriptionGate <- struct{}{}
		defer func() {
			<-driver.subscriptionGate
		}()

		driver.mu.Lock()

		if _, registered := driver.handlers[topic][handlerID]; !registered {
			driver.mu.Unlock()

			return nil
		}

		delete(driver.handlers[topic], handlerID)
		shouldUnsubscribe := len(driver.handlers[topic]) == 0

		if shouldUnsubscribe {
			delete(driver.handlers, topic)
			delete(driver.subscriptions, topic)
		}

		driver.mu.Unlock()

		if shouldUnsubscribe {
			return driver.unsubscribeTopics(ctx, []string{topic})
		}

		return nil
	})
}

// unsubscribeTopics attempts cleanup independently of the registration or Stop
// context's cancellation, retaining its values. The fresh timeout bounds Paho's
// acknowledgement wait; a native socket write can still outlive that timeout.
func (driver *Driver) unsubscribeTopics(ctx context.Context, topics []string) error {
	if len(topics) == 0 {
		return nil
	}

	driver.lifecycle.Lock()
	client := driver.client
	driver.lifecycle.Unlock()

	if client == nil {
		return ErrNotStarted
	}

	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	_, err := client.Unsubscribe(cleanup, &paho.Unsubscribe{Topics: topics})

	return err
}

// Ping waits for an available connection or ctx cancellation, without issuing a
// broker PING request. It rejects a stopped driver or missing native manager.
// Availability does not establish that consumer subscriptions are ready.
//
// Example:
//
//	if err := driver.Ping(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	client, err := driver.connection()

	if err != nil {
		return err
	}

	return client.AwaitConnection(ctx)
}

// Stop permanently closes admission and waits for admitted deliveries and
// reconnect tasks until ctx ends. It always clears local registrations and
// attempts subscription release, even after a drain timeout. New's manager is
// canceled and disconnected; NewFrom's manager stays connected and only this driver's
// topic filters are unsubscribed. Do not share the same filters with native
// users: MQTT subscriptions belong to the client, not to individual callbacks.
//
// Borrowed-client unsubscription has a separate ten-second acknowledgement
// timeout, even for an expired ctx. Cleanup serializes with subscription RPCs;
// neither ctx nor Stop can forcibly interrupt arbitrary native I/O or handlers.
// Errors from draining and cleanup are joined. Call the native owner afterward
// if failed unsubscription requires closing its session. Cancel Consume contexts
// separately: Stop does not end their context-waiting loops.
//
// Example:
//
//	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	defer cancel()
//	if err := driver.Stop(cleanup); err != nil {
//		return err
//	}
func (driver *Driver) Stop(ctx context.Context) error {
	driver.lifecycle.Lock()
	driver.closed = true
	client := driver.client
	cancel := driver.cancel
	driver.lifecycle.Unlock()

	waitErr := waitGroupContext(ctx, &driver.work)

	if cancel != nil {
		cancel()
	}

	cleanupErr := driver.releaseSubscriptions(ctx)

	if driver.owned && client != nil {
		cleanupErr = errors.Join(cleanupErr, client.Disconnect(ctx))
	}

	return errors.Join(waitErr, cleanupErr)
}

// releaseSubscriptions detaches local callbacks even if broker cleanup fails.
// Serialization includes pending subscribe, unsubscribe, and reconnect requests,
// so no earlier operation can repopulate the maps after this method completes.
func (driver *Driver) releaseSubscriptions(ctx context.Context) error {
	driver.subscriptionGate <- struct{}{}
	defer func() {
		<-driver.subscriptionGate
	}()

	driver.mu.Lock()
	topics := make([]string, 0, len(driver.subscriptions))

	for topic := range driver.subscriptions {
		topics = append(topics, topic)
	}

	clear(driver.handlers)
	clear(driver.subscriptions)
	driver.mu.Unlock()

	if driver.owned {
		return nil
	}

	return driver.unsubscribeTopics(ctx, topics)
}

// waitGroupContext waits for admitted work, returning a preexisting context error
// without starting a waiter. Later cancellation stops only this wait; the waiter
// and callbacks finish when the group completes, not by forcibly stopping work.
func waitGroupContext(ctx context.Context, group *sync.WaitGroup) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	done := make(chan struct{})

	go func() {
		group.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// MessagingSystem returns "mqtt", the messaging.system telemetry value that
// the broadcast facades report for this driver. Native operations bypass facade
// telemetry; this method emits no metrics or events itself.
//
// Example:
//
//	driver := mqttbroadcast.NewFrom(nil)
//	if driver.MessagingSystem() != "mqtt" {
//		return errors.New("unexpected broadcast transport")
//	}
func (driver *Driver) MessagingSystem() string {
	return "mqtt"
}

// Client returns the shared manager without transferring lifecycle ownership.
func (driver *Driver) Client() *autopaho.ConnectionManager {
	driver.lifecycle.Lock()
	defer driver.lifecycle.Unlock()

	return driver.client
}

// Construction returns immutable construction metadata before startup. Native
// callbacks and the once-selected logger remain shared with the broker owner.
func (driver *Driver) Construction() (autopaho.ClientConfig, bool, byte, *slog.Logger) {
	return driver.clientConfig, driver.owned, driver.qos, driver.logger
}
