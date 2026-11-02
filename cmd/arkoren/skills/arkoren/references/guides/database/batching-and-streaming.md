---
title: Batching and Streaming
description: Process large datasets in batches or as a stream without loading every row into memory.
summary:
  - Walk large tables in bounded keyset batches with Each.
  - Stream a single query with Cursor and handle iteration failures.
  - Account for partial work, changing data, and cursor cleanup.
order: 50
---

# Batching and Streaming

An export or background job often needs every matching row, not one page for
a user. `Each` reads successive bounded batches; `Cursor` streams one query.
Both return fallible lazy collections: work starts when you iterate, and errors
arrive during iteration. For user-facing result windows, use
[Pagination](../queries/pagination.md) instead.

Assume a configured `db`, an operation context `ctx`, and imports from
`arkoren.dev/foundation/query`. The `users` table has non-null `id` and `email`
columns, and `User` has matching `db` fields as in [Overview](overview.md#reading-data).

## Bounded Batches

For a large user export, order by a unique non-null key so the next batch can
start after the last row already processed:

```go
users := query.Table("users", func(statement *query.Builder) {
	statement.Select("id", "email")
	statement.OrderBy("id")
})
```

Read at most 100 rows per database query, then process them individually:

```go
for user, err := range db.Each[User](ctx, users, 100).Iter() {
	if err != nil {
		return err
	}
	if err := writeUser(user); err != nil {
		return err
	}
}
```

`writeUser` is your export sink. `Each` buffers one batch, checks cancellation
between buffered rows, and seeks after the final ordering values for the next
query. It does not revisit inserts behind that position. Stopping the loop
prevents later batches from being fetched; consuming the iterator again starts
another traversal, not a resume.

The query must be a SELECT with an order and without `Limit` or `Offset`.
Size below one yields `database.ErrInvalidBatchSize`. Missing order yields
`database.ErrNoOrder`; preset paging yields `database.ErrPaged`. Every ordered
column must be selected into a matching result field. End compound ordering
with a unique tiebreaker; uniqueness is your responsibility, not a runtime check.

All ordering values in the selected dataset must be non-NULL. Fetched NULL keys
fail with `database.ErrCursorValue`, but SQL comparisons can exclude NULL rows
before the application can detect them. Use non-null keys or explicitly filter
out NULLs. Keep ordering keys stable while the job runs; changing them can move
rows across the current position.

Each batch is a separate query, not one snapshot. A consistent view requires
database isolation that preserves a snapshot across statements; a transaction
alone, including PostgreSQL's default read committed transaction, is insufficient.

## Streaming Rows

If one query should remain open while rows are consumed, use `Cursor` with the
same `users` statement:

```go
for user, err := range db.Cursor[User](ctx, users).Iter() {
	if err != nil {
		return err
	}
	if err := writeUser(user); err != nil {
		return err
	}
}
```

This avoids collecting the complete result in application memory, but holds
the database cursor and its connection during consumption. Slow sinks prolong
that lifetime. Do not leave the iterator unconsumed inside a transaction or
session and then use it after the callback returns. Close or finish streaming
before another statement or transaction boundary on the same connection.

Exhaustion, scan failure, and an early `break` close the rows. On exhaustion or
failure, close errors are joined with the iteration error. After an early stop,
cleanup errors can only reach telemetry because the iterator cannot yield again.
If application code must receive early-close failures, use the driver's `Query`
and handle `Rows.Close` explicitly; raw driver access also bypasses facade
scope routing and telemetry, so choose the correct scoped driver deliberately.

## Partial Work

A later read, scan, or sink failure does not undo output already written.
Design exports for incomplete-file cleanup and background effects for
duplicate-safe recovery. Neither lazy iterator is a persisted checkpoint.
Do not turn a fallible stream into a success-only iterator that hides its error;
see [Fallible Collections](../collections/fallible-collections.md).

For the iteration contracts, see the
[`database` API reference](https://pkg.go.dev/arkoren.dev/core/service/database).
