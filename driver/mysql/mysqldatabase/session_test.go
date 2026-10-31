package mysqldatabase_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqlprotocol "github.com/go-sql-driver/mysql"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	mysql "arkoren.dev/driver/mysql/mysqldatabase"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"
)

// TestMySQLSessionPinsAcrossAutocommitAndTransactions proves a capacity-one
// session retains its physical identity across ordinary queries, multiple
// transactions, and a borrowed session inside a transaction.
func TestMySQLSessionPinsAcrossAutocommitAndTransactions(t *testing.T) {
	t.Parallel()

	driver := boundedMySQL(t, 1)
	ctx := testContext(t)

	err := driver.WithSession(ctx, func(session database.Driver) error {
		var id int64

		if err := session.Find(ctx, "SELECT CONNECTION_ID()", &id); err != nil {
			return err
		}

		if session.(*mysql.Driver).SQL() != driver.SQL() {
			return errors.New("scoped SQL accessor did not preserve the root pool")
		}

		for range 2 {
			if err := session.WithTransaction(ctx, func(tx database.Driver) error {
				if err := sameMySQLSession(ctx, tx, id); err != nil {
					return err
				}

				return tx.WithSession(ctx, func(borrowed database.Driver) error {
					return sameMySQLSession(ctx, borrowed, id)
				})
			}); err != nil {
				return err
			}
		}

		return sameMySQLSession(ctx, session, id)
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.SQL().Stats().InUse != 0 {
		t.Fatal("session retained pool capacity after return")
	}
}

// sameMySQLSession verifies both query and ping routing without a second pool
// borrow. It returns errors to callback callers so their deferred cleanup runs.
func sameMySQLSession(ctx context.Context, driver database.Driver, expected int64) error {
	var id int64

	if err := driver.Find(ctx, "SELECT CONNECTION_ID()", &id); err != nil {
		return err
	}

	if id != expected {
		return fmt.Errorf("session changed from %d to %d", expected, id)
	}

	return driver.(*mysql.Driver).Ping(ctx)
}

// TestMySQLSessionErrorDoesNotUndoAutocommit distinguishes a retained session
// from a transaction: committed inserts remain after its callback fails.
func TestMySQLSessionErrorDoesNotUndoAutocommit(t *testing.T) {
	t.Parallel()

	driver := boundedMySQL(t, 1)
	ctx := testContext(t)
	table := uniqueName(t, "session_autocommit")

	if _, err := driver.Exec(ctx, "CREATE TABLE "+table+" (x INT)"); err != nil {
		t.Fatal(err)
	}

	dropTables(t, database.New(driver), table)

	err := driver.WithSession(ctx, func(session database.Driver) error {
		if _, err := session.Exec(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
			return err
		}

		return errPlanned
	})

	if !errors.Is(err, errPlanned) {
		t.Fatalf("session error = %v, want planned failure", err)
	}

	var count int

	if err := driver.Find(ctx, "SELECT COUNT(*) FROM "+table, &count); err != nil || count != 1 {
		t.Fatalf("autocommit rows = %d, error = %v", count, err)
	}
}

// TestMySQLEscapedSessionDriverAndCursorExpire leaves a native streaming cursor
// open deliberately. Automatic cleanup must free the only connection, while
// every escaped entrypoint reports ErrSessionDone rather than reusing it.
func TestMySQLEscapedSessionDriverAndCursorExpire(t *testing.T) {
	t.Parallel()

	driver := boundedMySQL(t, 1)
	ctx := testContext(t)
	var escaped *mysql.Driver
	var cursor database.Rows

	if err := driver.WithSession(ctx, func(session database.Driver) error {
		escaped = session.(*mysql.Driver)

		var err error
		cursor, err = session.Query(ctx, "SELECT 1 UNION ALL SELECT 2")

		return err
	}); err != nil {
		t.Fatal(err)
	}

	if cursor.Next() {
		t.Fatal("expired cursor advanced")
	}

	var value int

	if err := cursor.Scan(&value); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped Scan = %v", err)
	}

	if err := cursor.Err(); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped Err = %v", err)
	}

	if err := cursor.Close(); err != nil {
		t.Fatalf("escaped Close = %v", err)
	}

	if _, err := escaped.Exec(ctx, "SELECT 1"); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped Exec = %v", err)
	}

	if err := escaped.Find(ctx, "SELECT 1", &value); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped Find = %v", err)
	}

	var values []int

	if err := escaped.Select(ctx, "SELECT 1", &values); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped Select = %v", err)
	}

	if _, err := escaped.Query(ctx, "SELECT 1"); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped Query = %v", err)
	}

	if err := escaped.Ping(ctx); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped Ping = %v", err)
	}

	if _, err := escaped.Lock(ctx, "expired"); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped Lock = %v", err)
	}

	if _, _, err := escaped.TryLock(ctx, "expired"); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped TryLock = %v", err)
	}

	err := escaped.WithSession(ctx, func(database.Driver) error {
		return nil
	})

	if !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped WithSession = %v", err)
	}

	err = escaped.WithTransaction(ctx, func(database.Driver) error {
		return nil
	})

	if !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped WithTransaction = %v", err)
	}

	if err := driver.Ping(ctx); err != nil {
		t.Fatalf("pool reuse after cursor cleanup: %v", err)
	}
}

// TestMySQLEscapedTransactionAndBorrowedSessionHaveIndependentLifetimes checks
// that a borrowed session expires before its transaction and never ends or
// creates a savepoint in that transaction. Its leaked cursor is closed first.
func TestMySQLEscapedTransactionAndBorrowedSessionHaveIndependentLifetimes(t *testing.T) {
	t.Parallel()

	driver := boundedMySQL(t, 1)
	ctx := testContext(t)
	var escapedTx, escapedSession database.Driver
	var escapedRows database.Rows

	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
		escapedTx = tx

		if err := tx.WithSession(ctx, func(borrowed database.Driver) error {
			escapedSession = borrowed

			var err error
			escapedRows, err = borrowed.Query(ctx, "SELECT 1 UNION ALL SELECT 2")

			return err
		}); err != nil {
			return err
		}

		if _, err := escapedSession.Exec(ctx, "SELECT 1"); !errors.Is(err, database.ErrSessionDone) {
			return fmt.Errorf("borrowed session remained live: %w", err)
		}

		var value int

		return tx.Find(ctx, "SELECT 1", &value)
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := escapedTx.Exec(ctx, "SELECT 1"); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("escaped transaction = %v", err)
	}

	if !errors.Is(escapedRows.Err(), database.ErrSessionDone) {
		t.Fatalf("escaped borrowed cursor = %v", escapedRows.Err())
	}
}

// TestMySQLNestedSessionLocksDoNotReleaseEnclosingOwnership checks cleanup by
// callback owner, including an old unlock called after a later borrower owns
// the same canonical lock name.
func TestMySQLNestedSessionLocksDoNotReleaseEnclosingOwnership(t *testing.T) {
	t.Parallel()

	driver, observer := boundedMySQL(t, 1), boundedMySQL(t, 1)
	ctx := testContext(t)
	outerName, innerName := uniqueName(t, "outer_session"), uniqueName(t, "inner_session")

	err := driver.WithSession(ctx, func(session database.Driver) error {
		if _, err := session.Lock(ctx, outerName); err != nil {
			return err
		}

		var oldUnlock func(context.Context) error

		if err := session.WithSession(ctx, func(inner database.Driver) error {
			var err error
			oldUnlock, err = inner.Lock(ctx, innerName)

			return err
		}); err != nil {
			return err
		}

		assertMySQLLockFree(t, observer, innerName)

		if _, err := session.Lock(ctx, innerName); err != nil {
			return err
		}

		if err := oldUnlock(ctx); err != nil {
			return err
		}

		assertMySQLLockHeld(t, observer, innerName)
		assertMySQLLockHeld(t, observer, outerName)

		if err := session.WithTransaction(ctx, func(tx database.Driver) error {
			unlock, acquired, err := tx.TryLock(ctx, outerName)

			if err != nil || acquired || unlock != nil {
				return fmt.Errorf("transaction stole enclosing lock: %v, %w", acquired, err)
			}

			return nil
		}); err != nil {
			return err
		}

		assertMySQLLockHeld(t, observer, outerName)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	assertMySQLLockFree(t, observer, innerName)
	assertMySQLLockFree(t, observer, outerName)
}

// TestMySQLNestedRollbackKeepsFreshLocksUntilOuterCompletion acquires new
// names only after nested savepoints and inside a borrowed session. Recovering
// an inner panic or error cannot release any of those transaction-owned locks.
func TestMySQLNestedRollbackKeepsFreshLocksUntilOuterCompletion(t *testing.T) {
	t.Parallel()

	driver, observer := boundedMySQL(t, 1), boundedMySQL(t, 1)
	ctx := testContext(t)
	errorName, panicName := uniqueName(t, "nested_error"), uniqueName(t, "nested_panic")

	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
		innerErr := tx.WithTransaction(ctx, func(inner database.Driver) error {
			return inner.WithSession(ctx, func(borrowed database.Driver) error {
				if _, err := borrowed.Lock(ctx, errorName); err != nil {
					return err
				}

				return errPlanned
			})
		})

		if !errors.Is(innerErr, errPlanned) {
			return fmt.Errorf("nested error = %w", innerErr)
		}

		func() {
			defer func() {
				if value := recover(); value != "nested lock panic" {
					t.Errorf("nested panic = %v", value)
				}
			}()

			if err := tx.WithTransaction(ctx, func(inner database.Driver) error {
				return inner.WithTransaction(ctx, func(deeper database.Driver) error {
					_, acquired, err := deeper.TryLock(ctx, panicName)

					if err != nil || !acquired {
						return fmt.Errorf("fresh nested TryLock = %v, %w", acquired, err)
					}

					panic("nested lock panic")
				})
			}); err != nil {
				t.Error(err)
			}
		}()

		assertMySQLLockHeld(t, observer, errorName)
		assertMySQLLockHeld(t, observer, panicName)

		unlock, acquired, err := tx.TryLock(ctx, errorName)

		if err != nil || acquired || unlock != nil {
			return fmt.Errorf("reentrant TryLock = %v, %w", acquired, err)
		}

		waitCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()

		if _, err := tx.Lock(waitCtx, errorName); !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("reentrant Lock = %w", err)
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	assertMySQLLockFree(t, observer, errorName)
	assertMySQLLockFree(t, observer, panicName)
}

// TestMySQLCanceledTransactionDiscardsAfterAutomaticRollback exercises the race
// between database/sql's cancellation rollback and explicit wrapper cleanup.
// Each iteration must finish with no retained connection and no session lock.
func TestMySQLCanceledTransactionDiscardsAfterAutomaticRollback(t *testing.T) {
	t.Parallel()

	driver, observer := boundedMySQL(t, 1), boundedMySQL(t, 1)

	for range 20 {
		ctx, cancel := context.WithCancel(testContext(t))
		name := uniqueName(t, "cancel_cleanup")

		err := driver.WithTransaction(ctx, func(tx database.Driver) error {
			if _, err := tx.Lock(ctx, name); err != nil {
				return err
			}

			cancel()
			runtime.Gosched()

			return nil
		})
		cancel()

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled transaction = %v", err)
		}

		if driver.SQL().Stats().InUse != 0 {
			t.Fatal("cancellation leaked a retained connection")
		}

		assertMySQLLockFree(t, observer, name)
	}
}

// TestMySQLConfirmedCommitCleanupFailureIsAfterCommit intentionally removes a
// tracked lock with native SQL. Cleanup must reject RELEASE_LOCK's NULL result,
// discard that session, and identify the already committed database outcome.
func TestMySQLConfirmedCommitCleanupFailureIsAfterCommit(t *testing.T) {
	t.Parallel()

	driver, observer := boundedMySQL(t, 1), boundedMySQL(t, 1)
	ctx := testContext(t)
	table, name := uniqueName(t, "confirmed_commit"), uniqueName(t, "cleanup_failure")

	if _, err := driver.Exec(ctx, "CREATE TABLE "+table+" (x INT)"); err != nil {
		t.Fatal(err)
	}

	dropTables(t, database.New(driver), table)
	var originalID int64

	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
		if err := tx.Find(ctx, "SELECT CONNECTION_ID()", &originalID); err != nil {
			return err
		}

		if _, err := tx.Lock(ctx, name); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
			return err
		}

		var released int

		return tx.Find(ctx, "SELECT RELEASE_LOCK(?)", &released, name)
	})

	if !errors.Is(err, database.ErrAfterCommit) {
		t.Fatalf("confirmed commit cleanup = %v, want ErrAfterCommit", err)
	}

	var count int

	if err := driver.Find(ctx, "SELECT COUNT(*) FROM "+table, &count); err != nil || count != 1 {
		t.Fatalf("committed rows = %d, error = %v", count, err)
	}

	var replacementID int64

	if err := driver.Find(ctx, "SELECT CONNECTION_ID()", &replacementID); err != nil {
		t.Fatal(err)
	}

	if replacementID == originalID {
		t.Fatal("uncertain cleanup session was reused")
	}

	assertMySQLLockFree(t, observer, name)
}

// TestMySQLBodyErrorCleanupFailureDoesNotClaimCommit forces the same failed
// unlock on a rollback path. The body error is preserved and cleanup must not
// add an ErrAfterCommit marker suggesting the transaction committed.
func TestMySQLBodyErrorCleanupFailureDoesNotClaimCommit(t *testing.T) {
	t.Parallel()

	driver, observer := boundedMySQL(t, 1), boundedMySQL(t, 1)
	ctx := testContext(t)
	name := uniqueName(t, "rollback_cleanup")

	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
		if _, err := tx.Lock(ctx, name); err != nil {
			return err
		}

		var released int

		if err := tx.Find(ctx, "SELECT RELEASE_LOCK(?)", &released, name); err != nil {
			return err
		}

		return errPlanned
	})

	if !errors.Is(err, errPlanned) || errors.Is(err, database.ErrAfterCommit) {
		t.Fatalf("rollback cleanup = %v", err)
	}

	if driver.SQL().Stats().InUse != 0 {
		t.Fatal("rollback cleanup leaked capacity")
	}

	assertMySQLLockFree(t, observer, name)
}

// TestMySQLSessionLossNeverReconnects terminates a confirmed test-owned session
// between statements. Subsequent queries and transaction scopes must fail on
// that same session, never replay a callback or borrow a replacement connection.
func TestMySQLSessionLossNeverReconnects(t *testing.T) {
	t.Parallel()

	driver, admin := boundedMySQL(t, 1), boundedMySQL(t, 1)
	ctx := testContext(t)
	name := uniqueName(t, "pinned_loss")

	err := driver.WithSession(ctx, func(session database.Driver) error {
		if _, err := session.Lock(ctx, name); err != nil {
			return err
		}

		var id int64

		if err := session.Find(ctx, "SELECT CONNECTION_ID()", &id); err != nil {
			return err
		}

		if owner := killMySQLMigrationSession(t, admin.SQL(), ctx, name); owner != id {
			return fmt.Errorf("lock session = %d, execution session = %d", owner, id)
		}

		var value int

		if err := session.Find(ctx, "SELECT 1", &value); err == nil {
			return errors.New("query reconnected after session loss")
		}

		called := false

		if err := session.WithTransaction(ctx, func(database.Driver) error {
			called = true

			return nil
		}); err == nil || called {
			return errors.New("transaction resumed on a replacement session")
		}

		return nil // Handling the immediate error must not restore session health.
	})

	if err == nil || errors.Is(err, database.ErrAfterCommit) {
		t.Fatalf("lost session outcome = %v", err)
	}

	if driver.SQL().Stats().InUse != 0 {
		t.Fatal("lost session retained a pool slot")
	}

	assertMySQLLockFree(t, admin, name)
}

// TestMySQLTransactionAcquisitionFailureDiscardsAfterRollback covers the
// dangerous ErrBadConn path while sql.Tx retains Conn's read lock. Discarding
// directly in TryLock would deadlock before rollback can release that hold.
func TestMySQLTransactionAcquisitionFailureDiscardsAfterRollback(t *testing.T) {
	t.Parallel()

	driver, admin := boundedMySQL(t, 1), boundedMySQL(t, 1)
	ctx := testContext(t)
	name := uniqueName(t, "tx_loss")

	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
		if _, err := tx.Lock(ctx, name); err != nil {
			return err
		}

		var id int64

		if err := tx.Find(ctx, "SELECT CONNECTION_ID()", &id); err != nil {
			return err
		}

		if owner := killMySQLMigrationSession(t, admin.SQL(), ctx, name); owner != id {
			return fmt.Errorf("lock session = %d, transaction session = %d", owner, id)
		}

		if _, acquired, err := tx.TryLock(ctx, uniqueName(t, "lost_acquisition")); err == nil || acquired {
			return errors.New("acquisition succeeded on a killed transaction session")
		}

		return nil // The failed acquisition must still prevent commit and reuse.
	})

	if err == nil || errors.Is(err, database.ErrAfterCommit) {
		t.Fatalf("lost transaction outcome = %v", err)
	}

	if driver.SQL().Stats().InUse != 0 {
		t.Fatal("failed acquisition leaked a pool slot")
	}

	assertMySQLLockFree(t, admin, name)
}

// TestMySQLUnlockZeroDiscardsWithoutReleasingAnotherHolder forces RELEASE_LOCK
// to return zero rather than NULL. The uncertain session is discarded, while
// the independent session that now owns the name must remain protected.
func TestMySQLUnlockZeroDiscardsWithoutReleasingAnotherHolder(t *testing.T) {
	t.Parallel()

	driver, other, observer := boundedMySQL(t, 1), boundedMySQL(t, 1), boundedMySQL(t, 1)
	ctx := testContext(t)
	name := uniqueName(t, "release_zero")
	var otherUnlock func(context.Context) error

	err := driver.WithSession(ctx, func(session database.Driver) error {
		unlock, err := session.Lock(ctx, name)

		if err != nil {
			return err
		}

		var released int

		if err := session.Find(ctx, "SELECT RELEASE_LOCK(?)", &released, name); err != nil {
			return err
		}

		otherUnlock, err = other.Lock(ctx, name)

		if err != nil {
			return err
		}

		return unlock(ctx)
	})

	if otherUnlock != nil {
		t.Cleanup(func() {
			if err := otherUnlock(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}

	if err == nil {
		t.Fatal("zero RELEASE_LOCK result was treated as successful cleanup")
	}

	if driver.SQL().Stats().InUse != 0 {
		t.Fatal("failed unlock leaked a pool slot")
	}

	assertMySQLLockHeld(t, observer, name)
}

// assertMySQLLockHeld checks exclusion without blocking on a timeout. Its
// observer has independent pool capacity, so failure identifies real backend
// ownership rather than a depleted pool in the lock-owning driver.
func assertMySQLLockHeld(t *testing.T, observer *mysql.Driver, name string) {
	t.Helper()

	unlock, acquired, err := observer.TryLock(testContext(t), name)

	if acquired {
		if err := unlock(testContext(t)); err != nil {
			t.Error(err)
		}
	}

	if err != nil || acquired || unlock != nil {
		t.Fatalf("lock %q was not held: acquired=%v, error=%v", name, acquired, err)
	}
}

// killMySQLMigrationSession terminates only the session currently owning this
// test's randomly named migration lock. The independent admin session verifies
// both the owner and schema before issuing KILL; it never kills itself or an
// arbitrary server connection. The returned ID also proves lock/work affinity.
func killMySQLMigrationSession(t *testing.T, admin *sql.DB, ctx context.Context, name string) int64 {
	t.Helper()

	var owner sql.NullInt64

	if err := admin.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", name).Scan(&owner); err != nil {
		t.Fatal(err)
	}

	var adminID int64

	if err := admin.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	if !owner.Valid || owner.Int64 <= 0 || owner.Int64 == adminID {
		t.Fatalf("unsafe test session identity: lock owner=%v, admin=%d", owner, adminID)
	}

	var databaseName string

	// The legacy PROCESSLIST view materializes other sessions' SQL as utf8mb3
	// and can fail when parallel Unicode fixtures include four-byte characters.
	// Inspect the same owner/schema identity through the native thread metadata.
	if err := admin.QueryRowContext(ctx,
		"SELECT PROCESSLIST_DB FROM performance_schema.threads WHERE PROCESSLIST_ID = ?",
		owner.Int64).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}

	if databaseName != mysqlDatabaseName {
		t.Fatalf("lock owner belongs to schema %q, not the disposable test schema", databaseName)
	}

	if _, err := admin.ExecContext(ctx, fmt.Sprintf("KILL CONNECTION %d", owner.Int64)); err != nil {
		t.Fatal(err)
	}

	return owner.Int64
}

// TestMySQLMigrationSessionLossStopsStaleRollback pauses an earlier harmless
// Down before its bookkeeping deletion. A second migrator adds a column after
// the first lock session dies. The first must not resume its stale target plan
// on another session and drop that table or delete either prior history row.
func TestMySQLMigrationSessionLossStopsStaleRollback(t *testing.T) {
	t.Parallel()

	first, second, admin := boundedMySQL(t, 2), boundedMySQL(t, 2), boundedMySQL(t, 1)
	db, other := database.New(first), database.New(second)
	ctx := testContext(t)
	bookkeeping, table, name := uniqueName(t, "loss_history"), uniqueName(t, "loss_base"), uniqueName(t, "loss_lock")
	dropTables(t, other, bookkeeping, table)
	paused, resume := make(chan int64, 1), make(chan struct{})
	var resumeOnce sync.Once

	base := migrate.NewMigration("base", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Schema(schema.Create(table, func(table *schema.Table) {
			table.Integer("x")
		})))
		migration.Down(migrate.Schema(schema.Drop(table)))
	})
	harmless := migrate.NewMigration("harmless", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Exec(query.Raw("SELECT 1")))
		migration.Down(func(ctx context.Context, db *database.Database) error {
			id, err := db.Find[int64](ctx, query.Raw("SELECT CONNECTION_ID()"))

			if err != nil {
				return err
			}

			paused <- id

			select {
			case <-resume:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	})
	initial := newMigrator(t, db, bookkeeping, name, base, harmless)

	if err := initial.Up(ctx); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	var group sync.WaitGroup

	group.Go(func() {
		result <- initial.Rollback(ctx, migrate.Steps(2))
	})

	t.Cleanup(func() {
		resumeOnce.Do(func() {
			close(resume)
		})
		group.Wait()
	})

	var executionID int64

	select {
	case executionID = <-paused:
	case err := <-result:
		t.Fatalf("rollback ended before barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	owner := killMySQLMigrationSession(t, admin.SQL(), ctx, name)
	future := migrate.NewMigration("future", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Exec(query.Raw(
			fmt.Sprintf("ALTER TABLE %s ADD COLUMN future_value INT NULL", table))))
	})

	if err := newMigrator(t, other, bookkeeping, name, base, harmless, future).Up(ctx); err != nil {
		t.Fatalf("second migrator after session loss: %v", err)
	}

	resumeOnce.Do(func() {
		close(resume)
	})
	group.Wait()

	if err := <-result; err == nil {
		t.Error("first migrator succeeded after losing its session")
	}

	if owner != executionID {
		t.Errorf("migration SQL used session %d but lock used %d", executionID, owner)
	}

	var columns int

	if err := admin.Find(ctx, `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'future_value'`, &columns, table); err != nil {
		t.Fatal(err)
	}

	if columns != 1 {
		t.Error("stale rollback removed the second migrator's schema")
	}

	var records int

	if err := admin.Find(ctx, "SELECT COUNT(*) FROM "+bookkeeping, &records); err != nil {
		t.Fatal(err)
	}

	if records != 3 {
		t.Fatalf("history rows = %d, want unchanged base, harmless, and future rows", records)
	}
}

// TestMySQLNontransactionalMigrationSessionLossPreservesOnlyEarlierDDL shows
// the honest MySQL boundary: committed DDL before session loss remains, but
// later SQL and bookkeeping from the stale runner must never use a new session.
// The second deployment has a different pending migration and can advance once
// the first session is killed; neither deployment edits recorded definitions.
func TestMySQLNontransactionalMigrationSessionLossPreservesOnlyEarlierDDL(t *testing.T) {
	t.Parallel()

	first, second, admin := boundedMySQL(t, 2), boundedMySQL(t, 2), boundedMySQL(t, 1)
	db, other := database.New(first), database.New(second)
	ctx := testContext(t)
	bookkeeping, name := uniqueName(t, "ddl_history"), uniqueName(t, "ddl_lock")
	earlier, later, future := uniqueName(t, "ddl_earlier"), uniqueName(t, "ddl_later"), uniqueName(t, "ddl_future")
	dropTables(t, other, bookkeeping, earlier, later, future)
	paused, resume := make(chan int64, 1), make(chan struct{})
	var resumeOnce sync.Once
	definition := migrate.NewMigration("partial", func(migration *migrate.MigrationBuilder) {
		migration.WithoutTransaction()
		migration.Up(
			migrate.Schema(schema.Create(earlier, func(table *schema.Table) {
				table.Integer("x")
			})),
			func(ctx context.Context, db *database.Database) error {
				id, err := db.Find[int64](ctx, query.Raw("SELECT CONNECTION_ID()"))

				if err != nil {
					return err
				}

				paused <- id

				select {
				case <-resume:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
			migrate.Schema(schema.Create(later, func(table *schema.Table) {
				table.Integer("x")
			})),
		)
	})
	initial := newMigrator(t, db, bookkeeping, name, definition)
	result := make(chan error, 1)
	var group sync.WaitGroup

	group.Go(func() {
		result <- initial.Up(ctx)
	})

	t.Cleanup(func() {
		resumeOnce.Do(func() {
			close(resume)
		})
		group.Wait()
	})

	var executionID int64

	select {
	case executionID = <-paused:
	case err := <-result:
		t.Fatalf("migration ended before barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	owner := killMySQLMigrationSession(t, admin.SQL(), ctx, name)
	next := migrate.NewMigration("future", func(migration *migrate.MigrationBuilder) {
		migration.Up(
			migrate.Schema(schema.Create(future, func(table *schema.Table) {
				table.Integer("x")
			})))
	})

	if err := newMigrator(t, other, bookkeeping, name, next).Up(ctx); err != nil {
		t.Fatalf("second deployment: %v", err)
	}

	resumeOnce.Do(func() {
		close(resume)
	})
	group.Wait()

	if err := <-result; err == nil {
		t.Error("first migration succeeded after losing its session")
	}

	if owner != executionID {
		t.Errorf("migration SQL used session %d but lock used %d", executionID, owner)
	}

	if !tableExists(t, other, earlier) || !tableExists(t, other, future) {
		t.Error("previously committed or second-deployment DDL is missing")
	}

	if tableExists(t, other, later) {
		t.Error("first deployment executed later DDL after losing its lock")
	}

	var records int

	if err := admin.Find(ctx, "SELECT COUNT(*) FROM "+bookkeeping+" WHERE id = ?", &records, "partial"); err != nil {
		t.Fatal(err)
	}

	if records != 0 {
		t.Error("partial nontransactional migration was recorded as complete")
	}

	if err := admin.Find(ctx, "SELECT COUNT(*) FROM "+bookkeeping+" WHERE id = ?", &records, "future"); err != nil {
		t.Fatal(err)
	}

	if records != 1 {
		t.Error("second deployment's migration was not recorded")
	}
}

// shortWait is how long a test waits to prove a locker stays blocked.
const shortWait = 200 * time.Millisecond

// errPlanned is the failure that a test migration returns on purpose.
var errPlanned = errors.New("planned failure")

// integrationDSN returns the DSN of the compose MySQL service, or skips the
// test in short mode.
func integrationDSN(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("integration test")
	}

	return mysqlConnectionURL()
}

// openPool opens a separate connection pool, so it is a separate session set.
func openPool(t *testing.T, dsn string) *database.Database {
	t.Helper()

	driver, err := mysql.New(dsn)

	if err != nil {
		t.Fatalf("new MySQL driver: %v", err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Errorf("close MySQL driver: %v", err)
		}
	})

	return database.New(driver)
}

// uniqueName returns a lower-case identifier unique to the call.
func uniqueName(t *testing.T, prefix string) string {
	t.Helper()

	suffix := make([]byte, 6)

	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}

	return prefix + "_" + hex.EncodeToString(suffix)
}

// cleanupContext returns a context that outlives the test for cleanup work.
func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// dropTables drops the tables when the test ends.
func dropTables(t *testing.T, db *database.Database, tables ...string) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := cleanupContext()
		defer cancel()

		for _, table := range tables {
			if _, err := db.Exec(ctx, query.Raw(fmt.Sprintf("DROP TABLE IF EXISTS %s", table))); err != nil {
				t.Errorf("drop table %s: %v", table, err)
			}
		}
	})
}

// tableExists reports whether the table exists.
func tableExists(t *testing.T, db *database.Database, table string) bool {
	t.Helper()

	count, err := db.Find[int](t.Context(), query.Raw(
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table))

	if err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}

	return count > 0
}

// countingMigration creates a table, counts its runs, and sleeps briefly so
// concurrent runners overlap if the lock fails.
func countingMigration(table string, runs *atomic.Int32) migrate.Definition {
	return migrate.NewMigration(table, func(migration *migrate.MigrationBuilder) {
		migration.Up(
			func(context.Context, *database.Database) error {
				runs.Add(1)
				time.Sleep(20 * time.Millisecond)

				return nil
			},
			migrate.Schema(schema.Create(table, func(table *schema.Table) {
				table.Integer("x")
			})),
		)
		migration.Down(migrate.Schema(schema.Drop(table)))
	})
}

// newMigrator returns a migrator with its own table and lock name.
func newMigrator(t *testing.T, db *database.Database, bookkeeping, lockName string, migrations ...migrate.Definition) *migrate.Migrator {
	t.Helper()

	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.Table(bookkeeping)
	}, func(migrator *migrate.Builder) {
		migrator.LockName(lockName)
	})

	if err := migrator.Add(migrations...); err != nil {
		t.Fatal(err)
	}

	return migrator
}

// TestMySQLLockBlocksSecondConnection checks exclusion across independent pools.
// The second connection must time out while the first owns the named lock.
func TestMySQLLockBlocksSecondConnection(t *testing.T) {
	dsn := integrationDSN(t)
	t.Parallel()

	first, second := openPool(t, dsn), openPool(t, dsn)
	name := uniqueName(t, "lock")

	unlock, err := first.Lock(t.Context(), name)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unlock(context.Background()); err != nil {
			t.Error(err)
		}
	})

	waitContext, cancel := context.WithTimeout(t.Context(), shortWait)
	defer cancel()

	if _, err := second.Lock(waitContext, name); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock() error = %v, want DeadlineExceeded", err)
	}
}

// TestMySQLUnlockReleasesLock checks that explicit release frees the server lock.
// A different pool must then acquire and release the same name successfully.
func TestMySQLUnlockReleasesLock(t *testing.T) {
	dsn := integrationDSN(t)
	t.Parallel()

	first, second := openPool(t, dsn), openPool(t, dsn)
	name := uniqueName(t, "lock")

	unlock, err := first.Lock(t.Context(), name)

	if err != nil {
		t.Fatal(err)
	}

	if err := unlock(t.Context()); err != nil {
		t.Fatal(err)
	}

	waitContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	other, err := second.Lock(waitContext, name)

	if err != nil {
		t.Fatalf("Lock() after unlock error = %v", err)
	}

	if err := other(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestMySQLLockInTransactionReleasedByUnlock checks automatic lock release
// after transaction completion. Locks use the same retained physical session
// as SQL, and an explicit unlock inside the transaction does nothing.
func TestMySQLLockInTransactionReleasedByUnlock(t *testing.T) {
	dsn := integrationDSN(t)
	t.Parallel()

	first, second := openPool(t, dsn), openPool(t, dsn)
	name := uniqueName(t, "lock")

	err := first.WithTransaction(t.Context(), func(txCtx context.Context) error {
		unlock, err := first.Lock(txCtx, name)

		if err != nil {
			return err
		}

		waitContext, cancel := context.WithTimeout(t.Context(), shortWait)
		defer cancel()

		if _, err := second.Lock(waitContext, name); !errors.Is(err, context.DeadlineExceeded) {
			return errors.Join(fmt.Errorf("Lock() inside transaction error = %w, want DeadlineExceeded", err), unlock(t.Context()))
		}

		return unlock(t.Context())
	})

	if err != nil {
		t.Fatal(err)
	}

	waitContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	other, err := second.Lock(waitContext, name)

	if err != nil {
		t.Fatalf("Lock() after unlock error = %v", err)
	}

	if err := other(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestMySQLLockReturnsContextCanceledWhileWaiting checks cancellation of a waiter.
// The driver must preserve context.Canceled while another pool retains the lock.
func TestMySQLLockReturnsContextCanceledWhileWaiting(t *testing.T) {
	dsn := integrationDSN(t)
	t.Parallel()

	first, second := openPool(t, dsn), openPool(t, dsn)
	name := uniqueName(t, "lock")

	unlock, err := first.Lock(t.Context(), name)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unlock(context.Background()); err != nil {
			t.Error(err)
		}
	})

	waitContext, cancel := context.WithCancel(t.Context())
	time.AfterFunc(shortWait, cancel)

	if _, err := second.Lock(waitContext, name); !errors.Is(err, context.Canceled) {
		t.Fatalf("Lock() error = %v, want Canceled", err)
	}
}

// TestMySQLMigrateUpTwiceAppliesOnce checks persisted migration bookkeeping.
// Repeating Up must leave the created table present without rerunning its step.
func TestMySQLMigrateUpTwiceAppliesOnce(t *testing.T) {
	dsn := integrationDSN(t)
	t.Parallel()

	db := openPool(t, dsn)
	table, bookkeeping := uniqueName(t, "mig_users"), uniqueName(t, "mig_book")
	dropTables(t, db, table, bookkeeping)

	var runs atomic.Int32

	migrator := newMigrator(t, db, bookkeeping, uniqueName(t, "lock"), countingMigration(table, &runs))

	for range 2 {
		if err := migrator.Up(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	if got := runs.Load(); got != 1 {
		t.Fatalf("migration ran %d times, want 1", got)
	}

	if !tableExists(t, db, table) {
		t.Fatalf("table %s does not exist", table)
	}
}

// TestMySQLMigrateRollbackDropsTable exercises a migration's reverse schema step.
// After Up succeeds, Rollback must remove the application table it created.
func TestMySQLMigrateRollbackDropsTable(t *testing.T) {
	dsn := integrationDSN(t)
	t.Parallel()

	db := openPool(t, dsn)
	table, bookkeeping := uniqueName(t, "mig_users"), uniqueName(t, "mig_book")
	dropTables(t, db, table, bookkeeping)

	var runs atomic.Int32

	migrator := newMigrator(t, db, bookkeeping, uniqueName(t, "lock"), countingMigration(table, &runs))

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}

	if tableExists(t, db, table) {
		t.Fatalf("table %s still exists after Rollback", table)
	}
}

// TestMySQLMigrateConcurrentUpAppliesEachMigrationOnce checks migration locking.
// Independent pools sharing a ledger and lock must execute each step only once.
func TestMySQLMigrateConcurrentUpAppliesEachMigrationOnce(t *testing.T) {
	dsn := integrationDSN(t)
	t.Parallel()

	first, second := openPool(t, dsn), openPool(t, dsn)
	bookkeeping, lockName := uniqueName(t, "mig_book"), uniqueName(t, "lock")
	tables := []string{uniqueName(t, "mig_a"), uniqueName(t, "mig_b"), uniqueName(t, "mig_c")}
	dropTables(t, first, append(tables, bookkeeping)...)

	runs := make([]*atomic.Int32, len(tables))
	migrations := make([]migrate.Definition, len(tables))

	for index, table := range tables {
		runs[index] = new(atomic.Int32)
		migrations[index] = countingMigration(table, runs[index])
	}

	migrators := []*migrate.Migrator{
		newMigrator(t, first, bookkeeping, lockName, migrations...),
		newMigrator(t, second, bookkeeping, lockName, migrations...),
	}

	var group sync.WaitGroup

	errs := make([]error, len(migrators))

	for index, migrator := range migrators {
		group.Go(func() {
			errs[index] = migrator.Up(t.Context())
		})
	}

	group.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}

	for index, counter := range runs {
		if got := counter.Load(); got != 1 {
			t.Fatalf("migration %s ran %d times, want 1", tables[index], got)
		}
	}
}

// TestMySQLMigrateIdempotentMigrationRetriesAfterFailure checks MySQL's DDL commits.
// A failed, unrecorded migration can retry its idempotent schema step safely.
func TestMySQLMigrateIdempotentMigrationRetriesAfterFailure(t *testing.T) {
	dsn := integrationDSN(t)
	t.Parallel()

	db := openPool(t, dsn)
	table, bookkeeping := uniqueName(t, "mig_retry"), uniqueName(t, "mig_book")
	dropTables(t, db, table, bookkeeping)

	var attempts atomic.Int32

	flaky := migrate.NewMigration(table, func(migration *migrate.MigrationBuilder) {
		migration.Up(
			migrate.Schema(schema.CreateIfNotExists(table, func(table *schema.Table) {
				table.Integer("x")
			})),
			func(context.Context, *database.Database) error {
				if attempts.Add(1) == 1 {
					return errPlanned
				}

				return nil
			},
		)
	})
	migrator := newMigrator(t, db, bookkeeping, uniqueName(t, "lock"), flaky)

	if err := migrator.Up(t.Context()); !errors.Is(err, errPlanned) {
		t.Fatalf("first Up() error = %v, want planned failure", err)
	}

	if !tableExists(t, db, table) {
		t.Fatalf("table %s missing: MySQL should keep committed DDL", table)
	}

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatalf("second Up() error = %v, want the idempotent migration to succeed", err)
	}
}

// TestMySQLMigrateLockTimeoutWhileAnotherPoolHoldsLock checks timeout translation.
// A held migration lock must make Up return the migrator's ErrLockTimeout.
func TestMySQLMigrateLockTimeoutWhileAnotherPoolHoldsLock(t *testing.T) {
	dsn := integrationDSN(t)
	t.Parallel()

	holder, waiter := openPool(t, dsn), openPool(t, dsn)
	lockName := uniqueName(t, "lock")

	unlock, err := holder.Lock(t.Context(), lockName)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unlock(context.Background()); err != nil {
			t.Error(err)
		}
	})

	migrator := migrate.New(waiter,
		func(migrator *migrate.Builder) {
			migrator.Table(uniqueName(t, "mig_book"))
		}, func(migrator *migrate.Builder) {
			migrator.LockName(lockName)
		},
		func(migrator *migrate.Builder) {
			migrator.LockTimeout(shortWait)
		})

	if err := migrator.Up(t.Context()); !errors.Is(err, migrate.ErrLockTimeout) {
		t.Fatalf("Up() error = %v, want ErrLockTimeout", err)
	}
}

// TestMySQLLedgerNativeNeedsNoAlterPrivilege proves normal binary-ledger use
// succeeds with inspection/read/write/create access but no ALTER authorization.
func TestMySQLLedgerNativeNeedsNoAlterPrivilege(t *testing.T) {
	t.Parallel()

	root := openPool(t, integrationDSN(t))
	schema := createLedgerSchema(t, root)
	user := createLedgerUser(t, root, schema)
	dsn := fmt.Sprintf("%s:ledger-fixture-only@tcp(localhost:3306)/%s?parseTime=true", user, schema)
	db := openPool(t, dsn)
	migrator := newMigrator(t, db, "history", uniqueName(t, "lock"),
		migrate.NewMigration("without-alter", func(migration *migrate.MigrationBuilder) {
			migration.Up(func(context.Context, *database.Database) error {
				return nil
			})
		}))

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}

	_, err := db.Exec(t.Context(), query.Raw("ALTER TABLE "+ledgerSQL(schema)+" ADD COLUMN forbidden int"))
	var denied *mysqlprotocol.MySQLError

	if !errors.As(err, &denied) || denied.Number != 1142 {
		t.Fatalf("fixture unexpectedly has ALTER privilege: %v", err)
	}

	assertLedgerBytes(t, ledgerSnapshot(t, root, ledgerSQL(schema)), []string{"without-alter"})
}

// TestMySQLLedgerNativeInspectionPreservesServerCause proves a real SHOW error
// remains discoverable and is not misreported as confirmed conversion need.
func TestMySQLLedgerNativeInspectionPreservesServerCause(t *testing.T) {
	t.Parallel()

	root := openPool(t, integrationDSN(t))
	schema := createLedgerSchema(t, root)
	db := database.New(disappearingLedgerDriver{Driver: root.Driver(), table: ledgerSQL(schema)})
	migrator := newMigrator(t, db, schema+".history", uniqueName(t, "lock"),
		migrate.NewMigration("not-started", func(migration *migrate.MigrationBuilder) {
			migration.Up(func(context.Context, *database.Database) error {
				t.Fatal("metadata failure started Up")

				return nil
			})
		}))

	err := migrator.Up(t.Context())
	var missing *mysqlprotocol.MySQLError

	if !errors.As(err, &missing) || missing.Number != 1146 {
		t.Fatalf("inspection = %v, want original MySQL missing-table error", err)
	}

	if errors.Is(err, migrate.ErrInvalidMigration) || strings.Contains(err.Error(), "convert") {
		t.Fatalf("inspection prescribed conversion: %v", err)
	}
}
