package server

import (
	"context"
	"errors"
	"net"
)

// shuttingDownKey is the context key under which a [Server] publishes its
// shutdown signal.
type shuttingDownKey struct{}

// ShuttingDown returns a channel that is closed when the [Server] that
// accepted the request starts shutting down. Pass the request context.
//
// Beginning shutdown does not cancel active request contexts. Long-lived
// handlers must cooperate with this signal to finish before the drain deadline;
// closing HTTP connections cannot forcibly stop arbitrary application work.
// Hijacked connections, including WebSockets, are neither closed nor awaited by
// the server: their owners must close them and join any associated work.
// [arkoren.dev/core/http/response.Stream] and
// [arkoren.dev/core/http/response.SSE], including their channel forms, watch this
// signal. Without a [Server] in ctx the result is nil, so that select case is
// disabled. In a long-lived router.Handler, check before the next operation:
//
// Example:
//
//	select {
//	case <-server.ShuttingDown(request.Context()):
//		return nil
//	default:
//	}
//	// Continue the next application-owned operation, which must also be cancelable.
func ShuttingDown(ctx context.Context) <-chan struct{} {
	signal, _ := ctx.Value(shuttingDownKey{}).(chan struct{})

	return signal
}

// signalShutdown closes the shutdown signal once.
func (server *Server) signalShutdown() {
	server.signalOnce.Do(func() {
		close(server.shuttingDown)
	})
}

// shutdown signals cooperative handlers and asks net/http to drain until ctx
// ends. On failure it also closes tracked HTTP connections to release their
// resources. Neither operation closes hijacked connections or guarantees that
// noncooperating application goroutines have finished.
func (server *Server) shutdown(ctx context.Context) error {
	server.signalShutdown()

	err := server.http.Shutdown(ctx)

	if err != nil {
		err = errors.Join(err, server.http.Close())
	}

	return err
}

// Stop signals shutdown and drains tracked HTTP connections until ctx ends,
// then closes those connections even when ctx was already expired. Its waiting
// budget comes from ctx, not [Builder.ShutdownTimeout]. When Serve never ran it
// also closes the listener bound by Start. Stop is safe to call repeatedly and
// after Serve returned; shutdown and listener-close errors remain available
// through errors.Is. Hijacked connections remain caller-owned and are not closed
// or awaited. Closing a connection does not forcibly stop its handler goroutine.
//
// Example:
//
//	srv := server.New(http.NotFoundHandler())
//	if err := srv.Stop(context.Background()); err != nil {
//		return err
//	}
func (server *Server) Stop(ctx context.Context) (err error) {
	server.mu.Lock()

	listener, serving := server.listener, server.serving
	server.mu.Unlock()

	err = server.shutdown(ctx)

	if listener == nil || serving {
		return err
	}

	if e := listener.Close(); e != nil && !errors.Is(e, net.ErrClosed) {
		err = errors.Join(err, e)
	}

	return err
}
