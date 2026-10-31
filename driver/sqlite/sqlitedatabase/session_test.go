package sqlitedatabase_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	sqlite "arkoren.dev/driver/sqlite/sqlitedatabase"
	"arkoren.dev/foundation/query"
)

// TestSessionUsesOneConnection verifies temporary state survives multiple
// transactions and ended callback drivers cannot reach a reused connection.
func TestSessionUsesOneConnection(t *testing.T) {
	t.Parallel()

	driver, err := sqlite.New(":memory:")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), sqliteCleanupTimeout)
	defer cancel()

	var escaped database.Driver

	err = driver.WithSession(ctx, func(session database.Driver) error {
		escaped = session

		if _, err := session.Exec(ctx, "CREATE TEMPORARY TABLE pinned (id INTEGER)"); err != nil {
			return err
		}

		for range 2 {
			if err := session.WithTransaction(ctx, func(tx database.Driver) error {
				_, err := tx.Exec(ctx, "INSERT INTO pinned VALUES (1)")

				return err
			}); err != nil {
				return err
			}
		}

		var count int

		if err := session.Find(ctx, "SELECT COUNT(*) FROM pinned", &count); err != nil {
			return err
		}

		if count != 2 {
			t.Fatalf("count = %d, want 2", count)
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := escaped.Exec(ctx, "DELETE FROM pinned"); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped Exec = %v", err)
	}
}

// TestScopedPingUsesReservedConnection exercises the concrete driver's
// readiness method with a one-connection pool, then checks its escaped lifetime.
func TestScopedPingUsesReservedConnection(t *testing.T) {
	t.Parallel()

	driver, err := sqlite.New(filepath.Join(t.TempDir(), "session.db"), func(databaseClient *sqlite.Builder) {
		databaseClient.MaxOpenConns(1)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), sqliteCleanupTimeout)
	defer cancel()

	var escaped *sqlite.Driver

	err = driver.WithSession(ctx, func(session database.Driver) error {
		escaped = session.(*sqlite.Driver)

		if err := escaped.Ping(ctx); err != nil {
			return err
		}

		if inUse := driver.SQL().Stats().InUse; inUse != 1 {
			t.Fatalf("connections in use = %d, want 1", inUse)
		}

		return escaped.WithTransaction(ctx, func(tx database.Driver) error {
			_, err := tx.Lock(ctx, "single-connection-"+t.Name())

			return err
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := escaped.Ping(ctx); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("ended Ping = %v", err)
	}

	if inUse := driver.SQL().Stats().InUse; inUse != 0 {
		t.Fatalf("connections still in use = %d", inUse)
	}
}

// TestCanceledSessionRejectsFreshOperationContext prevents callers from
// continuing a canceled scope by passing context.Background to an operation.
func TestCanceledSessionRejectsFreshOperationContext(t *testing.T) {
	t.Parallel()

	driver, err := sqlite.New(filepath.Join(t.TempDir(), "session.db"))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	err = driver.WithSession(ctx, func(session database.Driver) error {
		cancel()

		if _, err := session.Exec(context.Background(), "CREATE TABLE forbidden (id INTEGER)"); !errors.Is(err, context.Canceled) {
			t.Fatalf("fresh-context Exec = %v", err)
		}

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WithSession = %v", err)
	}

	var count int

	if err := driver.Find(t.Context(), "SELECT COUNT(*) FROM sqlite_master WHERE name = 'forbidden'", &count); err != nil {
		t.Fatal(err)
	}

	if count != 0 {
		t.Fatal("canceled session executed SQL")
	}
}

// TestCanceledTransactionRetainsMemorySession verifies cancellation rolls back
// without discarding the in-memory database. Synctest lets cancellation work
// quiesce before callback return, without sleeps or access to private transaction
// state; the same retained session must remain usable after its child rolls back.
func TestCanceledTransactionRetainsMemorySession(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver, connection := newSQLiteDriver(t)
		registerSQLiteConnectionCleanup(t, connection)
		ctx := t.Context()

		if _, err := driver.Exec(ctx, "CREATE TABLE retained (value INTEGER)"); err != nil {
			t.Fatal(err)
		}

		err := driver.WithSession(ctx, func(session database.Driver) error {
			work, cancel := context.WithCancel(ctx)
			t.Cleanup(cancel)

			err := session.WithTransaction(work, func(tx database.Driver) error {
				if _, err := tx.Exec(work, "INSERT INTO retained VALUES (7)"); err != nil {
					return err
				}

				cancel()
				synctest.Wait()

				return nil
			})

			if !errors.Is(err, context.Canceled) || errors.Is(err, database.ErrAfterCommit) {
				t.Fatalf("canceled transaction = %v", err)
			}

			var count int

			if err := session.Find(ctx, "SELECT COUNT(*) FROM retained", &count); err != nil {
				return err
			}

			if count != 0 {
				t.Fatalf("rolled-back rows = %d, want 0", count)
			}

			_, err = session.Exec(ctx, "INSERT INTO retained VALUES (9)")

			return err
		})

		if err != nil {
			t.Fatal(err)
		}

		var value int

		if err := driver.Find(ctx, "SELECT value FROM retained", &value); err != nil || value != 9 {
			t.Fatalf("reused memory database = %d, %v; want 9", value, err)
		}
	})
}

// migrationTestContext bounds native pool and lock waits without real sleeps.
// Each caller gets an independent budget and cancellation registered with the
// test, so a routing or cleanup regression fails instead of hanging the suite.
func migrationTestContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	return ctx
}

// newMigrationFileDatabase opens a one-connection pool on path and arranges its
// cleanup. A file preserves committed schema when cancellation discards a native
// connection; an in-memory database could disappear and hide a rollback bug.
func newMigrationFileDatabase(t *testing.T, path string) (*database.Database, *sql.DB) {
	t.Helper()

	driver, err := sqlite.New(path, func(databaseClient *sqlite.Builder) {
		databaseClient.MaxOpenConns(1)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return database.New(driver), driver.SQL()
}

// TestMigratePinsOneConnectionAcrossTransactionBoundaries uses a TEMP table as a
// physical-session marker. Both transactional and autocommit migrations must see
// it, without acquiring a second connection from this one-connection pool.
func TestMigratePinsOneConnectionAcrossTransactionBoundaries(t *testing.T) {
	t.Parallel()

	db, pool := newMigrationFileDatabase(t, filepath.Join(t.TempDir(), "migrations.db"))
	migrator := migrate.New(db)
	add(t, migrator,
		migrate.NewMigration("marker", func(migration *migrate.MigrationBuilder) {
			migration.Up(func(ctx context.Context, db *database.Database) error {
				if pool.Stats().InUse != 1 {
					t.Error("migration does not retain its physical connection")
				}

				return migrate.Exec(query.Raw("CREATE TEMP TABLE migration_marker (value INTEGER)"),
					query.Raw("INSERT INTO migration_marker VALUES (7)"))(ctx, db)
			})
			migration.WithoutTransaction()
		}),
		migrate.NewMigration("transactional", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Exec(
				query.Raw("CREATE TABLE persisted AS SELECT value FROM migration_marker")))
		}),
		migrate.NewMigration("autocommit", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Exec(
				query.Raw("INSERT INTO persisted SELECT value FROM migration_marker")))
			migration.WithoutTransaction()
		}),
		migrate.NewMigration("last", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Exec(
				query.Raw("INSERT INTO persisted SELECT value FROM migration_marker")))
		}),
	)
	ctx := migrationTestContext(t)

	if err := migrator.Up(ctx); err != nil {
		t.Fatal(err)
	}

	count, err := db.Find[int](ctx, query.Raw("SELECT COUNT(*) FROM persisted WHERE value = 7"))

	if err != nil || count != 3 {
		t.Fatalf("count = %d, error = %v", count, err)
	}

	for _, existing := range status(t, migrator) {
		if !existing.Ran || existing.Batch != 1 {
			t.Fatalf("status = %+v", existing)
		}
	}

	if pool.Stats().InUse != 0 {
		t.Fatal("migration leaked its connection")
	}
}

// TestMigrateLockTimeoutIncludesNativePoolWait occupies the only connection
// before migration entry. Timeout must precede any lock, preparation, or history
// SQL, rather than waiting forever for a session to become available.
func TestMigrateLockTimeoutIncludesNativePoolWait(t *testing.T) {
	t.Parallel()

	db, pool := newMigrationFileDatabase(t, filepath.Join(t.TempDir(), "migrations.db"))
	conn, err := pool.Conn(migrationTestContext(t))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := conn.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
			t.Error(err)
		}
	})

	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.LockTimeout(20 * time.Millisecond)
	})

	_, err = migrator.Status(migrationTestContext(t))

	if !errors.Is(err, migrate.ErrLockTimeout) || !errors.Is(err, context.DeadlineExceeded) || pool.Stats().WaitCount == 0 {
		t.Fatalf("error = %v, pool = %+v", err, pool.Stats())
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if hasTable(t, db, "arkoren_migrations") {
		t.Fatal("preparation ran before session acquisition")
	}
}

// TestMigrateJoinsIncomingTransactionRollback ensures the session wrapper does
// not escape a caller-owned transaction. Its schema, preparation, and ledger
// must all roll back together when the caller rejects that outer transaction.
func TestMigrateJoinsIncomingTransactionRollback(t *testing.T) {
	t.Parallel()

	db, _ := newMigrationFileDatabase(t, filepath.Join(t.TempDir(), "migrations.db"))
	migrator := migrate.New(db)
	add(t, migrator, create("a"), create("b", func(migration *migrate.MigrationBuilder) {
		migration.WithoutTransaction()
	}))
	ctx := migrationTestContext(t)

	err := db.WithTransaction(ctx, func(ctx context.Context) error {
		if err := migrator.Up(ctx); err != nil {
			return err
		}

		return errBoom
	})

	if !errors.Is(err, errBoom) || hasTable(t, db, "a") || hasTable(t, db, "b") || hasTable(t, db, "arkoren_migrations") {
		t.Fatalf("outer rollback error = %v", err)
	}

	up(t, migrator)

	if statuses := status(t, migrator); len(statuses) != 2 || !statuses[0].Ran || !statuses[1].Ran {
		t.Fatalf("retry after outer rollback = %+v", statuses)
	}
}

// TestMigrateCancellationKeepsEarlierCommitAndReleasesSession cancels after DDL
// in the second migration. Persistent schema and ledger must agree, and Status
// must reacquire the migration lock after cleanup despite the canceled caller.
func TestMigrateCancellationKeepsEarlierCommitAndReleasesSession(t *testing.T) {
	t.Parallel()

	db, pool := newMigrationFileDatabase(t, filepath.Join(t.TempDir(), "migrations.db"))
	ctx, cancel := context.WithCancel(migrationTestContext(t))
	defer cancel()

	migrator := migrate.New(db)
	add(t, migrator, create("a"), migrate.NewMigration("b", func(migration *migrate.MigrationBuilder) {
		migration.Up(func(ctx context.Context, db *database.Database) error {
			if _, err := db.Exec(ctx, query.Raw("CREATE TABLE b (x INTEGER)")); err != nil {
				return err
			}

			cancel()

			return ctx.Err()
		})
	}), create("c"))

	err := migrator.Up(ctx)

	if !errors.Is(err, context.Canceled) || errors.Is(err, migrate.ErrLockTimeout) {
		t.Fatalf("error = %v", err)
	}

	if !hasTable(t, db, "a") || hasTable(t, db, "b") || hasTable(t, db, "c") {
		t.Fatal("schema does not reflect individual migration commits")
	}

	statuses := status(t, migrator)

	if !statuses[0].Ran || statuses[1].Ran || statuses[2].Ran || pool.Stats().InUse != 0 {
		t.Fatalf("statuses = %+v, pool = %+v", statuses, pool.Stats())
	}
}

// TestMigratePanicKeepsEarlierCommitAndReleasesSession checks panic propagation
// and transaction rollback against a file database. A fresh migrator must be
// able to acquire the released lock and finish the remaining migrations.
func TestMigratePanicKeepsEarlierCommitAndReleasesSession(t *testing.T) {
	t.Parallel()

	db, _ := newMigrationFileDatabase(t, filepath.Join(t.TempDir(), "migrations.db"))
	migrator := migrate.New(db)
	add(t, migrator, create("a"), migrate.NewMigration("b", func(migration *migrate.MigrationBuilder) {
		migration.Up(func(ctx context.Context, db *database.Database) error {
			if _, err := db.Exec(ctx, query.Raw("CREATE TABLE b (x INTEGER)")); err != nil {
				return err
			}

			panic(errBoom)
		})
	}), create("c"))

	func() {
		defer func() {
			got := recover()
			err, ok := got.(error)

			if !ok || !errors.Is(err, errBoom) {
				t.Fatalf("panic = %v", got)
			}
		}()

		_ = migrator.Up(migrationTestContext(t))
	}()

	if !hasTable(t, db, "a") || hasTable(t, db, "b") || hasTable(t, db, "c") {
		t.Fatal("panic left schema inconsistent with earlier commits")
	}

	statuses := status(t, migrator)

	if !statuses[0].Ran || statuses[1].Ran || statuses[2].Ran {
		t.Fatalf("statuses = %+v", statuses)
	}

	remaining := migrate.New(db)
	add(t, remaining, create("a"), create("b"), create("c"))
	up(t, remaining)
}

// TestMigrateSeparatePoolsSerializeUpAndRollback holds an Up callback while a
// second pool starts Rollback. The latter must plan from the newly committed
// history, not revert the earlier batch from a stale pre-lock snapshot.
func TestMigrateSeparatePoolsSerializeUpAndRollback(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migrations.db")
	firstDB, _ := newMigrationFileDatabase(t, path)
	secondDB, _ := newMigrationFileDatabase(t, path)
	first := migrate.New(firstDB)
	add(t, first, create("a"))

	up(t, first)

	entered := make(chan struct{})
	release := make(chan struct{})
	add(t, first, migrate.NewMigration("b", func(migration *migrate.MigrationBuilder) {
		migration.Up(func(ctx context.Context, db *database.Database) error {
			close(entered)

			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}

			_, err := db.Exec(ctx, query.Raw("CREATE TABLE b (x INTEGER)"))

			return err
		})
		migration.Down(migrate.Exec(query.Raw("DROP TABLE b")))
	}))
	second := migrate.New(secondDB)
	add(t, second, create("a"), create("b"))
	ctx := migrationTestContext(t)
	upDone := make(chan error, 1)

	go func() {
		upDone <- first.Up(ctx)
	}()

	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	rollbackDone := make(chan error, 1)
	rollbackStarted := make(chan struct{})

	go func() {
		close(rollbackStarted)
		rollbackDone <- second.Rollback(ctx)
	}()

	<-rollbackStarted
	close(release)

	if err := <-upDone; err != nil {
		t.Fatal(err)
	}

	if err := <-rollbackDone; err != nil {
		t.Fatal(err)
	}

	statuses := status(t, second)

	if !hasTable(t, firstDB, "a") || hasTable(t, firstDB, "b") || !statuses[0].Ran || statuses[1].Ran {
		t.Fatalf("serialized statuses = %+v", statuses)
	}
}

// TestMigrateSeparatePoolsApplyOnce guards session-scoped exclusion across two
// independent one-connection pools. The schema and ledger must contain exactly
// one execution, not merely end with a duplicate-record error.
func TestMigrateSeparatePoolsApplyOnce(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migrations.db")
	firstDB, _ := newMigrationFileDatabase(t, path)
	secondDB, _ := newMigrationFileDatabase(t, path)
	var runs atomic.Int32
	definition := migrate.NewMigration("once", func(migration *migrate.MigrationBuilder) {
		migration.Up(func(ctx context.Context, db *database.Database) error {
			runs.Add(1)

			_, err := db.Exec(ctx, query.Raw("CREATE TABLE once (x INTEGER)"))

			return err
		})
	})
	first, second := migrate.New(firstDB), migrate.New(secondDB)
	add(t, first, definition)
	add(t, second, definition)
	ctx := migrationTestContext(t)
	done := make(chan error, 2)

	go func() {
		done <- first.Up(ctx)
	}()
	go func() {
		done <- second.Up(ctx)
	}()

	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}

	if runs.Load() != 1 || !hasTable(t, firstDB, "once") || !status(t, second)[0].Ran {
		t.Fatalf("executions = %d", runs.Load())
	}
}

// TestMigrateCommittedHookFailureKeepsAppliedEvent checks a real commit followed
// by an after-commit error. Hooks still use the pinned session, the completed
// migration is recorded and reported as applied, and later work does not run.
func TestMigrateCommittedHookFailureKeepsAppliedEvent(t *testing.T) {
	t.Parallel()

	db, _ := newMigrationFileDatabase(t, filepath.Join(t.TempDir(), "migrations.db"))
	bus, seen := migrationEvents(t)
	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.Events(bus)
	})
	add(t, migrator, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Exec(query.Raw("CREATE TABLE a (x INTEGER)")),
			func(ctx context.Context, db *database.Database) error {
				return db.AfterCommit(ctx, func(ctx context.Context) error {
					if _, err := db.Exec(ctx, query.Raw("INSERT INTO a VALUES (1)")); err != nil {
						return err
					}

					return errBoom
				})
			})
	}), create("b"))

	err := migrator.Up(migrationTestContext(t))

	if !errors.Is(err, database.ErrAfterCommit) || !errors.Is(err, errBoom) || !slices.Equal(*seen, []string{"database.migration.applied a"}) {
		t.Fatalf("error = %v, events = %v", err, *seen)
	}

	statuses := status(t, migrator)
	count, err := db.Find[int](migrationTestContext(t), query.Raw("SELECT COUNT(*) FROM a"))

	if err != nil || count != 1 || !statuses[0].Ran || statuses[1].Ran || hasTable(t, db, "b") {
		t.Fatalf("count = %d, error = %v, statuses = %+v", count, err, statuses)
	}
}

// TestMigrateCallbackAfterCommitMarkerStillRollsBack uses the same sentinel as a
// callback failure instead of a hook result. It must not claim that schema and
// bookkeeping committed merely because errors.Is finds ErrAfterCommit.
func TestMigrateCallbackAfterCommitMarkerStillRollsBack(t *testing.T) {
	t.Parallel()

	db, _ := newMigrationFileDatabase(t, filepath.Join(t.TempDir(), "migrations.db"))
	bus, seen := migrationEvents(t)
	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.Events(bus)
	})
	add(t, migrator, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Exec(query.Raw("CREATE TABLE a (x INTEGER)")),
			func(context.Context, *database.Database) error {
				return database.ErrAfterCommit
			})
	}))

	err := migrator.Up(migrationTestContext(t))

	if !errors.Is(err, database.ErrAfterCommit) || !slices.Equal(*seen, []string{"database.migration.failed a"}) || hasTable(t, db, "a") || status(t, migrator)[0].Ran {
		t.Fatalf("error = %v, events = %v", err, *seen)
	}
}

// TestMigrateRollbackFailureKeepsEarlierDownCommit protects downward partial
// progress. The completed later migration stays reverted while the failing
// earlier migration retains both its schema and bookkeeping row.
func TestMigrateRollbackFailureKeepsEarlierDownCommit(t *testing.T) {
	t.Parallel()

	db, _ := newMigrationFileDatabase(t, filepath.Join(t.TempDir(), "migrations.db"))
	migrator := migrate.New(db)
	add(t, migrator, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Exec(query.Raw("CREATE TABLE a (x INTEGER)")))
		migration.Down(
			migrate.Exec(query.Raw("DROP TABLE a")), func(context.Context, *database.Database) error {
				return errBoom
			})
	}), create("b"))
	up(t, migrator)

	if err := migrator.Rollback(migrationTestContext(t)); !errors.Is(err, errBoom) {
		t.Fatalf("error = %v", err)
	}

	statuses := status(t, migrator)

	if !hasTable(t, db, "a") || hasTable(t, db, "b") || !statuses[0].Ran || statuses[1].Ran {
		t.Fatalf("statuses = %+v", statuses)
	}
}

// TestMigrateNontransactionalFailureKeepsUnrecordedDDL verifies that session
// pinning does not add a hidden encompassing transaction. Earlier commits and
// the failed autocommit migration's partial schema must both remain visible.
func TestMigrateNontransactionalFailureKeepsUnrecordedDDL(t *testing.T) {
	t.Parallel()

	db, _ := newMigrationFileDatabase(t, filepath.Join(t.TempDir(), "migrations.db"))
	migrator := migrate.New(db)
	add(t, migrator, create("a"), failing("b", func(migration *migrate.MigrationBuilder) {
		migration.WithoutTransaction()
	}), create("c"))

	if err := migrator.Up(migrationTestContext(t)); !errors.Is(err, errBoom) {
		t.Fatalf("error = %v", err)
	}

	statuses := status(t, migrator)

	if !hasTable(t, db, "a") || !hasTable(t, db, "b") || hasTable(t, db, "c") || !statuses[0].Ran || statuses[1].Ran || statuses[2].Ran {
		t.Fatalf("statuses = %+v", statuses)
	}
}

// TestMigrateRollbackHookFailureKeepsRolledBackEvent checks the downward
// post-commit outcome against persistent schema and ledger, including stopping
// before the next target instead of replaying a committed Down callback.
func TestMigrateRollbackHookFailureKeepsRolledBackEvent(t *testing.T) {
	t.Parallel()

	db, _ := newMigrationFileDatabase(t, filepath.Join(t.TempDir(), "migrations.db"))
	bus, seen := migrationEvents(t)
	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.Events(bus)
	})
	add(t, migrator, create("a"), migrate.NewMigration("b", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Exec(query.Raw("CREATE TABLE b (x INTEGER)")))
		migration.Down(
			migrate.Exec(query.Raw("DROP TABLE b")), func(ctx context.Context, db *database.Database) error {
				return db.AfterCommit(ctx, func(context.Context) error {
					return errBoom
				})
			})
	}))
	up(t, migrator)

	err := migrator.Rollback(migrationTestContext(t))

	if !errors.Is(err, database.ErrAfterCommit) || !errors.Is(err, errBoom) || !slices.Equal(*seen, []string{
		"database.migration.applied a", "database.migration.applied b", "database.migration.rolled-back b",
	}) {
		t.Fatalf("error = %v, events = %v", err, *seen)
	}

	statuses := status(t, migrator)

	if !hasTable(t, db, "a") || hasTable(t, db, "b") || !statuses[0].Ran || statuses[1].Ran {
		t.Fatalf("statuses = %+v", statuses)
	}
}

// TestMigrateEventListenerUsesLivePinnedSession reads connection-local state
// synchronously from Applied. The event must carry the session context, not the
// original caller context that would request another pool connection.
func TestMigrateEventListenerUsesLivePinnedSession(t *testing.T) {
	t.Parallel()

	db, _ := newMigrationFileDatabase(t, filepath.Join(t.TempDir(), "migrations.db"))
	bus := event.New()
	count := 0

	if _, err := bus.Listen(migrate.Applied, func(ctx context.Context, _ migrate.MigrationEvent) error {
		value, err := db.Find[int](ctx, query.Raw("SELECT value FROM event_marker"))

		if err != nil {
			t.Error(err)

			return err
		}

		count += value

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.Events(bus)
	})
	add(t, migrator, migrate.NewMigration("marker", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Exec(
			query.Raw("CREATE TEMP TABLE event_marker (value INTEGER)"),
			query.Raw("INSERT INTO event_marker VALUES (1)")))
		migration.WithoutTransaction()
	}), create("a"))
	ctx := migrationTestContext(t)

	if err := migrator.Up(ctx); err != nil || count != 2 {
		t.Fatalf("error = %v, event reads = %d", err, count)
	}
}
