# SQL schema declarations

Use `arkoren.dev/foundation/query/schema` to declare SQL table changes without
executing them during application construction. Each immutable `Operation`
publishes argument-free statements that compile for the selected query dialect.
The package uses only the standard library and its parent query package.

## Declare and compile a table

Import `arkoren.dev/foundation/query` and `arkoren.dev/foundation/query/schema`.
Declare columns in callbacks, then compile the statements for PostgreSQL:

```go
op := schema.Create("users", func(table *schema.Table) {
	table.ID()
	table.String("email", 255).Unique()
	table.Integer("visits").Default(0)
})
err := op.Err()
var statements []query.Statement
for _, definition := range op.Statements() {
	statement, err := definition.BuildFor(query.Postgres)
	statements = append(statements, statement)
}
```

On success, `statements` contains a CREATE TABLE and a separate CREATE UNIQUE
INDEX. No database connection is opened and no DDL is executed. Pass the
published statements to your database or migration execution boundary; SQL text
is available through `Statement.SQL()`.

## Construction and ownership

`Create`, `CreateIfNotExists`, and `Alter` validate the table name before calling
any declaration callback. Supplied callbacks run directly once, synchronously,
in order on one draft. Nil callback entries and application panics propagate.
An expanded nil or empty callback slice supplies no callbacks; missing columns
or actions remain ordinary construction errors.

Publication detaches the root, actions, children, and owned name slices before
lazy rendering. Retained drafts cannot modify published operations. Drafts are
not concurrency-safe; published operations support concurrent compilation.

The first invalid setting remains an error even after replacement. Literal
defaults accept strings, integers, finite floats, booleans, and nil (SQL NULL),
including named scalar types. Unsupported types, nonfinite floats, and strings
containing NUL fail construction. Column and index identifiers, including
generated names for `DropForeign`, must fit the 63-byte limit.

## Dialect and execution limits

Construction failures match `query.ErrInvalidQuery`. Dialect restrictions are
checked by `BuildFor` and also match `query.ErrUnsupported`. On a failed
construction, `Statements()` returns one failing statement, not partial DDL.
Compile statements before execution when you need to detect dialect failures
before any changes are applied. Transactional DDL support remains
database-specific; `Dialect.TransactionalDDL()` reports the package's guarantee.

SQLite cannot change existing columns or add/drop foreign-key constraints. An
added NOT NULL column needs a non-NULL literal default; `Default(nil)` does not
satisfy that requirement. SQLite REFERENCES targets must be unqualified, even
when the referencing table has a schema. ALTER cannot mark multiple added
columns as separate primary keys; CREATE still supports composite primary keys.

Empty `ReferentialAction` clears an explicit delete/update action and leaves
the database default. Raw column types, defaults, and USING expressions remain
trusted SQL: never interpolate user input. Literal escaping assumes PostgreSQL
`standard_conforming_strings=on` and MySQL without `NO_BACKSLASH_ESCAPES`.

See the [schema API documentation](https://pkg.go.dev/arkoren.dev/foundation/query/schema)
for type mappings, callback examples, and individual operation contracts.
