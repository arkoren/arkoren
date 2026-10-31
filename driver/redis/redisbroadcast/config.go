package redisbroadcast

import (
	"crypto/tls"
	"log/slog"
)

// Builder configures one construction draft. New and NewFrom invoke supplied
// callbacks directly, once, in order; a nil callback panics. An expanded nil
// callback slice makes no changes. Retaining this draft does not permit changes
// to the constructed driver. Native clients, TLS configurations, loggers and
// SDK option captures retain their existing shared ownership.
//
// Example:
//
//	var configure redisbroadcast.BuilderFunc = func(broker *redisbroadcast.Builder) {
//		broker.Network("tcp")
//	}
//	driver := redisbroadcast.New(configure)
//	_ = driver.Client()
type Builder struct {
	// config holds draft settings, never the driver's published root.
	config Config
}

// BuilderFunc edits one New or NewFrom construction draft. Policies run once,
// synchronously in order; supplied nil entries panic. NewFrom applies only Prefix
// and Logger. Retained drafts cannot change the published scalar settings.
//
// Example:
//
//	var configure redisbroadcast.BuilderFunc = func(broker *redisbroadcast.Builder) {
//		broker.Prefix("billing:")
//	}
//	driver := redisbroadcast.New(configure)
//	_ = driver.Client()
type BuilderFunc = func(*Builder)

// configureDriver applies construction policies before validation. It returns
// a detached value so a retained draft cannot mutate published settings.
func configureDriver(configure ...BuilderFunc) Config {
	broadcast := &Builder{
		config: defaultConfig(),
	}

	for _, configure := range configure {
		configure(broadcast)
	}

	return broadcast.config
}

// Network replaces the connection network and returns the same draft. Empty
// selects "tcp" after callbacks; "unix" uses Addr as a socket path. NewFrom
// ignores this setting and preserves the native client's configuration.
//
// Example:
//
//	configure := func(broker *redisbroadcast.Builder) {
//		broker.Network("tcp")
//	}
//	driver := redisbroadcast.New(configure)
//	_ = driver.Client()
func (broker *Builder) Network(network string) *Builder {
	broker.config.Network = network

	return broker
}

// Addr replaces the server address and returns the same draft. Empty selects
// "localhost:6379" after callbacks. New reports connection failures at first I/O;
// NewFrom ignores this setting rather than mutating a borrowed client.
//
// Example:
//
//	configure := func(broker *redisbroadcast.Builder) {
//		broker.Addr("localhost:6379")
//	}
//	driver := redisbroadcast.New(configure)
//	_ = driver.Client()
func (broker *Builder) Addr(addr string) *Builder {
	broker.config.Addr = addr

	return broker
}

// Username replaces the draft's Username setting and returns the same draft.
// Defaults and validation are applied by the constructor after all callbacks.
// Native values remain shared; this method does not clone their internals.
//
// Example:
//
//	configure := func(broker *redisbroadcast.Builder) {
//		broker.Username("billing")
//	}
//	driver := redisbroadcast.New(configure)
//	_ = driver.Client()
func (broker *Builder) Username(username string) *Builder {
	broker.config.Username = username

	return broker
}

// Password replaces the draft's Password setting and returns the same draft.
// Defaults and validation are applied by the constructor after all callbacks.
// Native values remain shared; this method does not clone their internals.
//
// Example:
//
//	configure := func(broker *redisbroadcast.Builder) {
//		broker.Password("example-password")
//	}
//	driver := redisbroadcast.New(configure)
//	_ = driver.Client()
func (broker *Builder) Password(password string) *Builder {
	broker.config.Password = password

	return broker
}

// TLS replaces the draft's TLS setting and returns the same draft.
// Defaults and validation are applied by the constructor after all callbacks.
// Native values remain shared; this method does not clone their internals.
//
// Example:
//
//	configure := func(broker *redisbroadcast.Builder) {
//		broker.TLS(&tls.Config{
//			MinVersion: tls.VersionTLS12,
//		})
//	}
//	driver := redisbroadcast.New(configure)
//	_ = driver.Client()
func (broker *Builder) TLS(tlsConfig *tls.Config) *Builder {
	broker.config.TLS = tlsConfig

	return broker
}

// DB replaces the native logical database number and returns the same draft.
// Omission selects zero; NewFrom ignores the setting. Redis Pub/Sub ignores
// database numbers, so use disjoint prefixes and ACLs for channel separation.
//
// Example:
//
//	configure := func(broker *redisbroadcast.Builder) {
//		broker.DB(1)
//	}
//	driver := redisbroadcast.New(configure)
//	_ = driver.Client()
func (broker *Builder) DB(db int) *Builder {
	broker.config.DB = db

	return broker
}

// Prefix replaces the draft's Prefix setting and returns the same draft.
// Defaults and validation are applied by the constructor after all callbacks.
// Native values remain shared; this method does not clone their internals.
//
// Example:
//
//	configure := func(broker *redisbroadcast.Builder) {
//		broker.Prefix("billing")
//	}
//	driver := redisbroadcast.New(configure)
//	_ = driver.Client()
func (broker *Builder) Prefix(prefix string) *Builder {
	broker.config.Prefix = prefix

	return broker
}

// Logger replaces the draft's Logger setting and returns the same draft.
// Defaults and validation are applied by the constructor after all callbacks.
// Native values remain shared; this method does not clone their internals.
//
// Example:
//
//	configure := func(broker *redisbroadcast.Builder) {
//		broker.Logger(slog.Default())
//	}
//	driver := redisbroadcast.New(configure)
//	_ = driver.Client()
func (broker *Builder) Logger(logger *slog.Logger) *Builder {
	broker.config.Logger = logger

	return broker
}
