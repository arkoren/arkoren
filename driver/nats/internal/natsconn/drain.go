// Package natsconn owns shared native connection teardown policy. It does not
// decide ownership: callers must invoke Drain only for connections they own.
//
// Example:
//
//	if err := natsconn.Drain(ctx, ownedConnection); err != nil {
//		return err
//	}
package natsconn

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go"
)

// Drain waits for native subscription and publisher draining to close conn.
// Native DrainTimeout bounds a context without a deadline. Cancellation forces
// Close and returns the context error, including an already-expired context.
// Native drain setup failures also force Close. An already-closed connection is
// released successfully; nil is not accepted. Native callbacks are not replaced.
// A native drain timeout is returned as nats.ErrDrainTimeout after closure;
// unrelated historical native errors do not turn successful teardown into failure.
//
// Example:
//
//	if err := natsconn.Drain(ctx, ownedConnection); err != nil {
//		return fmt.Errorf("release NATS connection: %w", err)
//	}
func Drain(ctx context.Context, conn *nats.Conn) error {
	closed := conn.StatusChanged(nats.CLOSED)
	defer conn.RemoveStatusListener(closed)

	if conn.IsClosed() {
		return nil
	}

	if err := ctx.Err(); err != nil {
		conn.Close()

		return err
	}

	if err := conn.Drain(); err != nil {
		conn.Close()

		return err
	}

	select {
	case <-closed:
	case <-ctx.Done():
		conn.Close()

		return ctx.Err()
	}

	if err := conn.LastError(); errors.Is(err, nats.ErrDrainTimeout) {
		return err
	}

	return nil
}
