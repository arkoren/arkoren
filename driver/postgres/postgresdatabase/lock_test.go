package postgresdatabase_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	postgres "arkoren.dev/driver/postgres/postgresdatabase"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"
)

// shortWait is how long a test waits to prove a locker stays blocked.
const shortWait = 200 * time.Millisecond

// errPlanned is the failure that a test migration returns on purpose.
var errPlanned = errors.New("planned failure")

// integrationDSN returns the DSN of the compose PostgreSQL service, or skips
// the test in short mode.
func integrationDSN(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("integration test")
	}

	return postgresConnectionURL(sharedPostgresEndpoint.host, sharedPostgresEndpoint.port)
}

// openPool opens a separate connection pool, so it is a separate session set.
func openPool(t *testing.T, dsn string) *database.Database {
	t.Helper()

	driver, err := postgres.New(dsn)

	if err != nil {
		t.Fatalf("new PostgreSQL driver: %v", err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Errorf("close PostgreSQL driver: %v", err)
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

	exists, err := db.Find[bool](t.Context(), query.Raw("SELECT to_regclass(?) IS NOT NULL", table))

	if err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}

	return exists
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

// TestPostgresLockBlocksSecondConnection checks exclusion across independent pools.
// The second connection must time out while the first owns the named lock.
func TestPostgresLockBlocksSecondConnection(t *testing.T) {
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

// TestPostgresUnlockReleasesLock checks explicit advisory-lock release.
// Another pool must then acquire and release the same name successfully.
func TestPostgresUnlockReleasesLock(t *testing.T) {
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

// TestPostgresLockInTransactionReleasedAtCommit checks transaction lock ownership.
// A competing pool cannot acquire the name until the owning transaction commits.
func TestPostgresLockInTransactionReleasedAtCommit(t *testing.T) {
	dsn := integrationDSN(t)

	t.Parallel()

	first, second := openPool(t, dsn), openPool(t, dsn)
	name := uniqueName(t, "lock")

	err := first.WithTransaction(t.Context(), func(txCtx context.Context) error {
		if _, err := first.Lock(txCtx, name); err != nil {
			return err
		}

		waitContext, cancel := context.WithTimeout(t.Context(), shortWait)
		defer cancel()

		if _, err := second.Lock(waitContext, name); !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("Lock() inside transaction error = %w, want DeadlineExceeded", err)
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	waitContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	other, err := second.Lock(waitContext, name)

	if err != nil {
		t.Fatalf("Lock() after commit error = %v", err)
	}

	if err := other(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresLockReturnsContextCanceledWhileWaiting checks a canceled lock waiter.
// The driver must preserve context.Canceled while another pool retains the lock.
func TestPostgresLockReturnsContextCanceledWhileWaiting(t *testing.T) {
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

// TestPostgresMigrateUpTwiceAppliesOnce checks persisted migration bookkeeping.
// Repeating Up must preserve the table without rerunning its migration step.
func TestPostgresMigrateUpTwiceAppliesOnce(t *testing.T) {
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

// TestPostgresMigrateRollbackDropsTable exercises a migration's reverse schema step.
// After Up succeeds, Rollback must remove the application table it created.
func TestPostgresMigrateRollbackDropsTable(t *testing.T) {
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

// TestPostgresMigrateConcurrentUpAppliesEachMigrationOnce checks migration locking.
// Independent pools sharing a ledger and lock must execute each step only once.
func TestPostgresMigrateConcurrentUpAppliesEachMigrationOnce(t *testing.T) {
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

// TestPostgresMigrateTransactionalFailureRollsBackDDL checks schema atomicity.
// A failing later step must undo the table created earlier in that migration.
func TestPostgresMigrateTransactionalFailureRollsBackDDL(t *testing.T) {
	dsn := integrationDSN(t)

	t.Parallel()

	db := openPool(t, dsn)
	table, bookkeeping := uniqueName(t, "mig_fail"), uniqueName(t, "mig_book")
	dropTables(t, db, table, bookkeeping)

	failing := migrate.NewMigration(table, func(migration *migrate.MigrationBuilder) {
		migration.Up(
			migrate.Schema(schema.Create(table, func(table *schema.Table) {
				table.Integer("x")
			})),
			func(context.Context, *database.Database) error {
				return errPlanned
			},
		)
	})
	migrator := newMigrator(t, db, bookkeeping, uniqueName(t, "lock"), failing)

	if err := migrator.Up(t.Context()); !errors.Is(err, errPlanned) {
		t.Fatalf("Up() error = %v, want planned failure", err)
	}

	if tableExists(t, db, table) {
		t.Fatalf("table %s exists: DDL was not rolled back", table)
	}
}

// TestPostgresMigrateLockTimeoutWhileAnotherPoolHoldsLock checks timeout translation.
// A held migration lock must make Up return the migrator's ErrLockTimeout.
func TestPostgresMigrateLockTimeoutWhileAnotherPoolHoldsLock(t *testing.T) {
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
