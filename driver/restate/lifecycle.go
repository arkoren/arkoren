package restate

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/restatedev/sdk-go/server"
)

// Start builds the endpoint and opens the listener. It does not serve; call
// [Host.Serve] for that. A client-only host, or a host with no bound
// definitions, opens no listener. A host that would listen returns
// [ErrNoIdentityKeys] unless [Builder.IdentityKeys] is set or
// [Builder.AllowInsecure] is true.
// A successfully opened listener belongs to Stop even if Serve never runs.
//
// Example:
//
//	host := New() // With no bound services this starts as a client only.
//	if err := host.Start(context.Background()); err != nil {
//		panic(err)
//	}
func (host *Host) Start(ctx context.Context) error {
	host.mu.Lock()
	defer host.mu.Unlock()

	if host.started {
		return fmt.Errorf("start: %w", ErrStarted)
	}

	if host.config.ClientOnly || len(host.definitions) == 0 {
		host.started = true

		return nil
	}

	if len(host.config.IdentityKeys) == 0 && !host.config.AllowInsecure {
		return fmt.Errorf("start: %w", ErrNoIdentityKeys)
	}

	endpoint := server.NewRestate()

	if len(host.config.IdentityKeys) > 0 {
		endpoint = endpoint.WithIdentityV1(host.config.IdentityKeys...)
	}

	for _, definition := range host.definitions {
		endpoint.Bind(definition)
	}

	handler, err := endpoint.Handler()

	if err != nil {
		return fmt.Errorf("build restate endpoint: %w", err)
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", host.config.Addr)

	if err != nil {
		return fmt.Errorf("listen on %s: %w", host.config.Addr, err)
	}

	// No ReadTimeout or WriteTimeout: Restate keeps long-lived HTTP/2 streams
	// open for the duration of an invocation.
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		Protocols:         new(http.Protocols),
	}
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetUnencryptedHTTP2(true)

	host.listener = listener
	host.server = srv
	host.started = true

	return nil
}

// Serve serves the endpoint, registers the deployment when
// [Builder.Register] is set, and blocks until ctx is done. It returns nil on
// a clean shutdown. A host that opened no listener registers nothing and
// only waits for ctx.
// Stop remains the listener's cleanup owner on registration failure or return;
// canceling Serve alone does not close active connections.
//
// Example:
//
//	host := New()
//	ctx, cancel := context.WithCancel(context.Background())
//	cancel()
//	if err := host.Start(ctx); err != nil {
//		panic(err)
//	}
//	if err := host.Serve(ctx); err != nil {
//		panic(err)
//	}
func (host *Host) Serve(ctx context.Context) error {
	host.mu.Lock()
	srv, listener, started := host.server, host.listener, host.started
	host.mu.Unlock()

	if !started {
		return errors.New("serve restate: host is not started")
	}

	if srv == nil {
		<-ctx.Done()

		return nil
	}

	failed := make(chan error, 1)
	go func() {
		failed <- srv.Serve(listener)
	}()

	if host.config.Register {
		if err := host.register(ctx); err != nil {
			return err
		}
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-failed:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("serve restate: %w", err)
	}
}

// Stop closes the listener, including a listener opened by Start but never
// served, and waits for active HTTP requests until ctx expires. If requests
// remain when ctx expires, it closes their connections and returns the shutdown
// error. Closing a connection cancels its request context; it cannot forcibly
// stop application code that ignores cancellation. The shared HTTP client
// remains caller-owned.
// Repeated calls are safe, and a host that never listened needs no cleanup.
//
// Example:
//
//	host := New()
//	if err := host.Stop(context.Background()); err != nil {
//		panic(err)
//	}
func (host *Host) Stop(ctx context.Context) error {
	host.mu.Lock()
	srv, listener := host.server, host.listener
	host.mu.Unlock()

	if srv == nil {
		return nil
	}

	err := srv.Shutdown(ctx)
	// Shutdown only knows listeners already handed to Serve. Startup rollback
	// must also release the listener acquired independently by Start.
	err = errors.Join(err, releaseListener(listener))

	if err != nil {
		return fmt.Errorf("stop restate: %w", errors.Join(err, srv.Close()))
	}

	return nil
}

// releaseListener releases Start's separately owned listener. Serve and
// Shutdown may already have closed it, which is successful idempotent cleanup.
func releaseListener(listener net.Listener) error {
	err := listener.Close()

	if errors.Is(err, net.ErrClosed) {
		return nil
	}

	return err
}
