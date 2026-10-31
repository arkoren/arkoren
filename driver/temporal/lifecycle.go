package temporal

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// Start dials Temporal, unless the client was supplied, then creates the
// worker, applies the registrations, and starts the worker. With no
// registrations, or with [Builder.NoWorker], it creates no worker and the Host is
// only a client. A canceled context is rejected before acquiring resources.
// A nil NewFrom client is an error, not a request to dial a connection.
// SDK worker startup has no context parameter: cancellation during that call
// is observed after it returns, then acquired resources are released before
// Start returns the cancellation error. Registration and SDK hooks must return;
// Start cannot interrupt arbitrary callbacks. Failed attempts may be retried.
//
// Example:
//
//	if err := host.Start(ctx); err != nil {
//		return err
//	}
func (host *Host) Start(ctx context.Context) error {
	host.mu.Lock()
	defer host.mu.Unlock()

	if host.started {
		return fmt.Errorf("start: %w", ErrStarted)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start temporal: %w", err)
	}

	conn, err := host.connect(ctx)

	if err != nil {
		return err
	}

	var instance worker.Worker
	defer func() {
		if host.started {
			return
		}

		if instance != nil {
			instance.Stop()
		}

		if host.owned {
			conn.Close()
		}
	}()

	instance, err = host.startWorker(conn)

	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start temporal: %w", err)
	}

	host.client = conn
	host.worker = instance
	host.started = true

	return nil
}

// connect acquires a native client or borrows the explicitly supplied one. A
// missing borrowed client is rejected; only New hosts dial with the caller's
// context. The caller owns cleanup of a successfully acquired owned client.
func (host *Host) connect(ctx context.Context) (client.Client, error) {
	if !host.owned {
		if host.client == nil {
			return nil, errors.New("start temporal: nil native client")
		}

		return host.client, nil
	}

	conn, err := client.DialContext(ctx, host.clientOptions())

	if err != nil {
		return nil, fmt.Errorf("dial temporal: %w", err)
	}

	return conn, nil
}

// Stop initiates worker cleanup once and waits for it until ctx is done. An
// expired context still initiates cleanup. The owned native client is closed
// after worker cleanup returns, possibly after Stop returns; borrowed clients
// stay open. Call Stop after Start has returned: lifecycle calls are serialized.
//
// WorkerStopTimeout is native graceful-stop policy, not a total Stop deadline.
// SDK shutdown hooks must return and activities must cooperate with context
// cancellation; neither Stop nor the SDK can forcibly stop arbitrary callbacks.
// Repeated calls wait for the same worker cleanup, each with its own context.
//
// Example:
//
//	if err := host.Stop(ctx); err != nil {
//		return err // Worker cleanup continues after a context error.
//	}
func (host *Host) Stop(ctx context.Context) error {
	host.mu.Lock()
	instance, conn := host.worker, host.client
	host.mu.Unlock()

	var err error

	if instance != nil {
		host.stopOnce.Do(func() {
			go func() {
				instance.Stop()
				close(host.stopped)
			}()
		})

		select {
		case <-host.stopped:
		case <-ctx.Done():
			err = fmt.Errorf("stop temporal worker: %w", ctx.Err())
		}
	}

	if host.owned && conn != nil {
		if instance == nil {
			host.closeOnce.Do(conn.Close)
		} else {
			// Close the client only after the worker stopped, which may be
			// after ctx is done.
			go func() {
				<-host.stopped
				host.closeOnce.Do(conn.Close)
			}()
		}
	}

	return err
}
