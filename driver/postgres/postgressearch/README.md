# Arkoren Search Postgres Driver

`arkoren.dev/driver/postgres/postgressearch` implements `search.Driver` on
PostgreSQL full-text search, in two tables that the application creates by
adding `driver.Migrations()` to its migrator. It never runs DDL.

Run `go get arkoren.dev/driver/postgres` in your application's Go module. With
an existing PostgreSQL facade `db` and application migrator, wrap the database
and register the shipped migrations:

```go
import "arkoren.dev/driver/postgres/postgressearch"

driver, err := postgressearch.NewFrom(db)
err = migrator.Add(driver.Migrations()...)
```

Run the migrator before starting the search driver, then wrap it with
`search.New` and synchronize your registered indexes. The
[PostgreSQL search guide](../../../cmd/arkoren/skills/arkoren/references/guides/search/drivers.md#postgres)
provides consumer-local database setup, migration wiring, indexing, and search
verification. The database pool remains yours; `driver.Stop` does not close it.
Rolling back these migrations deletes all indexed documents. Keep source data
and plan search downtime before a rollback or rebuild.

For multiple independent search stores, set `Builder.Instance` during `NewFrom`.
The empty instance retains the default tables; nonempty instances accept 1–40
bytes of `[a-z0-9_]`, including a leading digit such as `2026_catalog`. Both
migration directions quote generated SQL identifiers without renaming tables,
indexes, advisory locks, or migration groups. Renaming an existing instance
remains unsupported.
`Start` and `Ping` only check table readiness; the application migrator is still
the only DDL execution owner.

See the [driver API reference](https://pkg.go.dev/arkoren.dev/driver/postgres/postgressearch).
