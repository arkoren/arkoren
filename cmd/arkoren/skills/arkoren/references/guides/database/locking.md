---
title: Locking
description: Coordinate concurrent work with row or named locks, with explicit ownership, scope, and lifetime.
summary:
  - Protect read-modify-write operations with transactional row locks.
  - Choose waiting or skip-on-contention behavior for named locks.
  - Retain one connection for session-scoped work and release resources safely.
order: 40
---

# Locking

Use a row lock when concurrent transactions must coordinate changes to the
same records. Use a named lock when cooperating workers must exclude one another
by an application-defined name. Neither mechanism authorizes access to data;
apply your normal access filters as well.

The examples assume a configured `db` and `ctx`. Import `context`,
`arkoren.dev/core/service/database`, and `arkoren.dev/foundation/query` as needed.
Provider lock scope is compared in [Drivers](drivers.md#compatibility-matrix).

## Row Locks

Suppose an inventory reservation first reads a product, then changes its stock.
On PostgreSQL or MySQL 8, build a locking SELECT inside a transaction:

```go
locked := query.Table("products", func(statement *query.Builder) {
	statement.Select("stock")
	statement.Where("id", 7)
	statement.ForUpdate()
})
```

Keep the dependent update in that same callback. Here `reserveStock` is an
already built update for product 7; your application must check the quantity
before issuing it. `quantity` is a validated positive requested quantity and
`ErrInsufficientStock` is an application error:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	stock, err := db.Find[int](ctx, locked)
	if err != nil {
		return err
	}
	if stock < quantity {
		return ErrInsufficientStock
	}
	_, err = db.Exec(ctx, reserveStock)
	return err
})
```

The lock lasts until the transaction ends; reading with
the outer context would bypass it. `ForShare` requests a shared row lock instead.
SQLite and ANSI compilation reject these clauses with `query.ErrUnsupported`.
PostgreSQL also rejects row locks with DISTINCT, GROUP BY, or HAVING.

When workers claim independent rows, `SkipLocked` skips rows another transaction
has locked. `NoWait` requests an immediate database error instead of waiting.
Use either with `ForUpdate` or `ForShare`, not as a substitute for one; the two
contention policies cannot be combined. Skipping
locked rows is suitable for a work queue, not a complete business report.

## Named Locks

For a report that only one worker should generate, skip rather than wait when
another worker already holds the lock. This fragment belongs in a function
with a named `err` result and imports the standard-library `errors` package:

```go
unlock, acquired, err := db.TryLock(ctx, "daily-report")
if err != nil || !acquired {
	return err
}
defer func() {
	err = errors.Join(err, unlock(context.WithoutCancel(ctx)))
}()
```

Run the protected report after acquisition. The defer preserves
release errors independently of caller cancellation; in production, bound cleanup
with an application-chosen timeout. `acquired == false` with nil error means
contention, not failure. Pool acquisition and network I/O can still wait under
`ctx`. Use `Lock` instead if the worker must wait until acquisition or cancellation.

Names must be nonempty; an empty name fails with `database.ErrEmptyLockName`.
Locks are exclusive and not reentrant, even through nested scopes. Unlock is
safe to call more than once. PostgreSQL coordinates within one database; MySQL
coordinates within one server and hashes names longer than 64 bytes. Choose
names that distinguish application databases when sharing a MySQL server.

Inside a transaction, unlock does nothing: the outermost transaction releases
its locks after commit or rollback. A lock first acquired in a nested transaction
survives that nested rollback. A session lock acquired before the transaction
remains owned by the session instead.

## Retained Sessions

When a lock and several independently committed operations must share one
physical connection, use `WithSession`:

```go
err := db.WithSession(ctx, func(ctx context.Context) error {
	_, acquired, err := db.TryLock(ctx, "daily-report")
	if err != nil || !acquired {
		return err
	}
	return generateReport(ctx)
})
```

Here `generateReport` is application code that forwards `ctx` to facade calls.
The session releases the lock and closes outstanding cursors when the callback
returns. It never reconnects or replays the callback after connection loss.
It does not begin a transaction: an error does not undo earlier autocommit work.
Transactions started with its context use the retained connection.

Use sessions synchronously from one goroutine. A callback context or cursor
used after return fails with `database.ErrSessionDone`. Nested sessions borrow
the connection but expire independently and release their own nontransactional
locks. Arbitrary SQL session state is not automatically reset. Native pool
access and `db.Driver()` bypass this protection.

> [!WARNING]
> Every held root named lock can reserve a pool connection. Sessions and
> transactions also reserve a connection while waiting. These combinations can
> exhaust a bounded pool; no pool size makes arbitrary lock nesting safe. Use
> `TryLock` in a short session and leave that session on contention so other work
> can acquire connections. Never reacquire a lock you already hold.

SQLite coordinates within one process. On Linux, macOS, and BSD hosts, file-backed
databases also use sibling lock files for same-host process coordination. Memory
databases, other operating systems, and network filesystems do not provide that
cross-process guarantee. PostgreSQL session locks require a direct or
session-pooled connection, not PgBouncer transaction pooling.

For every scope and cleanup rule, see the
[`database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
