package schema

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
)

// columnKind identifies a portable column type. The zero kind means no type.
type columnKind uint8

const (
	// kindString is a bounded varchar; length holds the limit.
	kindString columnKind = iota + 1

	// kindText stores text without a declared length limit.
	kindText

	// kindInteger uses the dialect's regular integer type.
	kindInteger

	// kindBigInteger uses the dialect's large integer type.
	kindBigInteger

	// kindBoolean stores truth values with dialect-specific representation.
	kindBoolean

	// kindFloat stores approximate floating-point numbers.
	kindFloat

	// kindDecimal stores numbers parameterized by precision and scale.
	kindDecimal

	// kindBinary stores byte sequences in the dialect's binary type.
	kindBinary

	// kindUUID uses native UUID support or the dialect's string representation.
	kindUUID

	// kindJSON uses the dialect's JSON type or SQLite TEXT.
	kindJSON

	// kindDate stores a calendar date without a time of day.
	kindDate

	// kindTimestamp stores a timestamp without a declared time zone.
	kindTimestamp

	// kindTimestampTZ requests a timestamp with time-zone support where available.
	kindTimestampTZ

	// kindSmallInteger uses the dialect's small integer type.
	kindSmallInteger

	// kindChar is a fixed-length character type; length holds the size.
	kindChar

	// kindRaw is a caller-supplied type rendered verbatim. It must stay last:
	// typeNames is sized from it.
	kindRaw
)

// defaultKind identifies how a column default is rendered.
type defaultKind uint8

const (
	// defaultNone means the column has no DEFAULT clause.
	defaultNone defaultKind = iota

	// defaultLiteral renders defaultValue as a quoted or numeric literal.
	defaultLiteral

	// defaultNow renders the dialect's current-timestamp expression.
	defaultNow

	// defaultRaw renders defaultExpr verbatim.
	defaultRaw
)

// foreignKey describes a reference from a column to another table's column.
type foreignKey struct {
	// table is the validated, optionally qualified referenced table name.
	table tableName

	// column is the validated referenced column name.
	column string

	// onDelete selects the delete action; empty omits ON DELETE.
	onDelete ReferentialAction

	// onUpdate selects the update action; empty omits ON UPDATE.
	onUpdate ReferentialAction
}

// Column describes one column of a [Table]. Table methods such as
// [Table.String] return it so modifiers can chain; it is mutable only inside
// the callback that created it, and later changes are ignored. Columns are
// NOT NULL unless [Column.Nullable] is called. Combinations no dialect
// accepts, such as [Column.DefaultNow] on a text column, are reported by
// [Operation.Err].
// All modifiers return the same draft; retain the published Operation, not a
// Column, for concurrent compilation. Invalid individual settings stay errors.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		column := table.String("nickname", 80)
//		column.Nullable().Default("guest")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
type Column struct {
	// table receives the errors recorded by modifiers.
	table *Table

	// name is the validated column name, without table qualification.
	name string

	// kind selects the portable type or a caller-supplied raw type.
	kind columnKind

	// length bounds kindString and fixes the size of kindChar.
	length int

	// precision is the total number of digits for kindDecimal.
	precision int

	// scale is the number of fractional digits for kindDecimal.
	scale int

	// rawType is trusted SQL rendered verbatim for kindRaw.
	rawType string

	// nullable permits NULL; columns are NOT NULL unless explicitly changed.
	nullable bool

	// defaultKind selects between defaultValue, defaultExpr, and the
	// current-timestamp default.
	defaultKind defaultKind

	// defaultValue is the literal default; arbitrary objects are not cloned.
	defaultValue any

	// defaultExpr is trusted SQL used only for defaultRaw.
	defaultExpr string

	// unique requests a separate unique index after the column is created.
	unique bool

	// primary includes this column in the table's primary key.
	primary bool

	// autoIncrement implies the primary key and excludes nullable and defaults.
	autoIncrement bool

	// references records an explicit foreign key declaration.
	references bool

	// foreign holds the reference target and optional referential actions.
	foreign foreignKey
}

// setKind sets the type and records a problem with its parameters.
func (column *Column) setKind(kind columnKind, length, precision, scale int) {
	column.kind, column.length, column.precision, column.scale = kind, length, precision, scale

	switch {
	case kind == kindString && length <= 0:
		column.table.fail(fmt.Errorf("string length %d must be positive", length))
	case kind == kindChar && length <= 0:
		column.table.fail(fmt.Errorf("char length %d must be positive", length))
	case kind == kindDecimal && (precision <= 0 || scale < 0 || scale > precision):
		column.table.fail(fmt.Errorf("decimal(%d, %d) needs 0 <= scale <= precision and precision > 0", precision, scale))
	}
}

// setRawType sets a caller-supplied type rendered verbatim.
func (column *Column) setRawType(sqlType string) {
	if sqlType == "" {
		column.table.fail(errors.New("raw column type cannot be empty"))
	}

	column.kind, column.rawType = kindRaw, sqlType
}

// validate rejects modifier combinations that no dialect accepts. It runs
// when the operation is constructed, so modifier order does not matter.
func (column *Column) validate() error {
	if err := column.validateAutoIncrement(); err != nil {
		return err
	}

	if err := column.validateTimestampDefault(); err != nil {
		return err
	}

	if !column.references && (column.foreign.onDelete != "" || column.foreign.onUpdate != "") {
		return fmt.Errorf("column %q: referential actions need References", column.name)
	}

	return nil
}

// validateAutoIncrement checks the identity-column invariant in its established
// diagnostic order: integer type, nullability, then default. Ordinary columns
// have no identity restrictions; this method changes no declaration state.
func (column *Column) validateAutoIncrement() error {
	if !column.autoIncrement {
		return nil
	}

	switch column.kind {
	case kindSmallInteger, kindInteger, kindBigInteger:
	default:
		return fmt.Errorf("auto-increment column %q must be an integer", column.name)
	}

	if column.nullable {
		return fmt.Errorf("auto-increment column %q cannot be nullable", column.name)
	}

	if column.defaultKind != defaultNone {
		return fmt.Errorf("auto-increment column %q cannot have a default", column.name)
	}

	return nil
}

// validateTimestampDefault checks the shared type restriction for DefaultNow.
// CREATE always knows the type; Change calls this only with an explicit type,
// leaving an untyped default-only change to database validation at execution.
func (column *Column) validateTimestampDefault() error {
	timestamp := column.kind == kindTimestamp || column.kind == kindTimestampTZ

	if column.defaultKind == defaultNow && !timestamp {
		return fmt.Errorf("column %q: DefaultNow needs a timestamp column", column.name)
	}

	return nil
}

// Nullable allows NULL values in the column and returns the same draft.
// It does not change a default; auto-increment columns cannot be nullable.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.Text("bio").Nullable()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (column *Column) Nullable() *Column {
	column.nullable = true

	return column
}

// Default sets a literal default. It accepts strings, integers, finite floats,
// booleans, and nil, which means NULL, including named forms of these types.
// Unsupported types, nonfinite floats, and NUL-bearing strings record a sticky
// construction error: replacing the default does not clear it. Valid strings
// are escaped for the selected dialect when compiled.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.Integer("visits").Default(0)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (column *Column) Default(value any) *Column {
	if err := validateLiteral(value); err != nil {
		column.table.fail(fmt.Errorf("column %q: %w", column.name, err))
	}

	column.defaultKind, column.defaultValue = defaultLiteral, value

	return column
}

// validateLiteral checks dialect-independent literal restrictions at capture.
// Rendering can then format validated scalar values without repeating content
// validation. Nil means SQL NULL; arbitrary containers and pointers are invalid.
func validateLiteral(value any) error {
	if value == nil {
		return nil
	}

	reflected := reflect.ValueOf(value)

	switch reflected.Kind() {
	case reflect.String:
		if strings.ContainsRune(reflected.String(), 0) {
			return errors.New("default string cannot contain NUL")
		}
	case reflect.Float32, reflect.Float64:
		number := reflected.Float()

		if math.IsNaN(number) || math.IsInf(number, 0) {
			return errors.New("default float must be finite")
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return nil
	default:
		return fmt.Errorf("unsupported default type %T", value)
	}

	return nil
}

// DefaultNow defaults the column to the current timestamp. It is valid only
// on [Table.Timestamp] and [Table.TimestampTZ] columns. It replaces any earlier
// default and returns the same draft; [Operation.Err] reports incompatible types.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.TimestampTZ("created_at").DefaultNow()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (column *Column) DefaultNow() *Column {
	column.defaultKind = defaultNow

	return column
}

// DefaultRaw defaults the column to expression, rendered verbatim. Never pass
// user input: it is not escaped, and a "?" outside quotes fails.
// Empty expressions record a sticky construction error. The same draft is
// returned; dialect syntax and raw placeholder failures surface at compilation.
//
// Example:
//
//	op := schema.Create("jobs", func(table *schema.Table) {
//		table.Integer("attempts").DefaultRaw("(40 + 2)")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (column *Column) DefaultRaw(expression string) *Column {
	if expression == "" {
		column.table.fail(errors.New("raw default expression cannot be empty"))
	}

	column.defaultKind, column.defaultExpr = defaultRaw, expression

	return column
}

// Unique makes the column unique with a unique index named
// <table>_<column>_key, created after the table (or the added column).
// It returns the same draft; repeating it does not add another index.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.String("email", 255).Unique()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (column *Column) Unique() *Column {
	column.unique = true

	return column
}

// Primary marks the column as part of the primary key. Marking several
// columns forms one composite primary key, in declaration order. It cannot be
// combined with [Table.Primary]. In [Alter] it adds a primary key constraint
// on this one column; marking multiple added columns fails construction rather
// than emitting multiple primary-key constraints. Use raw DDL for a composite
// primary key on an existing table.
//
// Example:
//
//	operation := schema.Create("memberships", func(table *schema.Table) {
//		table.Integer("tenant_id").Primary()
//		table.Integer("user_id").Primary()
//	})
//	if err := operation.Err(); err != nil {
//		return err
//	}
func (column *Column) Primary() *Column {
	column.primary = true

	return column
}

// AutoIncrement makes an integer column an auto-incrementing primary key. It
// cannot be combined with Nullable or any default. It returns the same draft;
// [Operation.Err] reports incompatible types and conflicting primary declarations.
//
// Example:
//
//	op := schema.Create("users", func(table *schema.Table) {
//		table.BigInteger("id").AutoIncrement()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (column *Column) AutoIncrement() *Column {
	column.autoIncrement = true

	return column
}

// References adds a foreign key from the column to referenced in
// foreignTable, which may be schema-qualified. The constraint is named
// <table>_<column>_fkey. SQLite requires an unqualified foreignTable in the
// referencing table's schema; a qualified target fails compilation with
// [arkoren.dev/foundation/query.ErrUnsupported], even when it names that schema.
//
// Example:
//
//	operation := schema.Create("posts", func(table *schema.Table) {
//		table.BigInteger("user_id").References("users", "id")
//	})
//	statement, err := operation.Statements()[0].BuildFor(query.SQLite)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
func (column *Column) References(foreignTable, referenced string) *Column {
	parsed, err := parseName(foreignTable, true)

	if err == nil {
		err = checkName(referenced)
	}

	if err != nil {
		column.table.fail(err)

		return column
	}

	column.references = true
	column.foreign.table, column.foreign.column = parsed, referenced

	return column
}

// OnDelete sets the ON DELETE action of the column's foreign key. The column
// must also call [Column.References]. Empty clears the action; unknown values
// record a sticky construction error. It returns the same draft.
//
// Example:
//
//	op := schema.Create("posts", func(table *schema.Table) {
//		table.ForeignID("user_id", "users").OnDelete(schema.Cascade)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (column *Column) OnDelete(action ReferentialAction) *Column {
	if !action.valid() {
		column.table.fail(fmt.Errorf("unknown referential action %q", action))
	}

	column.foreign.onDelete = action

	return column
}

// OnUpdate sets the ON UPDATE action of the column's foreign key. The column
// must also call [Column.References]. Empty clears the action; unknown values
// record a sticky construction error. It returns the same draft.
//
// Example:
//
//	op := schema.Create("posts", func(table *schema.Table) {
//		table.ForeignID("user_id", "users").OnUpdate(schema.Cascade)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (column *Column) OnUpdate(action ReferentialAction) *Column {
	if !action.valid() {
		column.table.fail(fmt.Errorf("unknown referential action %q", action))
	}

	column.foreign.onUpdate = action

	return column
}

// Foreign is a foreign key added to an existing table with [Table.Foreign].
// Its modifiers edit only the declaration draft; publication detaches them and
// [Operation.Err] retains invalid actions. SQLite cannot add this constraint.
//
// Example:
//
//	op := schema.Alter("posts", func(table *schema.Table) {
//		foreign := table.Foreign("user_id", "users", "id")
//		foreign.OnDelete(schema.Cascade)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
type Foreign struct {
	// table receives validation errors from foreign key modifiers.
	table *Table

	// column is the referencing column on the altered table.
	column string

	// key holds the referenced table, column, and optional actions.
	key foreignKey
}

// OnDelete sets the ON DELETE action of the foreign key and returns this draft.
// Empty clears the action; unknown actions retain a construction error.
//
// Example:
//
//	op := schema.Alter("posts", func(table *schema.Table) {
//		table.Foreign("user_id", "users", "id").OnDelete(schema.Restrict)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (foreign *Foreign) OnDelete(action ReferentialAction) *Foreign {
	if !action.valid() {
		foreign.table.fail(fmt.Errorf("unknown referential action %q", action))
	}

	foreign.key.onDelete = action

	return foreign
}

// OnUpdate sets the ON UPDATE action of the foreign key and returns this draft.
// Empty clears the action; unknown actions retain a construction error.
//
// Example:
//
//	op := schema.Alter("posts", func(table *schema.Table) {
//		table.Foreign("user_id", "users", "id").OnUpdate(schema.NoAction)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (foreign *Foreign) OnUpdate(action ReferentialAction) *Foreign {
	if !action.valid() {
		foreign.table.fail(fmt.Errorf("unknown referential action %q", action))
	}

	foreign.key.onUpdate = action

	return foreign
}

// Change redefines an existing column with [Table.Change]. It changes only
// what is called: a type method, [Change.Nullable] or [Change.NotNull], and a
// default method. Calling nothing is an error. When an aspect is called
// twice, the last call wins.
//
// Postgres and ANSI emit one ALTER COLUMN clause per aspect set. MySQL emits
// ALTER COLUMN SET/DROP DEFAULT for a default-only change; a type or
// nullability change emits MODIFY COLUMN, which restates the whole column, so
// it needs both a type and Nullable or NotNull, and drops a default that is
// not restated. SQLite cannot change a column.
// Changes remain mutable only as declaration drafts; later edits cannot modify
// a published Operation. Individual invalid settings remain errors after edits.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		change := table.Change("name")
//		change.String(160).NotNull().Default("guest")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
type Change struct {
	// column carries the type, nullability, and default being set; its name
	// is the column being changed.
	column Column

	// nullSet records that Nullable or NotNull was called; column.nullable
	// then holds the choice.
	nullSet bool

	// dropDefault records DropDefault.
	dropDefault bool

	// using is the Postgres USING expression.
	using string
}

// String changes the type to a varchar holding up to length characters.
// Length must be positive; invalid lengths stay construction errors. The same
// draft is returned, and nullability is unchanged unless explicitly configured.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("name").String(160).NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) String(length int) *Change {
	change.column.setKind(kindString, length, 0, 0)

	return change
}

// Char changes the type to a fixed-length character type of length characters.
// Length must be positive. It returns the same draft and does not change the
// requested nullability or default; SQLite cannot compile column changes.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("country").Char(2).NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) Char(length int) *Change {
	change.column.setKind(kindChar, length, 0, 0)

	return change
}

// Text changes the type to unbounded text.
// It returns this draft, leaving other aspects unchanged. MySQL also requires
// explicit nullability when changing a type; SQLite cannot compile the change.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("bio").Text().Nullable()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) Text() *Change {
	change.column.setKind(kindText, 0, 0, 0)

	return change
}

// SmallInteger changes the type to a 16-bit integer.
// It returns the same draft. Nullability and defaults are separate aspects;
// SQLite does not support changing an existing column's type.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("priority").SmallInteger().NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) SmallInteger() *Change {
	change.column.setKind(kindSmallInteger, 0, 0, 0)

	return change
}

// Integer changes the type to a 32-bit integer.
// It returns the same draft and leaves other aspects untouched. MySQL needs
// explicit nullability too; SQLite rejects type changes at compilation.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("visits").Integer().NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) Integer() *Change {
	change.column.setKind(kindInteger, 0, 0, 0)

	return change
}

// BigInteger changes the type to a 64-bit integer.
// It returns the same draft, without selecting nullability or a default.
// MySQL needs explicit nullability; SQLite cannot compile a type change.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("visits").BigInteger().NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) BigInteger() *Change {
	change.column.setKind(kindBigInteger, 0, 0, 0)

	return change
}

// Boolean changes the type to boolean.
// It returns the same draft; conversion of existing data is database-owned.
// Other aspects remain unchanged except for MySQL's whole-column restatement.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("active").Boolean().NotNull().Default(true)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) Boolean() *Change {
	change.column.setKind(kindBoolean, 0, 0, 0)

	return change
}

// Float changes the type to double-precision floating point.
// It returns this draft. Use Decimal instead when approximate representation
// is unsuitable; existing-value conversion remains the database's responsibility.
//
// Example:
//
//	op := schema.Alter("readings", func(table *schema.Table) {
//		table.Change("temperature").Float().NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) Float() *Change {
	change.column.setKind(kindFloat, 0, 0, 0)

	return change
}

// Decimal changes the type to an exact numeric with precision digits, scale
// of them after the decimal point.
// Precision must be positive and 0 <= scale <= precision. Invalid parameters
// stay construction errors; valid calls return the same draft.
//
// Example:
//
//	op := schema.Alter("orders", func(table *schema.Table) {
//		table.Change("amount").Decimal(12, 2).NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) Decimal(precision, scale int) *Change {
	change.column.setKind(kindDecimal, 0, precision, scale)

	return change
}

// Binary changes the type to a binary large object.
// It returns this draft, leaving nullability and defaults to separate methods.
// The database determines whether existing values can convert to its binary type.
//
// Example:
//
//	op := schema.Alter("files", func(table *schema.Table) {
//		table.Change("contents").Binary().NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) Binary() *Change {
	change.column.setKind(kindBinary, 0, 0, 0)

	return change
}

// UUID changes the type to UUID.
// It returns this draft. PostgreSQL uses uuid; other supported non-SQLite
// dialects use char(36). SQLite rejects all existing-column changes.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("external_id").UUID().NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) UUID() *Change {
	change.column.setKind(kindUUID, 0, 0, 0)

	return change
}

// JSON changes the type to a JSON document.
// It returns this draft. PostgreSQL uses jsonb and MySQL/ANSI use json; the
// database validates existing content when the compiled DDL is executed.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("preferences").JSON().NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) JSON() *Change {
	change.column.setKind(kindJSON, 0, 0, 0)

	return change
}

// Date changes the type to a calendar date.
// It returns the same draft and does not supply a default or conversion policy.
// SQLite cannot compile the change; MySQL also needs explicit nullability.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("birthday").Date().Nullable()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) Date() *Change {
	change.column.setKind(kindDate, 0, 0, 0)

	return change
}

// Timestamp changes the type to a timestamp without time zone.
// It returns the same draft. Defaults and nullability are separate settings;
// TimestampTZ selects the timezone-aware type where the dialect supports it.
//
// Example:
//
//	op := schema.Alter("events", func(table *schema.Table) {
//		table.Change("recorded_at").Timestamp().NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) Timestamp() *Change {
	change.column.setKind(kindTimestamp, 0, 0, 0)

	return change
}

// TimestampTZ changes the type to a timestamp with time zone. MySQL renders
// datetime(6), which stores no zone: store UTC values.
// It returns the same draft; SQLite cannot change an existing column.
//
// Example:
//
//	op := schema.Alter("events", func(table *schema.Table) {
//		table.Change("recorded_at").TimestampTZ().NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) TimestampTZ() *Change {
	change.column.setKind(kindTimestampTZ, 0, 0, 0)

	return change
}

// Type changes the type to sqlType, rendered verbatim for every dialect.
// Never pass user input: it is not escaped.
// Empty types retain a construction error. It returns the same draft and does
// not check whether a nonempty native type exists on a particular database.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("email").Type("varchar(320)").NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) Type(sqlType string) *Change {
	change.column.setRawType(sqlType)

	return change
}

// Nullable allows NULL values in the column and returns this draft.
// It replaces NotNull; MySQL also requires a type for its MODIFY COLUMN form.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("name").String(160).Nullable()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) Nullable() *Change {
	change.column.nullable, change.nullSet = true, true

	return change
}

// NotNull forbids NULL values in the column. Existing NULLs make the
// statement fail at run time.
// It replaces Nullable and returns this draft. MySQL also requires a type.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("name").String(160).NotNull()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) NotNull() *Change {
	change.column.nullable, change.nullSet = false, true

	return change
}

// Default sets a literal default, like [Column.Default].
// It replaces a previous default or DropDefault and returns this draft. Invalid
// values remain construction errors even after a later replacement or removal.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("visits").Default(0)
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) Default(value any) *Change {
	change.column.Default(value)
	change.dropDefault = false

	return change
}

// DefaultNow defaults the column to the current timestamp. With a type set it
// must be a timestamp type.
// It replaces a previous default or DropDefault and returns the same draft.
// Without a type, checking the existing column's suitability belongs to the database.
//
// Example:
//
//	op := schema.Alter("events", func(table *schema.Table) {
//		table.Change("recorded_at").DefaultNow()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) DefaultNow() *Change {
	change.column.DefaultNow()
	change.dropDefault = false

	return change
}

// DefaultRaw defaults the column to expression, like [Column.DefaultRaw].
// It replaces a previous default or DropDefault and returns this draft. Empty
// expressions retain a construction error; never pass untrusted SQL text.
//
// Example:
//
//	op := schema.Alter("jobs", func(table *schema.Table) {
//		table.Change("attempts").DefaultRaw("(40 + 2)")
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) DefaultRaw(expression string) *Change {
	change.column.DefaultRaw(expression)
	change.dropDefault = false

	return change
}

// DropDefault removes the column's default.
// It replaces any requested default and returns this draft, without clearing
// errors from invalid earlier values. SQLite cannot compile column changes.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("nickname").DropDefault()
//	})
//	if err := op.Err(); err != nil {
//		return err
//	}
func (change *Change) DropDefault() *Change {
	change.column.defaultKind, change.dropDefault = defaultNone, true

	return change
}

// Using sets the expression that converts existing values when the type
// changes, such as "id::integer". It needs a type method and only Postgres
// supports it: other dialects fail with [arkoren.dev/foundation/query.ErrUnsupported].
// The expression is rendered verbatim: never pass user input.
// Empty expressions retain a construction error; successful calls return this
// draft and do not evaluate the conversion during declaration.
//
// Example:
//
//	op := schema.Alter("users", func(table *schema.Table) {
//		table.Change("id").Integer().Using("id::integer")
//	})
//	statement, err := op.Statements()[0].BuildFor(query.Postgres)
//	if err != nil {
//		return err
//	}
//	_ = statement.SQL()
func (change *Change) Using(expression string) *Change {
	if expression == "" {
		change.column.table.fail(errors.New("using expression cannot be empty"))
	}

	change.using = expression

	return change
}

// validate rejects an empty change before checking type-dependent aspects.
// USING alone remains empty; an untyped DefaultNow remains valid because the
// existing database column's type is deliberately unknown to the declaration.
func (change *Change) validate() error {
	column := &change.column
	typed := column.kind != 0
	editedColumn := typed || change.nullSet
	editedDefault := column.defaultKind != defaultNone || change.dropDefault

	if !editedColumn && !editedDefault {
		return fmt.Errorf("change of column %q sets nothing", column.name)
	}

	if change.using != "" && !typed {
		return fmt.Errorf("change of column %q: Using needs a type", column.name)
	}

	if typed {
		return column.validateTimestampDefault()
	}

	return nil
}
