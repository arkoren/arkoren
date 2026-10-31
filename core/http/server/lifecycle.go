package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// Start binds the TCP listener and publishes [Listening] when [Builder.Events]
// is set. It does not serve. Starting twice returns an error.
// Non-nil [http.Server.TLSConfig] selects HTTPS: missing credentials and
// obviously invalid static certificates fail before binding or publishing.
// TLS callbacks are not called here; handshake-time errors remain possible.
// Do not mutate server or TLS configuration after Start.
//
// Example:
//
//	if err := srv.Start(ctx); err != nil {
//		return err
//	}
func (server *Server) Start(ctx context.Context) error {
	addr, err := server.bind(ctx)

	if err != nil {
		return err
	}

	server.events.Emit(ctx, Listening, ListeningEvent{
		Addr: addr,
	})

	return nil
}

// bind binds the listener and returns its address.
func (server *Server) bind(ctx context.Context) (string, error) {
	server.mu.Lock()
	defer server.mu.Unlock()

	if server.listener != nil {
		return "", errors.New("http server: already started")
	}

	if err := validateTLS(server.http.TLSConfig); err != nil {
		return "", fmt.Errorf("http server: tls: %w", err)
	}

	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", server.http.Addr)

	if err != nil {
		return "", fmt.Errorf("http server: %w", err)
	}

	server.listener = listener

	return listener.Addr().String(), nil
}

// Serve serves requests on the bound listener until ctx ends, then asks net/http
// to drain for at most [Builder.ShutdownTimeout]. An expired drain deadline
// closes remaining tracked HTTP connections, publishes [ShutdownTimedOut], and
// returns an error matching context.DeadlineExceeded. Hijacked connections are
// not closed or awaited, and arbitrary handler goroutines cannot be forcibly
// stopped. Their owners must coordinate completion before releasing dependencies.
// Serve returns nil after normal shutdown and an error when [Server.Start] has
// not run or serving fails. HTTPS uses [http.Server.ServeTLS] with in-memory
// credentials, retaining native HTTP/2 negotiation and TLS callbacks; no
// certificate files are loaded.
//
// Example:
//
//	if err := srv.Serve(ctx); err != nil {
//		return err
//	}
func (server *Server) Serve(ctx context.Context) error {
	server.mu.Lock()

	listener := server.listener
	server.serving = listener != nil
	server.mu.Unlock()

	if listener == nil {
		return errors.New("http server: not started")
	}

	served := make(chan error, 1)

	go func() {
		served <- server.serveListener(listener)
	}()

	select {
	case err := <-served:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}

		return nil
	case <-ctx.Done():
	}

	shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), server.shutdownTimeout)
	defer cancel()

	err := server.shutdown(shutdownContext)

	if errors.Is(err, context.DeadlineExceeded) {
		server.events.Emit(shutdownContext, ShutdownTimedOut, ShutdownTimedOutEvent{
			Addr:    server.Addr(),
			Timeout: server.shutdownTimeout,
		})
	}

	<-served

	return err
}

// serveListener owns the bound listener while serving the selected protocol.
// ServeTLS can fail during setup before net/http takes ownership, so the
// listener is closed even on that early failure. A TLS error never starts HTTP.
func (server *Server) serveListener(listener net.Listener) error {
	defer func() {
		// Serve normally closes it first; repeated listener closes are harmless.
		_ = listener.Close()
	}()

	if server.http.TLSConfig != nil {
		return server.http.ServeTLS(listener, "", "")
	}

	return server.http.Serve(listener)
}
