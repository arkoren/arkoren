package mysqldatabase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
)

// errCleanupUncertain simulates transport uncertainty without disconnecting the
// ancestor cursor that is needed to exercise database/sql's retained read lock.
var errCleanupUncertain = errors.New("test session ownership is uncertain")

// cleanupConnector supplies a small native database/sql driver, not a database
// contract mock. The real sql.Conn and sql.Rows implement all retention and
// locking behavior under test, without depending on server packet timing.
type cleanupConnector struct {
	// beginErr makes native transaction creation fail while a parent cursor is open.
	beginErr error

	// releaseErr fails RELEASE_LOCK only after the native transaction commits.
	releaseErr error

	// connections counts physical connections, proving poisoned scopes do not reconnect.
	connections atomic.Int32

	// closed counts physical closes, which must happen only at the owning scope's exit.
	closed atomic.Int32

	// queries counts native I/O so guards can be checked after poisoning.
	queries atomic.Int32
}

// Connect creates one physical test connection. sql.OpenDB calls it directly,
// so the tests need neither global driver registration nor package init hooks.
func (connector *cleanupConnector) Connect(context.Context) (driver.Conn, error) {
	connector.connections.Add(1)

	return &cleanupConnection{
		connector: connector,
	}, nil
}

// Driver supplies the standard driver metadata required by sql.OpenDB. The
// connector itself implements Open only to satisfy that native interface.
func (connector *cleanupConnector) Driver() driver.Driver {
	return connector
}

// Open implements the native fallback entrypoint. These tests use Connect,
// but returning the same native connection keeps the fake's ownership uniform.
func (connector *cleanupConnector) Open(string) (driver.Conn, error) {
	return connector.Connect(context.Background())
}

// cleanupConnection leaves cursors open until database/sql closes them. Native
// operations are immediate; any blocked cleanup therefore comes from Conn's
// read/write ownership, not a fabricated network delay.
type cleanupConnection struct {
	// connector retains the injected errors and physical lifecycle counters.
	connector *cleanupConnector
}

// Prepare is unused because QueryContext implements the direct native path.
// Rejecting fallback makes an accidental prepared-statement path fail clearly.
func (*cleanupConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.ErrUnsupported
}

// Close records the physical discard. database/sql, rather than the fake,
// decides when closing is safe with respect to outstanding Rows.
func (connection *cleanupConnection) Close() error {
	connection.connector.closed.Add(1)

	return nil
}

// Begin supplies default native transaction behavior. The configured begin
// failure is returned before a transaction exists, matching a failed BeginTx.
func (connection *cleanupConnection) Begin() (driver.Tx, error) {
	if connection.connector.beginErr != nil {
		return nil, connection.connector.beginErr
	}

	return cleanupTransaction{}, nil
}

// QueryContext opens a real sql.Rows read-lock owner around a single native
// value. GET_LOCK succeeds; only a configured RELEASE_LOCK attempt fails, so
// confirmed-commit cleanup can be tested independently from transaction outcome.
func (connection *cleanupConnection) QueryContext(
	_ context.Context, statement string, _ []driver.NamedValue,
) (driver.Rows, error) {
	connection.connector.queries.Add(1)

	if strings.Contains(statement, "RELEASE_LOCK") && connection.connector.releaseErr != nil {
		return nil, connection.connector.releaseErr
	}

	return &cleanupRows{
		remaining: true,
	}, nil
}

// cleanupTransaction completes native transaction operations immediately. Its
// success lets tests distinguish post-commit cleanup from ambiguous outcomes.
type cleanupTransaction struct{}

// Commit confirms the native commit before any injected release error. The
// driver must preserve that confirmed outcome while deferring physical discard.
func (cleanupTransaction) Commit() error {
	return nil
}

// Rollback completes without error so it cannot account for a blocked test.
// The scenarios under test concern retained cursor ownership, not network I/O.
func (cleanupTransaction) Rollback() error {
	return nil
}

// cleanupRows provides one scalar without releasing its enclosing sql.Rows.
// An abandoned cursor consequently retains sql.Conn's read lock until cleanup.
type cleanupRows struct {
	// remaining distinguishes the first row from exhaustion without real waits.
	remaining bool
}

// Columns exposes the scalar name used by query.Targets. There is only one
// result column and no schema or user data is read or modified.
func (*cleanupRows) Columns() []string {
	return []string{"value"}
}

// Close needs no native cleanup; database/sql still releases the retained Conn
// read lock around this call, which is the behavior these tests exercise.
func (*cleanupRows) Close() error {
	return nil
}

// Next supplies one deterministic scalar and then EOF. Tests deliberately do
// not advance the ancestor cursor, leaving its connection ownership intact.
func (cursor *cleanupRows) Next(values []driver.Value) error {
	if !cursor.remaining {
		return io.EOF
	}

	cursor.remaining = false
	values[0] = int64(1)

	return nil
}

// assertBorrowedCleanupReturns keeps an ancestor's real sql.Rows open while fn
// exercises a borrowed cleanup path. Its safety timer closes the native Rows
// and cancels the test context before joining the goroutine, so a RED result
// reports the deadlock without leaking either the row or the blocked callback.
func assertBorrowedCleanupReturns(
	t *testing.T, connector *cleanupConnector, fn func(*Driver) error, want error,
) {
	t.Helper()

	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(1)
	databaseDriver := NewFrom(pool)

	t.Cleanup(func() {
		if err := databaseDriver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	opened := make(chan *sql.Rows, 1)
	result := make(chan error, 1)
	var group sync.WaitGroup

	group.Go(func() {
		result <- databaseDriver.WithSession(ctx, func(scoped database.Driver) error {
			outer := scoped.(*Driver)
			cursor, err := outer.Query(ctx, "SELECT 1")

			if err != nil {
				return err
			}

			opened <- cursor.(*rows).Rows

			if err := fn(outer); !errors.Is(err, want) {
				t.Errorf("borrowed cleanup result = %v, want %v", err, want)

				return errors.New("unexpected borrowed cleanup result")
			}

			if connector.closed.Load() != 0 {
				return errors.New("borrower physically discarded the connection before owner cleanup")
			}

			if cursor.Next() || !errors.Is(cursor.Err(), errCleanupUncertain) {
				return errors.New("poisoned ancestor cursor remained readable")
			}

			before := connector.queries.Load()

			if _, err := outer.Query(ctx, "SELECT 2"); !errors.Is(err, errCleanupUncertain) {
				return fmt.Errorf("poisoned ancestor query: %w", err)
			}

			if connector.queries.Load() != before {
				return errors.New("poisoned ancestor performed native I/O")
			}

			return nil
		})
	})

	var nativeRows *sql.Rows

	select {
	case nativeRows = <-opened:
	case err := <-result:
		group.Wait()
		t.Fatalf("ancestor query failed before reproduction: %v", err)
	}

	t.Cleanup(func() {
		cancel()

		if err := nativeRows.Close(); err != nil {
			t.Error(err)
		}

		group.Wait()
	})

	timer := time.NewTimer(time.Second)
	t.Cleanup(func() {
		timer.Stop()
	})

	select {
	case err := <-result:
		if !errors.Is(err, errCleanupUncertain) {
			t.Errorf("owner cleanup = %v, want injected uncertainty", err)
		}
	case <-timer.C:
		t.Error("borrowed cleanup waited for the ancestor cursor instead of returning to its owner")

		if err := nativeRows.Close(); err != nil {
			t.Error(err)
		}

		cancel()
	}

	group.Wait()

	if connector.connections.Load() != 1 || connector.closed.Load() != 1 {
		t.Errorf("physical connections=%d, closes=%d, want one discarded connection",
			connector.connections.Load(), connector.closed.Load())
	}

	if pool.Stats().InUse != 0 || pool.Stats().OpenConnections != 0 {
		t.Errorf("uncertain session was leaked or pooled: %+v", pool.Stats())
	}
}

// TestBorrowedPoisonedSessionReturnsWithAncestorCursor injects uncertainty in
// a nested session while the owner has an abandoned streaming cursor. The
// borrower must fail closed but leave physical discard to the connection owner.
func TestBorrowedPoisonedSessionReturnsWithAncestorCursor(t *testing.T) {
	t.Parallel()

	assertBorrowedCleanupReturns(t, &cleanupConnector{}, func(outer *Driver) error {
		return outer.WithSession(t.Context(), func(inner database.Driver) error {
			inner.(*Driver).scope.session.poison(errCleanupUncertain)

			return nil
		})
	}, errCleanupUncertain)
}

// TestFailedTransactionBeginReturnsWithAncestorCursor covers the early BeginTx
// failure path, which has no active transaction but still must not discard an
// enclosing session before its outstanding cursor can be closed by owner cleanup.
func TestFailedTransactionBeginReturnsWithAncestorCursor(t *testing.T) {
	t.Parallel()

	connector := &cleanupConnector{
		beginErr: errCleanupUncertain,
	}

	assertBorrowedCleanupReturns(t, connector, func(outer *Driver) error {
		return outer.WithTransaction(t.Context(), func(database.Driver) error {
			return errors.New("callback ran after a failed native BeginTx")
		})
	}, errCleanupUncertain)
}

// TestConfirmedCommitCleanupReturnsWithAncestorCursor preserves ErrAfterCommit
// when native commit succeeds but lock release fails. Physical discard must
// still wait for owner cleanup rather than blocking on an ancestor cursor.
func TestConfirmedCommitCleanupReturnsWithAncestorCursor(t *testing.T) {
	t.Parallel()

	connector := &cleanupConnector{
		releaseErr: errCleanupUncertain,
	}

	assertBorrowedCleanupReturns(t, connector, func(outer *Driver) error {
		return outer.WithTransaction(t.Context(), func(tx database.Driver) error {
			_, err := tx.Lock(t.Context(), "cleanup-test-lock")

			return err
		})
	}, database.ErrAfterCommit)
}

// TestExplicitUnlockFailureReturnsWithAncestorCursor covers a scoped unlock
// closure while its owning callback still has a streaming result open. Failed
// release must poison the session without trying to discard it inline.
func TestExplicitUnlockFailureReturnsWithAncestorCursor(t *testing.T) {
	t.Parallel()

	connector := &cleanupConnector{
		releaseErr: errCleanupUncertain,
	}

	assertBorrowedCleanupReturns(t, connector, func(outer *Driver) error {
		unlock, err := outer.Lock(t.Context(), "cleanup-test-unlock")

		if err != nil {
			return err
		}

		return unlock(t.Context())
	}, errCleanupUncertain)
}

// TestBorrowedPoisonedPanicReturnsWithAncestorCursor verifies that deferring
// discard does not replace the original panic. The caller can recover while
// the owner still retains its cursor and its poisoned physical connection.
func TestBorrowedPoisonedPanicReturnsWithAncestorCursor(t *testing.T) {
	t.Parallel()

	assertBorrowedCleanupReturns(t, &cleanupConnector{}, func(outer *Driver) (err error) {
		defer func() {
			if value := recover(); value != "poisoned callback panic" {
				err = fmt.Errorf("original callback panic was replaced: %v", value)
			} else {
				err = errCleanupUncertain
			}
		}()

		return outer.WithSession(t.Context(), func(inner database.Driver) error {
			inner.(*Driver).scope.session.poison(errCleanupUncertain)
			panic("poisoned callback panic")
		})
	}, errCleanupUncertain)
}
