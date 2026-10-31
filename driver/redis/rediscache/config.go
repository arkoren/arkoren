package rediscache

import "crypto/tls"

// Builder configures one construction draft. New invokes supplied
// callbacks directly, once, in order; a nil callback panics. An expanded nil
// callback slice makes no changes. Retaining this draft does not permit changes
// to the constructed driver. Native clients, TLS configurations, loggers and
// SDK option captures retain their existing shared ownership.
//
// Example:
//
//	var configure rediscache.BuilderFunc = func(store *rediscache.Builder) {
//		store.Network("tcp")
//	}
//	driver := rediscache.New(configure)
//	_ = driver.Client()
type Builder struct {
	// config holds draft settings, never the driver's published root.
	config Config
}

// BuilderFunc edits one New construction draft. Policies run synchronously once
// in order; supplied nil entries panic. A retained draft cannot change the
// published driver, while native TLS objects remain shared.
//
// Example:
//
//	var configure rediscache.BuilderFunc = func(store *rediscache.Builder) {
//		store.DB(1)
//	}
//	driver := rediscache.New(configure)
//	_ = driver.Client()
type BuilderFunc = func(*Builder)

// configureDriver applies construction policies before validation. It returns
// a detached value so a retained draft cannot mutate published settings.
func configureDriver(configure ...BuilderFunc) Config {
	cache := &Builder{
		config: defaultConfig(),
	}

	for _, configure := range configure {
		configure(cache)
	}

	return cache.config
}

// Network replaces the connection network and returns the same draft. Empty
// selects "tcp" after callbacks; "unix" uses Addr as a socket path. Native
// connection errors are reported when connecting, not during this scalar edit.
//
// Example:
//
//	configure := func(store *rediscache.Builder) {
//		store.Network("tcp")
//	}
//	driver := rediscache.New(configure)
//	_ = driver.Client()
func (store *Builder) Network(network string) *Builder {
	store.config.Network = network

	return store
}

// Addr replaces the server address and returns the same draft. Empty selects
// "localhost:6379" after callbacks. New creates no connection; Start or the first
// command reports dial and authentication errors.
//
// Example:
//
//	configure := func(store *rediscache.Builder) {
//		store.Addr("localhost:6379")
//	}
//	driver := rediscache.New(configure)
//	_ = driver.Client()
func (store *Builder) Addr(addr string) *Builder {
	store.config.Addr = addr

	return store
}

// Username replaces the draft's Username setting and returns the same draft.
// Defaults and validation are applied by the constructor after all callbacks.
// Native values remain shared; this method does not clone their internals.
//
// Example:
//
//	configure := func(store *rediscache.Builder) {
//		store.Username("billing")
//	}
//	driver := rediscache.New(configure)
//	_ = driver.Client()
func (store *Builder) Username(username string) *Builder {
	store.config.Username = username

	return store
}

// Password replaces the draft's Password setting and returns the same draft.
// Defaults and validation are applied by the constructor after all callbacks.
// Native values remain shared; this method does not clone their internals.
//
// Example:
//
//	configure := func(store *rediscache.Builder) {
//		store.Password("example-password")
//	}
//	driver := rediscache.New(configure)
//	_ = driver.Client()
func (store *Builder) Password(password string) *Builder {
	store.config.Password = password

	return store
}

// TLS replaces the draft's TLS setting and returns the same draft.
// Defaults and validation are applied by the constructor after all callbacks.
// Native values remain shared; this method does not clone their internals.
//
// Example:
//
//	configure := func(store *rediscache.Builder) {
//		store.TLS(&tls.Config{
//			MinVersion: tls.VersionTLS12,
//		})
//	}
//	driver := rediscache.New(configure)
//	_ = driver.Client()
func (store *Builder) TLS(tlsConfig *tls.Config) *Builder {
	store.config.TLS = tlsConfig

	return store
}

// DB replaces the Redis logical database number and returns the same draft.
// Omission selects database zero. The server validates availability when the
// client connects; this is not a Redis Cluster client.
//
// Example:
//
//	configure := func(store *rediscache.Builder) {
//		store.DB(1)
//	}
//	driver := rediscache.New(configure)
//	_ = driver.Client()
func (store *Builder) DB(db int) *Builder {
	store.config.DB = db

	return store
}
