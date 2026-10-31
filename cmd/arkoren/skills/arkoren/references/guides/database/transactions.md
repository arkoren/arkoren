---
title: Transactions
description: Group statements into one atomic unit with WithTransaction, join it through the context, nest, lock rows, retry, and react to commits.
order: 70
---

# Transactions

## Introduction

A **transaction** groups statements so that they succeed or fail together. If
the second of two updates fails, the first is undone, and no other connection
sees a half-finished state. Moving money between two accounts, creating an
order with its lines, and reserving stock are all transactions.

Arkoren does not hand you a transaction object. `db.WithTransaction` calls your
function with a context that **carries** the transaction, and every database
call made with that context runs inside it. A service function that receives
only a `context.Context` takes part in the transaction without knowing it,
which is what lets independent services compose into one unit of work. The
mechanism is the same on PostgreSQL, MySQL, and SQLite.

## Running code in a transaction

To run statements atomically, pass a function to `WithTransaction` and use the
context it receives. The fragments assume the configured `db` from the
[overview](overview.md), your operation's `context.Context` named `ctx`, and an
existing `accounts` table with integer `id` and `balance` columns. Import
`arkoren.dev/core/service/database` and `arkoren.dev/foundation/query` alongside
the standard-library `context` package:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if _, err := db.Exec(ctx, query.Table("accounts", func(statement *query.Builder) {
		statement.Where("id", 1)
		statement.Decrement("balance", 100)
	})); err != nil {
		return err
	}

	_, err := db.Exec(ctx, query.Table("accounts", func(statement *query.Builder) {
		statement.Where("id", 2)
		statement.Increment("balance", 100)
	}))

	return err
})
```

If the function returns `nil`, the transaction commits. If it returns an error,
the transaction rolls back, and `WithTransaction` returns that same error, so
`errors.Is` still matches it. The `ctx` parameter shadows the outer one on
purpose: inside the function, the name always refers to the transaction.

This baseline moves balances only when both accounts exist; `Exec` does not
treat zero affected rows as an error. For application rules such as insufficient
funds or a missing account, inspect affected-row counts as the next example does.

> [!WARNING]
> A call that receives the outer context instead of the function's runs
> outside the transaction, on its own connection, and commits on its own. If a
> failed transaction leaves some of its rows behind, look for such a call.

## Joining a transaction

Because the transaction travels in the context, code deep in your application
joins it by running with that context. Here a service that knows nothing about
transactions takes part in one. The application-owned `Ledger` type holds
`db *database.Database`, and `ErrInsufficientFunds` is your domain error:

```go
func (ledger *Ledger) Debit(ctx context.Context, id, amount int64) error {
	affected, err := ledger.db.Exec(ctx, query.Table("accounts", func(statement *query.Builder) {
		statement.Where("id", id)
		statement.Where("balance", ">=", amount)
		statement.Decrement("balance", amount)
	}))
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrInsufficientFunds
	}

	return nil
}
```

The caller decides whether `Debit` is atomic with something else:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if err := ledger.Debit(ctx, from, amount); err != nil {
		return err
	}

	return ledger.Credit(ctx, to, amount)
})
```

If `Credit` fails, the debit is rolled back. If you call `Debit` alone, with an
ordinary context, it runs as a single statement on its own. A service never has
to choose between "version for transactions" and "version without".

A context joins a transaction for every `Database` over the **same driver**,
not only the one that started it. Even so, you should create one `Database`
per driver and share it, which is what the application wiring does anyway.
Custom driver values that Go cannot compare join only through the same
`*Database`; shipped drivers use comparable pointer values.
A `Database` over a different driver does not join, since it points at another
database; see [Several databases](#several-databases).

Everything on the facade joins: `Exec`, `Select`, `Find`, `Cursor`,
[`Paginate`, `CursorPaginate`, and `Each`](pagination.md), and `Lock`. The
`db.Driver()` method is the exception. It always returns the root driver, so
raw SQL run through it bypasses the transaction. To run raw SQL inside one, use
`query.Raw` with the facade, as [Raw SQL and locks](raw-sql-and-locks.md)
shows.

### Do not outlive the function

The context is valid only while the function runs. After it returns, the
transaction is over, and using the context fails with `sql.ErrTxDone`. This
catches a goroutine that you started inside the function and that keeps
running after the commit. If work must outlive the transaction, hand it a fresh
context, or use [`AfterCommit`](#running-code-after-commit).

A transaction holds one database connection, so the transaction context must
not be used from several goroutines at once. Statements run one after another.

Likewise, do not make unrelated calls with another context while a transaction
is open if your pool is small. They need a second connection, and the
transaction is holding the only one. An in-memory SQLite database is limited
to one connection for this reason, so in tests, any call inside the function
that is not given the transaction context waits for a connection that never
frees up, until its own context ends.

## Nested transactions

A transaction inside `db.WithSession` uses that retained connection and keeps
its own commit boundary. A session inside a transaction borrows the connection
without starting a new transaction or escaping the existing one. The session
does not make multiple transactions atomic; use an outer transaction for that.

Calling `WithTransaction` with a context that already carries a transaction
starts a **nested transaction**. The nested function joins the outer one, and
it rolls back on its own:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if err := orders.Create(ctx, order); err != nil {
		return err
	}

	// A failed notification must not cancel the order.
	if err := db.WithTransaction(ctx, func(ctx context.Context) error {
		return outbox.Add(ctx, orderPlaced(order))
	}); err != nil {
		logger.WarnContext(ctx, "outbox failed", "error", err)
	}

	return nil
})
```

If the inner function returns an error, only its own work is undone, and the
outer transaction continues. The inner work is final only when the outermost
transaction commits: if the outer function fails afterwards, everything,
including the inner work, is rolled back.

The bundled drivers implement this with savepoints. Savepoints have limits
that come from the databases:

- PostgreSQL caches up to 64 open subtransaction IDs per backend; exceeding that
  increases storage lookups. Read-only subtransactions do not receive these IDs.
  See [PostgreSQL's subtransaction limits](https://www.postgresql.org/docs/current/subxacts.html).
  Keep nesting shallow, and do not open a nested
  transaction per row of a large batch.
- Many MySQL DDL statements commit implicitly, ending the transaction and its
  savepoints; a nested transaction then fails during cleanup. Temporary-table
  exceptions still cannot generally be rolled back. Keep DDL out of application
  transactions; see [MySQL's implicit-commit rules](https://dev.mysql.com/doc/refman/8.4/en/implicit-commit.html).
- Locks first taken inside a nested transaction remain held until the outermost
  transaction ends, including after nested rollback or a recovered panic. This
  also applies to a nested session inside that transaction. A lock already held
  by an enclosing session remains session-owned instead; a repeated `TryLock`
  returns false and does not transfer ownership. Locks are not reentrant.

A driver that cannot nest makes the nested call fail with
`database.ErrNestedTransaction`. The PostgreSQL, MySQL, and SQLite drivers
nest, so you only meet this error with a custom driver.

## Commit, rollback, and panics

These rules decide what happens to the outermost transaction:

| What happens in the function | Result |
| --- | --- |
| It returns `nil` | Commit. |
| It returns an error | Rollback; `WithTransaction` returns the error. |
| It panics | Rollback; the panic continues to the caller. |
| The context ends before the commit | Rollback; `WithTransaction` returns the context's error, even if the function returned `nil`. |
| The commit itself fails | The error is returned. The outcome may be unknown if the connection failed before the acknowledgement arrived. |

Rolling back on a panic means a crashing handler cannot leave a connection with
an open transaction, and your recovery middleware still sees the panic.
Canceling the context, for example when the client disconnects, rolls back the
whole transaction, nested transactions included.

Do not retry an ambiguous commit as though it were a confirmed rollback. The
database may have committed before the connection failed; reconcile using an
application operation ID or another idempotency mechanism first.

An error from the rollback itself, which is rare, is joined with the original
error, so the cause is still there for `errors.Is`.

SQLite completes native rollback synchronously on cancellation before releasing
managed locks. Native Commit/Rollback calls do not take a context and can outlive
the cleanup budget; cancellation is not a promise to interrupt them. An uncertain
physical connection is discarded before its locks release. A SQLite pool passed
to `NewFrom` must provide `driver.ConnBeginTx`; unsupported native profiles fail
with `errors.ErrUnsupported` instead of losing BEGIN cancellation support. See
the [SQLite profile](drivers.md#configuration).

## Running code after commit

Some effects must happen only if the data was committed: invalidating a cache,
broadcasting a message, sending a notification. `db.AfterCommit` registers a
function that runs once the outermost transaction commits.

> [!WARNING]
> Hooks live in memory. A crash after commit, before all hooks finish, can lose
> work without a trace. Use hooks only for work that tolerates being lost.
> Work that must not be lost, such as an email the customer is owed, belongs
> in a row written inside the same transaction and processed by a queue.

Here `store` is your application's existing cache facade and `key` is the
`cache.Key[Product]` used when caching this product (`Product` is your application type):

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if _, err := db.Exec(ctx, query.Table("products", func(statement *query.Builder) {
		statement.Where("id", id)
		statement.Update(changes)
	})); err != nil {
		return err
	}

	return db.AfterCommit(ctx, func(ctx context.Context) error {
		return store.Delete(ctx, key)
	})
})
if errors.Is(err, database.ErrAfterCommit) {
	logger.WarnContext(ctx, "cache invalidation failed", "error", err) // the update committed
}
```

Hooks run in the order registered, synchronously, before `WithTransaction`
returns. A rollback discards them. A hook registered in a nested transaction
waits for the outermost commit, and is discarded if the nested transaction rolls
back. Called without a transaction, `AfterCommit` runs the function at once.

The hook receives the context `WithTransaction` was called with, not the
finished transaction, so database calls inside it run normally.

If a hook fails, the others still run, and `WithTransaction` returns their
errors wrapped with `database.ErrAfterCommit`. The data is committed at that
point, so you must not retry the transaction.

The same marker reports driver lock-cleanup failure after a confirmed commit.
The committed event and hooks still run. Failed or ambiguous commits do not
claim success. An error returned by the transaction body still requests rollback,
even if it contains `ErrAfterCommit` from work on another database.

When the transaction started inside a session, hook database calls remain on
that live session, outside the finished transaction. Passing an expired callback
context to `AfterCommit` fails rather than silently registering a lost hook.

## Isolation

Concurrent transactions may read different versions of the same data. An
**isolation level** controls which changes each transaction can observe.
`WithTransaction` starts transactions with the database's default isolation
level, and the facade has no option to change it: PostgreSQL defaults to read
committed, MySQL to repeatable read, and SQLite allows a single writer at a
time. For most code the default is right, and the tools in the next two sections
(row locks and conditional updates) are more precise than raising the level for
the whole transaction.

If you need serializable isolation on PostgreSQL, set it with the first
statement of the transaction:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if _, err := db.Exec(ctx, query.Raw("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")); err != nil {
		return err
	}

	// ...
	return nil
})
```

Serializable transactions can abort with a serialization failure, so pair
them with a [retry](#retrying-aborted-transactions).

## Locking rows

Reading a value, deciding in Go, and writing it back is a race: another
transaction can change the row between your read and your write. Where possible,
let the database do the check, as `Debit` does above with a conditional update.
When the decision needs the row's data, lock the row with `ForUpdate` for the rest
of the transaction:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	account, err := db.Find[Account](ctx, query.Table("accounts", func(statement *query.Builder) {
		statement.Where("id", id)
		statement.ForUpdate()
	}))
	if err != nil {
		return err
	}
	if account.Balance < amount {
		return ErrInsufficientFunds
	}

	_, err = db.Exec(ctx, query.Table("accounts", func(statement *query.Builder) {
		statement.Where("id", id)
		statement.Decrement("balance", amount)
	}))

	return err
})
```

On PostgreSQL this compiles to `SELECT * FROM "accounts" WHERE "id" = $1 FOR UPDATE`.
Another transaction that tries to lock or change the row waits until this one
ends. `ForShare` allows other readers to share the lock, `NoWait` fails at once
instead of waiting, and `SkipLocked` passes over locked rows, which is how a
worker takes the next unclaimed job. Run this query with the transaction's
`ctx`, and update the job's state before committing:

```go
job, err := db.Find[Job](ctx, query.Table("jobs", func(statement *query.Builder) {
	statement.Where("state", "pending")
	statement.OrderBy("id")
	statement.Limit(1)
	statement.ForUpdate()
	statement.SkipLocked()
}))
```

Three rules keep row locks safe. Use them only inside a transaction, because
outside one the lock is released as soon as the statement finishes. Lock rows
in a consistent order, for example by ascending id, since two transactions
that lock the same rows in opposite orders deadlock. And keep the transaction
short, since every lock is held until it ends.

PostgreSQL and MySQL support row locks. SQLite has none, so building
`ForUpdate` for SQLite fails with `query.ErrUnsupported`; SQLite serializes
writers instead. Code that locks rows therefore needs a PostgreSQL or MySQL
database in its tests, or a branch on `db.Dialect()`. Named locks that are not
tied to a row are in [Raw SQL and locks](raw-sql-and-locks.md).

## Retrying aborted transactions

Databases abort a transaction when they detect a deadlock or, at higher
isolation levels, a serialization failure. Nothing is wrong with your code: the
right response is to run the whole transaction again. Arkoren does not retry for
you, because only your code knows whether the function is safe to repeat, but
a small helper does it:

```go
func WithRetry(ctx context.Context, db *database.Database, fn func(ctx context.Context) error) error {
	var err error

	for attempt := 1; attempt <= 3; attempt++ {
		err = db.WithTransaction(ctx, fn)
		if err == nil || errors.Is(err, database.ErrAfterCommit) || !retryable(err) {
			return err
		}
		if attempt == 3 {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * 50 * time.Millisecond):
		}
	}

	return err
}
```

What counts as retryable depends on the database. With PostgreSQL through the
bundled driver, the error is a `*pgconn.PgError` whose code is `40001` for a
serialization failure or `40P01` for a deadlock. Import
`github.com/jackc/pgx/v5/pgconn` for that native error type. The helper above uses
the standard-library `errors` and `time` packages and makes at most three
attempts, waiting 50 milliseconds and then 100 milliseconds between them:

```go
func retryable(err error) bool {
	var failure *pgconn.PgError

	return errors.As(err, &failure) && (failure.Code == "40001" || failure.Code == "40P01")
}
```

Retry only the outermost transaction: an aborted transaction cannot continue,
so retrying a nested one does not help. The function must not have effects
outside the database that you cannot repeat. Hooks registered with `AfterCommit`
are discarded on a rollback, so they are safe, but an email sent inside the
function is not. Errors from after-commit hooks or cleanup after a confirmed
commit carry `database.ErrAfterCommit`; the helper conservatively returns any
error bearing that marker without retrying. A marker supplied by the body is
not proof that the current transaction committed. Determine which operation
produced it before recovery, even if its underlying error would be retryable.

## Several databases

A transaction belongs to one database. If you have two `Database` values over
different drivers, `WithTransaction` on one does not make the other
transactional:

```go
err := primary.WithTransaction(ctx, func(ctx context.Context) error {
	if _, err := primary.Exec(ctx, insertion); err != nil { // in the transaction
		return err
	}

	_, err := reporting.Exec(ctx, projection) // autocommit: not part of it

	return err
})
```

If the function fails after the second statement, `primary` rolls back and
`reporting` keeps its row. The two databases are never atomic together. Order
the work so that a failure leaves a state you can repair, for example by
writing to the less important database last, and make the second write
idempotent. A database queue or an outbox row in the main database is the
durable way to feed the second one. [Multiple databases](multiple-databases.md)
covers patterns for this.

You can nest the transactions of two databases, and each context keeps joining
the right one, but this still gives you two independent commits.

## Transaction events

With an event bus, the database publishes `database.TransactionCommitted` after
an outermost transaction commits and `database.TransactionRolledBack` when it
ends in an error. Nested transactions publish nothing. [Events and
telemetry](query-monitoring.md#transaction-events) shows how to listen.

## Testing

A common test runs the code under test and asserts that a failure leaves nothing
behind. SQLite in memory is enough for this, because rollback behaves the same.
In your test fixture, create accounts 1 and 2 with balances of 100 and 50, and
build `ledger` over that same `db`. Then assert the failed transfer and unchanged
balance:

```go
err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
	if err := ledger.Debit(ctx, 1, 10); err != nil {
		return err
	}

	return ledger.Debit(ctx, 2, 1000) // insufficient funds
})
if !errors.Is(err, ErrInsufficientFunds) {
	t.Fatalf("err = %v, want ErrInsufficientFunds", err)
}

account, err := db.Find[Account](t.Context(), query.Table("accounts", func(statement *query.Builder) {
	statement.Where("id", 1)
}))
if err != nil {
	t.Fatal(err)
}
if account.Balance != 100 {
	t.Errorf("balance = %d, want 100: the first debit was not rolled back", account.Balance)
}
```

Remember the single connection of an in-memory database: do not read with a
second, non-transaction context from inside the function. Tests of row locks and
of concurrent transactions need a real PostgreSQL or MySQL database; see
[Integration tests](../testing/integration-tests.md).

For every function and option, see [the `database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
