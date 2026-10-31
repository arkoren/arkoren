package schema

import (
	"errors"
	"fmt"
	"slices"

	"arkoren.dev/foundation/query"
)

// actionKind identifies what one Table method call asks for.
type actionKind uint8

const (
	// actionAddColumn adds a column: part of CREATE TABLE, or ADD COLUMN.
	actionAddColumn actionKind = iota

	// actionPrimary declares the table-level primary key.
	actionPrimary

	// actionUnique creates a unique index over the named columns.
	actionUnique

	// actionIndex creates a nonunique index over the named columns.
	actionIndex

	// actionDropColumn removes existing columns during ALTER.
	actionDropColumn

	// actionRenameColumn renames an existing column during ALTER.
	actionRenameColumn

	// actionChange modifies explicitly selected aspects of an existing column.
	actionChange

	// actionDropIndex removes the generated nonunique index.
	actionDropIndex

	// actionDropUnique removes the generated unique index.
	actionDropUnique

	// actionForeign adds a foreign key to an existing table.
	actionForeign

	// actionDropForeign removes the generated foreign key constraint.
	actionDropForeign
)

// action is one recorded Table method call. Only the fields its kind uses
// are set.
type action struct {
	// kind selects the validation and SQL generation for this declaration.
	kind actionKind

	// column is the new column for actionAddColumn; otherwise it is nil.
	column *Column

	// change holds the requested aspects for actionChange; otherwise it is nil.
	change *Change

	// foreign holds the new key for actionForeign; otherwise it is nil.
	foreign *Foreign

	// names holds the key or index columns, the dropped column, or the
	// from and to names of a column rename.
	names []string
}

// Table collects the definition of one table inside the callbacks of [Create],
// [CreateIfNotExists], or [Alter]. Column methods such as [Table.String] add
// a column and return it for modifiers; the other methods add keys, indexes,
// and, in Alter, changes to existing columns. Calls are recorded in order,
// and the first problem is reported by [Operation.Err].
//
// The callbacks run synchronously, in order, on the same Table. They must not
// mutate it concurrently. The completed declaration is copied before validation
// and publication; later changes to retained tables, columns, changes, or foreign
// keys have no effect on the operation. Arbitrary values passed to Column.Default
// or Change.Default are not recursively copied.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.Text("name")
//		table.Index("name")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
type Table struct {
	// name is the validated, optionally qualified table being declared.
	name tableName

	// alter enables operations that require an existing table.
	alter bool

	// err retains the first declaration error through publication.
	err error

	// actions are the recorded calls in the order they were written.
	actions []action

	// primary is the primary key columns, set by validate.
	primary []string
}

// fail records err unless the table already holds an earlier problem.
func (table *Table) fail(err error) {
	if table.err == nil {
		table.err = err
	}
}

// requireAlter records an error when method is used outside [Alter].
func (table *Table) requireAlter(method string) bool {
	if !table.alter {
		table.fail(fmt.Errorf("%s is only valid in Alter", method))
	}

	return table.alter
}

// record appends an action with the given column names.
func (table *Table) record(kind actionKind, names ...string) {
	table.actions = append(table.actions, action{
		kind:  kind,
		names: slices.Clone(names),
	})
}

// add appends a column of the given type.
func (table *Table) add(name string, kind columnKind, length, precision, scale int) *Column {
	column := &Column{
		table: table,
		name:  name,
	}

	if err := checkName(name); err != nil {
		table.fail(fmt.Errorf("invalid column name %q: %w", name, err))
	}

	column.setKind(kind, length, precision, scale)
	table.actions = append(table.actions, action{
		kind:   actionAddColumn,
		column: column,
	})

	return column
}

// String adds a varchar column holding up to length characters.
// Length must be positive; invalid lengths stay construction errors. The
// returned Column is NOT NULL until Nullable is called; SQLite renders TEXT.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.String("name", 160)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) String(name string, length int) *Column {
	return table.add(name, kindString, length, 0, 0)
}

// Char adds a fixed-length character column of length characters, which must
// be positive. SQLite stores it as TEXT and does not pad.
// It returns the added NOT NULL column for optional modifiers.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.Char("country", 2)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Char(name string, length int) *Column {
	return table.add(name, kindChar, length, 0, 0)
}

// Text adds an unbounded text column.
// It returns the added NOT NULL column for modifiers. SQLite renders TEXT;
// other dialects render text, without enforcing an application length limit.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.Text("bio").Nullable()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Text(name string) *Column {
	return table.add(name, kindText, 0, 0, 0)
}

// SmallInteger adds a 16-bit integer column. SQLite stores it as INTEGER.
// It returns the NOT NULL column for modifiers; SQLite does not enforce the
// narrower range, so applications own range validation for portable data.
//
// Example:
//
//	op := schema.Create("jobs", func(table *schema.Table) {
//		table.SmallInteger("priority").Default(0)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) SmallInteger(name string) *Column {
	return table.add(name, kindSmallInteger, 0, 0, 0)
}

// Integer adds a 32-bit integer column.
// It returns a NOT NULL Column. SQLite uses INTEGER without enforcing a
// 32-bit range; application validation must enforce portable value limits.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.Integer("visits").Default(0)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Integer(name string) *Column {
	return table.add(name, kindInteger, 0, 0, 0)
}

// BigInteger adds a 64-bit integer column.
// It returns a NOT NULL Column for modifiers. SQLite renders INTEGER; other
// dialects use bigint. Use AutoIncrement explicitly for an identity column.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.BigInteger("visits").Default(0)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) BigInteger(name string) *Column {
	return table.add(name, kindBigInteger, 0, 0, 0)
}

// Boolean adds a boolean column.
// It returns a NOT NULL Column. SQLite stores INTEGER; boolean defaults use
// dialect-appropriate truth literals without adding a CHECK constraint.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.Boolean("active").Default(true)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Boolean(name string) *Column {
	return table.add(name, kindBoolean, 0, 0, 0)
}

// Float adds a double-precision floating-point column.
// It returns a NOT NULL Column. Use Decimal for exact decimal storage;
// nonfinite default values are rejected during declaration capture.
//
// Example:
//
//	op := schema.Create("readings", func(table *schema.Table) {
//		table.Float("temperature")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Float(name string) *Column {
	return table.add(name, kindFloat, 0, 0, 0)
}

// Decimal adds an exact numeric column with precision digits, scale of them
// after the decimal point.
// Precision must be positive and 0 <= scale <= precision; invalid values stay
// construction errors. SQLite renders NUMERIC without enforcing those limits.
//
// Example:
//
//	op := schema.Create("orders", func(table *schema.Table) {
//		table.Decimal("amount", 12, 2)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Decimal(name string, precision, scale int) *Column {
	return table.add(name, kindDecimal, 0, precision, scale)
}

// Binary adds a binary large object column.
// It returns a NOT NULL Column. PostgreSQL uses bytea; ANSI/MySQL use blob and
// SQLite uses BLOB. The declaration does not retain any application payload.
//
// Example:
//
//	op := schema.Create("files", func(table *schema.Table) {
//		table.Binary("contents")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Binary(name string) *Column {
	return table.add(name, kindBinary, 0, 0, 0)
}

// UUID adds a UUID column.
// It returns a NOT NULL Column. PostgreSQL uses uuid, ANSI/MySQL use char(36),
// and SQLite uses TEXT; text-backed dialects do not gain UUID validation.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.UUID("external_id").Unique()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) UUID(name string) *Column {
	return table.add(name, kindUUID, 0, 0, 0)
}

// JSON adds a JSON document column.
// It returns a NOT NULL Column. PostgreSQL uses jsonb, ANSI/MySQL use json,
// and SQLite uses TEXT without an added JSON-validity constraint.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.JSON("preferences").Default("{}")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) JSON(name string) *Column {
	return table.add(name, kindJSON, 0, 0, 0)
}

// Date adds a calendar date column.
// It returns a NOT NULL Column. The declaration selects a SQL type, not an
// application codec or date-validation policy; SQLite's type is DATE.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.Date("birthday").Nullable()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Date(name string) *Column {
	return table.add(name, kindDate, 0, 0, 0)
}

// Timestamp adds a timestamp column without time zone.
// It returns a NOT NULL Column. ANSI/PostgreSQL use timestamp(6), MySQL uses
// datetime(6), and SQLite uses DATETIME; no current-time default is implicit.
//
// Example:
//
//	op := schema.Create("events", func(table *schema.Table) {
//		table.Timestamp("recorded_at").DefaultNow()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Timestamp(name string) *Column {
	return table.add(name, kindTimestamp, 0, 0, 0)
}

// TimestampTZ adds a timestamp column with time zone. MySQL has no such type,
// so it renders datetime(6), which stores no time zone: store UTC values.
// It returns a NOT NULL Column; a current-time default requires DefaultNow.
//
// Example:
//
//	op := schema.Create("events", func(table *schema.Table) {
//		table.TimestampTZ("recorded_at").DefaultNow()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) TimestampTZ(name string) *Column {
	return table.add(name, kindTimestampTZ, 0, 0, 0)
}

// Column adds a column whose type, sqlType, is rendered verbatim for every
// dialect. Never pass user input: it is not escaped.
// An empty type records a construction error. It returns the NOT NULL Column;
// nonempty native syntax remains the database's responsibility to accept.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.Column("email", "varchar(320)")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Column(name, sqlType string) *Column {
	column := table.add(name, kindRaw, 0, 0, 0)
	column.setRawType(sqlType)

	return column
}

// ID adds an auto-incrementing bigint primary key column named id.
// It returns that column for modifiers; conflicting primary declarations are
// construction errors. SQLite renders INTEGER PRIMARY KEY AUTOINCREMENT.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.ID()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) ID() *Column {
	return table.add("id", kindBigInteger, 0, 0, 0).AutoIncrement()
}

// ForeignID adds a bigint column that references the id column of
// foreignTable. Chain OnDelete or OnUpdate on the result, and Nullable for an
// optional relation.
// The returned column is NOT NULL by default. Reference names are validated
// at construction; dialect-specific target restrictions surface at compilation.
//
// Example:
//
//	op := schema.Create("posts", func(table *schema.Table) {
//		table.ForeignID("user_id", "users").OnDelete(schema.Cascade)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) ForeignID(name, foreignTable string) *Column {
	return table.BigInteger(name).References(foreignTable, "id")
}

// Timestamps adds NOT NULL created_at and updated_at timestamptz columns that
// default to the current time.
// It does not install an update trigger: applications must update updated_at
// explicitly. Duplicate column names remain construction errors.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.ID()
//		table.Timestamps()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Timestamps() {
	table.TimestampTZ("created_at").DefaultNow()
	table.TimestampTZ("updated_at").DefaultNow()
}

// Primary sets a table-level primary key over columns. It is valid only in
// [Create] and cannot be combined with a column's Primary modifier.
// Input names are copied. Empty, repeated, or undefined CREATE columns record
// a construction error; a table-level key can be declared only once.
//
// Example:
//
//	op := schema.Create("memberships", func(table *schema.Table) {
//		table.Integer("tenant_id")
//		table.Integer("user_id")
//		table.Primary("tenant_id", "user_id")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Primary(columns ...string) {
	if table.alter {
		table.fail(errors.New("Primary is only valid in Create; use the column's Primary modifier"))
	}

	table.record(actionPrimary, columns...)
}

// Unique adds a unique index over columns, named
// <table>_<columns>_key. Unique keys are always indexes, so [Table.DropUnique]
// is a DROP INDEX on every dialect.
// Names are copied; empty lists, repeated columns, or unknown CREATE columns
// fail construction. No database schema is inspected during declaration.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.String("email", 255)
//		table.Unique("email")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Unique(columns ...string) {
	table.record(actionUnique, columns...)
}

// Index adds a plain index over columns, named <table>_<columns>_index.
// Names are copied. CREATE validates membership after all callbacks, so an
// index may precede its columns; empty or repeated column lists are invalid.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.Text("name")
//		table.Index("name")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Index(columns ...string) {
	table.record(actionIndex, columns...)
}

// DropColumn drops each named column, one statement per column. It is valid
// only in [Alter].
// Names are copied and validated without inspecting the database. An empty
// list adds no actions; an otherwise empty ALTER remains invalid.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.DropColumn("legacy")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) DropColumn(names ...string) {
	if !table.requireAlter("DropColumn") {
		return
	}

	for _, name := range names {
		table.record(actionDropColumn, name)
	}
}

// RenameColumn renames column from to to. It is valid only in [Alter].
// Both names must be unqualified identifiers. Construction checks spelling,
// not whether the source exists or the target is available in the database.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.RenameColumn("name", "full_name")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) RenameColumn(from, to string) {
	if table.requireAlter("RenameColumn") {
		table.record(actionRenameColumn, from, to)
	}
}

// Change redefines the existing column name; see [Change] for what it
// changes and the MySQL and SQLite rules. It is valid only in [Alter].
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("email").String(320).NotNull()
//		table.Change("bio").DropDefault()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Change(name string) *Change {
	change := &Change{
		column: Column{
			table: table,
			name:  name,
		},
	}

	if table.requireAlter("Change") {
		table.actions = append(table.actions, action{
			kind:   actionChange,
			change: change,
		})
	}

	return change
}

// DropIndex drops the index that [Table.Index] created over the same columns.
// It is valid only in [Alter].
// Names are copied and must form a nonempty valid key list; database index
// existence is not checked at construction.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.DropIndex("name")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) DropIndex(columns ...string) {
	if table.requireAlter("DropIndex") {
		table.record(actionDropIndex, columns...)
	}
}

// DropUnique drops the index that [Table.Unique] created over the same
// columns. It is valid only in [Alter].
// Names are copied and validated, including the generated identifier's length.
// The operation does not check whether that index exists in the database.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.DropUnique("email")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) DropUnique(columns ...string) {
	if table.requireAlter("DropUnique") {
		table.record(actionDropUnique, columns...)
	}
}

// Foreign adds a foreign key from column to refColumn in foreignTable to an
// existing table, named <table>_<column>_fkey. SQLite cannot add one. It is
// valid only in [Alter]; in [Create] use [Column.References].
// It returns a mutable child for referential actions. Invalid source/target
// identifiers are retained as construction errors without contacting the database.
//
// Example:
//
//	op := schema.Alter("posts", func(table *schema.Table) {
//		table.Foreign("user_id", "users", "id").OnDelete(schema.Cascade)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) Foreign(column, foreignTable, refColumn string) *Foreign {
	foreign := &Foreign{
		table:  table,
		column: column,
	}

	if !table.requireAlter("Foreign") {
		return foreign
	}

	parsed, err := parseName(foreignTable, true)

	if err == nil {
		err = checkName(refColumn)
	}

	if err != nil {
		table.fail(err)
	}

	foreign.key = foreignKey{
		table:  parsed,
		column: refColumn,
	}
	table.actions = append(table.actions, action{
		kind:    actionForeign,
		foreign: foreign,
	})

	return foreign
}

// DropForeign drops the foreign key that [Table.Foreign] or
// [Column.References] created on column. It is valid only in [Alter].
// Construction validates the column and generated constraint-name length.
// SQLite rejects compilation; no database existence check happens here.
//
// Example:
//
//	op := schema.Alter("posts", func(table *schema.Table) {
//		table.DropForeign("user_id")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (table *Table) DropForeign(column string) {
	if table.requireAlter("DropForeign") {
		table.record(actionDropForeign, column)
	}
}

// validate checks the detached declaration and derives its primary key once.
// Publication calls it after all callbacks, so a later callback can complete an
// earlier key or column declaration without changing individual-setting checks.
func (table *Table) validate() error {
	if table.err != nil {
		return table.err
	}

	if len(table.actions) == 0 {
		return errors.New("callback defines nothing")
	}

	added := table.addedColumns()

	if !table.alter && len(added) == 0 {
		return errors.New("table needs at least one column")
	}

	validation := tableValidation{
		table:   table,
		added:   added,
		claimed: make(map[string]bool),
	}

	for _, declaration := range table.actions {
		if err := validation.check(declaration); err != nil {
			return err
		}
	}

	if err := table.validatePrimary(validation.autoIncrements, validation.primary, added); err != nil {
		return err
	}

	if len(table.primary) > 0 {
		return validation.claim(nil, "pkey")
	}

	return nil
}

// validatePrimary combines the detached column and table key declarations.
// Auto-increment owns the sole key, while ALTER's per-column statements cannot
// form a composite key: each would add a separate primary-key constraint.
// Create keeps its existing declaration-order composite key behavior.
func (table *Table) validatePrimary(autoIncrements int, tablePrimary, added []string) error {
	if autoIncrements > 1 {
		return errors.New("at most one auto-increment column is allowed")
	}

	if len(table.primary) > 0 && len(tablePrimary) > 0 {
		return errors.New("primary key declared on both column and table")
	}

	if autoIncrements > 0 && len(table.primary)+len(tablePrimary) > 0 {
		return errors.New("auto-increment column is already the primary key")
	}

	if table.alter && len(table.primary) > 1 {
		return errors.New("alter cannot add more than one primary-key column")
	}

	table.primary = append(table.primary, tablePrimary...)

	if len(table.primary) == 0 {
		return nil
	}

	return table.checkKey(table.primary, added)
}

// checkNames requires every name to be a valid identifier.
func (table *Table) checkNames(names []string) error {
	for _, name := range names {
		if err := checkName(name); err != nil {
			return fmt.Errorf("invalid column name %q: %w", name, err)
		}
	}

	return nil
}

// checkKey validates the columns of a key or index: at least one, valid
// names, none listed twice, and, in Create, every one defined by the table.
func (table *Table) checkKey(columns, defined []string) error {
	if len(columns) == 0 {
		return errors.New("key needs at least one column")
	}

	if err := table.checkNames(columns); err != nil {
		return err
	}

	for index, column := range columns {
		if slices.Contains(columns[:index], column) {
			return fmt.Errorf("key column %q is listed twice", column)
		}

		if !table.alter && !slices.Contains(defined, column) {
			return fmt.Errorf("key column %q is not defined", column)
		}
	}

	return nil
}

// Create returns the operation that creates table with the columns, keys, and
// indexes define declares. The name may be schema-qualified.
//
// It compiles to one CREATE TABLE with the columns in written order and the
// primary key and foreign keys inline, then one CREATE [UNIQUE] INDEX per
// [Table.Index], [Table.Unique], and [Column.Unique], in written order. Unique
// keys are always unique indexes, never inline constraints. After validating the
// table name, Create invokes each supplied callback directly, once, synchronously
// in argument order on the same Table. A nil callback or an application panic
// propagates; later callbacks do not run. Invalid names skip the callbacks.
//
// The combined declaration is copied and validated after all callbacks. Omitting
// callbacks, including an expanded nil or empty callback slice, or supplying only
// empty callbacks leaves an invalid declaration reported by [Operation.Err].
// Retained tables and child declarations cannot change the published operation.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.ID()
//		table.ForeignID("tenant_id", "tenants").OnDelete(schema.Cascade)
//		table.String("email", 255)
//		table.Text("bio").Nullable()
//		table.Timestamps()
//		table.Unique("tenant_id", "email")
//		table.Index("bio")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func Create(table string, define ...func(*Table)) Operation {
	return tableOperation(table, define, false, false)
}

// CreateIfNotExists is [Create] with IF NOT EXISTS on the table and on its
// indexes. ANSI has no table IF NOT EXISTS, and MySQL and ANSI have none for
// indexes, so those statements fail with [query.ErrUnsupported] on them.
// Callback ordering, omission, panic propagation, and ownership follow Create.
//
// Example:
//
//	op := schema.CreateIfNotExists("users", func(table *schema.Table) {
//		table.Text("name")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func CreateIfNotExists(table string, define ...func(*Table)) Operation {
	return tableOperation(table, define, false, true)
}

// Alter returns the operation that changes table as define declares, one
// statement per call in written order. A column type method adds the column
// (ADD COLUMN, followed by CREATE UNIQUE INDEX for a unique column). The name
// may be schema-qualified.
// Callbacks follow [Create]'s ordering, direct invocation, panic propagation,
// and detached publication rules. An omitted or empty declaration reports its
// missing actions through [Operation.Err]; it is not a successful no-op.
//
// SQLite cannot change columns or add and drop foreign keys, and its ADD
// COLUMN rejects primary keys, auto-increment, current-time and raw defaults,
// a non-NULL default on a referencing column, and NOT NULL without a non-NULL
// literal default (including an explicit Default(nil)).
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.String("avatar", 255).Nullable()
//		table.RenameColumn("name", "full_name")
//		table.DropColumn("legacy")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func Alter(table string, define ...func(*Table)) Operation {
	return tableOperation(table, define, true, false)
}

// tableOperation rejects invalid names before invoking declarations, then freezes
// their combined draft before validation and lazy statement compilation. Callback
// panics propagate; domain failures become the existing invalid Operation.
func tableOperation(name string, define []func(*Table), alter, ifNotExists bool) Operation {
	parsed, err := parseName(name, true)

	if err != nil {
		return failed(err)
	}

	table := &Table{
		name:  parsed,
		alter: alter,
	}

	for _, callback := range define {
		callback(table)
	}

	table = table.snapshot()

	if err := table.validate(); err != nil {
		return failed(err)
	}

	return Operation{
		steps: table.steps(ifNotExists),
	}
}

// snapshot detaches the root and every owned declaration container before any
// renderer retains them. Validation then operates only on this private copy;
// application-retained Table and child pointers remain independent drafts.
func (table *Table) snapshot() *Table {
	copied := *table
	copied.name = slices.Clone(table.name)
	copied.primary = slices.Clone(table.primary)
	copied.actions = make([]action, len(table.actions))

	for index, declaration := range table.actions {
		copied.actions[index] = declaration.snapshot(&copied)
	}

	return &copied
}

// snapshot copies an action's mutable child and name lists and binds its error
// reporting to the detached table. Only the child used by the action kind exists;
// arbitrary Default values keep their established shallow ownership.
func (declaration action) snapshot(table *Table) action {
	declaration.names = slices.Clone(declaration.names)

	switch declaration.kind {
	case actionAddColumn:
		column := declaration.column.snapshot(table)
		declaration.column = &column
	case actionChange:
		change := *declaration.change
		change.column = change.column.snapshot(table)
		declaration.change = &change
	case actionForeign:
		foreign := *declaration.foreign
		foreign.table = table
		foreign.key.table = slices.Clone(foreign.key.table)
		declaration.foreign = &foreign
	}

	return declaration
}

// snapshot copies the column's owned reference name and retargets its error
// destination. The arbitrary value stored by Default is retained without recursive
// copying, matching its existing validation and rendering contract.
func (column Column) snapshot(table *Table) Column {
	column.table = table
	column.foreign.table = slices.Clone(column.foreign.table)

	return column
}

// steps publishes lazy renderers from the validated private snapshot. CREATE
// comes first; each recorded action then contributes its statements in written
// order. A unique added column keeps its index immediately after that action.
func (table *Table) steps(ifNotExists bool) []step {
	var steps []step

	if !table.alter {
		steps = append(steps, step{
			render: func(dialect query.Dialect) (string, error) {
				return table.createTable(dialect, ifNotExists)
			},
		})
	}

	for _, declaration := range table.actions {
		switch declaration.kind {
		case actionAddColumn:
			if table.alter {
				steps = append(steps, step{
					render: func(dialect query.Dialect) (string, error) {
						return table.addColumn(dialect, declaration.column)
					},
				})
			}

			if declaration.column.unique {
				steps = append(steps, table.indexStep(true, ifNotExists, declaration.column.name))
			}
		case actionUnique:
			steps = append(steps, table.indexStep(true, ifNotExists, declaration.names...))
		case actionIndex:
			steps = append(steps, table.indexStep(false, ifNotExists, declaration.names...))
		case actionPrimary:
			continue // The primary key is inline in CREATE, never a separate step.
		default:
			steps = append(steps, table.alterActionStep(declaration))
		}
	}

	return steps
}

// alterActionStep captures one validated ALTER-only action without rendering
// it. Column additions and created indexes are handled by steps because they
// can contribute multiple statements or carry CREATE's IF NOT EXISTS policy.
func (table *Table) alterActionStep(declaration action) step {
	switch declaration.kind {
	case actionDropColumn:
		return table.alterStep(func(dialect query.Dialect) (string, error) {
			return "DROP COLUMN " + dialect.Quote(declaration.names[0]), nil
		})
	case actionRenameColumn:
		return table.alterStep(func(dialect query.Dialect) (string, error) {
			return "RENAME COLUMN " + dialect.Quote(declaration.names[0]) + " TO " + dialect.Quote(declaration.names[1]), nil
		})
	case actionChange:
		return step{
			render: func(dialect query.Dialect) (string, error) {
				return table.changeColumn(dialect, declaration.change)
			},
		}
	case actionDropIndex:
		return table.dropIndexStep("index", declaration.names)
	case actionDropUnique:
		return table.dropIndexStep("key", declaration.names)
	case actionForeign:
		return table.alterStep(func(dialect query.Dialect) (string, error) {
			return table.addForeignClause(dialect, declaration.foreign)
		})
	case actionDropForeign:
		return table.alterStep(func(dialect query.Dialect) (string, error) {
			return table.dropForeignClause(dialect, declaration.names)
		})
	}

	return step{}
}

// addForeignClause renders the named constraint on an existing column. SQLite's
// feature restriction remains first; generated-name validation then precedes SQL
// formatting. The input is an already detached child of the published table.
func (table *Table) addForeignClause(dialect query.Dialect, foreign *Foreign) (string, error) {
	if dialect == query.SQLite {
		return "", unsupported(dialect, "ADD FOREIGN KEY")
	}

	name, err := constraintName(table.name, []string{foreign.column}, "fkey")

	if err != nil {
		return "", err
	}

	return "ADD CONSTRAINT " + dialect.Quote(name) + " " + foreignKeyClause(dialect, foreign.column, foreign.key), nil
}

// dropForeignClause derives the existing constraint name before choosing the
// native drop syntax. MySQL uses DROP FOREIGN KEY, ANSI/PostgreSQL use DROP
// CONSTRAINT, and SQLite retains its unsupported-feature error at compilation.
func (table *Table) dropForeignClause(dialect query.Dialect, columns []string) (string, error) {
	name, err := constraintName(table.name, columns, "fkey")

	if err != nil {
		return "", err
	}

	switch dialect {
	case query.SQLite:
		return "", unsupported(dialect, "DROP FOREIGN KEY")
	case query.MySQL:
		return "DROP FOREIGN KEY " + dialect.Quote(name), nil
	default:
		return "DROP CONSTRAINT " + dialect.Quote(name), nil
	}
}

// alterStep wraps a clause as ALTER TABLE <table> <clause>.
func (table *Table) alterStep(clause func(dialect query.Dialect) (string, error)) step {
	return step{
		render: func(dialect query.Dialect) (string, error) {
			sql, err := clause(dialect)

			if err != nil {
				return "", err
			}

			return "ALTER TABLE " + table.name.quoted(dialect) + " " + sql, nil
		},
	}
}
