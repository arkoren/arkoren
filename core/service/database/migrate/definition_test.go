package migrate_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"
)

// fakeDriver records executed SQL and fails on demand.
type fakeDriver struct {
	// executed holds the SQL statements in order.
	executed []string

	// failOn makes Exec fail for the statement with this 1-based position.
	failOn int

	// dialect is the dialect Dialect reports.
	dialect query.Dialect
}

// errFake marks the configured statement failure in migration tests.
var errFake = errors.New("fake failure")

// Exec records statements and fails at the configured execution number.
func (driver *fakeDriver) Exec(_ context.Context, statement string, _ ...any) (int64, error) {
	driver.executed = append(driver.executed, statement)

	if driver.failOn == len(driver.executed) {
		return 0, errFake
	}

	return 1, nil
}

// Select accepts reads without populating the destination.
func (*fakeDriver) Select(context.Context, string, any, ...any) error {
	return nil
}

// Query returns the fixed failure because declaration tests do not use cursors.
func (*fakeDriver) Query(context.Context, string, ...any) (database.Rows, error) {
	return nil, errFake
}

// Dialect returns the dialect configured for statement rendering.
func (driver *fakeDriver) Dialect() query.Dialect {
	return driver.dialect
}

// Find accepts single-row reads without populating the destination.
func (*fakeDriver) Find(context.Context, string, any, ...any) error {
	return nil
}

// WithTransaction accepts a transaction without executing its declaration-test body.
func (*fakeDriver) WithTransaction(context.Context, func(database.Driver) error) error {
	return nil
}

// WithSession invokes fn with the same recording driver and dialect. This
// bridge adds no migration behavior; real drivers own physical sessions.
func (driver *fakeDriver) WithSession(_ context.Context, fn func(database.Driver) error) error {
	return fn(driver)
}

// Lock returns a successful no-op unlock callback.
func (*fakeDriver) Lock(context.Context, string) (func(context.Context) error, error) {
	return func(context.Context) error {
		return nil
	}, nil
}

// TryLock reports acquisition and returns a successful no-op unlock callback.
func (*fakeDriver) TryLock(context.Context, string) (func(context.Context) error, bool, error) {
	return func(context.Context) error {
		return nil
	}, true, nil
}

// noop supplies a successful migration step for declaration validation.
func noop(context.Context, *database.Database) error {
	return nil
}

// requireInvalid fails t unless the definition reports ErrInvalidMigration.
func requireInvalid(t *testing.T, definition migrate.Definition) {
	t.Helper()

	if err := definition.Err(); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Err() = %v, want ErrInvalidMigration", err)
	}
}

// TestMigrationRejectsEmptyID verifies an identity is required before registration.
func TestMigrationRejectsEmptyID(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewMigration("", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}))
}

// TestMigrationRejectsLongID verifies identities exceeding the storage limit fail.
func TestMigrationRejectsLongID(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewMigration(strings.Repeat("a", 192), func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}))
}

// TestMigrationAcceptsMaxLengthID checks the inclusive identity length boundary.
func TestMigrationAcceptsMaxLengthID(t *testing.T) {
	t.Parallel()

	if err := migrate.NewMigration(strings.Repeat("a", 191), func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}).Err(); err != nil {
		t.Fatal(err)
	}
}

// TestMigrationRejectsWhitespaceID verifies spaces cannot enter migration identities.
func TestMigrationRejectsWhitespaceID(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewMigration("a b", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}))
}

// TestMigrationRejectsControlCharacterID verifies non-printing identities fail validation.
func TestMigrationRejectsControlCharacterID(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewMigration("a\x00b", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}))
}

// TestMigrationRequiresUp verifies an identity alone is not an executable definition.
func TestMigrationRequiresUp(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewMigration("a"))
}

// TestMigrationRejectsUpTwice guards against silently replacing forward steps.
func TestMigrationRejectsUpTwice(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Up(noop)
	}))
}

// TestMigrationRejectsDownTwice guards against silently replacing rollback steps.
func TestMigrationRejectsDownTwice(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Down(noop)
		migration.Down(noop)
	}))
}

// TestMigrationRejectsEmptyUp verifies a forward declaration must contain work.
func TestMigrationRejectsEmptyUp(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up()
	}))
}

// TestMigrationRejectsEmptyDown verifies an explicit rollback declaration needs steps.
func TestMigrationRejectsEmptyDown(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Down()
	}))
}

// TestMigrationRejectsNilUpStep verifies missing forward functions fail at declaration.
func TestMigrationRejectsNilUpStep(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop, nil)
	}))
}

// TestMigrationRejectsNilDownStep verifies missing rollback functions fail at declaration.
func TestMigrationRejectsNilDownStep(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Down(nil)
	}))
}

// TestMigrationKeepsFirstError verifies later settings cannot erase an invalid identity.
func TestMigrationKeepsFirstError(t *testing.T) {
	t.Parallel()

	definition := migrate.NewMigration("", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	})

	if !strings.Contains(definition.Err().Error(), "id") {
		t.Fatalf("Err() = %v", definition.Err())
	}
}

// TestDefinitionAccessors checks published identity, description, and execution policy.
func TestDefinitionAccessors(t *testing.T) {
	t.Parallel()

	definition := migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Down(noop)
		migration.Description("text")
	})

	if definition.ID() != "a" || definition.DescriptionValue() != "text" ||
		!definition.Reversible() || !definition.Transactional() {
		t.Fatalf("unexpected accessors for %+v", definition)
	}
}

// TestDefinitionWithoutTransactionClearsTransactional checks the explicit transaction opt-out.
func TestDefinitionWithoutTransactionClearsTransactional(t *testing.T) {
	t.Parallel()

	if migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.WithoutTransaction()
	}).Transactional() {
		t.Fatal("Transactional() = true")
	}
}

// TestDefinitionWithoutDownIsIrreversible checks forward-only migration metadata.
func TestDefinitionWithoutDownIsIrreversible(t *testing.T) {
	t.Parallel()

	if migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}).Reversible() {
		t.Fatal("Reversible() = true")
	}
}

// TestDefinitionIsImmutable verifies publication detaches caller-owned step slices.
func TestDefinitionIsImmutable(t *testing.T) {
	t.Parallel()

	var retained *migrate.MigrationBuilder
	base := migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		retained = migration
		migration.Up(noop)
	})
	retained.WithoutTransaction()
	retained.Description("changed")
	retained.Only(query.Postgres)
	retained.Down(noop)
	retained.Up(noop)

	if !base.Transactional() || base.Reversible() || base.DescriptionValue() != "" {
		t.Fatal("published settings were modified")
	}

	if base.Dialects() != nil || base.Err() != nil {
		t.Fatal("published dialects or error were modified")
	}
}

// TestAddRejectsInvalidDefinitionAndAddsNone verifies registration is atomic on validation failure.
func TestAddRejectsInvalidDefinitionAndAddsNone(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(database.New(&fakeDriver{}))

	err := migrator.Add(migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}), migrate.NewMigration("b"))

	if !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Add() = %v", err)
	}

	if err := migrator.Add(migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	})); err != nil {
		t.Fatalf("a was added despite failure: %v", err)
	}
}

// TestAddRejectsDuplicateInSameCall checks duplicate detection within one registration batch.
func TestAddRejectsDuplicateInSameCall(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(database.New(&fakeDriver{}))

	err := migrator.Add(migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}), migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}))

	if !errors.Is(err, migrate.ErrDuplicateID) {
		t.Fatalf("Add() = %v", err)
	}
}

// TestAddRejectsDuplicateOfEarlierCall checks duplicate detection against existing registrations.
func TestAddRejectsDuplicateOfEarlierCall(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(database.New(&fakeDriver{}))

	if err := migrator.Add(migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Add(migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	})); !errors.Is(err, migrate.ErrDuplicateID) {
		t.Fatalf("Add() = %v", err)
	}
}

// TestMigratorDatabaseReturnsDatabase verifies the original facade remains accessible.
func TestMigratorDatabaseReturnsDatabase(t *testing.T) {
	t.Parallel()

	db := database.New(&fakeDriver{})

	if migrate.New(db).Database() != db {
		t.Fatal("Database() mismatch")
	}
}

// TestExecRunsStatementsInOrder verifies migration statements retain declaration order.
func TestExecRunsStatementsInOrder(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{}
	step := migrate.Exec(query.Raw("DROP TABLE a"), query.Raw("DROP TABLE b"))

	if err := step(context.Background(), database.New(driver)); err != nil {
		t.Fatal(err)
	}

	if len(driver.executed) != 2 || !strings.Contains(driver.executed[1], "b") {
		t.Fatalf("executed = %v", driver.executed)
	}
}

// TestExecWrapsStatementNumber checks failure diagnostics identify the failing statement.
func TestExecWrapsStatementNumber(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{failOn: 2}
	step := migrate.Exec(query.Raw("DROP TABLE a"), query.Raw("DROP TABLE b"))

	err := step(context.Background(), database.New(driver))

	if !errors.Is(err, errFake) || !strings.HasPrefix(err.Error(), "statement 2: ") {
		t.Fatalf("err = %v", err)
	}
}

// TestExecWithoutStatementsFails verifies empty execution steps fail before driver access.
func TestExecWithoutStatementsFails(t *testing.T) {
	t.Parallel()

	err := migrate.Exec()(context.Background(), database.New(&fakeDriver{}))

	if !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("err = %v", err)
	}
}

// TestExecNilStatementFailsBeforeRunning verifies the entire batch is validated before execution.
func TestExecNilStatementFailsBeforeRunning(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{}

	err := migrate.Exec(query.Raw("DROP TABLE a"), nil)(context.Background(), database.New(driver))

	if !errors.Is(err, migrate.ErrInvalidMigration) || len(driver.executed) != 0 {
		t.Fatalf("err = %v, executed = %v", err, driver.executed)
	}
}

// TestSchemaRunsOperationStatementsInOrder checks schema operations retain SQL execution order.
func TestSchemaRunsOperationStatementsInOrder(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{dialect: query.Postgres}
	step := migrate.Schema(
		schema.Create("users", func(table *schema.Table) {
			table.ID()
			table.String("email", 255).Unique()
		}),
		schema.Drop("old"),
	)

	if err := step(context.Background(), database.New(driver)); err != nil {
		t.Fatal(err)
	}

	want := []string{
		`CREATE TABLE "users" ("id" bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, "email" varchar(255) NOT NULL)`,
		`CREATE UNIQUE INDEX "users_email_key" ON "users" ("email")`,
		`DROP TABLE "old"`,
	}

	if !slices.Equal(driver.executed, want) {
		t.Fatalf("executed = %q", driver.executed)
	}
}

// TestSchemaWrapsOperationAndStatementNumber checks diagnostics identify both failing positions.
func TestSchemaWrapsOperationAndStatementNumber(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{failOn: 2}
	step := migrate.Schema(
		schema.Drop("a"),
		schema.Alter("t", func(table *schema.Table) {
			table.DropColumn("x", "y")
		}),
	)

	err := step(context.Background(), database.New(driver))

	if !errors.Is(err, errFake) || !strings.HasPrefix(err.Error(), "operation 2 statement 1: ") {
		t.Fatalf("err = %v", err)
	}
}

// TestSchemaInvalidOperationFailsBeforeRunning verifies schema validation precedes SQL effects.
func TestSchemaInvalidOperationFailsBeforeRunning(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{}
	step := migrate.Schema(schema.Drop("a"), schema.Create("t"))

	err := step(context.Background(), database.New(driver))

	if !errors.Is(err, query.ErrInvalidQuery) || !strings.HasPrefix(err.Error(), "operation 2: ") || len(driver.executed) != 0 {
		t.Fatalf("err = %v, executed = %v", err, driver.executed)
	}
}

// TestSchemaWithoutOperationsFails checks an empty schema step cannot succeed silently.
func TestSchemaWithoutOperationsFails(t *testing.T) {
	t.Parallel()

	err := migrate.Schema()(context.Background(), database.New(&fakeDriver{}))

	if !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("err = %v", err)
	}
}

// TestRollbackStepsBelowOneFails checks rollback rejects invalid counts before I/O.
func TestRollbackStepsBelowOneFails(t *testing.T) {
	t.Parallel()

	err := migrate.New(database.New(&fakeDriver{})).Rollback(context.Background(), migrate.Steps(0))

	if !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("err = %v", err)
	}
}

// TestUpRejectsInvalidTableName checks bookkeeping identifiers before SQL execution.
func TestUpRejectsInvalidTableName(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(database.New(&fakeDriver{}), func(migrator *migrate.Builder) {
		migrator.Table("bad name;")
	})

	if err := migrator.Up(context.Background()); !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("err = %v", err)
	}
}

// TestGroupPrefixesIDs verifies grouped migrations receive stable qualified identities.
func TestGroupPrefixesIDs(t *testing.T) {
	t.Parallel()

	definitions := migrate.NewGroup("acme.users").Migrations(
		migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
			migration.Up(noop)
		}),
		migrate.NewMigration("add-email-index", func(migration *migrate.MigrationBuilder) {
			migration.Up(noop)
		}),
	)

	if len(definitions) != 2 || definitions[0].Group() != "acme.users" || definitions[1].Group() != "acme.users" {
		t.Fatalf("definitions = %+v", definitions)
	}

	if definitions[0].ID() != "acme.users/create-users" || definitions[1].ID() != "acme.users/add-email-index" {
		t.Fatalf("ids = %q, %q", definitions[0].ID(), definitions[1].ID())
	}

	if err := definitions[0].Err(); err != nil {
		t.Fatal(err)
	}
}

// TestGroupRejectsSlashInID checks group qualification cannot be confused with an existing prefix.
func TestGroupRejectsSlashInID(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewGroup("acme.users").Migrations(migrate.NewMigration("acme.users/create-users", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}))[0])
}

// TestGroupRejectsTooLongID verifies the group prefix counts toward the identity limit.
func TestGroupRejectsTooLongID(t *testing.T) {
	t.Parallel()

	group := migrate.NewGroup("acme.users")
	fits := strings.Repeat("x", 191-len("acme.users/"))

	if err := group.Migrations(migrate.NewMigration(fits, func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}))[0].Err(); err != nil {
		t.Fatalf("longest id rejected: %v", err)
	}

	definition := group.Migrations(migrate.NewMigration(fits+"x", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}))[0]

	requireInvalid(t, definition)

	if err := definition.Err(); !strings.Contains(err.Error(), "acme.users") {
		t.Fatalf("Err() = %v, want group named", err)
	}
}

// TestUngroupedIDMayContainSlash preserves opaque identities outside group qualification.
func TestUngroupedIDMayContainSlash(t *testing.T) {
	t.Parallel()

	if err := migrate.NewMigration("acme/create-users", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}).Err(); err != nil {
		t.Fatal(err)
	}
}

// TestGroupedIDAppearsInStatus verifies reporting retains qualified identities and group metadata.
func TestGroupedIDAppearsInStatus(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(database.New(&fakeDriver{dialect: query.SQLite}))

	if err := migrator.Add(migrate.NewGroup("acme.users").Migrations(migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}))...); err != nil {
		t.Fatal(err)
	}

	statuses, err := migrator.Status(t.Context())

	if err != nil {
		t.Fatal(err)
	}

	if len(statuses) != 1 || statuses[0].ID != "acme.users/create-users" || statuses[0].Group != "acme.users" {
		t.Fatalf("statuses = %+v", statuses)
	}
}

// TestMigrationWithoutGroupHasDefaultGroup checks the metadata of unqualified definitions.
func TestMigrationWithoutGroupHasDefaultGroup(t *testing.T) {
	t.Parallel()

	if got := migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}).Group(); got != "" {
		t.Fatalf("Group() = %q", got)
	}
}

// TestGroupRejectsInvalidName checks malformed group names fail before publication.
func TestGroupRejectsInvalidName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "has space", strings.Repeat("x", 192)} {
		requireInvalid(t, migrate.NewGroup(name).Migrations(migrate.NewMigration("x", func(migration *migrate.MigrationBuilder) {
			migration.Up(noop)
		}))[0])
	}
}

// TestGroupRejectsGroupedDefinition verifies definitions cannot acquire a second group prefix.
func TestGroupRejectsGroupedDefinition(t *testing.T) {
	t.Parallel()

	grouped := migrate.NewGroup("a").Migrations(migrate.NewMigration("x", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}))

	requireInvalid(t, migrate.NewGroup("b").Migrations(grouped...)[0])
}

// TestGroupKeepsFirstError verifies invalid groups do not hide earlier definition errors.
func TestGroupKeepsFirstError(t *testing.T) {
	t.Parallel()

	definition := migrate.NewGroup("has space").Migrations(migrate.NewMigration("bad id"))[0]

	if err := definition.Err(); err == nil || !strings.Contains(err.Error(), "bad id") || strings.Contains(err.Error(), "group") {
		t.Fatalf("Err() = %v", err)
	}
}

// TestAddReportsInvalidGroup checks registration surfaces group validation failures.
func TestAddReportsInvalidGroup(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(database.New(&fakeDriver{}))

	err := migrator.Add(migrate.NewGroup("").Migrations(migrate.NewMigration("x", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}))...)

	if !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("err = %v", err)
	}
}

// TestOnlyAcceptedWhenDialectListed checks the active dialect can register restricted definitions.
func TestOnlyAcceptedWhenDialectListed(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(database.New(&fakeDriver{dialect: query.Postgres}))

	err := migrator.Add(migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Only(query.MySQL, query.Postgres)
	}))

	if err != nil {
		t.Fatalf("Add() = %v", err)
	}
}

// TestOnlyRejectedWhenDialectNotListed checks incompatible definitions cannot register.
func TestOnlyRejectedWhenDialectNotListed(t *testing.T) {
	t.Parallel()

	driver := &fakeDriver{dialect: query.SQLite}
	migrator := migrate.New(database.New(driver))

	err := migrator.Add(migrate.NewMigration("cache", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Only(query.Postgres)
	}))

	if !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Add() = %v", err)
	}

	for _, want := range []string{"cache", "postgres", "sqlite"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}

	if len(driver.executed) != 0 {
		t.Fatalf("SQL ran: %v", driver.executed)
	}
}

// TestOnlyWithoutDialectsIsInvalid verifies explicit restrictions must name a dialect.
func TestOnlyWithoutDialectsIsInvalid(t *testing.T) {
	t.Parallel()

	definition := migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Only()
	})

	if !errors.Is(definition.Err(), migrate.ErrInvalidMigration) {
		t.Fatalf("Err() = %v", definition.Err())
	}
}

// TestOnlyReplacesAndDeduplicates verifies repeated settings replace the dialect restriction.
func TestOnlyReplacesAndDeduplicates(t *testing.T) {
	t.Parallel()

	definition := migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Only(query.MySQL)
		migration.Only(query.Postgres, query.SQLite, query.Postgres)
	})

	got := definition.Dialects()

	if len(got) != 2 || got[0] != query.Postgres || got[1] != query.SQLite {
		t.Fatalf("Dialects() = %v", got)
	}
}

// TestDialectsNilByDefaultAndCopied checks unrestricted defaults and detached accessor results.
func TestDialectsNilByDefaultAndCopied(t *testing.T) {
	t.Parallel()

	if migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}).Dialects() != nil {
		t.Fatal("Dialects() not nil by default")
	}

	definition := migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Only(query.Postgres)
	})

	definition.Dialects()[0] = query.MySQL

	if got := definition.Dialects(); got[0] != query.Postgres {
		t.Fatalf("Dialects() = %v after mutation", got)
	}
}

// TestAddWithDialectMismatchAddsNone verifies incompatible registration batches are atomic.
func TestAddWithDialectMismatchAddsNone(t *testing.T) {
	t.Parallel()

	migrator := migrate.New(database.New(&fakeDriver{dialect: query.SQLite}))

	err := migrator.Add(migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}), migrate.NewMigration("b", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Only(query.Postgres)
	}))

	if !errors.Is(err, migrate.ErrInvalidMigration) {
		t.Fatalf("Add() = %v", err)
	}

	if err := migrator.Add(migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	})); err != nil {
		t.Fatalf("a was added despite failure: %v", err)
	}
}

// TestMigrationCallbacksShareDraftInOrder verifies synchronous publication and
// deferred required-Up validation across callbacks, without executing steps.
func TestMigrationCallbacksShareDraftInOrder(t *testing.T) {
	t.Parallel()

	var retained *migrate.MigrationBuilder
	var order []int
	definition := migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		retained = migration
		order = append(order, 1)
		migration.Description("first")
	}, func(migration *migrate.MigrationBuilder) {
		if migration != retained {
			t.Fatal("callbacks received different drafts")
		}

		order = append(order, 2)
		migration.Description("second")
		migration.Up(func(context.Context, *database.Database) error {
			t.Fatal("construction executed a migration step")

			return nil
		})
	})

	if !slices.Equal(order, []int{1, 2}) {
		t.Fatalf("callback order = %v", order)
	}

	if err := definition.Err(); err != nil {
		t.Fatal(err)
	}

	if definition.DescriptionValue() != "second" {
		t.Fatalf("DescriptionValue() = %q", definition.DescriptionValue())
	}
}

// TestMigrationSettingsReturnDraft verifies that settings preserve draft identity
// rather than publishing new definitions or replacing the configuration root.
func TestMigrationSettingsReturnDraft(t *testing.T) {
	t.Parallel()

	definition := migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		if migration.Up(noop) != migration || migration.Down(noop) != migration {
			t.Fatal("step settings replaced the draft")
		}

		if migration.Only(query.SQLite) != migration || migration.Description("text") != migration {
			t.Fatal("metadata settings replaced the draft")
		}

		if migration.WithoutTransaction() != migration {
			t.Fatal("WithoutTransaction replaced the draft")
		}
	})

	if err := definition.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestMigrationInvalidSettingsReturnDraft preserves the same receiver contract
// on error paths while the first domain validation error remains sticky.
func TestMigrationInvalidSettingsReturnDraft(t *testing.T) {
	t.Parallel()

	definition := migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		if migration.Up(nil) != migration || migration.Down(nil) != migration {
			t.Fatal("invalid step settings replaced the draft")
		}

		if migration.Only() != migration || migration.Description("text") != migration {
			t.Fatal("invalid metadata settings replaced the draft")
		}

		if migration.WithoutTransaction() != migration {
			t.Fatal("invalid transaction setting replaced the draft")
		}
	})

	if !strings.Contains(definition.Err().Error(), "nil up step") {
		t.Fatalf("Err() = %v", definition.Err())
	}
}

// TestMigrationEmptyCallbackSlicesRequireUp treats omission and expanded empty
// callback slices alike; empty callbacks do not bypass normal domain checks.
func TestMigrationEmptyCallbackSlicesRequireUp(t *testing.T) {
	t.Parallel()

	var configure []func(*migrate.MigrationBuilder)

	requireInvalid(t, migrate.NewMigration("a", configure...))
	requireInvalid(t, migrate.NewMigration("a", []func(*migrate.MigrationBuilder){}...))
	requireInvalid(t, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Description("no steps")
	}))
}

// TestMigrationNilCallbackPanics ensures nil entries are invoked, not filtered
// out even when construction already holds an invalid-id error.
func TestMigrationNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	migrate.NewMigration("", nil)
}

// TestMigrationCallbackPanicPropagates preserves application panics unchanged
// and stops invoking later callbacks when the application aborts construction.
func TestMigrationCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	defer func() {
		if got := recover(); got != "application panic" {
			t.Fatalf("panic = %v", got)
		}
	}()
	migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		panic("application panic")
	}, func(migration *migrate.MigrationBuilder) {
		t.Fatal("callback after panic ran")
	})
}

// TestMigrationCallbacksRunAfterDomainError distinguishes validation errors
// from panics: all callbacks still run once, but cannot clear the first error.
func TestMigrationCallbacksRunAfterDomainError(t *testing.T) {
	t.Parallel()

	calls := 0
	definition := migrate.NewMigration("", func(migration *migrate.MigrationBuilder) {
		calls++
		migration.Up(noop)
	}, func(migration *migrate.MigrationBuilder) {
		calls++
		migration.Only()
	})

	if calls != 2 {
		t.Fatalf("callback calls = %d", calls)
	}

	if !strings.Contains(definition.Err().Error(), "id") {
		t.Fatalf("Err() = %v", definition.Err())
	}
}

// TestMigrationDuplicateStepsAcrossCallbacks keeps duplicate-setting validation
// across the shared draft rather than treating callbacks as independent values.
func TestMigrationDuplicateStepsAcrossCallbacks(t *testing.T) {
	t.Parallel()

	requireInvalid(t, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}, func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
	}))
	requireInvalid(t, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Down(noop)
	}, func(migration *migrate.MigrationBuilder) {
		migration.Down(noop)
	}))
}

// TestMigrationExpandedNilStepsAreInvalid keeps step lists distinct from
// construction callback lists: an omitted Up or Down list is a domain error.
func TestMigrationExpandedNilStepsAreInvalid(t *testing.T) {
	t.Parallel()

	var steps []migrate.Func

	requireInvalid(t, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(steps...)
	}))
	requireInvalid(t, migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Down(steps...)
	}))
}

// TestMigrationLaterCallbackReplacesMetadata verifies that callbacks share
// replacement settings while transactional and dialect defaults stay explicit.
func TestMigrationLaterCallbackReplacesMetadata(t *testing.T) {
	t.Parallel()

	definition := migrate.NewMigration("a", func(migration *migrate.MigrationBuilder) {
		migration.Up(noop)
		migration.Description("first")
		migration.Only(query.MySQL)
	}, func(migration *migrate.MigrationBuilder) {
		migration.Description("")
		migration.Only(query.SQLite, query.Postgres, query.SQLite)
	})

	if err := definition.Err(); err != nil {
		t.Fatal(err)
	}

	if definition.DescriptionValue() != "" || !definition.Transactional() {
		t.Fatal("unexpected description or transaction setting")
	}

	if !slices.Equal(definition.Dialects(), []query.Dialect{query.SQLite, query.Postgres}) {
		t.Fatalf("Dialects() = %v", definition.Dialects())
	}
}

// TestMigratorExecCapturesStatementSlice verifies that changing the caller's
// variadic slice cannot replace SQL in an already registered migration.
func TestMigratorExecCapturesStatementSlice(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	statements := []query.Statementer{query.Raw("original-statement")}
	migrator := migrate.New(database.New(driver))

	if err := migrator.Add(migrate.NewMigration("owned", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Exec(statements...))
	})); err != nil {
		t.Fatal(err)
	}

	statements[0] = query.Raw("replaced-statement")

	if len(driver.executed) != 0 {
		t.Fatal("construction executed SQL")
	}

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(driver.executed, "original-statement") {
		t.Fatalf("migration executed %q, want original-statement", driver.executed)
	}
}

// TestMigratorSchemaCapturesOperationSlice verifies the schema step owns its
// operation list without eagerly rendering or executing migration SQL.
func TestMigratorSchemaCapturesOperationSlice(t *testing.T) {
	t.Parallel()

	driver := newMySQLLedgerDriver()
	operations := []schema.Operation{schema.Drop("original_table")}
	migrator := migrate.New(database.New(driver))

	if err := migrator.Add(migrate.NewMigration("owned", func(migration *migrate.MigrationBuilder) {
		migration.Up(migrate.Schema(operations...))
	})); err != nil {
		t.Fatal(err)
	}

	operations[0] = schema.Drop("replacement_table")

	if len(driver.executed) != 0 {
		t.Fatal("construction executed SQL")
	}

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(driver.executed, "DROP TABLE `original_table`") {
		t.Fatalf("migration executed %q, want original table", driver.executed)
	}
}
