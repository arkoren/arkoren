package server

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"

	"arkoren.dev/core/event"
)

// settings is the resolved server configuration. [New] fills zero
// timeouts and limits with secure defaults.
type settings struct {
	// Addr is the TCP address to listen on, such as ":8080" or
	// "127.0.0.1:9090". Defaults to ":8080".
	Addr string

	// TLS is the shared native TLS configuration. Nil serves plaintext HTTP;
	// a non-nil value is validated by Start before binding the listener.
	TLS *tls.Config

	// ReadHeaderTimeout limits the time allowed to read request headers,
	// protecting against Slowloris attacks. Defaults to 10s.
	ReadHeaderTimeout time.Duration

	// ReadTimeout limits the time to read the whole request, including the
	// body. Defaults to 30s.
	ReadTimeout time.Duration

	// WriteTimeout limits the time to write the response. Defaults to 60s. A
	// negative value disables the deadline for long-lived streams.
	WriteTimeout time.Duration

	// IdleTimeout limits the keep-alive idle time between requests. Defaults
	// to 120s.
	IdleTimeout time.Duration

	// MaxHeaderBytes limits the size of request headers. Defaults to 1 MB.
	MaxHeaderBytes int

	// ShutdownTimeout bounds native graceful draining in [Server.Serve]. Zero
	// selects 15s; a negative value expires the drain deadline immediately.
	// Hijacked connections and noncooperating handler goroutines are not joined.
	ShutdownTimeout time.Duration

	// Events receives [Listening] and [ShutdownTimedOut]. Nil, the default, publishes nothing.
	Events *event.Bus
}

// Builder configures New before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	listener := server.New(handler, func(listener *server.Builder) {
//		listener.Addr("127.0.0.1:8080")
//	})
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// defaultConfig holds the secure defaults applied by [New].
var defaultConfig = settings{
	Addr:              ":8080",
	ReadHeaderTimeout: 10 * time.Second,
	ReadTimeout:       30 * time.Second,
	WriteTimeout:      60 * time.Second,
	IdleTimeout:       120 * time.Second,
	MaxHeaderBytes:    1 << 20,
	ShutdownTimeout:   15 * time.Second,
}

// Addr is the TCP address to listen on, such as ":8080" or
// "127.0.0.1:9090". Defaults to ":8080".
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// In a [New] callback:
//
//	listener.Addr("127.0.0.1:8080")
func (listener *Builder) Addr(addr string) *Builder {
	listener.settings.Addr = addr

	return listener
}

// TLS selects HTTPS using config; nil restores the default plaintext HTTP.
// The native configuration, its certificates, and its callback captures remain
// shared, not cloned. Finish configuration before [Server.Start]; subsequent
// configuration mutation is unsupported. Runtime TLS callbacks may select
// certificates or return separately prepared configurations as crypto/tls allows.
//
// [New] performs no TLS I/O or validation. [Server.Start] rejects missing
// credentials and obviously invalid static certificates before binding. Callback
// results and full certificate suitability are checked during the handshake;
// callbacks are never invoked to validate construction or startup. HTTPS never
// falls back to plaintext.
//
// Example:
//
//	srv := server.New(handler, func(listener *server.Builder) {
//		listener.TLS(&tls.Config{
//			Certificates: []tls.Certificate{identity},
//			MinVersion:   tls.VersionTLS12,
//		})
//	})
//	if err := srv.Start(ctx); err != nil {
//		return err
//	}
func (listener *Builder) TLS(config *tls.Config) *Builder {
	listener.settings.TLS = config

	return listener
}

// ReadHeaderTimeout limits the time allowed to read request headers,
// protecting against Slowloris attacks. Defaults to 10s.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// In a [New] callback:
//
//	listener.ReadHeaderTimeout(time.Second)
func (listener *Builder) ReadHeaderTimeout(timeout time.Duration) *Builder {
	listener.settings.ReadHeaderTimeout = timeout

	return listener
}

// ReadTimeout limits the time to read the whole request, including the
// body. Defaults to 30s.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// In a [New] callback:
//
//	listener.ReadTimeout(time.Second)
func (listener *Builder) ReadTimeout(timeout time.Duration) *Builder {
	listener.settings.ReadTimeout = timeout

	return listener
}

// WriteTimeout limits the time to write the response. Defaults to 60s. A
// negative value disables the deadline for long-lived streams.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// In a [New] callback:
//
//	listener.WriteTimeout(time.Second)
func (listener *Builder) WriteTimeout(timeout time.Duration) *Builder {
	listener.settings.WriteTimeout = timeout

	return listener
}

// IdleTimeout limits the keep-alive idle time between requests. Defaults
// to 120s.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// In a [New] callback:
//
//	listener.IdleTimeout(time.Second)
func (listener *Builder) IdleTimeout(timeout time.Duration) *Builder {
	listener.settings.IdleTimeout = timeout

	return listener
}

// MaxHeaderBytes limits the size of request headers. Defaults to 1 MB.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// In a [New] callback:
//
//	listener.MaxHeaderBytes(1 << 20)
func (listener *Builder) MaxHeaderBytes(size int) *Builder {
	listener.settings.MaxHeaderBytes = size

	return listener
}

// ShutdownTimeout bounds native graceful draining in [Server.Serve]. Zero
// selects 15s; a negative value expires the drain deadline immediately. A timeout
// closes tracked HTTP connections, not hijacked connections or arbitrary handler
// goroutines. [Server.Stop] instead uses the context supplied by its caller.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// In a [New] callback:
//
//	listener.ShutdownTimeout(time.Second)
func (listener *Builder) ShutdownTimeout(timeout time.Duration) *Builder {
	listener.settings.ShutdownTimeout = timeout

	return listener
}

// Events receives [Listening] and [ShutdownTimedOut]. Nil, the default, publishes nothing.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// In a [New] callback:
//
//	listener.Events(bus)
func (listener *Builder) Events(bus *event.Bus) *Builder {
	listener.settings.Events = bus

	return listener
}

// Server is an HTTP server with a separate bind step, so an unavailable
// address fails startup before serving begins. Register it on an
// application composer as startup work, acquired cleanup, and a runner:
//
//	srv := server.New(handler)
//	compose.Cleanup(srv.Stop)
//	compose.Startup(srv.Start)
//	compose.Run(srv.Serve)
type Server struct {
	// http is the underlying server.
	http *http.Server

	// shutdownTimeout bounds the graceful drain in Serve.
	shutdownTimeout time.Duration

	// events receives [Listening] and [ShutdownTimedOut]. Nil publishes nothing.
	events *event.Bus

	// mu guards the fields below.
	mu sync.Mutex

	// listener is the bound listener. It is nil before Start.
	listener net.Listener

	// serving is set once Serve has taken ownership of the listener.
	serving bool

	// shuttingDown is closed once shutdown begins. It is carried by every
	// request context, see [ShuttingDown].
	shuttingDown chan struct{}

	// signalOnce makes closing shuttingDown safe from Serve, Stop, and the
	// http.Server shutdown hook.
	signalOnce sync.Once
}

// New creates a [Server] serving handler with completed construction settings.
// Callbacks run directly once in order; nil entries panic. Zero limits and
// timeouts receive secure defaults after the final callback. The server retains
// independent settings, not the draft. It does not bind; call [Server.Start].
// Plaintext HTTP is the default; [Builder.TLS] opts into HTTPS. Native TLS
// configuration remains shared and is validated at Start, not construction.
//
// Example:
//
//	listener := server.New(handler, func(listener *server.Builder) {
//		listener.Addr("127.0.0.1:8080")
//	})
func New(handler http.Handler, configure ...BuilderFunc) *Server {
	config := newSettings(configure...)

	server := &Server{
		http: &http.Server{
			Addr:              config.Addr,
			Handler:           handler,
			TLSConfig:         config.TLS,
			ReadHeaderTimeout: config.ReadHeaderTimeout,
			ReadTimeout:       config.ReadTimeout,
			WriteTimeout:      max(config.WriteTimeout, 0),
			IdleTimeout:       config.IdleTimeout,
			MaxHeaderBytes:    config.MaxHeaderBytes,
		},
		shutdownTimeout: config.ShutdownTimeout,
		events:          config.Events,
		shuttingDown:    make(chan struct{}),
	}
	server.http.BaseContext = func(net.Listener) context.Context {
		return context.WithValue(context.Background(), shuttingDownKey{}, server.shuttingDown)
	}
	// Covers a caller that shuts the underlying http.Server down directly.
	server.http.RegisterOnShutdown(server.signalShutdown)

	return server
}

// HTTP returns the shared underlying [http.Server] for native configuration.
// Set its fields, including TLSConfig, before [Server.Start]. Configuration
// mutation after Start is unsupported. A non-nil TLSConfig selects HTTPS just
// as [Builder.TLS] does; nil selects plaintext HTTP.
//
// Example:
//
//	srv := server.New(handler)
//	srv.HTTP().TLSConfig = &tls.Config{Certificates: []tls.Certificate{identity}}
func (server *Server) HTTP() *http.Server {
	return server.http
}

// Addr returns the bound listener address, or the configured address when
// the server has not started. After [Server.Start], a port-zero address reports
// the operating system's chosen port; reading it does not start the server.
//
// Example:
//
//	srv := server.New(http.NotFoundHandler(), func(listener *server.Builder) {
//		listener.Addr("127.0.0.1:8080")
//	})
//	fmt.Println(srv.Addr())
func (server *Server) Addr() string {
	server.mu.Lock()
	defer server.mu.Unlock()

	if server.listener == nil {
		return server.http.Addr
	}

	return server.listener.Addr().String()
}
