package sqlitedatabase_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"arkoren.dev/core/service/database"
	sqlite "arkoren.dev/driver/sqlite/sqlitedatabase"
)

// nativeConnector supplies the public database/sql native seam used by NewFrom.
// Tests control only native rows and transaction completion, leaving real
// sql.Conn retention and cancellation machinery in charge of resource ordering.
type nativeConnector struct {
	// query creates a fresh native cursor for each SQL statement.
	query func(string) driver.Rows

	// tx is returned by native BeginTx and can delay completion deterministically.
	tx driver.Tx

	// close observes physical disposal, after database/sql permits native Close.
	close func() error
}

// Connect supplies one native connection without global driver registration.
// The real database/sql pool still owns its checkout and closing lifecycle.
func (connector *nativeConnector) Connect(context.Context) (driver.Conn, error) {
	return &nativeConnection{connector: connector}, nil
}

// Driver provides metadata required by sql.OpenDB. Open uses the same native
// connection implementation if database/sql selects that fallback entrypoint.
func (connector *nativeConnector) Driver() driver.Driver {
	return connector
}

// Open satisfies the native Driver interface without a registry or external I/O.
// The DSN is unused because this fixture is configured through its connector.
func (connector *nativeConnector) Open(string) (driver.Conn, error) {
	return connector.Connect(context.Background())
}

// nativeConnection exposes native optional interfaces implemented by modernc.
// In particular, reset and validation keep cancellation from discarding the
// connection before the wrapper's own retention behavior can be observed.
type nativeConnection struct {
	// connector holds the native responses and transaction for this fixture.
	connector *nativeConnector
}

// Prepare rejects unexpected fallback rather than simulating statement parsing.
// Tests use direct QueryContext and native transaction entrypoints only.
func (*nativeConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.ErrUnsupported
}

// Close completes physical disposal immediately; no external resource exists.
// database/sql still decides when its outstanding transaction permits this call.
func (connection *nativeConnection) Close() error {
	if connection.connector.close != nil {
		return connection.connector.close()
	}

	return nil
}

// Begin returns the controlled native transaction. BeginTx provides the same
// result through the context-aware interface used by the pinned SQLite driver.
func (connection *nativeConnection) Begin() (driver.Tx, error) {
	return connection.connector.tx, nil
}

// BeginTx models a successful native BEGIN without replacing transaction
// ownership or observing cancellation outside the supplied operation context.
func (connection *nativeConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return connection.Begin()
}

// QueryContext supplies controlled native rows while real sql.Rows handles
// scan conversion, auto-closing, cursor retention, and finalization errors.
func (connection *nativeConnection) QueryContext(
	_ context.Context, statement string, _ []driver.NamedValue,
) (driver.Rows, error) {
	return connection.connector.query(statement), nil
}

// ResetSession reports this controlled connection reusable, like a healthy
// modernc connection. It never performs SQL or fabricates a transaction result.
func (*nativeConnection) ResetSession(context.Context) error {
	return nil
}

// IsValid prevents the native fixture from discarding an otherwise healthy
// connection on automatic rollback, matching the pinned in-memory SQLite path.
func (*nativeConnection) IsValid() bool {
	return true
}

// nativeRows emits at most one row and then EOF. Its close error can model a
// native statement-finalization failure independently from conversion failures.
type nativeRows struct {
	// columns names the output values for query.Targets.
	columns []string

	// values is consumed on the first Next; nil indicates exhaustion.
	values []driver.Value

	// closeErr is returned when sql.Rows finalizes this native statement.
	closeErr error
}

// Columns describes the controlled native row without allocating scan targets.
// Its length matches values whenever a fixture supplies a row.
func (rows *nativeRows) Columns() []string {
	return rows.columns
}

// Next copies one native row and then reports exhaustion. database/sql performs
// all destination conversion and owns the enclosing sql.Rows state machine.
func (rows *nativeRows) Next(values []driver.Value) error {
	if rows.values == nil {
		return io.EOF
	}

	copy(values, rows.values)
	rows.values = nil

	return nil
}

// Close exposes an injected native finalization failure without suppressing it.
// No resource or goroutine is retained by the cursor itself.
func (rows *nativeRows) Close() error {
	return rows.closeErr
}

// newNativeDriver transfers a configured native pool through the public NewFrom
// constructor. Tests receive no access to the wrapper's private executors/scopes.
func newNativeDriver(t *testing.T, connector *nativeConnector) *sqlite.Driver {
	t.Helper()

	driver := sqlite.NewFrom(sql.OpenDB(connector))
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return driver
}

// blockedNativeTransaction makes native rollback completion observable through
// the public database/sql driver seam. Its channel wait is durable in synctest,
// unlike sleeps or an assumed scheduling order in sql.Tx's cancellation worker.
type blockedNativeTransaction struct {
	// started closes when native rollback has actually begun.
	started chan struct{}

	// release permits native rollback to finish after the lock observation.
	release chan struct{}
}

// Commit rejects an unexpected success path after the test cancels its scope.
// The test concerns rollback ordering, not ambiguous commit outcomes.
func (*blockedNativeTransaction) Commit() error {
	return errors.New("unexpected commit of canceled transaction")
}

// Rollback remains in progress until the test releases it. This exposes the
// interval where sql.Tx.Rollback can report ErrTxDone before native completion.
func (tx *blockedNativeTransaction) Rollback() error {
	close(tx.started)
	<-tx.release

	return nil
}

// TestCanceledTransactionKeepsLockUntilNativeRollbackFinishes reproduces the
// automatic sql.Tx rollback race without private wrapper state. An independent
// real SQLite driver must not acquire the managed lock while native rollback
// is still in progress on the original retained physical connection.
func TestCanceledTransactionKeepsLockUntilNativeRollbackFinishes(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		tx := &blockedNativeTransaction{started: make(chan struct{}), release: make(chan struct{})}
		databaseDriver := newNativeDriver(t, &nativeConnector{
			tx: tx,
			query: func(string) driver.Rows {
				return &nativeRows{columns: []string{"seq", "name", "file"}, values: []driver.Value{int64(0), "main", ""}}
			},
		})
		observer, connection := newSQLiteDriver(t)
		registerSQLiteConnectionCleanup(t, connection)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		var release sync.Once
		t.Cleanup(func() {
			release.Do(func() { close(tx.release) })
		})

		result := make(chan error, 1)
		prepared := make(chan struct{})
		var group sync.WaitGroup
		group.Go(func() {
			result <- databaseDriver.WithTransaction(ctx, func(scoped database.Driver) error {
				if _, err := scoped.Lock(ctx, t.Name()); err != nil {
					return err
				}

				cancel()
				synctest.Wait()
				close(prepared)

				return nil
			})
		})

		<-prepared
		synctest.Wait()

		select {
		case <-tx.started:
		default:
			t.Fatal("native rollback did not start")
		}

		unlock, acquired, err := observer.TryLock(t.Context(), t.Name())
		release.Do(func() { close(tx.release) })

		if acquired {
			if err := unlock(t.Context()); err != nil {
				t.Error(err)
			}
		}

		group.Wait()

		if acquired || err != nil {
			t.Fatalf("lock during native rollback = %v, %v; want refused", acquired, err)
		}

		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("transaction result = %v, want context.Canceled", err)
		}
	})
}

// TestFailedRollbackDiscardsBeforeLockRelease checks the uncertain-completion
// path too. Native Close observes that managed protection is still held; only
// physical disposal makes it safe to release a possibly active transaction.
func TestFailedRollbackDiscardsBeforeLockRelease(t *testing.T) {
	t.Parallel()

	observer, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)
	want := errors.New("native rollback failed")
	tx := &recordingNativeTransaction{rollbackErr: want}
	disposed := false
	databaseDriver := newNativeDriver(t, &nativeConnector{
		tx: tx,
		query: func(string) driver.Rows {
			return &nativeRows{columns: []string{"seq", "name", "file"}, values: []driver.Value{int64(0), "main", ""}}
		},
		close: func() error {
			disposed = true
			unlock, acquired, err := observer.TryLock(t.Context(), t.Name())

			if acquired {
				_ = unlock(t.Context())
			}

			if acquired || err != nil {
				t.Errorf("lock before physical disposal = %v, %v; want refused", acquired, err)
			}

			return nil
		},
	})

	err := databaseDriver.WithTransaction(t.Context(), func(scoped database.Driver) error {
		if _, err := scoped.Lock(t.Context(), t.Name()); err != nil {
			return err
		}

		return errors.New("callback requests rollback")
	})

	if !errors.Is(err, want) || !disposed {
		t.Fatalf("transaction = %v, disposed=%v; want failed rollback and disposal", err, disposed)
	}

	unlock, acquired, err := observer.TryLock(t.Context(), t.Name())

	if err != nil || !acquired {
		t.Fatalf("lock after disposal = %v, %v; want acquired", acquired, err)
	}

	if err := unlock(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// recordingNativeTransaction records the boundary selected after cursor cleanup.
// All access is synchronous through the public NewFrom native connection seam.
type recordingNativeTransaction struct {
	// commits counts native commit attempts, which uncertainty must prevent.
	commits int

	// rollbacks counts native rollback attempts after a failed callback scope.
	rollbacks int

	// rollbackErr injects an uncertain native completion instead of a success.
	rollbackErr error
}

// Commit records a confirmed native commit. It never injects unrelated failures.
func (tx *recordingNativeTransaction) Commit() error {
	tx.commits++

	return nil
}

// Rollback records successful cleanup without altering the injected row error.
func (tx *recordingNativeTransaction) Rollback() error {
	tx.rollbacks++

	return tx.rollbackErr
}

// TestFindReportsNativeCloseFailure ensures first-row convenience scanning does
// not turn native statement-finalization failure into an apparent success.
func TestFindReportsNativeCloseFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("native statement finalization failed")
	databaseDriver := newNativeDriver(t, &nativeConnector{
		query: func(string) driver.Rows {
			return &nativeRows{columns: []string{"value"}, values: []driver.Value{int64(7)}, closeErr: want}
		},
	})
	var value int

	if err := databaseDriver.Find(t.Context(), "SELECT 7", &value); !errors.Is(err, want) {
		t.Fatalf("Find = %v, want native close failure", err)
	}
}

// TestSelectPreservesScanAndCloseFailures checks an early conversion error does
// not hide the independent native error encountered while closing the cursor.
func TestSelectPreservesScanAndCloseFailures(t *testing.T) {
	t.Parallel()

	want := errors.New("native statement finalization failed")
	databaseDriver := newNativeDriver(t, &nativeConnector{
		query: func(string) driver.Rows {
			return &nativeRows{columns: []string{"value"}, values: []driver.Value{"not-an-integer"}, closeErr: want}
		},
	})
	var values []int

	err := databaseDriver.Select(t.Context(), "SELECT invalid_integer", &values)

	if !errors.Is(err, want) || !strings.Contains(err.Error(), "Scan error") {
		t.Fatalf("Select = %v, want scan and close failures", err)
	}
}

// TestFindPreservesScanAndCloseFailures exercises the analogous first-row path:
// both native cleanup and conversion errors must remain visible to the caller.
func TestFindPreservesScanAndCloseFailures(t *testing.T) {
	t.Parallel()

	want := errors.New("native statement finalization failed")
	databaseDriver := newNativeDriver(t, &nativeConnector{
		query: func(string) driver.Rows {
			return &nativeRows{columns: []string{"value"}, values: []driver.Value{"not-an-integer"}, closeErr: want}
		},
	})
	var value int

	err := databaseDriver.Find(t.Context(), "SELECT invalid_integer", &value)

	if !errors.Is(err, want) || !strings.Contains(err.Error(), "Scan error") {
		t.Fatalf("Find = %v, want scan and close failures", err)
	}
}

// TestIgnoredFindCloseFailurePreventsCommit ensures a callback cannot erase
// uncertain native statement cleanup merely by swallowing its immediate error.
func TestIgnoredFindCloseFailurePreventsCommit(t *testing.T) {
	t.Parallel()

	want := errors.New("native statement finalization failed")
	tx := &recordingNativeTransaction{}
	databaseDriver := newNativeDriver(t, &nativeConnector{
		tx: tx,
		query: func(string) driver.Rows {
			return &nativeRows{columns: []string{"value"}, values: []driver.Value{int64(7)}, closeErr: want}
		},
	})

	err := databaseDriver.WithTransaction(t.Context(), func(scoped database.Driver) error {
		var value int
		_ = scoped.Find(t.Context(), "SELECT 7", &value)

		return nil
	})

	if !errors.Is(err, want) || tx.commits != 0 || tx.rollbacks != 1 {
		t.Fatalf("transaction = %v, commits=%d, rollbacks=%d; want cleanup error and rollback",
			err, tx.commits, tx.rollbacks)
	}
}

// TestIgnoredSelectCloseFailurePreventsCommit covers EOF auto-close, where the
// native close error is cached in sql.Rows.Err and a repeated Close returns nil.
func TestIgnoredSelectCloseFailurePreventsCommit(t *testing.T) {
	t.Parallel()

	want := errors.New("native statement finalization failed")
	tx := &recordingNativeTransaction{}
	databaseDriver := newNativeDriver(t, &nativeConnector{
		tx: tx,
		query: func(string) driver.Rows {
			return &nativeRows{columns: []string{"value"}, values: []driver.Value{int64(7)}, closeErr: want}
		},
	})

	err := databaseDriver.WithTransaction(t.Context(), func(scoped database.Driver) error {
		var values []int
		_ = scoped.Select(t.Context(), "SELECT 7", &values)

		return nil
	})

	if !errors.Is(err, want) || tx.commits != 0 || tx.rollbacks != 1 {
		t.Fatalf("transaction = %v, commits=%d, rollbacks=%d; want cleanup error and rollback",
			err, tx.commits, tx.rollbacks)
	}
}

// TestGoexitRollsBackTransaction protects native transaction ownership when a
// callback never returns. The data assertion uses actual local SQLite, not a
// private transaction helper or a fabricated commit result.
func TestGoexitRollsBackTransaction(t *testing.T) {
	t.Parallel()

	driver, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)
	ctx := t.Context()

	if _, err := driver.Exec(ctx, "CREATE TABLE interrupted (value INTEGER)"); err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup
	group.Go(func() {
		_ = driver.WithTransaction(ctx, func(tx database.Driver) error {
			if _, err := tx.Exec(ctx, "INSERT INTO interrupted VALUES (7)"); err != nil {
				t.Error(err)

				return err
			}

			runtime.Goexit()

			return nil
		})
	})
	group.Wait()

	var count int

	if err := driver.Find(ctx, "SELECT COUNT(*) FROM interrupted", &count); err != nil || count != 0 {
		t.Fatalf("interrupted transaction rows = %d, %v; want 0", count, err)
	}
}

// TestGoexitRollsBackNestedTransaction ensures savepoint cleanup cannot commit
// abandoned nested work or accidentally authorize the enclosing transaction.
func TestGoexitRollsBackNestedTransaction(t *testing.T) {
	t.Parallel()

	driver, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)
	ctx := t.Context()

	if _, err := driver.Exec(ctx, "CREATE TABLE interrupted (value INTEGER)"); err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup
	group.Go(func() {
		_ = driver.WithTransaction(ctx, func(tx database.Driver) error {
			return tx.WithTransaction(ctx, func(inner database.Driver) error {
				if _, err := inner.Exec(ctx, "INSERT INTO interrupted VALUES (7)"); err != nil {
					t.Error(err)

					return err
				}

				runtime.Goexit()

				return nil
			})
		})
	})
	group.Wait()

	var count int

	if err := driver.Find(ctx, "SELECT COUNT(*) FROM interrupted", &count); err != nil || count != 0 {
		t.Fatalf("interrupted nested transaction rows = %d, %v; want 0", count, err)
	}
}
