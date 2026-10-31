package natsqueue

import (
	"context"

	"github.com/nats-io/nats.go"
)

// Start connects to NATS when the driver owns its connection, then provisions
// or validates the configured JetStream resources.
// The context controls all resource requests, including provisioning. A failed
// owned start closes its new connection; a borrowed connection remains open.
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

	if driver.closed {
		return ErrDriverClosed
	}

	if driver.jetStream != nil {
		return ErrAlreadyStarted
	}

	connection := driver.connection
	dialed := false

	if connection == nil {
		var err error
		connection, err = connect(ctx, driver.config)

		if err != nil {
			return err
		}

		dialed = true
	}

	jetStream, err := prepareJetStream(ctx, connection, driver.config)

	if err != nil {
		if dialed {
			connection.Close()
		}

		return err
	}

	driver.connection = connection
	driver.jetStream = jetStream

	return nil
}

// Connection returns the shared native connection. New exposes nil until Start;
// NewFrom exposes its supplied connection immediately, even before resources
// are validated. Ownership is unchanged: Stop closes only New-owned connections.
// Native calls bypass queue routing, settlement guards, and facade telemetry.
//
// Example:
//
//	if conn := driver.Connection(); conn != nil {
//		slog.Info("NATS readiness", "connected", conn.IsConnected())
//	}
func (driver *Driver) Connection() *nats.Conn {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	return driver.connection
}
