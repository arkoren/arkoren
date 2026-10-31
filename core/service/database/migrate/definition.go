package migrate

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"
)

// maxIDLength is the longest allowed migration id, in bytes.
const maxIDLength = 191

// Func performs one migration step using the supplied database and context.
// The context routes every facade operation onto the lock-owning physical
// session and, for transactional migrations, its current transaction. Always
// forward this context to database calls; a fresh context or direct Driver call
// bypasses the scope and can deadlock a one-connection pool. The context expires
// when its enclosing session or transaction callback ends; do not retain it or
// use it concurrently.
//
// Example:
//
//	step := func(ctx context.Context, db *database.Database) error {
//		_, err := db.Exec(ctx, query.Raw("UPDATE users SET active = TRUE"))
//		return err
//	}
type Func = func(ctx context.Context, db *database.Database) error

// Definition describes one immutable migration, published by [NewMigration].
// Its accessors never expose owned containers. Construction errors are retained
// for [Definition.Err] and rejected by [Migrator.Add] before SQL runs.
//
// Example:
//
//	definition := migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Exec(query.Raw("CREATE TABLE users (id INTEGER)")))
//	})
//	if err := definition.Err(); err != nil {
//		return err
//	}
type Definition struct {
	// id identifies the migration.
	id string

	// up holds the steps that apply the migration.
	up []Func

	// down holds the steps that revert the migration.
	down []Func

	// upSet records an Up declaration, even when its steps were invalid.
	// It distinguishes omission from a duplicate declaration during construction.
	upSet bool

	// downSet records a Down declaration independently of the retained steps.
	// Omission is valid for irreversible migrations; a duplicate is not.
	downSet bool

	// withoutTransaction disables the migration transaction.
	withoutTransaction bool

	// description is free text for humans.
	description string

	// dialects lists the dialects the migration may run on; nil means every
	// dialect.
	dialects []query.Dialect

	// group names the sequence the migration belongs to; empty is the
	// default group.
	group string

	// err is the first error found while building the definition.
	err error
}

// MigrationBuilder configures a migration during [NewMigration]. Settings
// return the same draft and retain the first error. A retained draft cannot
// change a published [Definition]; step functions and their captures are shared.
// Missing Up is checked on the published definition, after all callbacks.
//
// Example:
//
//	definition := migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Exec(query.Raw("CREATE TABLE users (id INTEGER)")))
//		migration.Description("Create the users table")
//	})
//	if err := definition.Err(); err != nil {
//		return err
//	}
type MigrationBuilder struct {
	// definition is the private draft, detached when construction finishes.
	definition Definition
}

// NewMigration publishes a definition with a stable bookkeeping id of 1-191
// printable UTF-8 bytes without whitespace. Callbacks run directly once,
// synchronously in order on one draft, even if domain validation fails.
// Nil callbacks panic; expanded nil or empty callback slices make no edits.
// Callback panics propagate. With no Up, the result has the normal required-Up
// error. Owned step and dialect slices are detached on setting and publication.
//
// Example:
//
//	definition := migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Exec(query.Raw("CREATE TABLE users (id INTEGER)")))
//	})
//	if err := definition.Err(); err != nil {
//		return err
//	}
func NewMigration(id string, configure ...MigrationBuilderFunc) Definition {
	definition := Definition{id: id}

	if !validID(id) {
		definition.err = fmt.Errorf("%w: id %q must be 1-%d printable characters without white space",
			ErrInvalidMigration, id, maxIDLength)
	}

	migration := &MigrationBuilder{definition: definition}

	for _, configure := range configure {
		configure(migration)
	}

	definition = migration.definition
	definition.up = slices.Clone(definition.up)
	definition.down = slices.Clone(definition.down)
	definition.dialects = slices.Clone(definition.dialects)

	return definition
}

// validID reports whether id is non-empty, short enough, printable, and free
// of white space.
func validID(id string) bool {
	if id == "" || len(id) > maxIDLength || !utf8.ValidString(id) {
		return false
	}

	for _, character := range id {
		if !unicode.IsPrint(character) || unicode.IsSpace(character) {
			return false
		}
	}

	return true
}

// Up sets the steps that apply the migration. Calling it twice, with no
// steps, or with a nil step retains an [ErrInvalidMigration] error. The input
// slice is copied; functions are stored for execution, not invoked here.
// Up returns the same draft, including when it is invalid.
//
// Example:
//
//	definition := migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Exec(query.Raw("CREATE TABLE users (id INTEGER)")))
//	})
//	if err := definition.Err(); err != nil {
//		return err
//	}
func (migration *MigrationBuilder) Up(steps ...Func) *MigrationBuilder {
	definition := &migration.definition

	if definition.err != nil {
		return migration
	}

	if definition.upSet {
		definition.err = fmt.Errorf("%w: %s: Up called twice", ErrInvalidMigration, definition.id)

		return migration
	}

	definition.upSet = true

	if err := checkSteps(definition.id, "up", steps); err != nil {
		definition.err = err

		return migration
	}

	definition.up = slices.Clone(steps)

	return migration
}

// Down sets the steps that revert the migration. Calling it twice, with no
// steps, or with a nil step retains an [ErrInvalidMigration] error. The input
// slice is copied without invoking its functions. Omit Down for an irreversible
// migration. Down returns the same draft, including when it is invalid.
//
// Example:
//
//	definition := migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Exec(query.Raw("CREATE TABLE users (id INTEGER)")))
//		migration.Down(migrate.Schema(schema.Drop("users")))
//	})
//	if err := definition.Err(); err != nil {
//		return err
//	}
func (migration *MigrationBuilder) Down(steps ...Func) *MigrationBuilder {
	definition := &migration.definition

	if definition.err != nil {
		return migration
	}

	if definition.downSet {
		definition.err = fmt.Errorf("%w: %s: Down called twice", ErrInvalidMigration, definition.id)

		return migration
	}

	definition.downSet = true

	if err := checkSteps(definition.id, "down", steps); err != nil {
		definition.err = err

		return migration
	}

	definition.down = slices.Clone(steps)

	return migration
}

// checkSteps reports an error for an empty list or a nil step.
func checkSteps(id, direction string, steps []Func) error {
	if len(steps) == 0 {
		return fmt.Errorf("%w: %s: no %s steps", ErrInvalidMigration, id, direction)
	}

	if slices.ContainsFunc(steps, func(step Func) bool {
		return step == nil
	}) {
		return fmt.Errorf("%w: %s: nil %s step", ErrInvalidMigration, id, direction)
	}

	return nil
}

// WithoutTransaction configures the draft not to start a per-migration
// transaction. Its steps still use the lock-owning session, and still join a
// transaction the caller already supplied. Without an enclosing transaction,
// failure can leave partial schema changes without a bookkeeping row, so the
// migration must be safe to run again after deliberate recovery.
// The setting is idempotent and returns the same draft.
//
// Example:
//
//	definition := migrate.NewMigration("create-audit", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Exec(query.Raw("CREATE TABLE IF NOT EXISTS audit (id INTEGER)")))
//		migration.WithoutTransaction()
//	})
func (migration *MigrationBuilder) WithoutTransaction() *MigrationBuilder {
	migration.definition.withoutTransaction = true

	return migration
}

// Description replaces the human-readable description, with empty text clearing
// it. It returns the same draft and does not change any retained error.
//
// Example:
//
//	definition := migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Exec(query.Raw("CREATE TABLE users (id INTEGER)")))
//		migration.Description("Create the users table")
//	})
//	fmt.Println(definition.DescriptionValue()) // Create the users table
func (migration *MigrationBuilder) Description(text string) *MigrationBuilder {
	migration.definition.description = text

	return migration
}

// Only restricts the migration to the listed dialects. [Migrator.Add]
// rejects the migration with [ErrInvalidMigration] when the migrator's
// database uses another dialect: it never skips a migration silently, and
// fails before any SQL runs.
//
// Use it for a shipped migration that only makes sense on one dialect, such
// as a PostgreSQL-only cache table:
//
//	migrate.NewMigration("create-cache", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Exec(query.Raw("CREATE UNLOGGED TABLE cache (id INTEGER)")))
//		migration.Down(migrate.Schema(schema.Drop("cache")))
//		migration.Only(query.Postgres)
//	})
//
// A migration whose logic differs per dialect should not use Only; switch on
// [database.Database.Dialect] inside a [Func] instead.
//
// Calling Only again replaces the earlier list, like [MigrationBuilder.Description].
// Duplicate dialects are removed. Calling it with no dialects is an error.
// Without Only the migration runs on every dialect. The input list is copied;
// Only returns the same draft, including when it is invalid.
func (migration *MigrationBuilder) Only(dialects ...query.Dialect) *MigrationBuilder {
	definition := &migration.definition

	if definition.err != nil {
		return migration
	}

	if len(dialects) == 0 {
		definition.err = fmt.Errorf("%w: %s: Only needs at least one dialect", ErrInvalidMigration, definition.id)

		return migration
	}

	definition.dialects = nil

	for _, dialect := range dialects {
		if !slices.Contains(definition.dialects, dialect) {
			definition.dialects = append(definition.dialects, dialect)
		}
	}

	return migration
}

// ID returns the migration id.
func (definition Definition) ID() string {
	return definition.id
}

// DescriptionValue returns the description text.
func (definition Definition) DescriptionValue() string {
	return definition.description
}

// Dialects returns a copy of the dialects set by [MigrationBuilder.Only], in the
// order given and without duplicates. Nil means the migration runs on every
// dialect. Changing the result does not affect the definition.
//
// Example:
//
//	definition := migrate.NewMigration("create-cache", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Exec(query.Raw("CREATE TABLE cache (id INTEGER)")))
//		migration.Only(query.Postgres)
//	})
//	dialects := definition.Dialects() // An independent, one-element slice.
func (definition Definition) Dialects() []query.Dialect {
	return slices.Clone(definition.dialects)
}

// Group returns the name of the group set by [MigrationGroup.Migrations], or
// the empty string for the default group.
func (definition Definition) Group() string {
	return definition.group
}

// Reversible reports whether the migration has Down steps.
func (definition Definition) Reversible() bool {
	return len(definition.down) > 0
}

// Transactional reports whether the migration asks for a transaction. The
// dialect can still run it without one; see the package documentation.
func (definition Definition) Transactional() bool {
	return !definition.withoutTransaction
}

// Err returns the first error found while building the definition, or a
// missing-Up error.
func (definition Definition) Err() error {
	if definition.err != nil {
		return definition.err
	}

	if !definition.upSet {
		return fmt.Errorf("%w: %s: Up is required", ErrInvalidMigration, definition.id)
	}

	return nil
}

// MigrationGroup names an ordered sequence of migrations that a package
// ships. Out-of-order detection compares migrations only within one group,
// so a package release that adds a migration does not count as out of order
// against the application's own migrations. Create one with [NewGroup].
type MigrationGroup struct {
	// name is the group name.
	name string
}

// NewGroup returns a [MigrationGroup] called name. The name has the same rules
// as a migration id: 1-191 bytes, all printable and none white space. Any valid
// name works; Arkoren's own packages follow the repository's
// "arkoren.dev:<module>:<package>" convention, which this package does not
// enforce.
//
// Example:
//
//	func Migrations() []migrate.Definition {
//		return migrate.NewGroup("acme.users").Migrations(
//			migrate.NewMigration("create-users", func(migration *migrate.MigrationBuilder) {
//				migration.Up(migrate.Exec(query.Raw("CREATE TABLE users (id INTEGER)")))
//				migration.Down(migrate.Schema(schema.Drop("users")))
//			}),
//		)
//	}
func NewGroup(name string) MigrationGroup {
	return MigrationGroup{name: name}
}

// Migrations returns copies of definitions that belong to the group, in the
// same order. Each copy's id becomes the group name, a slash, and the short id
// the package gave it, so a package writes stable step names
// ("create-users") and the group decides the full id ("acme.users/create-users")
// that [Definition.ID], [Migrator.Status], and [MigrationEvent] report.
//
// A definition becomes invalid, and [Migrator.Add] reports it as
// [ErrInvalidMigration], when the group name is invalid, when it already
// belongs to a group, when its short id contains a slash (which also rejects
// ids prefixed by hand), or when the combined id is longer than 191 bytes.
// Ungrouped ids may contain slashes.
func (group MigrationGroup) Migrations(definitions ...Definition) []Definition {
	grouped := make([]Definition, len(definitions))

	for index, definition := range definitions {
		switch {
		case definition.err != nil:
		case !validID(group.name):
			definition.err = fmt.Errorf("%w: %s: group %q must be 1-%d printable characters without white space",
				ErrInvalidMigration, definition.id, group.name, maxIDLength)
		case definition.group != "":
			definition.err = fmt.Errorf("%w: %s: already in group %q", ErrInvalidMigration, definition.id, definition.group)
		case strings.Contains(definition.id, "/"):
			definition.err = fmt.Errorf("%w: id %q in group %q must not contain %q: the group prefixes ids itself",
				ErrInvalidMigration, definition.id, group.name, "/")
		case len(group.name)+1+len(definition.id) > maxIDLength:
			definition.err = fmt.Errorf("%w: id %q in group %q is longer than %d bytes once prefixed",
				ErrInvalidMigration, definition.id, group.name, maxIDLength)
		default:
			definition.group = group.name
			definition.id = group.name + "/" + definition.id
		}

		grouped[index] = definition
	}

	return grouped
}

// Exec returns a step that runs each statement in order with
// [database.Database.Exec]. A failure is wrapped as "statement N: ..."
// with N starting at 1. Because a [Func] is opaque, calling Exec with no
// statements or a nil statement is detected when the step runs, before it
// executes anything, not when the migration is added.
// The statement slice is copied immediately; statement implementations and
// arbitrary argument values retain their existing ownership rules.
//
// Example:
//
//	definition := migrate.NewMigration("backfill-users", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Exec(query.Raw("UPDATE users SET active = TRUE")))
//	})
//	if err := migrator.Add(definition); err != nil {
//		return err
//	}
func Exec(statements ...query.Statementer) Func {
	statements = slices.Clone(statements)

	return func(ctx context.Context, db *database.Database) error {
		if len(statements) == 0 {
			return fmt.Errorf("%w: Exec has no statements", ErrInvalidMigration)
		}

		for index, statement := range statements {
			if statement == nil {
				return fmt.Errorf("%w: Exec statement %d is nil", ErrInvalidMigration, index+1)
			}
		}

		for index, statement := range statements {
			if _, err := db.Exec(ctx, statement); err != nil {
				return fmt.Errorf("statement %d: %w", index+1, err)
			}
		}

		return nil
	}
}

// Schema returns a step that runs each schema operation in order. For every
// operation it first checks [schema.Operation.Err], then runs its statements
// in order with [database.Database.Exec], compiled for the database's
// dialect. A failure is wrapped as "operation N statement M: ..." with both
// counted from 1; an operation that failed to construct reports "operation N: ..."
// before anything runs. As with [Exec], an empty list is detected when the step runs,
// before it executes anything, and fails with [ErrInvalidMigration].
//
// Statements run one at a time, so on a dialect without transactional DDL
// ([query.Dialect.TransactionalDDL]) a failure part way leaves the earlier
// statements applied.
// The operation slice is copied immediately without rendering or executing it;
// later replacement of a caller's slice entry cannot change the step.
//
// Example:
//
//	migrate.NewMigration("20261017_create_users", func(migration *migrate.MigrationBuilder) {
//		migration.Up(migrate.Schema(
//			schema.Create("users", func(table *schema.Table) {
//				table.ID()
//				table.String("email", 255).Unique()
//			}),
//		))
//		migration.Down(migrate.Schema(schema.Drop("users")))
//	})
func Schema(operations ...schema.Operation) Func {
	operations = slices.Clone(operations)

	return func(ctx context.Context, db *database.Database) error {
		if len(operations) == 0 {
			return fmt.Errorf("%w: Schema has no operations", ErrInvalidMigration)
		}

		for index, operation := range operations {
			if err := operation.Err(); err != nil {
				return fmt.Errorf("operation %d: %w", index+1, err)
			}
		}

		for opIndex, operation := range operations {
			for stIndex, statement := range operation.Statements() {
				if _, err := db.Exec(ctx, statement); err != nil {
					return fmt.Errorf("operation %d statement %d: %w", opIndex+1, stIndex+1, err)
				}
			}
		}

		return nil
	}
}
