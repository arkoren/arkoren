package mysqldatabase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

// canceledLockConnector supplies a connection that loses its native response
// when the operation deadline expires, matching MySQL's cancellation race.
type canceledLockConnector struct {
	cleanupConnector
}

// Connect creates the cancellation-aware native connection without a server.
func (connector *canceledLockConnector) Connect(context.Context) (driver.Conn, error) {
	return &canceledLockConnection{}, nil
}

// canceledLockConnection reuses the native lifecycle stub but fails queries
// only after their caller context ends.
type canceledLockConnection struct {
	cleanupConnection
}

// QueryContext simulates a native connection loss caused by cancellation.
func (*canceledLockConnection) QueryContext(
	ctx context.Context, _ string, _ []driver.NamedValue,
) (driver.Rows, error) {
	<-ctx.Done()

	return nil, driver.ErrBadConn
}

// Close needs no physical cleanup because this connection has no resources.
func (*canceledLockConnection) Close() error {
	return nil
}

// TestTryLockPreservesDeadlineAfterNativeConnectionLoss exercises the real
// sql.Conn acquisition path, including discard after an uncertain response.
func TestTryLockPreservesDeadlineAfterNativeConnectionLoss(t *testing.T) {
	pool := sql.OpenDB(&canceledLockConnector{})
	databaseDriver := NewFrom(pool)
	t.Cleanup(func() {
		if err := databaseDriver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	t.Cleanup(cancel)

	unlock, acquired, err := databaseDriver.TryLock(ctx, "deadline-response-loss")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("TryLock error = %v, want context.DeadlineExceeded", err)
	}
	if !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("TryLock error = %v, want preserved native driver.ErrBadConn", err)
	}
	if unlock != nil || acquired {
		t.Fatal("uncertain acquisition reported lock ownership")
	}
	if pool.Stats().OpenConnections != 0 {
		t.Fatal("uncertain native connection was returned to the pool")
	}
}
