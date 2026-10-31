---
title: Query Builder
description: Build parameterized SQL with the query package, run it through the database facade into typed rows, and filter, join, upsert, and scope.
order: 20
---

# Query Builder

## Introduction

To filter records without concatenating user input into SQL, use the `query`
package. You describe a statement in a builder callback, then publish an immutable
query that compiles into SQL text plus a list of arguments. Values never become part of
the SQL; they travel as arguments, and the names of tables and columns are
validated before they are quoted. That protects ordinary builder calls against SQL
injection. Compilation handles shared syntax differences between PostgreSQL,
MySQL, and SQLite; unsupported structured features fail before execution.

`query` is a foundation module with no dependencies, so it only builds
statements. Running them is the job of the `database.Database` facade, which
compiles a published query for the dialect of its driver, executes it, and scans the
result into your structs. The following fragments assume the configured `db`,
`ctx`, `User`, and migrated `users` table from [Database Overview](overview.md).
Import `arkoren.dev/foundation/query`:

```go
users, err := db.Select[User](ctx,
	query.Table("users", func(statement *query.Builder) {
		statement.Select("id", "email", "name")
		statement.Where("active", true)
		statement.OrderBy("email")
		statement.Limit(20)
	}))
```

The snippets use the normal `query` package name and call the mutable draft
`statement`:

```go
import "arkoren.dev/foundation/query"
```

The query above returns up to 20 active users, ordered by email. It selects
columns mapped by `User`; filtering on another column does not require returning
it. The examples below pair builder calls with the SQL they produce. The SQL comes
from `Build`, which compiles for the generic ANSI dialect; through the facade
you get your database's flavor, for example `$1` placeholders on PostgreSQL.

## Selecting rows

`query.Table` starts a query on a table, and a query selects every column
until you say otherwise. `Select` names the columns, `AddSelect` appends
more, and `Distinct` removes duplicate rows:

```go
query.Table("users", func(statement *query.Builder) {
	statement.Select("id", "email")
	statement.Where("active", true)
})
```

```sql
SELECT "id", "email" FROM "users" WHERE "active" = ?
```

A name may be qualified with a table, such as `users.id`. Columns are names,
not expressions, so an aggregate needs `SelectRaw`, which renders its text
verbatim:

```go
query.Table("orders", func(statement *query.Builder) {
	statement.Select("customer_id")
	statement.SelectRaw("SUM(total) AS spent")
	statement.GroupBy("customer_id")
})
```

`SelectRaw` accepts no arguments and does not scan placeholders or process `??`.
Its expression must be trusted, constant SQL. A PostgreSQL JSON operator in a
selection therefore uses a single `?`, unlike an argument-binding raw condition.
[Raw sources and fragments](raw-sql-and-locks.md#raw-sources-and-fragments)
explains that distinction.

### Reusing a base query

Published queries are immutable. Builder settings mutate one draft and return
that receiver. Keep a base `Query` and use `Derive` for independent variations:
callbacks run once, synchronously in order, and cannot alter the base. Omitted
or expanded nil/empty callback slices make no edits; supplied nil callbacks
panic. Publication detaches retained drafts and owned containers, not arbitrary
SQL argument objects. The first invalid setting remains an error even in Derive:

```go
active := query.Table("users", func(statement *query.Builder) {
	statement.Where("active", true)
})

admins := active.Derive(func(statement *query.Builder) {
	statement.Where("role", "admin")
})
recent := active.Derive(func(statement *query.Builder) {
	statement.OrderByDesc("created_at")
	statement.Limit(10)
})
```

## Filtering with where clauses

`Where` takes a column and a value, and compares with `=`. Pass an operator
between them for other comparisons:

```go
query.Table("users", func(statement *query.Builder) {
	statement.Where("age", ">=", 18)
	statement.Where("role", "admin")
})
```

```sql
SELECT * FROM "users" WHERE "age" >= ? AND "role" = ?
```

The operators include `=`, `<>` (also written `!=`), `<`, `<=`, `>`, `>=`, `LIKE`,
and `NOT LIKE`. `IS DISTINCT FROM` and `IS NOT DISTINCT FROM` compile on ANSI,
PostgreSQL, and SQLite. MySQL compilation rejects both with an error matching
`query.ErrUnsupported` and `query.ErrInvalidQuery`, before SQL reaches the
database. PostgreSQL also accepts `ILIKE` and `NOT ILIKE` for case-insensitive
matches; the other dialects reject them when the statement is built. These
operator checks apply to `Where`, `OrWhere`, `Having`, predicate groups,
column comparisons, and join conditions; raw SQL is not checked for operators.

Consecutive `Where` calls join with `AND`. `OrWhere` joins with `OR`, and
every method has an `OrWhere` form. Take care when you mix them, since SQL
evaluates `AND` before `OR`:

```go
query.Table("users", func(statement *query.Builder) {
	statement.Where("age", ">=", 18)
	statement.Where("role", "admin")
	statement.OrWhere("role", "owner")
})
```

```sql
SELECT * FROM "users" WHERE "age" >= ? AND "role" = ? OR "role" = ?
```

This returns every owner, of any age. To control the grouping, use a group.

### Null, lists, and ranges

Equality with `nil` is rewritten to `IS NULL`, and inequality (`<>` or `!=`)
to `IS NOT NULL`. `WhereNull` and `WhereNotNull` say it directly.
`WhereIn` and `WhereNotIn` take a list of
values, and `WhereBetween` an inclusive range:

```go
query.Table("users", func(statement *query.Builder) {
	statement.WhereNull("deleted_at")
	statement.WhereIn("role", "admin", "owner")
})
```

```sql
SELECT * FROM "users" WHERE "deleted_at" IS NULL AND "role" IN (?, ?)
```

An empty list for `WhereIn` renders `0 = 1`, which matches nothing, so a
filter built from an empty slice does not accidentally return every row.
Use an explicit type, such as `statement.WhereIn[int64]("id")`, when no value
supplies it. Empty `WhereNotIn` renders `1 = 1`, which restricts nothing; do not
use that condition alone as a safety check for an update or delete.

### Groups

To require an active account whether its role is admin or owner, group the role
alternatives. `WhereGroup` wraps the combined conditions of its variadic callbacks in parentheses.
`WhereNotGroup` negates the group, and `OrWhereGroup` joins it with `OR`:

```go
query.Table("users", func(statement *query.Builder) {
	statement.Where("active", true)
	statement.WhereGroup(func(predicate *query.PredicateBuilder) {
		predicate.Where("role", "admin").OrWhere("role", "owner")
	})
})
```

```sql
SELECT * FROM "users" WHERE "active" = ? AND ("role" = ? OR "role" = ?)
```

Group callbacks share one restricted `*PredicateBuilder`; it exposes only
predicates and nested groups, and each method returns that draft. A final empty
group is invalid, including when callbacks are omitted. Supplied nil callbacks
panic.

### Comparing columns and raw conditions

`WhereColumn` compares two columns, for example `WhereColumn("updated_at", ">", "created_at")`.
When no method fits, `WhereRaw` adds a condition written in SQL, with `?`
placeholders for its values. For a request-supplied `email`:

```go
query.Table("users", func(statement *query.Builder) {
	statement.WhereRaw("lower(email) = ?", email)
})
```

The text of a raw condition is not validated or escaped, so never build it
from user input. Pass values as arguments. [Raw SQL and locks](raw-sql-and-locks.md)
covers the raw methods in more detail.

## Joining tables

`Join`, `LeftJoin`, and `RightJoin` take the table to join and the columns to
compare. Qualify the columns with their table names, and select the ones you
need:

```go
query.Table("posts", func(statement *query.Builder) {
	statement.Select("posts.id", "posts.title", "users.name")
	statement.Join("users", "users.id", "=", "posts.user_id")
	statement.Where("posts.published", true)
})
```

```sql
SELECT "posts"."id", "posts"."title", "users"."name" FROM "posts" INNER JOIN "users" ON "users"."id" = "posts"."user_id" WHERE "posts"."published" = ?
```

`CrossJoin` joins without a condition. The builder does not support table
aliases, so refer to tables by name. When a query needs aliases or a more
elaborate join, use [raw SQL](raw-sql-and-locks.md).

## Ordering, grouping, and limiting

`OrderBy` sorts ascending and `OrderByDesc` descending; later calls break ties
in earlier ones. `Latest` and `Oldest` are readable names for descending and
ascending order on a timestamp column:

```go
query.Table("posts", func(statement *query.Builder) {
	statement.Latest("created_at")
	statement.Oldest("id")
})
```

```sql
SELECT * FROM "posts" ORDER BY "created_at" DESC, "id" ASC
```

`GroupBy` groups rows, `Having` filters the groups, and `HavingRaw` filters on
an aggregate expression:

```go
query.Table("orders", func(statement *query.Builder) {
	statement.Select("customer_id")
	statement.SelectRaw("SUM(total) AS spent")
	statement.GroupBy("customer_id")
	statement.HavingRaw("SUM(total) > ?", 1000)
})
```

```sql
SELECT "customer_id", SUM(total) AS spent FROM "orders" GROUP BY "customer_id" HAVING SUM(total) > ?
```

`Limit` caps the number of rows and `Offset` skips rows. Each dialect writes
paging its own way, with `LIMIT 10 OFFSET 20` on PostgreSQL, MySQL, and
SQLite, and `OFFSET 20 ROWS FETCH FIRST 10 ROWS ONLY` in ANSI SQL, and the
builder picks the right one. Paging through a whole table is the job of
[Pagination](pagination.md), which sets `Limit` and `Offset` for you.

Omitting `Limit` leaves the result unbounded; `Limit(0)` returns no rows.
`Offset(0)` skips none. Negative limits or offsets are invalid-query errors,
not defaults, and repeated calls replace that setting on the draft.

## Inserting rows

`Insert` turns the builder into an `INSERT` of one row, given as a map from
column to value. Run it with `Exec`, which returns the number of rows
written:

```go
query.Table("users", func(statement *query.Builder) {
	statement.Insert(map[string]any{"email": "a@example.com", "name": "Ann"})
})
```

```sql
INSERT INTO "users" ("email", "name") VALUES (?, ?)
```

`InsertRows` inserts several rows in one statement. Every map must have the
same columns:

```go
query.Table("tags", func(statement *query.Builder) {
	statement.InsertRows(map[string]any{"name": "go"}, map[string]any{"name": "sql"})
})
```

```sql
INSERT INTO "tags" ("name") VALUES (?), (?)
```

To get generated values back, such as an auto-increment ID, add `Returning`
and run the statement with `Find` or `Select`, which read the returned row:

```go
id, err := db.Find[int64](ctx,
	query.Table("users", func(statement *query.Builder) {
		statement.Insert(map[string]any{"email": "a@example.com"})
		statement.Returning("id")
	}))
```

`Returning` works on PostgreSQL and SQLite, and also on `Update` and `Delete`.
MySQL has no `RETURNING`, so building for MySQL fails with an error that
matches `query.ErrUnsupported`. The facade returns no last-insert ID. To retrieve
the inserted row, query an application-owned unique key in the same transaction.

On SQLite, mutation column names must be unqualified: use `"name"`, not
`"users.name"`, in inserted maps, update assignments, computed updates, and
upsert targets or update columns. Qualified table names and read predicates
remain valid. A qualified mutation column fails with `query.ErrUnsupported`.

### Upserts

An **upsert** inserts a row, or updates the existing one when a unique
constraint is violated. Name the constraint's columns with `OnConflict`, then
say what to do:

```go
query.Table("counters", func(statement *query.Builder) {
	statement.Insert(map[string]any{"name": "hits", "value": 1})
	statement.OnConflict("name").DoUpdate("value")
})
```

On PostgreSQL and SQLite this becomes
`ON CONFLICT ("name") DO UPDATE SET "value" = EXCLUDED."value"`, and on MySQL
``ON DUPLICATE KEY UPDATE `value` = VALUES(`value`)``. `DoUpdate` overwrites the
listed columns with the values the insert proposed. `DoNothing` skips a
conflicting row instead, rendered as `ON CONFLICT DO NOTHING` or, on MySQL,
`INSERT IGNORE`, which also downgrades other errors, such as an invalid value,
to warnings. Upserts are not available for the ANSI dialect.

MySQL has no conflict target and ignores the `OnConflict` column list;
PostgreSQL and SQLite require a target for `DoUpdate`. Update columns must be
distinct: listing the same column twice is an invalid-query error.

## Updating and deleting

`Update` takes a map of columns to new values, and `Delete` removes rows. Both
use the filters you have added:

```go
query.Table("users", func(statement *query.Builder) {
	statement.Where("id", 7)
	statement.Update(map[string]any{"active": false, "name": "Ann"})
})
query.Table("sessions", func(statement *query.Builder) {
	statement.Where("expires_at", "<", now)
	statement.Delete()
})
```

```sql
UPDATE "users" SET "active" = ?, "name" = ? WHERE "id" = ?
DELETE FROM "sessions" WHERE "expires_at" < ?
```

To change a column relative to its current value, `Increment` and `Decrement`
render `views = views + ?` without a read first, and `SetRaw` assigns an
expression:

```go
query.Table("posts", func(statement *query.Builder) {
	statement.Where("id", 1)
	statement.Increment("views", 1)
})
```

An `UPDATE` or `DELETE` without a filter would change every row, which is
almost always a mistake, so the builder refuses to build one:

```go
_, err := db.Exec(ctx, query.Table("sessions", func(statement *query.Builder) {
	statement.Delete()
}))
// build query: invalid query: update or delete without where; call Unfiltered to affect every row
```

When you do mean every row, say so with `Unfiltered`:

```go
_, err := db.Exec(ctx, query.Table("sessions", func(statement *query.Builder) {
	statement.Unfiltered()
	statement.Delete()
}))
```

Statements that cannot carry a clause fail instead of silently dropping it. An
`UPDATE` with a join, an `ORDER BY`, or a `LIMIT` is an error.

## Writing structs

Building the map by hand repeats every column of the struct you already have.
`query.Values` builds it from the struct's `db` tags, the same tags the facade
reads when it scans rows into that struct:

```go
type Post struct {
	ID        int64     `db:"id"`
	AuthorID  int64     `db:"author_id"`
	Title     string    `db:"title"`
	CreatedAt time.Time `db:"created_at"`
}

post := Post{AuthorID: 7, Title: "Hello", CreatedAt: time.Now().UTC()}

id, err := db.Find[int64](ctx, query.Table("posts", func(statement *query.Builder) {
	statement.Insert(query.Values(post, query.Except("id")))
	statement.Returning("id")
}))
```

The result is an ordinary map, so it works with `Insert`, `InsertRows`,
`Update`, upserts, and [named raw statements](raw-sql-and-locks.md#named-parameters).
`Except` drops columns, such as a generated ID, and `Only` keeps just the
columns you name, which suits an update that may change only some fields:

```go
query.Table("posts", func(statement *query.Builder) {
	statement.Where("id", post.ID)
	statement.Update(query.Values(post, query.Only("title")))
})
```

```sql
UPDATE "posts" SET "title" = ? WHERE "id" = ?
```

Both ignore names that are not columns of the struct, so check the spelling.
Several `Only` options accumulate their named columns, as do several `Except`
options; `Except` removes columns after `Only` has selected them, regardless of
option order. `Only()` adds no restriction: by itself it keeps every column,
and it does not clear a preceding `Only` selection. An empty `Except()` drops
nothing. Expanded nil and empty column slices have the same empty-option
behavior. If an empty application allowlist must permit no fields, handle that
case before calling `Values`; do not rely on `Only()` to drop every column.

Both options copy their column-name slices when you create the option, not
when you later call `Values`. You can reuse an option without changes to the
original slice altering its selection:

```go
columns := []string{"title"}
editable := query.Only(columns...)
columns[0] = "author_id"

values := query.Values(post, editable)
// values contains only "title", not "author_id".
```

This captures column names, not the struct's field values: each `Values` call
reads the struct you pass to it. A nil embedded struct pointer contributes
typed nil field pointers, which are written as NULL.
Scanning allocates embedded pointers it needs and leaves fields without a
returned column untouched. A tagged embedded struct is one column rather than
flattened. Values are retained as supplied; pointer-valued fields stay pointers.

The columns follow one set of rules, shared with row scanning and
[cursor pagination](pagination.md):

- The column is the part of the `db` tag before its first comma. An empty tag
  name or an untagged exported field uses its
  lowercased name, so `CreatedAt` becomes `createdat`; `db:"-"` skips a field,
  and unexported fields are skipped.
- An untagged embedded struct is flattened into its parent's columns. When both
  have the same column, the outer field wins.
- A declared field type that implements `driver.Valuer`, a type that implements
  `sql.Scanner` directly or through a pointer, and `time.Time` are one column
  each, including embedded fields. Examples include `database.JSON[T]` and
  `sql.Null[T]`. If only `*T` implements `driver.Valuer`, embedding `T` by value
  still maps its fields unless it is also a scanner or `time.Time`; use a `*T`
  field to bind that Valuer pointer. `Values` does not take field addresses to
  manufacture Valuer implementations.

`Values` accepts a struct or a non-nil pointer to one, and panics on anything
else, because passing another type is a programming error rather than a
condition to handle at runtime.

## Reusing conditions with scopes

Conditions such as "active" or "belongs to this tenant" appear in many
queries. A **scope** is a function that adds them, so you write the rule once:

```go
func Active(predicate *query.PredicateBuilder) {
	predicate.Where("active", true)
}

func Tenant(id int64) query.Scope {
	return func(predicate *query.PredicateBuilder) {
		predicate.Where("tenant_id", id)
	}
}
```

`Scope` applies them. The entire nonempty user condition chain becomes one
parenthesized group, followed by each scope in its own group joined with
`AND`. Neither a later `OrWhere` nor an OR inside `WhereRaw` can widen a scope:

```go
query.Table("users", func(statement *query.Builder) {
	statement.Where("role", "admin")
	statement.OrWhere("role", "owner")
	statement.Scope(Active, Tenant(4))
})
```

```sql
SELECT * FROM "users" WHERE ("role" = ? OR "role" = ?) AND ("active" = ?) AND ("tenant_id" = ?)
```

Without the scope, the same role and active conditions written with `Where` would read
`role = ? OR role = ? AND active = ?` and return inactive admins. This makes
scopes the right tool for rules that must always hold, such as tenant
isolation or soft deletes.

Raw conditions still need trusted, constant SQL text with values passed as
bound arguments. Grouping preserves scope boundaries; it does not validate
arbitrary SQL. Without scopes, raw conditions keep their verbatim precedence.

Scopes apply to `Update`, `Delete`, and `Count` as well, and a statement that
has a scope counts as filtered, so it does not need `Unfiltered`. Each callback receives its own
restricted `PredicateBuilder`, so ordering, selection, and mutation methods are
not available. `Scope()` and empty scopes add no restrictions; supplied nil
callbacks panic. `Insert` rejects scopes.

## Subqueries and existence checks

`WhereExists` filters on whether another query returns a row. The inner query
is a published `Query`, usually correlated with the outer one through `WhereColumn`:

```go
members := query.Table("members", func(statement *query.Builder) {
	statement.WhereColumn("members.group_id", "posts.group_id")
	statement.Where("members.user_id", 7)
})

query.Table("posts", func(statement *query.Builder) {
	statement.WhereExists(members)
})
```

```sql
SELECT * FROM "posts" WHERE EXISTS (SELECT * FROM "members" WHERE "members"."group_id" = "posts"."group_id" AND "members"."user_id" = ?)
```

`WhereNotExists` and the `OrWhere` forms work the same way. MySQL rejects an
`UPDATE` or `DELETE` whose subquery reads the table it changes.

For a condition such as `id IN (SELECT ...)`, use `WhereRaw`. To select from a
subquery, start with `query.FromRaw`, described in
[Raw SQL and locks](raw-sql-and-locks.md#raw-sources-and-fragments).

## Counting

`Count` derives the `COUNT(*)` query for the rows a builder selects. It drops
ordering, limits, and locks, and wraps grouped or distinct queries in a
subquery so the count matches the rows:

```go
query.Table("users", func(statement *query.Builder) {
	statement.Where("active", true)
	statement.OrderBy("id")
	statement.Limit(5)
}).Count()
```

```sql
SELECT COUNT(*) FROM "users" WHERE "active" = ?
```

[Pagination](pagination.md) uses it to compute totals.

## Building without executing

The facade builds and runs in one step. To see the SQL, for a test or a log
line, call `Build` or `BuildFor` yourself:

```go
statement, err := query.Table("users", func(statement *query.Builder) {
	statement.Where("active", true)
	statement.Limit(5)
}).BuildFor(query.Postgres)

fmt.Println(statement.SQL())  // SELECT * FROM "users" WHERE "active" = $1 LIMIT 5
fmt.Println(statement.Args()) // [true]
```

`Build` compiles for the ANSI dialect and `BuildFor` for `query.Postgres`,
`query.MySQL`, `query.SQLite`, or `query.ANSI`. To execute through the facade,
pass the published query or `query.Raw` directly to `db.Exec`, `db.Select`,
`db.Find`, or `db.Cursor`. These methods accept `query.Statementer` definitions
and compile them for the driver's dialect; a compiled `query.Statement` only
exposes SQL and arguments for direct execution through a native client.

Methods that can fail, such as one given an invalid column name, do not return
an error. The builder remembers the first mistake and reports it when the
statement is built, so a long chain reads cleanly:

```go
_, err := query.Table("users", func(statement *query.Builder) {
	statement.Where("id = 1 OR 1=1 --", 1)
}).Build()
// invalid query: invalid identifier "id = 1 OR 1=1 --"
```

Every build error matches `query.ErrInvalidQuery` with `errors.Is`, and a
feature the dialect lacks also matches `query.ErrUnsupported`. Through the
facade, the error comes back from `Select`, `Find`, `Exec`, or `Cursor`
before any query reaches the database.

`Inspect` returns a detached description of a published query, such as its table and
ordering. Pagination uses it to read the order of a query.

## How the builder keeps queries safe

Two rules do the work. First, values are always bound as arguments: the SQL
text contains a placeholder, and the database receives the value separately,
so no value can change the shape of a statement. Second, table and column
names are validated against a strict identifier pattern and then quoted for
the dialect, so a name taken from a request, for example a sort column, cannot
inject SQL:

```go
_, err := query.Table("users; DROP TABLE users").Build()
// invalid query: invalid identifier "users; DROP TABLE users"
```

Each identifier part is at most 63 bytes and begins with a letter or underscore,
followed by letters, digits, or underscores; a final `*` is allowed in selects.
Insert column order is sorted for deterministic output. An empty update map is
an error, not a no-op. Typed nil pointers, maps, and slices in comparisons receive
the same SQL NULL handling as nil.

Validation only rejects malformed names; it does not check that a name is
allowed. If a client chooses the sort column, compare it with a list of
columns you permit before you pass it to `OrderBy`.

The exceptions are the methods with `Raw` in their name: only trusted, constant
SQL belongs in their text. `WhereRaw`, `HavingRaw`, and `SetRaw` bind values
through `?` placeholders, and `FromRaw` retains the binding mode of its raw
statement. `SelectRaw` has no argument list and does not scan its expression;
do not put bind parameters in a selection expression. See
[raw SQL placeholder rules](raw-sql-and-locks.md#placeholder-scanning) before
using dialect-specific SQL text.

For every method and error, see
[the `query` API reference](https://pkg.go.dev/arkoren.dev/foundation/query), and for the
facade, [the `database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
