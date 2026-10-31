package migrate_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"
)

// ledgerCall records the SQL and argument values visible at the public driver
// seam. It intentionally does not model MySQL charset or collation semantics.
type ledgerCall struct {
	// sql is the exact compiled statement sent by the facade.
	sql string
	// args are detached outer arguments; binary IDs remain available for checks.
	args []any
}

// mysqlLedgerState scripts server metadata and history through public SQL.
// Each test owns its state; all access stays synchronous inside one session.
type mysqlLedgerState struct {
	// calls records successful scope routing, including reads and preparation.
	calls []ledgerCall
	// schema is the database returned by SELECT DATABASE; empty represents NULL.
	schema string
	// columns is the actual SHOW FULL COLUMNS result, not the requested DDL.
	columns []map[string]any
	// indexes is SHOW INDEX metadata, including primary-key parts and prefixes.
	indexes []map[string]any
	// records contains history rows read through SELECT id, batch, ran_at.
	records []map[string]any
	// databaseErr fails discovery without manufacturing a schema mismatch.
	databaseErr error
	// columnsErr fails column inspection independently of table preparation.
	columnsErr error
	// indexesErr fails index inspection independently of column inspection.
	indexesErr error
	// held tracks the public TryLock/unlock lifetime around every SQL call.
	held bool
}

// mysqlLedgerDriver extends the existing fake with a target-addressed metadata
// protocol. Pool SQL is a trap; only the locked retained session can execute it.
type mysqlLedgerDriver struct {
	// fakeDriver preserves recording and the remaining existing driver methods.
	*fakeDriver
	// state is shared by the root and its retained session.
	state *mysqlLedgerState
	// route is empty at the root and session inside WithSession.
	route string
}

// newMySQLLedgerDriver supplies canonical metadata but no real backend. Native
// byte transport, metadata visibility and charset behavior require integration.
func newMySQLLedgerDriver() *mysqlLedgerDriver {
	return &mysqlLedgerDriver{
		fakeDriver: &fakeDriver{dialect: query.MySQL},
		state: &mysqlLedgerState{
			schema: "application",
			columns: []map[string]any{{
				"Field": "id", "Type": "varbinary(191)", "Collation": nil,
				"Null": "NO", "Key": "PRI", "Default": nil, "Extra": "",
				"Privileges": "select,insert,update,references", "Comment": "",
			}},
			indexes: []map[string]any{{
				"Table": "arkoren_migrations", "Non_unique": int64(0), "Key_name": "PRIMARY",
				"Seq_in_index": int64(1), "Column_name": "id", "Collation": "A",
				"Cardinality": int64(0), "Sub_part": nil, "Packed": nil, "Null": "",
				"Index_type": "BTREE", "Comment": "", "Index_comment": "", "Visible": "YES", "Expression": nil,
			}},
		},
	}
}

// WithSession exposes a distinct executor without changing the shared fixture.
// The callback is the public boundary used by Migrator, not an internal hook.
func (driver *mysqlLedgerDriver) WithSession(_ context.Context, fn func(database.Driver) error) error {
	return fn(&mysqlLedgerDriver{fakeDriver: driver.fakeDriver, state: driver.state, route: "session"})
}

// TryLock brackets all SQL, including discovery and metadata. Acquiring the
// same fake twice is an error so accidental reentrancy cannot silently pass.
func (driver *mysqlLedgerDriver) TryLock(context.Context, string) (func(context.Context) error, bool, error) {
	if driver.route != "session" || driver.state.held {
		return nil, false, errFake
	}

	driver.state.held = true

	return func(context.Context) error {
		driver.state.held = false

		return nil
	}, true, nil
}

// recordSQL refuses scope escape and snapshots the public call before its
// scripted response. It does not interpret SQL beyond the test protocol.
func (driver *mysqlLedgerDriver) recordSQL(ctx context.Context, statement string, args []any) error {
	if driver.route != "session" || !driver.state.held {
		return fmt.Errorf("SQL escaped locked session: %s", statement)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	driver.state.calls = append(driver.state.calls, ledgerCall{sql: statement, args: slices.Clone(args)})

	return nil
}

// Find scripts database discovery as a nullable scalar and rejects unexpected
// lookups. Metadata errors must not be mistaken for a missing legacy table.
func (driver *mysqlLedgerDriver) Find(ctx context.Context, statement string, dest any, args ...any) error {
	if err := driver.recordSQL(ctx, statement, args); err != nil {
		return err
	}

	if statement != "SELECT DATABASE()" {
		return fmt.Errorf("unexpected Find: %s", statement)
	}

	if driver.state.databaseErr != nil {
		return driver.state.databaseErr
	}

	if driver.state.schema != "" {
		*dest.(**string) = &driver.state.schema
	}

	return nil
}

// Select returns complete server-shaped metadata or history through the public
// query.Targets scanning contract, without knowing migrator-private row types.
func (driver *mysqlLedgerDriver) Select(ctx context.Context, statement string, dest any, args ...any) error {
	if err := driver.recordSQL(ctx, statement, args); err != nil {
		return err
	}

	if strings.HasPrefix(statement, "SHOW FULL COLUMNS FROM ") {
		if driver.state.columnsErr != nil {
			return driver.state.columnsErr
		}

		return scanLedgerRows(dest, driver.state.columns)
	}

	if strings.HasPrefix(statement, "SHOW INDEX FROM ") {
		if driver.state.indexesErr != nil {
			return driver.state.indexesErr
		}

		return scanLedgerRows(dest, driver.state.indexes)
	}

	return scanLedgerRows(dest, driver.state.records)
}

// Exec records observable statements and simulates only USE, so tests can
// prove that subsequent bookkeeping does not follow a changed default schema.
func (driver *mysqlLedgerDriver) Exec(ctx context.Context, statement string, args ...any) (int64, error) {
	if err := driver.recordSQL(ctx, statement, args); err != nil {
		return 0, err
	}

	if statement == "USE `other`" {
		driver.state.schema = "other"
	}

	return driver.fakeDriver.Exec(ctx, statement, args...)
}

// scanLedgerRows appends synthetic SQL rows into a facade destination. Row
// field resolution uses query.Targets, just like a conforming SQL driver.
func scanLedgerRows(dest any, rows []map[string]any) error {
	output := reflect.ValueOf(dest).Elem()

	for _, values := range rows {
		row := reflect.New(output.Type().Elem())

		if err := scanLedgerRow(row.Interface(), values); err != nil {
			return err
		}

		output.Set(reflect.Append(output, row.Elem()))
	}

	return nil
}

// scanLedgerRow fills public SQL scan targets, including SQL NULL and nullable
// string metadata. It fails loudly on incompatible fixture representations.
func scanLedgerRow(dest any, values map[string]any) error {
	columns := slices.Sorted(maps.Keys(values))

	targets, err := query.Targets(dest, columns)

	if err != nil {
		return err
	}

	for index, column := range columns {
		field := reflect.ValueOf(targets[index]).Elem()
		value := values[column]

		if value == nil {
			field.SetZero()
			continue
		}

		if field.Kind() == reflect.Pointer {
			field.Set(reflect.New(field.Type().Elem()))

			field = field.Elem()
		}

		field.Set(reflect.ValueOf(value))
	}

	return nil
}

// TestMySQLLedgerCreatesByteExactQualifiedStorage proves creation is deferred
// to Up and requests canonical binary storage on the locked session.
func TestMySQLLedgerCreatesByteExactQualifiedStorage(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	migrator := migrate.New(database.New(driver))

	if err := migrator.Add(migrate.NewMigration("créate", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	})); err != nil {
		t.Fatal(err)
	}

	if len(driver.state.calls) != 0 {
		t.Fatal("construction performed ledger I/O")
	}

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}

	want := "CREATE TABLE IF NOT EXISTS `application`.`arkoren_migrations` (`id` VARBINARY(191) NOT NULL PRIMARY KEY, `batch` int NOT NULL, `ran_at` datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6))"

	if !slices.Contains(driver.executed, want) {
		t.Fatalf("SQL = %q, want %q", driver.executed, want)
	}
}

// TestMySQLLedgerRejectsTextBeforeCallbacksAndEvents verifies a legacy ledger
// is never converted, mutated, or used to start a definition before rejection.
func TestMySQLLedgerRejectsTextBeforeCallbacksAndEvents(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.columns[0]["Type"] = "varchar(191)"
	driver.state.columns[0]["Collation"] = "utf8mb4_general_ci"
	bus := event.New()
	events := 0

	for _, topic := range []event.Topic[migrate.MigrationEvent]{migrate.Applied, migrate.RolledBack, migrate.Failed} {
		unsubscribe, err := bus.Listen(topic, func(context.Context, migrate.MigrationEvent) error {
			events++

			return nil
		})

		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(unsubscribe)
	}

	migrator := migrate.New(database.New(driver), func(migrator *migrate.Builder) {
		migrator.Events(bus)
	})

	started := 0

	if err := migrator.Add(migrate.NewMigration("first", func(migration *migrate.MigrationBuilder) {
		migration.Up(func(context.Context, *database.Database) error {
			started++

			return nil
		})
	})); err != nil {
		t.Fatal(err)
	}

	err := migrator.Up(t.Context())

	if !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want ErrInvalidMigration", err)
	}

	if started != 0 || events != 0 {
		t.Fatalf("callbacks = %d, events = %d", started, events)
	}

	if !strings.Contains(err.Error(), "utf8mb4_general_ci") || !strings.Contains(err.Error(), "operator") {
		t.Fatalf("missing actual layout or repair notice: %v", err)
	}

	requireLedgerPreparationOnly(t, driver)
}

// TestMySQLLedgerInspectionFailureKeepsCause distinguishes unreadable metadata
// from confirmed unsafe storage; it must not tell callers to convert blindly.
func TestMySQLLedgerInspectionFailureKeepsCause(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	cause := errors.New("metadata access denied")
	driver.state.columnsErr = cause
	migrator := migrate.New(database.New(driver))

	if err := migrator.Add(migrate.NewMigration("pending", func(migration *migrate.MigrationBuilder) {
		migration.Up(func(context.Context, *database.Database) error {
			t.Fatal("unverified ledger started a migration")

			return nil
		})
	})); err != nil {
		t.Fatal(err)
	}

	err := migrator.Up(t.Context())

	if !errors.Is(err, cause) || errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want inspection cause, not invalid-layout classification", err)
	}

	if strings.Contains(err.Error(), "convert") {
		t.Fatalf("inspection failure prescribed a conversion: %v", err)
	}

	requireLedgerPreparationOnly(t, driver)
}

// TestMySQLLedgerFreezesDefaultSchemaAndBinaryID verifies UTF-8 bytes and the
// inspected table survive a user callback changing the session's current DB.
func TestMySQLLedgerFreezesDefaultSchemaAndBinaryID(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	migrator := migrate.New(database.New(driver))
	definitions := migrate.NewGroup("acme.users").Migrations(migrate.NewMigration("créate", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Exec(query.Raw("USE `other`")))
	}))

	if err := migrator.Add(definitions...); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}

	want := "INSERT INTO `application`.`arkoren_migrations` (`batch`, `id`, `ran_at`) VALUES (?, ?, ?)"
	last := driver.state.calls[len(driver.state.calls)-1]

	if last.sql != want {
		t.Fatalf("ledger insert = %s, want frozen %s", last.sql, want)
	}

	id, ok := last.args[1].([]byte)

	if !ok || !bytes.Equal(id, []byte("acme.users/créate")) {
		t.Fatalf("ledger id = %T(%v), want original UTF-8 bytes", last.args[1], last.args[1])
	}
}

// TestMySQLLedgerRejectsRollbackBeforeDown verifies safety validation also
// protects nontransactional downward steps and their ledger deletion.
func TestMySQLLedgerRejectsRollbackBeforeDown(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.columns[0]["Type"] = "varchar(191)"
	driver.state.columns[0]["Collation"] = "utf8mb4_bin"
	driver.state.records = []map[string]any{{"id": "applied", "batch": 1, "ran_at": time.Time{}}}
	migrator := migrate.New(database.New(driver))

	if err := migrator.Add(migrate.NewMigration("applied", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Down(func(context.Context, *database.Database) error {
			t.Fatal("legacy ledger started Down")

			return nil
		})
	})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Rollback(t.Context()); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Rollback = %v, want ErrInvalidMigration", err)
	}

	requireLedgerPreparationOnly(t, driver)
}

// requireLedgerPreparationOnly checks the observable no-user/history-I/O
// boundary shared by every rejected preparation, including missing metadata.
func requireLedgerPreparationOnly(t *testing.T, driver *mysqlLedgerDriver) {
	t.Helper()

	for _, call := range driver.state.calls {
		verb := strings.Fields(call.sql)[0]

		if slices.Contains([]string{"ALTER", "INSERT", "DELETE"}, verb) {
			t.Fatalf("rejected preparation changed the ledger: %s", call.sql)
		}

		if strings.HasPrefix(call.sql, "SELECT `id`") {
			t.Fatalf("history was read before layout verification: %s", call.sql)
		}
	}
}

// rejectMySQLLedgerUp drives the public seam for several unsupported metadata
// shapes. It asserts fail-closed behavior without examining private validators.
func rejectMySQLLedgerUp(t *testing.T, driver *mysqlLedgerDriver) error {
	t.Helper()

	migrator := migrate.New(database.New(driver))

	if err := migrator.Add(migrate.NewMigration("pending", func(migration *migrate.MigrationBuilder) {
		migration.Up(func(context.Context, *database.Database) error {
			t.Fatal("unverified ledger started Up")

			return nil
		})
	})); err != nil {
		t.Fatal(err)
	}

	err := migrator.Up(t.Context())

	if err == nil {
		t.Fatal("unverified ledger was accepted")
	}

	requireLedgerPreparationOnly(t, driver)

	return err
}

// TestMySQLLedgerRejectsCompositePrimary checks PRI alone cannot establish
// uniqueness when id is only one part of a primary key.
func TestMySQLLedgerRejectsCompositePrimary(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	part := maps.Clone(driver.state.indexes[0])
	part["Seq_in_index"], part["Column_name"] = int64(2), "batch"
	driver.state.indexes = append(driver.state.indexes, part)

	if err := rejectMySQLLedgerUp(t, driver); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want invalid composite layout", err)
	}
}

// TestMySQLLedgerRejectsPrefixPrimary checks uniqueness must cover all ID bytes,
// not only a shared prefix that could fail after nontransactional work.
func TestMySQLLedgerRejectsPrefixPrimary(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.indexes[0]["Sub_part"] = int64(32)

	if err := rejectMySQLLedgerUp(t, driver); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want invalid prefix layout", err)
	}
}

// TestMySQLLedgerRejectsGeneratedColumn checks a binary type is not sufficient
// when server expressions, rather than the supplied bytes, determine the ID.
func TestMySQLLedgerRejectsGeneratedColumn(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.columns[0]["Extra"] = "STORED GENERATED"

	if err := rejectMySQLLedgerUp(t, driver); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want invalid generated layout", err)
	}
}

// TestMySQLLedgerRejectsNullableColumn checks canonical ID storage never permits
// SQL NULL, even if the rest of the reported metadata appears suitable.
func TestMySQLLedgerRejectsNullableColumn(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.columns[0]["Null"] = "YES"

	if err := rejectMySQLLedgerUp(t, driver); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want invalid nullable layout", err)
	}
}

// TestMySQLLedgerHiddenColumnIsUnverified distinguishes unavailable metadata
// from a confirmed text layout. It must not recommend a blind conversion.
func TestMySQLLedgerHiddenColumnIsUnverified(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.columns = nil

	err := rejectMySQLLedgerUp(t, driver)

	if errors.Is(err, migrate.ErrInvalidMigration) || strings.Contains(err.Error(), "convert") {
		t.Fatalf("hidden metadata was misclassified: %v", err)
	}
}

// TestMySQLLedgerHiddenPrimaryIsUnverified refuses to infer an index from the
// column's PRI marker when SHOW INDEX supplies no verifiable primary key.
func TestMySQLLedgerHiddenPrimaryIsUnverified(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.indexes = nil

	err := rejectMySQLLedgerUp(t, driver)

	if errors.Is(err, migrate.ErrInvalidMigration) || strings.Contains(err.Error(), "convert") {
		t.Fatalf("hidden index metadata was misclassified: %v", err)
	}
}

// TestMySQLLedgerIndexInspectionKeepsCause covers a failure after column
// verification, before any migration callback or history read begins.
func TestMySQLLedgerIndexInspectionKeepsCause(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	cause := errors.New("index inspection denied")
	driver.state.indexesErr = cause

	if err := rejectMySQLLedgerUp(t, driver); !errors.Is(err, cause) {
		t.Fatalf("Up = %v, want index inspection cause", err)
	}
}

// TestMySQLLedgerQualifiedTargetIgnoresDefaultSchema checks target-addressed
// SHOW statements inspect the configured schema without consulting DATABASE().
func TestMySQLLedgerQualifiedTargetIgnoresDefaultSchema(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.databaseErr = errors.New("qualified target must not discover default schema")
	migrator := migrate.New(database.New(driver), func(migrator *migrate.Builder) {
		migrator.Table("Archive.History")
	})

	if _, err := migrator.Status(t.Context()); err != nil {
		t.Fatal(err)
	}

	statements := make([]string, 0, len(driver.state.calls))

	for _, call := range driver.state.calls {
		statements = append(statements, call.sql)
	}

	if !slices.Contains(statements, "SHOW FULL COLUMNS FROM `Archive`.`History`") {
		t.Fatalf("metadata inspected wrong target: %v", statements)
	}

	if !slices.Contains(statements, "SHOW INDEX FROM `Archive`.`History`") {
		t.Fatalf("indexes inspected wrong target: %v", statements)
	}
}

// TestMySQLLedgerDiscoveredSchemaIsOneQuotedComponent preserves punctuation
// from DATABASE() without reinterpreting dots or backticks as SQL structure.
func TestMySQLLedgerDiscoveredSchemaIsOneQuotedComponent(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.schema = "archive.part`two"

	if _, err := migrate.New(database.New(driver)).Status(t.Context()); err != nil {
		t.Fatal(err)
	}

	want := "SHOW FULL COLUMNS FROM `archive.part``two`.`arkoren_migrations`"

	if !slices.ContainsFunc(driver.state.calls, func(call ledgerCall) bool {
		return call.sql == want
	}) {
		t.Fatalf("discovered schema was not quoted as one identifier: %v", driver.state.calls)
	}
}

// TestMySQLLedgerNullDatabaseFailsBeforePreparation requires an explicit
// selected schema rather than creating or inspecting an ambiguous target.
func TestMySQLLedgerNullDatabaseFailsBeforePreparation(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.schema = ""

	if err := rejectMySQLLedgerUp(t, driver); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want missing database failure", err)
	}

	if len(driver.executed) != 0 {
		t.Fatalf("missing schema executed DDL: %v", driver.executed)
	}
}

// TestMySQLLedgerRollbackFreezesTargetAndID checks downward bookkeeping uses
// the original table and bytes even after its callback changes the default DB.
func TestMySQLLedgerRollbackFreezesTargetAndID(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.records = []map[string]any{{"id": "créate", "batch": 1, "ran_at": time.Time{}}}
	migrator := migrate.New(database.New(driver))

	if err := migrator.Add(migrate.NewMigration("créate", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Down(migrate.Exec(query.Raw("USE `other`")))
	})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}

	last := driver.state.calls[len(driver.state.calls)-1]

	if last.sql != "DELETE FROM `application`.`arkoren_migrations` WHERE `id` = ?" {
		t.Fatalf("delete did not use frozen target: %s", last.sql)
	}

	id, ok := last.args[0].([]byte)

	if !ok || !bytes.Equal(id, []byte("créate")) {
		t.Fatalf("delete id = %T(%v), want original bytes", last.args[0], last.args[0])
	}
}

// TestMySQLLedgerRejectsFixedBinaryStorage protects exact ID lengths: BINARY
// pads short values, unlike the canonical variable-length representation.
func TestMySQLLedgerRejectsFixedBinaryStorage(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.columns[0]["Type"] = "binary(191)"

	if err := rejectMySQLLedgerUp(t, driver); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want fixed-binary rejection", err)
	}
}

// TestMySQLLedgerRejectsUnsupportedNoPadText documents the deliberately narrow
// canonical policy without claiming a NO PAD text collation folds case.
func TestMySQLLedgerRejectsUnsupportedNoPadText(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.columns[0]["Type"] = "varchar(191)"
	driver.state.columns[0]["Collation"] = "utf8mb4_0900_bin"

	if err := rejectMySQLLedgerUp(t, driver); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want noncanonical-text rejection", err)
	}
}

// TestMySQLLedgerDiscoveryErrorKeepsCause verifies a failed SELECT DATABASE()
// is not treated as a missing database or a schema-conversion requirement.
func TestMySQLLedgerDiscoveryErrorKeepsCause(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	cause := errors.New("database discovery failed")
	driver.state.databaseErr = cause

	err := rejectMySQLLedgerUp(t, driver)

	if !errors.Is(err, cause) || errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want original discovery cause", err)
	}
}

// TestMySQLLedgerRejectsAmbiguousTarget refuses more than MySQL's schema/table
// components before any DDL, instead of quoting a whole dotted path as one name.
func TestMySQLLedgerRejectsAmbiguousTarget(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	migrator := migrate.New(database.New(driver), func(migrator *migrate.Builder) {
		migrator.Table("catalog.schema.history")
	})

	_, err := migrator.Status(t.Context())

	if !errors.Is(err, migrate.ErrInvalidMigration) || len(driver.state.calls) != 0 {
		t.Fatalf("ambiguous target = %v, calls = %v", err, driver.state.calls)
	}
}

// TestMySQLLedgerStatusRejectsUnsafeSnapshot checks inspection never returns a
// misleading historical snapshot from an unsupported legacy character ledger.
func TestMySQLLedgerStatusRejectsUnsafeSnapshot(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	driver.state.columns[0]["Type"] = "varchar(191)"
	driver.state.columns[0]["Collation"] = "utf8mb4_bin"

	statuses, err := migrate.New(database.New(driver)).Status(t.Context())

	if !errors.Is(err, migrate.ErrInvalidMigration) || statuses != nil {
		t.Fatalf("Status = %v, %v; want no snapshot", statuses, err)
	}

	requireLedgerPreparationOnly(t, driver)
}

// TestMySQLLedgerRejectsAdditionalUniqueIndex checks a prefix/expression unique
// index cannot reintroduce collisions after an otherwise exact primary key.
func TestMySQLLedgerRejectsAdditionalUniqueIndex(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	part := maps.Clone(driver.state.indexes[0])
	part["Key_name"], part["Sub_part"] = "id_prefix", int64(8)
	driver.state.indexes = append(driver.state.indexes, part)

	if err := rejectMySQLLedgerUp(t, driver); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Up = %v, want rejection of additional uniqueness constraints", err)
	}
}
