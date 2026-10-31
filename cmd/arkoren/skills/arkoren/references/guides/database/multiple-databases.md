---
title: Multiple Databases
description: Work with several databases in one application, split reads from writes with a replica, tell them apart in telemetry, and migrate each.
order: 80
---

# Multiple Databases

## Introduction

Most applications use one database, and the facade you create for it is all you
need. Some use more: a read replica that takes the reporting queries off the
primary, a separate database for analytics or for a legacy system, or a SQLite
file next to a PostgreSQL server. Arkoren has no special mode for this. Each
database is its own driver with its own `*database.Database` facade, and your
code chooses which one to call.

Keep the boundary explicit: transactions and migrations belong to one database
at a time. A transaction context does not make operations on another driver
part of that transaction. Give each facade a distinct namespace in
[telemetry](query-monitoring.md) so you can also see which database did the work.

## Creating several databases

Build one driver per database, register each driver's lifecycle on the
application, and wrap each driver in its own facade. Because a driver has its own
connection pool, each database can have its own pool limits. Import
`arkoren.dev/driver/postgres/postgresdatabase` and
`arkoren.dev/core/service/database`. Here `cfg.PrimaryDSN` and `cfg.ReplicaDSN`
are your application's configured connection strings:

```go
primary, err := postgresdatabase.New(cfg.PrimaryDSN)
replica, err := postgresdatabase.New(cfg.ReplicaDSN)
```

Check both construction errors before wrapping the drivers. Register each
driver's `Stop` and `Start` with the
[application lifecycle](../application/lifecycle-and-ownership.md), and give
their `Ping` health checks distinct names, such as `database` and `database-replica`.

Group the facades in an application-owned type before passing them to your
repositories:

```go
type Databases struct {
	Primary *database.Database // writes, and reads that must be current
	Replica *database.Database // reads that can be slightly behind
}
```

Wrap each driver in its own facade:

```go
dbs := Databases{
	Primary: database.New(primary),
	Replica: database.New(replica),
}
```

`Start` connects and fails fast on a wrong address or password, `Stop` closes
the pool, and `Ping` backs a [health check](../application/health-and-readiness.md)
that reports each database under its own name. Stop hooks run in reverse order,
so the databases close after anything registered later that still uses them.

Create exactly one facade for each driver and share it. A transaction is joined
by facades over the same driver, so two facades over one driver work, but they
add nothing and invite mistakes. If telemetry is enabled, configure a distinct
[namespace](#telling-databases-apart-in-telemetry) on each facade at construction.

The drivers need not be the same kind. A PostgreSQL primary and a SQLite file
are two drivers with two dialects, and each facade compiles the same query
builder for its own database. The code that picks which database to call stays
the same either way.

## Splitting reads and writes

A **read replica** is a copy of the primary that the database keeps up to date
and that serves reads. It lets you scale reporting or read-heavy endpoints
without loading the primary. A repository holding the `Databases` value above
then picks the right facade for each operation. The application-owned `Posts`
type holds `dbs Databases`; its `Post` result type maps selected table columns
with `db` tags. Import `arkoren.dev/foundation/collection` for the return type
and `arkoren.dev/foundation/query` for statements:

```go
func (posts *Posts) Create(ctx context.Context, post Post) error {
	_, err := posts.dbs.Primary.Exec(ctx, query.Table("posts", func(statement *query.Builder) {
		statement.Insert(map[string]any{"title": post.Title})
	}))

	return err
}

func (posts *Posts) Popular(ctx context.Context) (collection.Slice[Post], error) {
	return posts.dbs.Replica.Select[Post](ctx, query.Table("posts", func(statement *query.Builder) {
		statement.OrderByDesc("views")
		statement.Limit(20)
	}))
}
```

Three rules keep this correct:

- **Writes and transactions use the primary.** A replica is read-only, and a
  database refuses a write on it.
- **Reads that must see a recent write use the primary.** A replica applies
  changes after a delay, the **replication lag**, which is usually milliseconds
  and sometimes minutes. A user who saves a profile and is shown the replica's
  old copy will think the save failed. Use the replica for data that tolerates
  being stale: listings, search, reports, dashboards.
- **Reads inside a transaction use the primary.** The transaction belongs to
  the primary, and the replica cannot see its uncommitted rows.

The third rule follows from the driver boundary. A replica facade does not join a transaction that
the primary started, even when it receives the transaction's context, because
it is a different driver. It quietly reads from the replica and misses the rows
the transaction just wrote:

```go
err := dbs.Primary.WithTransaction(ctx, func(ctx context.Context) error {
	if _, err := dbs.Primary.Exec(ctx, insertion); err != nil {
		return err
	}

	_, err := dbs.Replica.Select[Post](ctx, listing) // does not see the insertion
	return err
})
```

If a function sometimes runs in a transaction and sometimes not, give it the
facade to read from instead of letting it choose, or have it read from the
primary, which sees the transaction and also works without one.

If your cloud provider offers one endpoint for the primary and another for the
replicas, this pattern is all you need. If the replicas sit behind a connection
router that sends statements to the right server for you, you can use a single
facade over that endpoint, and your application cannot control which statements
are stale.

## Telling databases apart in telemetry

With telemetry enabled, operations record spans and duration metrics with attributes that
describe the database. With two databases of the same kind, the attributes
are identical unless you set the namespace, so queries to the primary and to
the replica look the same on a dashboard. `Builder.Namespace` sets the
`db.namespace` attribute, conventionally the database name:

```go
writer := database.New(primary, func(connection *database.Builder) {
	connection.Namespace("primary")
})
reader := database.New(replica, func(connection *database.Builder) {
	connection.Namespace("replica")
})
```

Spans and metrics from the replica now carry `db.namespace = "replica"`, so you can
filter slow queries by database, and the namespace also appears on the
[transaction events](query-monitoring.md#transaction-events) of each
database. The system (`postgresql`, `mysql`, or `sqlite`) is derived from the
driver's dialect; `Builder.System` overrides it for a PostgreSQL-compatible
database that you want to report as something else.

SQL text remains off by default on each facade. A namespace labels operations;
it does not enable query text. If one database opts in with `QueryText(true)`,
its compiled SQL—including literals and comments—is exported verbatim. Review
[query-text privacy](query-monitoring.md#what-is-recorded-and-what-is-not) separately
for each facade, including any facade used by a migrator.

## Transactions across databases

A transaction belongs to one database. Calling `WithTransaction` on the
primary does not make writes to another database atomic with it, and there is no
distributed commit. If the function fails after writing to both databases, the
primary rolls back and the other keeps its change.

When one operation must update two databases, make the first the source of
truth and derive the second from it. Write what must be consistent to the
primary in one transaction, including a row that records the work still owed to
the other database, and apply that work from a queue or a scheduled task, with
an idempotent write. [Transactions](transactions.md#several-databases) shows
the failure in code, and `AfterCommit` there is the lightweight alternative for
effects that tolerate being lost.

## Migrations per database

Each database has its own schema, so each has its own `Migrator`, its own
bookkeeping table, and its own lock. Add the migrations that belong to a
database to the migrator of that database only. Here `analytics` is a separate
analytics facade, and `primaryschema` and `analyticsschema` are your migration
packages:

```go
writer := migrate.New(dbs.Primary)
err := writer.Add(primaryschema.Migrations()...)

reporter := migrate.New(analytics)
err = reporter.Add(analyticsschema.Migrations()...)
```

Run them from the same command, one after the other. Do not run migrations
against a read replica: it receives the primary's schema changes through
replication, and a write on it fails. Migrate the primary and wait for the
replicas to catch up before you deploy code that relies on the change.

If two applications share one database, give each migrator its own table and
lock name, as described in [Migrations](migrations.md#the-bookkeeping-table).
Otherwise each would see the other's migrations as missing.

## Testing

In tests, use a SQLite database for each facade, so the code that chooses between
them runs unchanged. Create and migrate two isolated `:memory:` drivers using
your [database test setup](../testing/integration-tests.md). Given the resulting
`primary` and `replica` facades, pass them to the same application type:

```go
dbs := Databases{Primary: primary, Replica: replica}
```

A test database does not replicate, so a test can copy data to the replica
by hand to simulate it, or write to both. To test your lag handling, leave the
replica empty and assert that the operation that must be current reads from the
primary.

For every function and option, see [the `database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
