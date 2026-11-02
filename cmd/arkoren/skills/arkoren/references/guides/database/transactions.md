---
title: Transactions
description: Group related operations atomically, including context propagation, nesting, isolation, and retries.
summary:
  - Pass the callback context to keep related writes in one transaction.
  - Understand nested transactions, isolation limits, and retry boundaries.
  - Defer best-effort effects until commit without mistaking hooks for durable work.
order: 30
---

# Transactions

When several writes must succeed together, run them inside `WithTransaction`.
The callback's result decides whether to commit or roll back. The examples
assume a configured `db`, an operation context `ctx`, and the imports
`context`, `arkoren.dev/core/service/database`, and `arkoren.dev/foundation/query`.
Connection setup belongs in [Drivers](drivers.md).

## Related Writes

Suppose changing a user's email must also create an audit record. With existing
`updateEmail` and `insertAudit` statements, execute both using the callback context:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if _, err := db.Exec(ctx, updateEmail); err != nil {
		return err
	}
	_, err := db.Exec(ctx, insertAudit)
	return err
})
```

A nil callback result requests commit. An error requests rollback and remains
matchable through `errors.Is`; a panic rolls back and is re-raised. Cancellation
fails the transaction even if the callback returns nil. Neither an external
HTTP request nor a message already sent becomes reversible by putting it here.

The context carries the transaction, not the `db` variable. Pass it through
repository and service methods. Operations on another facade over the same
driver join it; a facade over another driver does not. Separate database
transactions are not one atomic unit. Calling `db.Driver()` bypasses routing
and facade telemetry, even inside the callback.

Use the callback context synchronously from one goroutine and only until the
callback returns. Later database I/O fails with `sql.ErrTxDone`. Close active
[cursors](batching-and-streaming.md#streaming-rows) before another statement or
a transaction boundary.

## Nested Work

A helper that calls `WithTransaction` with an existing transaction context
starts a nested transaction. The shipped SQLite, PostgreSQL, and MySQL drivers
use savepoints. A custom driver can reject nesting with
`database.ErrNestedTransaction`.

An ordinary nested callback error rolls back the nested work only; the outer
callback decides whether to continue or return that error. Canceling the parent
context still ends the whole transaction, including its nested work.
Successful nested work is still provisional
until the outermost commit. Nested rollback does not release named locks
acquired by that transaction; see [Locking](locking.md#named-locks).

## Isolation and Retries

Transactions use the driver's defaults; `WithTransaction` has no isolation-level
option. A transaction is not automatically a stable snapshot across statements.
For example, PostgreSQL's default read committed isolation can observe changes
committed between two reads. Use [row locks](locking.md#row-locks) when a
read followed by a write must exclude competing updates, and verify your
database's isolation policy for snapshot-sensitive work.

There is no automatic transaction retry. Retry only after classifying the
failure and establishing that repeating the entire callback is safe. A lost
connection or failed commit can leave an uncertain outcome; reconcile it using
application identifiers rather than assuming rollback.

> [!WARNING]
> If cleanup or a hook fails after a confirmed commit, `WithTransaction` wraps
> the failure with `database.ErrAfterCommit`. Do not retry that committed
> transaction: replay can duplicate its work. Check the error's origin rather
> than treating a marker returned by your own callback as proof of commit.

## After Commit

An in-process notification should not announce a change that later rolls back.
Register it with `AfterCommit`. Here `notify` is an application callback with
the type `func(context.Context) error`:

```go
err := db.WithTransaction(ctx, func(ctx context.Context) error {
	if _, err := db.Exec(ctx, updateEmail); err != nil {
		return err
	}
	return db.AfterCommit(ctx, notify)
})
```

Hooks run synchronously in registration order after the outermost commit and
before `WithTransaction` returns. They receive the outer call's context, without
the finished transaction. An enclosing session remains active. A nested hook
waits for the outer commit; nested rollback discards it. Without a matching
transaction, `AfterCommit` calls the function immediately.

Every hook runs even if an earlier one returns an error; those errors are joined
under `ErrAfterCommit`. A hook panic propagates after commit and prevents later
hooks from running. A transaction-body error carrying that marker still requests
rollback; it is not evidence that this transaction committed.

Hooks live only in memory. A crash after commit can lose them. Use them for
best-effort cache invalidation or broadcasts, not work the user is owed. For
durable delivery, write an outbox record in the same transaction and have a
worker process it with duplicate-safe delivery.

For the full contract, see the
[`database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
