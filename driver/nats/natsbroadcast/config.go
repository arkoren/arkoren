package natsbroadcast

import (
	"crypto/tls"
	"log/slog"
	"slices"
	"time"
)

// Builder configures one construction draft. New and NewFrom invoke supplied
// callbacks directly, once, in order; a nil callback panics. An expanded nil
// callback slice makes no changes. Retaining this draft does not permit changes
// to the constructed driver. Native clients, TLS configurations, loggers and
// SDK option captures retain their existing shared ownership.
//
// Example:
//
//	var configure natsbroadcast.BuilderFunc = func(broker *natsbroadcast.Builder) {
//		broker.URLs("nats://localhost:4222")
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
type Builder struct {
	// config holds draft settings, never the driver's published root.
	config Config
}

// BuilderFunc edits one fresh draft synchronously in argument order. Nil entries
// panic; expanded nil slices omit edits. Native objects remain shared.
type BuilderFunc = func(*Builder)

// URLs replaces and copies the server list; empty selects DefaultNATSURL.
// New validates supported native URLs after all callbacks without connecting.
// NewFrom ignores this connection setting. Publication detaches the slice again.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.URLs("nats://localhost:4222")
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) URLs(urls ...string) *Builder {
	broker.config.URLs = slices.Clone(urls)

	return broker
}

// Name sets the client name shown in native monitoring, returning the same draft.
// Empty leaves the native name unset. NewFrom ignores this connection setting;
// names are operational diagnostics and must not contain application secrets.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.Name("billing")
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) Name(name string) *Builder {
	broker.config.Name = name

	return broker
}

// MaxReconnects sets native reconnect attempts; zero uses the unlimited default.
// Native negative values also allow unlimited retries. NoReconnect takes
// precedence, and NewFrom ignores this connection-only setting.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.MaxReconnects(1)
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) MaxReconnects(maxReconnects int) *Builder {
	broker.config.MaxReconnects = maxReconnects

	return broker
}

// NoReconnect explicitly disables reconnection when true. False preserves the
// native reconnect policy, which defaults to unlimited attempts. This setting
// takes precedence over reconnect limits and is ignored by NewFrom.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.NoReconnect(true)
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) NoReconnect(noReconnect bool) *Builder {
	broker.config.NoReconnect = noReconnect

	return broker
}

// ReconnectWait sets the delay between native reconnect attempts. Zero restores
// DefaultNATSReconnectWait (two seconds); New rejects negative values after all
// callbacks. NewFrom ignores the setting and never reconfigures its connection.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.ReconnectWait(time.Second)
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) ReconnectWait(reconnectWait time.Duration) *Builder {
	broker.config.ReconnectWait = reconnectWait

	return broker
}

// Timeout bounds native connection establishment, not subsequent operations.
// Zero selects the native timeout; New rejects negative values. Publish and Ping
// use their own context semantics. NewFrom ignores this connection setting.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.Timeout(time.Second)
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) Timeout(timeout time.Duration) *Builder {
	broker.config.Timeout = timeout

	return broker
}

// Username sets basic authentication, used together with a nonempty Password.
// Omit both to leave native authentication unset. The string remains in memory;
// use TLS for transport. NewFrom ignores this connection setting.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.Username("billing")
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) Username(username string) *Builder {
	broker.config.Username = username

	return broker
}

// Password sets basic authentication when Username is also nonempty. The value
// stays in memory; Config formatting redacts it but cannot erase its source.
// Use TLS and load secrets externally. NewFrom ignores connection credentials.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.Password("example-password")
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) Password(password string) *Builder {
	broker.config.Password = password

	return broker
}

// Token sets native bearer authentication; empty leaves this mechanism unset.
// Config formatting hides the token, but the native option retains its string.
// Use TLS and external secret loading. NewFrom ignores connection credentials.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.Token("example-token")
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) Token(token string) *Builder {
	broker.config.Token = token

	return broker
}

// NKeySeed sets encoded native user seed material, not a filename. Empty omits
// NKey authentication. New validates the completed seed without file I/O and
// retains it for nonce signing; temporary signing keys are wiped. NewFrom
// ignores this setting. Use CredentialsFile for file-based credentials.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.NKeySeed(os.Getenv("NATS_NKEY_SEED"))
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) NKeySeed(nkeySeed string) *Builder {
	broker.config.NKeySeed = nkeySeed

	return broker
}

// CredentialsFile sets the native JWT/seed credential-file path. Empty omits
// this mechanism. The SDK reads the file during Start/authentication, not New,
// and may read it again on reconnect. NewFrom ignores this connection setting.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.CredentialsFile("/run/secrets/nats.creds")
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) CredentialsFile(credentialsFile string) *Builder {
	broker.config.CredentialsFile = credentialsFile

	return broker
}

// RootCAs replaces and copies root-certificate file paths. Empty leaves native
// trust selection unchanged. The SDK loads these files and enables TLS during
// Start; New performs no file I/O. NewFrom ignores this connection setting.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.RootCAs("/etc/ssl/certs/root.pem")
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) RootCAs(rootCAs ...string) *Builder {
	broker.config.RootCAs = slices.Clone(rootCAs)

	return broker
}

// Prefix sets the literal subject namespace used by both constructors. Empty
// selects "arkoren"; wildcards and empty tokens are invalid. Native spelling is
// preserved. This is routing separation, not broker authorization.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.Prefix("billing")
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) Prefix(prefix string) *Builder {
	broker.config.Prefix = prefix

	return broker
}

// QueueGroup selects competing subscribers within a native group. Empty keeps
// fan-out delivery; a nonempty group receives one member's copy per subject.
// Both constructors apply it without changing connection ownership.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.QueueGroup("billing-workers")
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) QueueGroup(queueGroup string) *Builder {
	broker.config.QueueGroup = queueGroup

	return broker
}

// TLS supplies the shared native TLS configuration for New-owned connections.
// Nil leaves native URL/RootCAs policy in control. Configure native values before
// sharing them; the driver does not clone TLS internals. NewFrom ignores it.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.TLS(&tls.Config{
//			MinVersion: tls.VersionTLS12,
//		})
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) TLS(tlsConfig *tls.Config) *Builder {
	broker.config.TLS = tlsConfig

	return broker
}

// Logger supplies the shared handler-error and recovered-panic logger. Nil uses
// slog.Default at construction; use slog.DiscardHandler to suppress records.
// Both constructors apply it without cloning or managing the logger's lifetime.
//
// Example:
//
//	configure := func(broker *natsbroadcast.Builder) {
//		broker.Logger(slog.Default())
//	}
//	driver, err := natsbroadcast.New(configure)
//	if err != nil {
//		return err
//	}
//	_ = driver
func (broker *Builder) Logger(logger *slog.Logger) *Builder {
	broker.config.Logger = logger

	return broker
}
