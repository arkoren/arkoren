package postgresdatabase_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	postgres "arkoren.dev/driver/postgres/postgresdatabase"
	"arkoren.dev/driver/postgres/postgressearch"
	"arkoren.dev/foundation/query"
)

// boundedDriver opens an isolated pool against the local integration server.
// Each test chooses its capacity explicitly so hidden extra acquisitions fail
// under the operation deadline instead of hanging the test process.
func boundedDriver(t *testing.T, capacity int) *postgres.Driver {
	t.Helper()

	driver, err := postgres.New(integrationDSN(t), func(databaseClient *postgres.Builder) {
		databaseClient.MaxOpenConns(capacity)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := cleanupContext()
		defer cancel()

		if err := driver.Stop(ctx); err != nil {
			t.Error(err)
		}
	})

	return driver
}

// unavailable checks exclusion from a genuinely independent PostgreSQL session.
// A successful unexpected acquisition is released before the assertion fails.
func unavailable(t *testing.T, observer database.Driver, name string) {
	t.Helper()

	unlock, acquired, err := observer.TryLock(postgresTestContext(t), name)

	if err != nil {
		t.Fatal(err)
	}

	if acquired {
		if err := unlock(postgresTestContext(t)); err != nil {
			t.Error(err)
		}

		t.Fatal("independent session acquired a protected lock")
	}

	if unlock != nil {
		t.Fatal("failed TryLock returned an unlock")
	}
}

// available verifies post-scope release through an independent session. Calling
// its unlock twice also checks idempotent cleanup after the acquisition ends.
func available(t *testing.T, observer database.Driver, name string) {
	t.Helper()

	unlock, acquired, err := observer.TryLock(postgresTestContext(t), name)

	if err != nil || !acquired {
		t.Fatalf("TryLock after scope = %v, %v", acquired, err)
	}

	if err := unlock(postgresTestContext(t)); err != nil {
		t.Fatal(err)
	}

	if err := unlock(postgresTestContext(t)); err != nil {
		t.Fatal(err)
	}
}

// TestNestedLockSurvivesRollback reproduces a fresh acquisition after a
// savepoint: rolling back that savepoint must not release outer ownership.
func TestNestedLockSurvivesRollback(t *testing.T) {
	driver, observer := boundedDriver(t, 1), boundedDriver(t, 1)

	t.Parallel()

	name := uniqueName(t, "nested_lock")

	err := driver.WithTransaction(postgresTestContext(t), func(tx database.Driver) error {
		err := tx.WithTransaction(postgresTestContext(t), func(inner database.Driver) error {
			if _, err := inner.Lock(postgresTestContext(t), name); err != nil {
				return err
			}

			return errPlanned
		})

		if !errors.Is(err, errPlanned) {
			return fmt.Errorf("nested rollback: %w", err)
		}

		unavailable(t, observer, name)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	available(t, observer, name)
}

// TestNestedTryLockSurvivesRollback covers the nonblocking acquisition path,
// which must have the same outer lifetime as blocking Lock.
func TestNestedTryLockSurvivesRollback(t *testing.T) {
	driver, observer := boundedDriver(t, 1), boundedDriver(t, 1)

	t.Parallel()

	name := uniqueName(t, "nested_try")

	err := driver.WithTransaction(postgresTestContext(t), func(tx database.Driver) error {
		err := tx.WithTransaction(postgresTestContext(t), func(inner database.Driver) error {
			_, acquired, err := inner.TryLock(postgresTestContext(t), name)

			if err != nil || !acquired {
				return fmt.Errorf("fresh TryLock = %v: %w", acquired, err)
			}

			return errPlanned
		})

		if !errors.Is(err, errPlanned) {
			return fmt.Errorf("nested rollback: %w", err)
		}

		unavailable(t, observer, name)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	available(t, observer, name)
}

// TestNestedLockSurvivesRecoveredPanic verifies panic recovery inside an outer
// transaction does not introduce an unprotected interval after rollback.
func TestNestedLockSurvivesRecoveredPanic(t *testing.T) {
	driver, observer := boundedDriver(t, 1), boundedDriver(t, 1)

	t.Parallel()

	name := uniqueName(t, "nested_panic")
	panicValue := &struct {
		// value makes this panic payload nonzero-sized for pointer identity checks.
		value int
	}{
		value: 1,
	}

	err := driver.WithTransaction(postgresTestContext(t), func(tx database.Driver) error {
		func() {
			defer func() {
				if got := recover(); got != panicValue {
					t.Errorf("panic = %v, want original error", got)
				}
			}()

			_ = tx.WithTransaction(postgresTestContext(t), func(inner database.Driver) error {
				if _, err := inner.Lock(postgresTestContext(t), name); err != nil {
					return err
				}

				panic(panicValue)
			})
		}()

		unavailable(t, observer, name)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	available(t, observer, name)
}

// TestNestedTryLockSurvivesMultipleLevels covers a successful deepest scope
// followed by an enclosing rollback, before the outer transaction commits.
func TestNestedTryLockSurvivesMultipleLevels(t *testing.T) {
	driver, observer := boundedDriver(t, 1), boundedDriver(t, 1)

	t.Parallel()

	name := uniqueName(t, "nested_levels")

	err := driver.WithTransaction(postgresTestContext(t), func(tx database.Driver) error {
		err := tx.WithTransaction(postgresTestContext(t), func(inner database.Driver) error {
			if err := inner.WithTransaction(postgresTestContext(t), func(deep database.Driver) error {
				_, acquired, err := deep.TryLock(postgresTestContext(t), name)

				if err != nil || !acquired {
					return fmt.Errorf("deep TryLock = %v: %w", acquired, err)
				}

				return nil
			}); err != nil {
				return err
			}

			unavailable(t, observer, name)

			return errPlanned
		})

		if !errors.Is(err, errPlanned) {
			return err
		}

		unavailable(t, observer, name)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	available(t, observer, name)
}

// TestTransactionLocksAreNotReentrant prevents PostgreSQL's native stacking
// from leaking through the portable contract, including nested wrappers.
func TestTransactionLocksAreNotReentrant(t *testing.T) {
	driver, observer := boundedDriver(t, 1), boundedDriver(t, 1)

	t.Parallel()

	name := uniqueName(t, "nonreentrant")

	err := driver.WithTransaction(postgresTestContext(t), func(tx database.Driver) error {
		if _, err := tx.Lock(postgresTestContext(t), name); err != nil {
			return err
		}

		return tx.WithTransaction(postgresTestContext(t), func(inner database.Driver) error {
			unavailable(t, inner, name)

			ctx, cancel := context.WithTimeout(postgresTestContext(t), 20*time.Millisecond)
			defer cancel()

			if _, err := inner.Lock(ctx, name); !errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("repeated Lock: %w", err)
			}

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	available(t, observer, name)
}

// TestSessionPinsAutocommitAndTransactions verifies one-slot pools can execute
// independent commits on one backend without opening an implicit transaction.
func TestSessionPinsAutocommitAndTransactions(t *testing.T) {
	driver := boundedDriver(t, 1)

	t.Parallel()

	var escaped database.Driver

	err := driver.WithSession(postgresTestContext(t), func(session database.Driver) error {
		escaped = session

		var pid int

		if err := session.Find(postgresTestContext(t), "SELECT pg_backend_pid()", &pid); err != nil {
			return err
		}

		if _, err := session.Exec(postgresTestContext(t), "SET application_name = 'arkoren_session_test'"); err != nil {
			return err
		}

		for range 2 {
			if err := session.WithTransaction(postgresTestContext(t), func(tx database.Driver) error {
				var got int

				if err := tx.Find(postgresTestContext(t), "SELECT pg_backend_pid()", &got); err != nil {
					return err
				}

				if got != pid {
					return fmt.Errorf("transaction PID %d, want %d", got, pid)
				}

				_, err := tx.Exec(postgresTestContext(t), "CREATE TEMP TABLE session_commit_test(x int) ON COMMIT DROP")

				return err
			}); err != nil {
				return err
			}
		}

		var exists bool

		if err := session.Find(postgresTestContext(t),
			"SELECT to_regclass('pg_temp.session_commit_test') IS NOT NULL", &exists); err != nil {
			return err
		}

		if exists {
			return errors.New("session unexpectedly retained an outer transaction")
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := escaped.Exec(postgresTestContext(t), "SELECT 1"); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped session = %v", err)
	}

	if err := escaped.(*postgres.Driver).Ping(postgresTestContext(t)); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped session Ping = %v", err)
	}
}

// TestNestedSessionExpiresWithoutReleasingOuterLocks checks callback-specific
// ownership and guards while the surrounding physical connection stays usable.
func TestNestedSessionExpiresWithoutReleasingOuterLocks(t *testing.T) {
	driver, observer := boundedDriver(t, 1), boundedDriver(t, 1)

	t.Parallel()

	outerName, innerName := uniqueName(t, "outer"), uniqueName(t, "inner")

	err := driver.WithSession(postgresTestContext(t), func(session database.Driver) error {
		if _, err := session.Lock(postgresTestContext(t), outerName); err != nil {
			return err
		}

		var escaped database.Driver
		var cursor database.Rows

		if err := session.WithSession(postgresTestContext(t), func(inner database.Driver) error {
			escaped = inner

			unavailable(t, inner, outerName)

			if _, err := inner.Lock(postgresTestContext(t), innerName); err != nil {
				return err
			}

			var err error
			cursor, err = inner.Query(postgresTestContext(t), "SELECT 1")

			return err
		}); err != nil {
			return err
		}

		unavailable(t, observer, outerName)
		available(t, observer, innerName)

		if cursor.Next() || !errors.Is(cursor.Err(), database.ErrSessionDone) {
			return fmt.Errorf("escaped cursor: %w", cursor.Err())
		}

		if err := escaped.WithTransaction(postgresTestContext(t), func(database.Driver) error {
			return errors.New("expired callback ran")
		}); !errors.Is(err, database.ErrSessionDone) {
			return fmt.Errorf("escaped transaction entrypoint: %w", err)
		}

		if _, _, err := escaped.TryLock(postgresTestContext(t), innerName); !errors.Is(err, database.ErrSessionDone) {
			return fmt.Errorf("escaped lock entrypoint: %w", err)
		}

		return session.WithTransaction(postgresTestContext(t), func(tx database.Driver) error {
			unavailable(t, tx, outerName)

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	available(t, observer, outerName)
}

// TestSessionInsideTransactionKeepsTransactionLockOwnership verifies a borrowed
// session does not unlock a fresh acquisition when its shorter callback ends.
func TestSessionInsideTransactionKeepsTransactionLockOwnership(t *testing.T) {
	driver, observer := boundedDriver(t, 1), boundedDriver(t, 1)

	t.Parallel()

	name := uniqueName(t, "borrowed_tx")
	var escaped database.Driver

	err := driver.WithTransaction(postgresTestContext(t), func(tx database.Driver) error {
		if err := tx.WithSession(postgresTestContext(t), func(session database.Driver) error {
			escaped = session

			_, err := session.Lock(postgresTestContext(t), name)

			return err
		}); err != nil {
			return err
		}

		unavailable(t, observer, name)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := escaped.Exec(postgresTestContext(t), "SELECT 1"); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped borrowed session = %v", err)
	}

	available(t, observer, name)
}

// waiterTracer exposes the point after a waiter has reserved the second pool
// slot. Query-start tracing works for both the old blocking SQL and polling SQL.
type waiterTracer struct {
	// started signals the first lock attempt after the raw holder is established.
	started chan struct{}

	// once prevents subsequent polling attempts from closing started twice.
	once sync.Once
}

// TraceQueryStart signals only named-lock acquisition, leaving holder queries
// and unlocks untouched so the test controls the resource dependency exactly.
func (tracer *waiterTracer) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "pg_advisory_lock(") || strings.Contains(data.SQL, "pg_try_advisory_lock(") {
		tracer.once.Do(func() {
			close(tracer.started)
		})
	}

	return ctx
}

// TraceQueryEnd completes pgx.QueryTracer without adding timing or modifying
// connection state; only the start barrier is relevant to the regression.
func (*waiterTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

// TestRootLockWaiterDoesNotStarveHolderQuery reproduces the two-slot client-side
// deadlock without sleeps: a waiter must release a refused attempt's slot.
func TestRootLockWaiterDoesNotStarveHolderQuery(t *testing.T) {
	dsn := integrationDSN(t)

	t.Parallel()

	config, err := pgxpool.ParseConfig(dsn)

	if err != nil {
		t.Fatal(err)
	}

	config.MaxConns = 2
	tracer := &waiterTracer{
		started: make(chan struct{}),
	}
	config.ConnConfig.Tracer = tracer

	pool, err := pgxpool.NewWithConfig(postgresTestContext(t), config)

	if err != nil {
		t.Fatal(err)
	}

	driver := postgres.NewFrom(pool)
	t.Cleanup(func() {
		pool.Close()
	})

	name := uniqueName(t, "pool_waiter")

	unlock, err := driver.Lock(postgresTestContext(t), name)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unlock(context.Background()); err != nil {
			t.Error(err)
		}
	})

	// Replace the already-consumed holder barrier before starting the waiter.
	tracer.started = make(chan struct{})
	tracer.once = sync.Once{}

	ctx, cancel := context.WithTimeout(postgresTestContext(t), 5*time.Second)
	result := make(chan error, 1)
	var group sync.WaitGroup

	group.Go(func() {
		unlock, err := driver.Lock(ctx, name)

		if err == nil {
			err = unlock(context.Background())
		}

		result <- err
	})
	t.Cleanup(func() {
		cancel()
		group.Wait()
	})

	select {
	case <-tracer.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	queryCtx, queryCancel := context.WithTimeout(postgresTestContext(t), time.Second)
	defer queryCancel()

	var one int

	if err := driver.Find(queryCtx, "SELECT 1", &one); err != nil {
		t.Fatalf("holder query starved behind waiter: %v", err)
	}

	if err := unlock(postgresTestContext(t)); err != nil {
		t.Fatal(err)
	}

	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

// TestExpiredTransactionLockCannotReacquire ensures the lock entrypoint is
// guarded just like SQL after the connection has returned to a one-slot pool.
func TestExpiredTransactionLockCannotReacquire(t *testing.T) {
	driver := boundedDriver(t, 1)

	t.Parallel()

	var escaped database.Driver

	if err := driver.WithTransaction(postgresTestContext(t), func(tx database.Driver) error {
		escaped = tx

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := escaped.TryLock(postgresTestContext(t), uniqueName(t, "expired")); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("expired transaction lock = %v", err)
	}

	if err := escaped.(*postgres.Driver).Ping(postgresTestContext(t)); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("expired transaction Ping = %v", err)
	}
}

// TestMigrationRollbackStopsAfterSessionLoss protects transactional migrations
// from executing a stale destructive plan after their exclusion session dies.
func TestMigrationRollbackStopsAfterSessionLoss(t *testing.T) {
	testMigrationSessionLoss(t, false)
}

// TestNontransactionalMigrationStopsAfterSessionLoss exercises the same physical
// session guarantee without relying on transactional DDL to undo later work.
func TestNontransactionalMigrationStopsAfterSessionLoss(t *testing.T) {
	testMigrationSessionLoss(t, true)
}

// testMigrationSessionLoss uses unique search-driver instances and bookkeeping
// tables. The only terminated backend is atomically checked against this test's
// unique application_name and granted advisory lock; no shared schema is dropped.
// A Go barrier pauses an earlier harmless Down before the shipped indexes DROP,
// so relation-lock ordering cannot hide stale execution on a fresh connection.
func testMigrationSessionLoss(t *testing.T, nontransactional bool) {
	t.Helper()

	dsn := integrationDSN(t)

	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)

	application := uniqueName(t, "migration_owner")
	config, err := pgxpool.ParseConfig(dsn)

	if err != nil {
		t.Fatal(err)
	}

	config.ConnConfig.RuntimeParams["application_name"] = application
	config.MaxConns = 2

	pool, err := pgxpool.NewWithConfig(ctx, config)

	if err != nil {
		t.Fatal(err)
	}

	first := postgres.NewFrom(pool)
	t.Cleanup(func() {
		ctx, cancel := cleanupContext()
		defer cancel()

		if err := first.Stop(ctx); err != nil {
			t.Error(err)
		}
	})

	second := boundedDriver(t, 2)
	firstDB, secondDB := database.New(first), database.New(second)
	instance := uniqueName(t, "search")

	search, err := postgressearch.NewFrom(firstDB, func(engine *postgressearch.Builder) {
		engine.Instance(instance)
	})

	if err != nil {
		t.Fatal(err)
	}

	bookkeeping, lockName := uniqueName(t, "migration_book"), uniqueName(t, "migration_lock")
	unrelated := uniqueName(t, "earlier_down")
	indexes, documents := instance+"_search_indexes", instance+"_search_documents"
	dropTables(t, secondDB, unrelated, indexes, documents, bookkeeping)

	reached, resume := make(chan int, 1), make(chan struct{})

	earlier := migrate.NewMigration("earlier-harmless-down", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Exec(
			query.Raw("CREATE TABLE " + unrelated + " (value integer)"),
		))
		migration.Down(
			func(ctx context.Context, db *database.Database) error {
				pid, err := db.Find[int](ctx, query.Raw("SELECT pg_backend_pid()"))

				if err != nil {
					return err
				}

				reached <- pid

				select {
				case <-resume:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
			migrate.Exec(query.Raw("DROP TABLE "+unrelated)),
		)

		if nontransactional {
			migration.WithoutTransaction()
		}
	})
	definitions := append(search.Migrations(), earlier)
	firstMigrator := newMigrator(t, firstDB, bookkeeping, lockName, definitions...)

	if err := firstMigrator.Up(ctx); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	var group sync.WaitGroup

	group.Go(func() {
		result <- firstMigrator.Rollback(ctx, migrate.Steps(2))
	})

	var release sync.Once
	t.Cleanup(func() {
		cancel()
		release.Do(func() {
			close(resume)
		})
		group.Wait()
	})

	var executionPID int

	select {
	case executionPID = <-reached:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	lockPID, err := secondDB.Find[int](ctx, query.Raw(`
SELECT a.pid FROM pg_stat_activity a JOIN pg_locks l ON l.pid = a.pid
WHERE a.application_name = ? AND a.datname = current_database()
AND l.locktype = 'advisory' AND l.granted`, application))

	if err != nil {
		t.Fatal(err)
	}

	terminated, err := secondDB.Find[bool](ctx, query.Raw(`
SELECT pg_terminate_backend(a.pid) FROM pg_stat_activity a
WHERE a.pid = ? AND a.application_name = ? AND a.datname = current_database()
AND a.pid <> pg_backend_pid()
AND EXISTS (SELECT 1 FROM pg_locks l WHERE l.pid = a.pid
AND l.locktype = 'advisory' AND l.granted)`, lockPID, application))

	if err != nil || !terminated {
		t.Fatalf("terminate owned backend %d = %v, %v", lockPID, terminated, err)
	}

	appended := migrate.NewMigration("concurrent-index-column", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Exec(
			query.Raw("ALTER TABLE " + indexes + " ADD COLUMN preserved text"),
		))
	})
	secondMigrator := newMigrator(t, secondDB, bookkeeping, lockName, append(definitions, appended)...)

	if err := secondMigrator.Up(ctx); err != nil {
		t.Fatal(err)
	}

	release.Do(func() {
		close(resume)
	})

	if err := <-result; err == nil {
		t.Fatal("first rollback succeeded after its exclusion session was terminated")
	}

	if !tableExists(t, secondDB, indexes) {
		t.Fatalf("stale rollback dropped protected search table (lock PID %d, execution PID %d)", lockPID, executionPID)
	}

	if !tableExists(t, secondDB, unrelated) {
		t.Fatal("terminated migration continued its earlier Down on another session")
	}

	for _, id := range []string{earlier.ID(), appended.ID()} {
		count, err := secondDB.Find[int](ctx,
			query.Raw("SELECT count(*) FROM "+bookkeeping+" WHERE id = ?", id))

		if err != nil || count != 1 {
			t.Fatalf("history %s = %d, %v", id, count, err)
		}
	}

	columns, err := secondDB.Find[int](ctx, query.Raw(
		"SELECT count(*) FROM information_schema.columns WHERE table_name = ? AND column_name = 'preserved'", indexes))

	if err != nil || columns != 1 {
		t.Fatal(fmt.Errorf("concurrent schema was not preserved: columns %d: %w", columns, err))
	}
}

// TestTransactionLockReleasedAfterError checks rollback completes before the
// physical connection returns, including a failed TryLock owned by somebody else.
func TestTransactionLockReleasedAfterError(t *testing.T) {
	driver, observer := boundedDriver(t, 1), boundedDriver(t, 1)

	t.Parallel()

	ctx := postgresTestContext(t)
	name, other := uniqueName(t, "rollback"), uniqueName(t, "other_owner")

	unlock, err := observer.Lock(ctx, other)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unlock(context.Background()); err != nil {
			t.Error(err)
		}
	})

	err = driver.WithTransaction(ctx, func(tx database.Driver) error {
		if _, err := tx.Lock(ctx, name); err != nil {
			return err
		}

		unavailable(t, tx, other)

		return errPlanned
	})

	if !errors.Is(err, errPlanned) || errors.Is(err, database.ErrAfterCommit) {
		t.Fatalf("rollback error = %v", err)
	}

	// The observer still owns its lock, while rollback released the fresh one.
	unavailable(t, driver, other)

	if err := unlock(ctx); err != nil {
		t.Fatal(err)
	}

	available(t, observer, name)
}

// TestTransactionLockReleasedAfterPanic verifies original panic identity and
// session cleanup even though the callback never returns an error value.
func TestTransactionLockReleasedAfterPanic(t *testing.T) {
	driver, observer := boundedDriver(t, 1), boundedDriver(t, 1)

	t.Parallel()

	ctx := postgresTestContext(t)
	name := uniqueName(t, "outer_panic")
	panicValue := &struct {
		// value makes this panic payload nonzero-sized for pointer identity checks.
		value int
	}{
		value: 1,
	}

	func() {
		defer func() {
			if got := recover(); got != panicValue {
				t.Errorf("panic = %v, want original value", got)
			}
		}()

		_ = driver.WithTransaction(ctx, func(tx database.Driver) error {
			if _, err := tx.Lock(ctx, name); err != nil {
				return err
			}

			panic(panicValue)
		})
	}()

	available(t, observer, name)

	var one int

	if err := driver.Find(ctx, "SELECT 1", &one); err != nil {
		t.Fatal(err)
	}
}

// TestTransactionLockReleasedAfterCancellation checks detached rollback and
// unlock work after the transaction's caller has canceled its context.
func TestTransactionLockReleasedAfterCancellation(t *testing.T) {
	driver, observer := boundedDriver(t, 1), boundedDriver(t, 1)

	t.Parallel()

	ctx, cancel := context.WithCancel(postgresTestContext(t))
	t.Cleanup(cancel)
	name := uniqueName(t, "outer_cancel")

	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
		if _, err := tx.Lock(ctx, name); err != nil {
			return err
		}

		cancel()

		return nil
	})

	if !errors.Is(err, context.Canceled) || errors.Is(err, database.ErrAfterCommit) {
		t.Fatalf("canceled transaction = %v", err)
	}

	available(t, observer, name)
}

// TestConfirmedCommitCleanupFailureDiscardsSession deliberately removes only
// this test's managed locks through raw SQL. A false unlock must report committed
// data without recycling the session whose ownership invariant was violated.
func TestConfirmedCommitCleanupFailureDiscardsSession(t *testing.T) {
	driver := boundedDriver(t, 1)

	t.Parallel()

	ctx := postgresTestContext(t)
	table, name := uniqueName(t, "committed"), uniqueName(t, "cleanup")
	db := database.New(driver)
	dropTables(t, db, table)

	if _, err := driver.Exec(ctx, "CREATE TABLE "+table+" (value integer)"); err != nil {
		t.Fatal(err)
	}

	var ownedPID int

	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
		if err := tx.Find(ctx, "SELECT pg_backend_pid()", &ownedPID); err != nil {
			return err
		}

		if _, err := tx.Lock(ctx, name); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
			return err
		}

		_, err := tx.Exec(ctx, "SELECT pg_advisory_unlock_all()")

		return err
	})

	if !errors.Is(err, database.ErrAfterCommit) {
		t.Fatalf("confirmed commit cleanup error = %v", err)
	}

	count, err := db.Find[int](ctx, query.Raw("SELECT count(*) FROM "+table))

	if err != nil || count != 1 {
		t.Fatalf("committed data = %d, %v", count, err)
	}

	var nextPID int

	if err := driver.Find(ctx, "SELECT pg_backend_pid()", &nextPID); err != nil {
		t.Fatal(err)
	}

	if nextPID == ownedPID {
		t.Fatal("uncertain unlock returned its physical session for reuse")
	}
}

// TestRollbackCleanupFailureIsNotAfterCommit verifies the marker depends on a
// confirmed native commit, not merely a failed post-callback unlock.
func TestRollbackCleanupFailureIsNotAfterCommit(t *testing.T) {
	driver := boundedDriver(t, 1)

	t.Parallel()

	ctx := postgresTestContext(t)

	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
		if _, err := tx.Lock(ctx, uniqueName(t, "rollback_cleanup")); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, "SELECT pg_advisory_unlock_all()"); err != nil {
			return err
		}

		return errPlanned
	})

	if !errors.Is(err, errPlanned) || errors.Is(err, database.ErrAfterCommit) {
		t.Fatalf("rollback cleanup = %v", err)
	}

	var one int

	if err := driver.Find(ctx, "SELECT 1", &one); err != nil {
		t.Fatal(err)
	}
}

// TestFailedCommitIsNotAfterCommit defers a unique constraint until native
// COMMIT. A successful callback alone must not mark failed commits as committed,
// and discarding the failed session must release its managed advisory key.
func TestFailedCommitIsNotAfterCommit(t *testing.T) {
	driver, observer := boundedDriver(t, 1), boundedDriver(t, 1)

	t.Parallel()

	ctx := postgresTestContext(t)
	name := uniqueName(t, "failed_commit")

	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
		if _, err := tx.Lock(ctx, name); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE deferred_constraint (
value integer UNIQUE DEFERRABLE INITIALLY DEFERRED) ON COMMIT DROP`); err != nil {
			return err
		}

		_, err := tx.Exec(ctx, "INSERT INTO deferred_constraint VALUES (1), (1)")

		return err
	})

	if err == nil || errors.Is(err, database.ErrAfterCommit) {
		t.Fatalf("failed native commit = %v", err)
	}

	available(t, observer, name)
}

// TestFullTransactionPoolAcquiresDistinctLocks synchronizes both occupied pool
// slots before either transaction asks for an uncontended advisory key.
func TestFullTransactionPoolAcquiresDistinctLocks(t *testing.T) {
	driver := boundedDriver(t, 2)

	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ready, proceed := make(chan struct{}, 2), make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup

	for range 2 {
		name := uniqueName(t, "full_pool")

		group.Go(func() {
			results <- driver.WithTransaction(ctx, func(tx database.Driver) error {
				ready <- struct{}{}

				select {
				case <-proceed:
				case <-ctx.Done():
					return ctx.Err()
				}

				_, acquired, err := tx.TryLock(ctx, name)

				if err != nil || !acquired {
					return fmt.Errorf("uncontended transaction lock = %v: %w", acquired, err)
				}

				return nil
			})
		})
	}

	t.Cleanup(func() {
		cancel()
		group.Wait()
	})

	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}

	close(proceed)

	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}
