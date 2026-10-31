package sqlitedatabase_test

import (
	"bytes"
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	nativesqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	sqlite "arkoren.dev/driver/sqlite/sqlitedatabase"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"
)

// contextlessConnector hides optional native interfaces behind the minimal
// driver.Conn profile. NewFrom must not silently use its contextless BEGIN when
// a caller expects SQLite transaction creation to honor the supplied context.
type contextlessConnector struct {
	// nativeConnector supplies ordinary connector metadata and connection lifetime.
	*nativeConnector
}

// Connect deliberately exposes only driver.Conn, omitting ConnBeginTx. The
// connection remains native database/sql state rather than an Arkoren mock.
func (connector *contextlessConnector) Connect(ctx context.Context) (sqldriver.Conn, error) {
	connection, err := connector.nativeConnector.Connect(ctx)

	if err != nil {
		return nil, err
	}

	return struct{ sqldriver.Conn }{Conn: connection}, nil
}

// TestNewFromRejectsContextlessNativeBegin covers the documented native profile
// requirement without allowing a callback to run outside a real transaction.
func TestNewFromRejectsContextlessNativeBegin(t *testing.T) {
	t.Parallel()

	pool := sql.OpenDB(&contextlessConnector{nativeConnector: &nativeConnector{}})
	driver := sqlite.NewFrom(pool)
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ran := false

	err := driver.WithTransaction(t.Context(), func(database.Driver) error {
		ran = true

		return nil
	})

	if !errors.Is(err, errors.ErrUnsupported) || ran {
		t.Fatalf("unsupported native profile = %v, callback ran=%v", err, ran)
	}
}

// TestNativeTransactionPreservesImmediateMode checks the direct native boundary
// still honors SQLite's DSN transaction policy. Without BEGIN IMMEDIATE, a
// separate writer would succeed before the first transaction's initial SQL.
func TestNativeTransactionPreservesImmediateMode(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "immediate.sqlite")
	driver, err := sqlite.New(path + "?_txlock=immediate")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	observer, err := sqlite.New(path)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := observer.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ctx := t.Context()

	if _, err := driver.Exec(ctx, "CREATE TABLE writes (value INTEGER)"); err != nil {
		t.Fatal(err)
	}

	err = driver.WithTransaction(ctx, func(database.Driver) error {
		err := observer.WithTransaction(ctx, func(other database.Driver) error {
			_, err := other.Exec(ctx, "INSERT INTO writes VALUES (7)")

			return err
		})

		var native *nativesqlite.Error

		if !errors.As(err, &native) || native.Code() != sqlite3.SQLITE_BUSY {
			t.Errorf("concurrent writer = %v, want SQLITE_BUSY before any first-transaction SQL", err)
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}
}

// TestFailedNativeBeginKeepsEnclosingSessionUsable protects the original native
// failure semantics: a rejected BEGIN IMMEDIATE does not poison a healthy parent
// session. Only a native bad-connection signal requires its owner's discard.
func TestFailedNativeBeginKeepsEnclosingSessionUsable(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "contended.sqlite")
	driver, err := sqlite.New(path + "?_txlock=immediate")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	blocker, err := sqlite.New(path, func(connection *sqlite.Builder) {
		connection.MaxOpenConns(1)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := blocker.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ctx := t.Context()

	if _, err := blocker.Exec(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}

	err = driver.WithSession(ctx, func(session database.Driver) error {
		err := session.WithTransaction(ctx, func(database.Driver) error {
			t.Error("callback ran after contended native BEGIN")

			return nil
		})

		var native *nativesqlite.Error

		if !errors.As(err, &native) || native.Code() != sqlite3.SQLITE_BUSY {
			t.Errorf("contended BEGIN = %v, want SQLITE_BUSY", err)
		}

		var value int

		return session.Find(ctx, "SELECT 1", &value)
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := blocker.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
}

// errBoom marks deliberately failing migration steps so tests can distinguish
// the original failure from wrapping and transaction-cleanup errors.
var errBoom = errors.New("boom")

// newDB opens an isolated in-memory SQLite database.
func newDB(t *testing.T) *database.Database {
	t.Helper()

	driver, err := sqlite.New(":memory:")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return database.New(driver)
}

// create returns a migration that creates and drops a table named id. Optional
// callbacks apply local policy after the shared steps on the same draft.
func create(id string, configure ...func(*migrate.MigrationBuilder)) migrate.Definition {
	return migrate.NewMigration(id, func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Schema(schema.Create(id, func(table *schema.Table) {
			table.Integer("x")
		})))
		migration.Down(migrate.Schema(schema.Drop(id)))

		for _, configure := range configure {
			configure(migration)
		}
	})
}

// failing returns a migration that creates a table and then fails. Optional
// callbacks apply local policy without replacing the failure steps.
func failing(id string, configure ...func(*migrate.MigrationBuilder)) migrate.Definition {
	return migrate.NewMigration(id, func(migration *migrate.MigrationBuilder) {
		migration.Up(
			migrate.Schema(schema.Create(id, func(table *schema.Table) {
				table.Integer("x")
			})),
			func(context.Context, *database.Database) error {
				return errBoom
			},
		)

		for _, configure := range configure {
			configure(migration)
		}
	})
}

// add registers migrations or fails the test.
func add(t *testing.T, migrator *migrate.Migrator, migrations ...migrate.Definition) {
	t.Helper()

	if err := migrator.Add(migrations...); err != nil {
		t.Fatal(err)
	}
}

// up runs Up with a bounded native-operation context or fails the test. The
// timeout prevents a leaked session or migration lock from hanging later checks.
func up(t *testing.T, migrator *migrate.Migrator) {
	t.Helper()

	if err := migrator.Up(migrationTestContext(t)); err != nil {
		t.Fatal(err)
	}
}

// hasTable checks the persisted schema through a fresh, bounded context. It is
// safe to use after a canceled migration and fails if cleanup left the pool stuck.
func hasTable(t *testing.T, db *database.Database, name string) bool {
	t.Helper()

	rows, err := db.Select[struct {
		// Name is the table identifier read from SQLite's schema catalog.
		Name string `db:"name"`
	}](migrationTestContext(t), query.Table("sqlite_master", func(statement *query.Builder) {
		statement.Select("name")
		statement.Where("name", name)
	}))

	if err != nil {
		t.Fatal(err)
	}

	return rows.Len() > 0
}

// status reads migration history with a bounded context or fails the test. A
// post-failure call also verifies that the migration lock can be reacquired.
func status(t *testing.T, migrator *migrate.Migrator) []migrate.Status {
	t.Helper()

	statuses, err := migrator.Status(migrationTestContext(t))

	if err != nil {
		t.Fatal(err)
	}

	return statuses
}

// TestMigrateUpCreatesTableAndRecordsBatchOne checks schema and ledger creation.
// Both application tables must be recorded in the first migration batch.
func TestMigrateUpCreatesTableAndRecordsBatchOne(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	migrator := migrate.New(db)
	add(t, migrator, create("a"), create("b"))

	up(t, migrator)

	statuses := status(t, migrator)

	if !hasTable(t, db, "arkoren_migrations") || !hasTable(t, db, "a") || !hasTable(t, db, "b") {
		t.Fatal("expected tables are missing")
	}

	if len(statuses) != 2 || !statuses[0].Ran || statuses[0].Batch != 1 || statuses[1].Batch != 1 {
		t.Fatalf("statuses = %+v", statuses)
	}
}

// TestMigrateUpRecordsRanAtInUTC checks the persisted execution timestamp.
// RanAt must represent this run and retain the contract's UTC location.
func TestMigrateUpRecordsRanAtInUTC(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(newDB(t))
	add(t, migrator, create("a"))
	before := time.Now().UTC().Add(-time.Minute)

	up(t, migrator)

	ranAt := status(t, migrator)[0].RanAt

	if ranAt.Before(before) || time.Since(ranAt) > time.Minute || ranAt.Location() != time.UTC {
		t.Fatalf("RanAt = %v", ranAt)
	}
}

// TestMigrateUpTwiceIsNoOp checks that applied migrations are not recorded twice.
// A repeated Up must leave the original single record in batch one.
func TestMigrateUpTwiceIsNoOp(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(newDB(t))
	add(t, migrator, create("a"))

	up(t, migrator)
	up(t, migrator)

	statuses := status(t, migrator)

	if len(statuses) != 1 || statuses[0].Batch != 1 {
		t.Fatalf("statuses = %+v", statuses)
	}
}

// TestMigrateUpNewMigrationGetsNextBatch checks batch numbering across runs.
// A definition registered after the first Up must be recorded in batch two.
func TestMigrateUpNewMigrationGetsNextBatch(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(newDB(t))
	add(t, migrator, create("a"))

	up(t, migrator)

	add(t, migrator, create("b"))

	up(t, migrator)

	if got := status(t, migrator)[1].Batch; got != 2 {
		t.Fatalf("Batch = %d", got)
	}
}

// TestMigrateUpFailureKeepsEarlierRecorded checks schema and ledger together.
// A failed later migration must not roll back an earlier migration's commit.
func TestMigrateUpFailureKeepsEarlierRecorded(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	migrator := migrate.New(db)
	add(t, migrator, create("a"), failing("b"), create("c"))

	err := migrator.Up(context.Background())
	statuses := status(t, migrator)

	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "migrate up b: step 2: ") {
		t.Fatalf("err = %v", err)
	}

	if !statuses[0].Ran || statuses[1].Ran || statuses[2].Ran {
		t.Fatalf("statuses = %+v", statuses)
	}

	if !hasTable(t, db, "a") || hasTable(t, db, "b") || hasTable(t, db, "c") {
		t.Fatal("schema does not match partial-progress records")
	}
}

// TestMigrateTransactionalFailureRollsBackDDL checks schema atomicity on failure.
// The original step error must survive and its newly created table must not.
func TestMigrateTransactionalFailureRollsBackDDL(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	migrator := migrate.New(db)
	add(t, migrator, failing("b"))

	if err := migrator.Up(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}

	if hasTable(t, db, "b") {
		t.Fatal("table b survived the rollback")
	}
}

// TestMigrateWithoutTransactionKeepsDDLOnFailure checks explicit nontransactional DDL.
// A later failed step leaves its table present without recording the migration.
func TestMigrateWithoutTransactionKeepsDDLOnFailure(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	migrator := migrate.New(db)
	add(t, migrator, failing("b", func(migration *migrate.MigrationBuilder) {
		migration.WithoutTransaction()
	}))

	if err := migrator.Up(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}

	if !hasTable(t, db, "b") {
		t.Fatal("table b was rolled back")
	}

	if status(t, migrator)[0].Ran {
		t.Fatal("failed migration was recorded")
	}
}

// TestMigrateWithoutTransactionRecordsSuccess checks the successful opt-out path.
// Disabling a migration transaction must not prevent its ledger entry.
func TestMigrateWithoutTransactionRecordsSuccess(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(newDB(t))
	add(t, migrator, create("a", func(migration *migrate.MigrationBuilder) {
		migration.WithoutTransaction()
	}))

	up(t, migrator)

	if !status(t, migrator)[0].Ran {
		t.Fatal("migration not recorded")
	}
}

// TestMigrateNestedTransactionInMigrationJoins checks transaction reuse in a step.
// A nested facade transaction must complete and permit normal ledger recording.
func TestMigrateNestedTransactionInMigrationJoins(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(newDB(t))
	add(t, migrator, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(func(ctx context.Context, db *database.Database) error {
			return db.WithTransaction(ctx, func(context.Context) error {
				return nil
			})
		})
	}))

	if err := migrator.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	if !status(t, migrator)[0].Ran {
		t.Fatal("migration not recorded")
	}
}

// outOfOrderSetup applies b, then adds a before it in a fresh migrator that
// shares the database.
func outOfOrderSetup(t *testing.T, opts ...func(*migrate.Builder)) *migrate.Migrator {
	t.Helper()

	db := newDB(t)
	first := migrate.New(db)
	add(t, first, create("b"))

	up(t, first)

	second := migrate.New(db, opts...)
	add(t, second, create("a"), create("b"))

	return second
}

// TestMigrateOutOfOrderWarnLogsAndRuns checks the default late-migration policy.
// The earlier definition must run while emitting a warning through the logger.
func TestMigrateOutOfOrderWarnLogsAndRuns(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, nil))
	migrator := outOfOrderSetup(t, func(migrator *migrate.Builder) {
		migrator.Logger(logger)
	})

	up(t, migrator)

	if !strings.Contains(buffer.String(), "level=WARN") || !status(t, migrator)[0].Ran {
		t.Fatalf("log = %q", buffer.String())
	}
}

// TestMigrateOutOfOrderAllowRunsSilently checks the explicit permissive policy.
// An earlier definition must run without an out-of-order warning.
func TestMigrateOutOfOrderAllowRunsSilently(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, nil))
	migrator := outOfOrderSetup(t, func(migrator *migrate.Builder) {
		migrator.Logger(logger)
	}, func(migrator *migrate.Builder) {
		migrator.OutOfOrder(migrate.OutOfOrderAllow)
	})

	up(t, migrator)

	if strings.Contains(buffer.String(), "level=WARN") || !status(t, migrator)[0].Ran {
		t.Fatalf("log = %q", buffer.String())
	}
}

// TestMigrateOutOfOrderErrorFailsBeforeRunning checks validation before execution.
// Neither the late definition nor a later valid definition may run on rejection.
func TestMigrateOutOfOrderErrorFailsBeforeRunning(t *testing.T) {
	t.Parallel()

	migrator := outOfOrderSetup(t, func(migrator *migrate.Builder) {
		migrator.OutOfOrder(migrate.OutOfOrderError)
	})
	add(t, migrator, create("c"))

	err := migrator.Up(context.Background())
	statuses := status(t, migrator)

	if !errors.Is(err, migrate.ErrOutOfOrder) || !strings.Contains(err.Error(), "a") {
		t.Fatalf("err = %v", err)
	}

	if statuses[0].Ran || statuses[2].Ran {
		t.Fatalf("migrations ran: %+v", statuses)
	}
}

// TestMigrateStatusFlagsOutOfOrder checks status without executing pending work.
// Only the pending definition before an already applied one is out of order.
func TestMigrateStatusFlagsOutOfOrder(t *testing.T) {
	t.Parallel()

	statuses := status(t, outOfOrderSetup(t))

	if !statuses[0].OutOfOrder || statuses[1].OutOfOrder {
		t.Fatalf("statuses = %+v", statuses)
	}
}

// TestMigrateStatusReportsMissing checks history for a removed definition.
// Its persisted execution remains visible and is marked missing, not pending.
func TestMigrateStatusReportsMissing(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	first := migrate.New(db)
	add(t, first, create("a"), create("gone"))

	up(t, first)

	second := migrate.New(db)
	add(t, second, create("a"))

	statuses := status(t, second)

	if len(statuses) != 2 || statuses[1].ID != "gone" || !statuses[1].Missing || !statuses[1].Ran || statuses[0].Missing {
		t.Fatalf("statuses = %+v", statuses)
	}
}

// TestMigrateRollbackRevertsLastBatch checks the default rollback boundary.
// The newest batch loses its tables and records while the earlier batch remains.
func TestMigrateRollbackRevertsLastBatch(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	migrator := migrate.New(db)
	add(t, migrator, create("a"))

	up(t, migrator)

	add(t, migrator, create("b"), create("c"))

	up(t, migrator)

	if err := migrator.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !hasTable(t, db, "a") || hasTable(t, db, "b") || hasTable(t, db, "c") {
		t.Fatal("unexpected tables after rollback")
	}

	if statuses := status(t, migrator); !statuses[0].Ran || statuses[1].Ran || statuses[2].Ran {
		t.Fatalf("statuses = %+v", statuses)
	}
}

// TestMigrateRollbackStepsRevertsLastN checks an explicit rollback step count.
// Reverting two definitions must preserve the first definition and its table.
func TestMigrateRollbackStepsRevertsLastN(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	migrator := migrate.New(db)
	add(t, migrator, create("a"), create("b"), create("c"))

	up(t, migrator)

	if err := migrator.Rollback(context.Background(), migrate.Steps(2)); err != nil {
		t.Fatal(err)
	}

	if !hasTable(t, db, "a") || hasTable(t, db, "b") || hasTable(t, db, "c") {
		t.Fatal("unexpected tables after rollback")
	}
}

// TestMigrateRollbackThenUpReappliesMigration checks reuse after history removal.
// A fully rolled-back definition must apply again as the first batch.
func TestMigrateRollbackThenUpReappliesMigration(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(newDB(t))
	add(t, migrator, create("a"))

	up(t, migrator)

	if err := migrator.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}

	up(t, migrator)

	if got := status(t, migrator)[0]; !got.Ran || got.Batch != 1 {
		t.Fatalf("status = %+v", got)
	}
}

// TestMigrateRollbackWithoutRecordsIsNoOp checks rollback before any Up call.
// Registered but unapplied definitions must not cause an error.
func TestMigrateRollbackWithoutRecordsIsNoOp(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(newDB(t))
	add(t, migrator, create("a"))

	if err := migrator.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestMigrateRollbackIrreversibleFailsBeforeRunning checks rollback prevalidation.
// Missing Down steps must preserve every applied table rather than partly revert.
func TestMigrateRollbackIrreversibleFailsBeforeRunning(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	migrator := migrate.New(db)
	add(t, migrator, create("a"), migrate.NewMigration("b", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Schema(schema.Create("b", func(table *schema.Table) {
			table.Integer("x")
		})))
	}))

	up(t, migrator)

	err := migrator.Rollback(context.Background())

	if !errors.Is(err, migrate.ErrIrreversible) {
		t.Fatalf("err = %v", err)
	}

	if !hasTable(t, db, "a") || !hasTable(t, db, "b") {
		t.Fatal("rollback ran despite validation failure")
	}
}

// TestMigrateRollbackUnknownMigrationFails checks missing registered definitions.
// Persisted history alone cannot provide Down steps and must report the sentinel.
func TestMigrateRollbackUnknownMigrationFails(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	first := migrate.New(db)
	add(t, first, create("a"))

	up(t, first)

	err := migrate.New(db).Rollback(context.Background())

	if !errors.Is(err, migrate.ErrUnknownMigration) {
		t.Fatalf("err = %v", err)
	}
}

// TestMigrateRollbackFailureKeepsRecord checks ledger consistency after a failed Down.
// The original step error remains discoverable and the migration stays applied.
func TestMigrateRollbackFailureKeepsRecord(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(newDB(t))
	add(t, migrator, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Schema(schema.Create("a", func(table *schema.Table) {
			table.Integer("x")
		})))
		migration.Down(func(context.Context, *database.Database) error {
			return errBoom
		})
	}))

	up(t, migrator)

	err := migrator.Rollback(context.Background())

	if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "migrate down a: step 1: ") {
		t.Fatalf("err = %v", err)
	}

	if !status(t, migrator)[0].Ran {
		t.Fatal("record was deleted")
	}
}

// TestMigrateUsesCustomTable checks the configured bookkeeping-table name.
// Applying work must create the custom ledger without creating the default one.
func TestMigrateUsesCustomTable(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.Table("custom_migrations")
	})
	add(t, migrator, create("a"))

	up(t, migrator)

	if !hasTable(t, db, "custom_migrations") || hasTable(t, db, "arkoren_migrations") {
		t.Fatal("custom table not used")
	}
}

// TestMigrateConcurrentUpAppliesEachOnce checks serialization on a shared database.
// Concurrent migrators must both succeed while the counted step executes once.
func TestMigrateConcurrentUpAppliesEachOnce(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	counter := 0
	var counterMu sync.Mutex
	counting := migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(func(context.Context, *database.Database) error {
			counterMu.Lock()
			defer counterMu.Unlock()

			counter++

			return nil
		})
	})

	errs := make(chan error, 2)

	for range 2 {
		migrator := migrate.New(db)
		add(t, migrator, counting, create("b"))

		go func() {
			errs <- migrator.Up(context.Background())
		}()
	}

	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	if counter != 1 {
		t.Fatalf("migration ran %d times", counter)
	}
}

// TestMigrateLockTimeoutFailsWithErrLockTimeout checks bounded migration acquisition.
// A held lock must preserve both the migration timeout and context deadline errors.
func TestMigrateLockTimeoutFailsWithErrLockTimeout(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	unlock, err := db.Lock(context.Background(), "timeout-held")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unlock(context.Background()); err != nil {
			t.Error(err)
		}
	})

	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.LockName("timeout-held")
	}, func(migrator *migrate.Builder) {
		migrator.LockTimeout(50 * time.Millisecond)
	})
	add(t, migrator, create("timeout_users"))

	err = migrator.Up(context.Background())

	if !errors.Is(err, migrate.ErrLockTimeout) {
		t.Fatalf("Up() error = %v, want ErrLockTimeout", err)
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Up() error = %v, want DeadlineExceeded", err)
	}
}

// TestMigrateCanceledContextIsNotLockTimeout checks caller cancellation precedence.
// An already canceled call must not be mislabeled as a migration lock timeout.
func TestMigrateCanceledContextIsNotLockTimeout(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	unlock, err := db.Lock(context.Background(), "cancel-held")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unlock(context.Background()); err != nil {
			t.Error(err)
		}
	})

	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.LockName("cancel-held")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = migrator.Up(ctx)

	if errors.Is(err, migrate.ErrLockTimeout) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Up() error = %v, want Canceled without ErrLockTimeout", err)
	}
}

// TestMigrateStatusLockTimeoutFailsWithErrLockTimeout checks locked history reads.
// Status must honor the configured acquisition bound and return ErrLockTimeout.
func TestMigrateStatusLockTimeoutFailsWithErrLockTimeout(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	unlock, err := db.Lock(context.Background(), "status-held")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unlock(context.Background()); err != nil {
			t.Error(err)
		}
	})

	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.LockName("status-held")
	}, func(migrator *migrate.Builder) {
		migrator.LockTimeout(50 * time.Millisecond)
	})

	if _, err := migrator.Status(context.Background()); !errors.Is(err, migrate.ErrLockTimeout) {
		t.Fatalf("Status() error = %v, want ErrLockTimeout", err)
	}
}

// TestMigrateRollbackLockTimeoutFailsWithErrLockTimeout checks locked rollback entry.
// Rollback must honor the configured acquisition bound before running Down steps.
func TestMigrateRollbackLockTimeoutFailsWithErrLockTimeout(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	unlock, err := db.Lock(context.Background(), "rollback-held")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unlock(context.Background()); err != nil {
			t.Error(err)
		}
	})

	migrator := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.LockName("rollback-held")
	}, func(migrator *migrate.Builder) {
		migrator.LockTimeout(50 * time.Millisecond)
	})

	if err := migrator.Rollback(context.Background()); !errors.Is(err, migrate.ErrLockTimeout) {
		t.Fatalf("Rollback() error = %v, want ErrLockTimeout", err)
	}
}

// queue returns the migrations of a package group named "pkg"; migrate
// prefixes each short id, so "p1" becomes "pkg/p1".
func queue(ids ...string) []migrate.Definition {
	definitions := make([]migrate.Definition, len(ids))

	for index, id := range ids {
		definitions[index] = create(id)
	}

	return migrate.NewGroup("pkg").Migrations(definitions...)
}

// TestMigrateGroupAppendedMigrationIsNotOutOfOrder checks group-local ordering.
// A package's appended definition may run even when an ungrouped one ran later.
func TestMigrateGroupAppendedMigrationIsNotOutOfOrder(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	first := migrate.New(db)
	add(t, first, queue("p1")...)
	add(t, first, create("a"))

	up(t, first)

	second := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.OutOfOrder(migrate.OutOfOrderError)
	})
	add(t, second, queue("p1", "p2")...)
	add(t, second, create("a"))

	if statuses := status(t, second); statuses[1].OutOfOrder || statuses[1].ID != "pkg/p2" || statuses[1].Group != "pkg" || statuses[2].Group != "" {
		t.Fatalf("statuses = %+v", statuses)
	}

	up(t, second)

	if !hasTable(t, db, "p2") {
		t.Fatal("p2 not applied")
	}
}

// TestMigrateGroupDetectsOutOfOrderWithinGroup checks a late package definition.
// Status and Up must reject its position before an applied sibling in that group.
func TestMigrateGroupDetectsOutOfOrderWithinGroup(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	first := migrate.New(db)
	add(t, first, queue("p2")...)

	up(t, first)

	second := migrate.New(db, func(migrator *migrate.Builder) {
		migrator.OutOfOrder(migrate.OutOfOrderError)
	})
	add(t, second, queue("p1", "p2")...)

	if !status(t, second)[0].OutOfOrder {
		t.Fatal("p1 not flagged out of order")
	}

	if err := second.Up(context.Background()); !errors.Is(err, migrate.ErrOutOfOrder) {
		t.Fatalf("err = %v", err)
	}
}

// TestMigrateGroupEventsCarryFullID checks package identity in emitted events.
// Listeners must receive the qualified migration ID and its separate group name.
func TestMigrateGroupEventsCarryFullID(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var applied []migrate.MigrationEvent

	if _, err := bus.Listen(migrate.Applied, func(_ context.Context, payload migrate.MigrationEvent) error {
		applied = append(applied, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	migrator := migrate.New(newDB(t), func(migrator *migrate.Builder) {
		migrator.Events(bus)
	})
	add(t, migrator, queue("p1")...)

	up(t, migrator)

	if len(applied) != 1 || applied[0].ID != "pkg/p1" || applied[0].Group != "pkg" {
		t.Fatalf("events = %+v", applied)
	}
}

// migrationEvents returns a bus and the "<topic> <id>" entries it records.
func migrationEvents(t *testing.T) (*event.Bus, *[]string) {
	t.Helper()

	bus := event.New()
	var seen []string

	for _, topic := range []event.Topic[migrate.MigrationEvent]{migrate.Applied, migrate.RolledBack, migrate.Failed} {
		if _, err := bus.Listen(topic, func(_ context.Context, payload migrate.MigrationEvent) error {
			seen = append(seen, topic.Name()+" "+payload.ID)

			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	return bus, &seen
}

// TestMigratePublishesAppliedAndRolledBack checks event order across both directions.
// Apply events follow declaration order and rollback events reverse that order.
func TestMigratePublishesAppliedAndRolledBack(t *testing.T) {
	t.Parallel()

	bus, seen := migrationEvents(t)
	migrator := migrate.New(newDB(t), func(migrator *migrate.Builder) {
		migrator.Events(bus)
	})
	add(t, migrator, create("first"), create("second"))

	up(t, migrator)

	if err := migrator.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"database.migration.applied first",
		"database.migration.applied second",
		"database.migration.rolled-back second",
		"database.migration.rolled-back first",
	}

	if len(*seen) != len(want) {
		t.Fatalf("events = %v, want %v", *seen, want)
	}

	for index, name := range want {
		if (*seen)[index] != name {
			t.Fatalf("events = %v, want %v", *seen, want)
		}
	}
}

// TestMigratePublishesFailed checks the event sequence after partial progress.
// A successful definition emits Applied before the next definition emits Failed.
func TestMigratePublishesFailed(t *testing.T) {
	t.Parallel()

	bus, seen := migrationEvents(t)
	migrator := migrate.New(newDB(t), func(migrator *migrate.Builder) {
		migrator.Events(bus)
	})
	add(t, migrator, create("first"), failing("second"))

	if err := migrator.Up(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("up error = %v, want errBoom", err)
	}

	if len(*seen) != 2 || (*seen)[0] != "database.migration.applied first" || (*seen)[1] != "database.migration.failed second" {
		t.Fatalf("events = %v", *seen)
	}
}
