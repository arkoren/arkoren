// Package schema builds DDL: creating, altering, renaming, and dropping
// tables, with their columns, keys, indexes, and foreign keys, for the
// dialects of [arkoren.dev/foundation/query].
//
// # Operations
//
// [Create], [CreateIfNotExists], [Alter], [Rename], [Drop], and [DropIfExists]
// return an [Operation], an immutable value that compiles to one or more
// argument-free statements. [Create], [CreateIfNotExists], and [Alter] accept
// zero or more callbacks that define the table on the same [*Table]:
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
//	for _, statement := range op.Statements() {
//		compiled, err := statement.BuildFor(query.Postgres)
//		if err != nil {
//			return err
//		}
//		fmt.Println(compiled.SQL())
//	}
//
// Supplied callbacks run directly, once, synchronously in argument order, after
// the table name is validated. Invalid names return an error without invoking
// callbacks. A nil callback or application panic propagates and stops the
// sequence; omit callbacks instead of supplying nil to request no declarations.
// An expanded nil or empty callback slice supplies no callbacks.
//
// The combined declaration is copied and validated after all callbacks. Omitting
// them or leaving a CREATE without columns or an ALTER without actions is an
// ordinary construction error. Retaining and changing the Table or its Column,
// Change, or Foreign children afterwards cannot change the operation. Builders
// are not safe for concurrent mutation. Arbitrary Default values are not
// recursively copied.
// Literal defaults accept scalar strings, integers, finite floats, booleans,
// and nil. Unsupported types, nonfinite floats, and NUL-bearing strings retain
// construction errors immediately, even if a later callback replaces the default.
//
// Column methods return a [*Column] for modifiers; the first problem (an unknown
// column in an index, a modifier used where it does not apply, an alter-only
// method in Create, an empty declaration, an invalid name) is kept and reported
// by [Operation.Err]. Every statement of an operation with an error fails with
// that error, so it never runs partially. Dialect limits, such as SQLite
// changing a column, surface when a statement is compiled and match
// [query.ErrUnsupported]; every error also matches [query.ErrInvalidQuery].
//
// # Statements
//
// [Operation.Statements] returns one [query.Statementer] per statement, and
// the count never depends on the dialect. Create emits a CREATE TABLE with the
// columns in written order and the primary key and foreign keys inline, then
// one CREATE [UNIQUE] INDEX per index. Alter emits one statement per call, in
// written order. Unique keys are always unique indexes, never inline
// constraints, so [Table.DropUnique] is a DROP INDEX on every dialect and a
// unique column can be added on SQLite.
//
// Columns are NOT NULL unless [Column.Nullable] is called. Identifiers must be
// unquoted SQL names of at most 63 bytes. Generated names are
// <table>_<columns>_key (unique index), _index (plain index), <table>_<column>_fkey,
// and <table>_pkey; a schema-qualified table contributes its schema, as in
// s_t_col_key, and a generated name longer than 63 bytes is an error.
// [Table.DropIndex], [Table.DropUnique], and [Table.DropForeign] take the same
// columns as their create and derive the name.
//
// # Changing columns
//
// [Table.Change] returns a [*Change] that changes only what is called: a type,
// nullability, and a default each become their own ALTER COLUMN clause on
// Postgres and ANSI. MySQL changes a default alone with ALTER COLUMN, but any
// type or nullability change is MODIFY COLUMN, which restates the whole column:
// it needs a type and Nullable or NotNull together, and drops a default that is
// not restated. SQLite cannot change columns.
//
// # Safety
//
// DDL cannot bind arguments, so defaults are rendered into the SQL: strings
// have single quotes doubled and NUL rejected, and MySQL backslashes are
// doubled. That is correct only under standard server modes
// (standard_conforming_strings=on on PostgreSQL, no NO_BACKSLASH_ESCAPES on
// MySQL). [Table.Column], [Column.DefaultRaw], [Change.Type], and
// [Change.Using] are verbatim: never pass user input. Statements go through
// [query.Raw], so a "?" outside quotes in a raw expression is a placeholder
// and fails; a "?" inside a quoted default is fine.
//
// MySQL parenthesizes defaults on text, blob, and json columns (MySQL 8.0.13
// or later) and has no time zone aware timestamp: TimestampTZ renders
// datetime(6), so store UTC. SQLite stores SmallInteger as INTEGER and Char
// as TEXT, and compares timestamps as text. IF NOT EXISTS is unavailable on
// ANSI for tables and on ANSI and MySQL for indexes. Use
// [query.Dialect.TransactionalDDL] to learn whether DDL rolls back.
// SQLite REFERENCES targets must be unqualified and resolve in the referencing
// table's schema; qualified targets fail compilation with [query.ErrUnsupported].
package schema
