package mqttbroadcast

import (
	"context"
	"log/slog"

	"github.com/eclipse/paho.golang/autopaho"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/driver/mqtt/mqttbroadcast/internal/broker"
)

// rawHandler receives borrowed, read-only bytes without envelope validation.
type rawHandler = func([]byte)

// Driver implements broadcast publishing and consumption over MQTT v5. It
// retains construction metadata; its private broker exclusively owns mutable
// routing, subscriptions, native-manager state, and lifecycle synchronization.
// New owns a clean session; NewFrom borrows native session policy. Stop is
// terminal. The zero value is invalid: construct with New or NewFrom.
//
// Example:
//
//	driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"})
//	if err != nil {
//		return err
//	}
//	_ = driver.Client()
type Driver struct {
	// broker exclusively owns all mutable transport and routing state.
	broker *broker.Driver
	// clientConfig retains the completed construction record, never live state.
	clientConfig autopaho.ClientConfig
	// owned records immutable construction ownership, not attachment state.
	owned bool
	// qos retains the published immutable delivery policy.
	qos byte
	// logger retains the shared construction logger.
	logger *slog.Logger
}

// ErrNotStarted reports that no native manager is attached.
//
// Example:
//
//	_ = mqttbroadcast.ErrNotStarted
var ErrNotStarted = broker.ErrNotStarted

// ErrAlreadyStarted reports repeated owned startup.
//
// Example:
//
//	_ = mqttbroadcast.ErrAlreadyStarted
var ErrAlreadyStarted = broker.ErrAlreadyStarted

var _ broadcast.PublisherDriver = (*Driver)(nil)
var _ broadcast.ConsumerDriver = (*Driver)(nil)

// Start creates an owned manager and awaits its first connection. The manager
// outlives ctx; Stop cancels it. Borrowed startup is a no-op. Cancellation of
// initial connection waiting retires and joins the newly acquired manager.
//
// Example:
//
//	return driver.Start(ctx)
func (driver *Driver) Start(ctx context.Context) error {
	return driver.broker.Start(ctx)
}

// Ping waits for connection availability, not subscription readiness. It
// rejects cancellation, stopped drivers, and missing managers before waiting.
//
// Example:
//
//	return driver.Ping(ctx)
func (driver *Driver) Ping(ctx context.Context) error {
	return driver.broker.Ping(ctx)
}

// Stop closes admission and drains admitted handlers and reconnect work until
// ctx ends. It always clears registrations and attempts subscription release.
// Owned managers are canceled and disconnected; borrowed managers remain open.
// Borrowed unsubscribe has a separate ten-second acknowledgement timeout and
// serializes with pending subscription requests. Native writes and handlers can
// outlive cancellation. Drain and cleanup errors are joined. Cancel Consume
// contexts separately; Stop does not end their context-waiting loops.
//
// Example:
//
//	return driver.Stop(ctx)
func (driver *Driver) Stop(ctx context.Context) error {
	return driver.broker.Stop(ctx)
}

// Client returns the shared native manager before and after Stop, or nil before
// startup or attachment. Native operations bypass envelope checks and telemetry.
// Ownership is unchanged: Stop disconnects New's manager, not NewFrom's manager.
//
// Example:
//
//	_ = driver.Client()
func (driver *Driver) Client() *autopaho.ConnectionManager {
	return driver.broker.Client()
}

// MessagingSystem returns the unchanged facade telemetry identity "mqtt".
//
// Example:
//
//	_ = driver.MessagingSystem()
func (driver *Driver) MessagingSystem() string {
	return "mqtt"
}
