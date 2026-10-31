package natsbroadcast

import (
	"github.com/nats-io/nats.go"

	"arkoren.dev/driver/nats/natsbroadcast/internal/policy"
)

// natsOptions builds connection options without changing native precedence.
func natsOptions(config Config) ([]nats.Option, error) {
	return policy.Options(policy.Config{
		Name: config.Name, NoReconnect: config.NoReconnect,
		MaxReconnects: config.MaxReconnects, ReconnectWait: config.ReconnectWait,
		Timeout: config.Timeout, Username: config.Username, Password: config.Password,
		Token: config.Token, NKeySeed: config.NKeySeed, CredentialsFile: config.CredentialsFile,
		TLS: config.TLS, RootCAs: config.RootCAs,
	}, DefaultNATSMaxReconnects, DefaultNATSReconnectWait)
}

// Connection returns the shared native connection. New returns nil until Start;
// NewFrom exposes its supplied connection immediately. The accessor transfers
// no ownership: do not close a New-owned connection outside Stop, and keep a
// borrowed connection alive for the driver's lifetime. Native access bypasses
// envelope validation and facade telemetry.
//
// Example:
//
//	if conn := driver.Connection(); conn != nil {
//		slog.Info("NATS readiness", "connected", conn.IsConnected())
//	}
func (driver *Driver) Connection() *nats.Conn {
	driver.mu.RLock()
	defer driver.mu.RUnlock()

	return driver.conn
}
