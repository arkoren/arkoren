// Package database defines the SQL database contract and a typed facade that
// runs published SQL queries into typed rows, with pinned sessions,
// transactions, and telemetry.
//
// Queries are built with arkoren.dev/foundation/query, either with the
// structured [query.Query] or with [query.Raw] for SQL text, and run
// through [Database.Exec], [Database.Select], [Database.Find], and
// [Database.Cursor]. They compile for the dialect a driver reports through
// Driver.Dialect. Named parameters use query.RawNamed through the facade;
// Database.Driver remains available for deliberate raw driver access, which
// bypasses facade compilation, scope routing, and telemetry.
//
// [Database.Paginate] returns an offset page with its total,
// [Database.CursorPaginate] returns a keyset window with opaque cursors, and
// [Database.Each] streams every row in batches. All three take a
// [query.Query] with an ORDER BY, for example one started from
// [query.FromRaw] for raw SQL, and fail with [ErrNoOrder] without one.
// [Database.CursorPaginate] and [Database.Each] read the ordered columns from
// db-tagged fields of the result type ([ErrCursorColumn]) and reject NULL key
// values ([ErrCursorValue]), including lookahead and short final batches.
// Every ordered value in the selected dataset must be non-NULL; checks cannot
// detect NULL rows excluded by a SQL seek. Use non-nullable keys or explicit
// NULL exclusion. The last ORDER BY column must be unique; this is the caller's
// responsibility, not something the facade verifies. All three own the query's
// Limit, and offset pagination also sets Offset. A query that already has either
// fails with ErrPaged.
// [Database.Each] rejects a batch size below one with [ErrInvalidBatchSize].
// Offset and cursor Items fields are eager collection.Slice values for one
// window. Their Map, Filter, and GroupBy operations do not execute SQL or change
// server totals, page limits, or the original cursor boundaries. Page helpers
// detach outer item storage; referenced element objects remain shared.
//
// Every [Database.Exec], [Database.Select], [Database.Find], [Database.Cursor],
// [Database.Lock], and [Database.TryLock] emits a client span and a
// db.client.operation.duration measurement when the selected telemetry
// (default: telemetry.Default) is started. Spans carry the operation and table,
// never bound arguments or error text. Query text is disabled by default.
// Builder.QueryText(true) exports compiled SQL verbatim, including any literals
// and comments; enable it only when those statements are safe to export.
//
// # Sessions
//
// [Database.WithSession] retains one physical connection without beginning a
// transaction. Use its callback context for every protected operation, including
// transactions that must share session-local state or a named lock:
//
//	err := db.WithSession(ctx, func(ctx context.Context) error {
//		return db.WithTransaction(ctx, func(ctx context.Context) error {
//			_, err := db.Exec(ctx, updateCounter)
//			return err
//		})
//	})
//
// Each transaction keeps its own commit. A session error does not undo prior
// autocommit work, and connection loss never reconnects or replays the callback.
// Nested sessions borrow the connection and preserve an enclosing transaction.
// New nontransactional locks belong to their session callback and are released
// when it ends; transaction acquisitions belong to the outermost transaction
// even when first taken in a nested session or transaction. Outer session locks
// survive child transactions. Locks are not reentrant, including on one session.
//
// Session drivers, contexts, and cursors expire when their callback returns:
// later I/O fails with [ErrSessionDone]. Use the callback synchronously from one
// goroutine. Cleanup closes cursors and releases owned locks after cancellation
// or panic, before returning the connection; uncertain cleanup discards it.
// Arbitrary raw SQL session state is not reset. [Database.Driver] and native
// accessors bypass session routing, just as they bypass transactions.
//
// Network raw held locks each consume a connection. Root lock waiters release
// contended connections between attempts, but already-pinned scopes cannot.
// Use [Database.TryLock] and end a contended session when other work needs the
// capacity. Pool acquisition and network I/O still respect the caller context;
// no guarantee prevents raw holders from exhausting all pool connections.
//
// # Transactions
//
// [Database.WithTransaction] passes fn a context that carries the transaction.
// Every operation given that context runs inside it, on any [Database] over
// the same driver, so code that receives only the context joins the
// transaction without knowing about it. A call with a different driver's
// Database, or with any other context, does not join. Use one [Database] per
// driver: Databases join by comparing their driver values, and a driver that
// cannot be compared joins only through the same *Database.
//
//	err := db.WithTransaction(ctx, func(ctx context.Context) error {
//		if _, err := db.Exec(ctx, insertOrder); err != nil {
//			return err
//		}
//
//		return orders.Reserve(ctx, items) // joins; a WithTransaction inside is nested
//	})
//
// A [Database.WithTransaction] inside a transaction is a nested transaction,
// which rolls back only its own work; a driver that cannot nest fails with
// [ErrNestedTransaction]. [Database.Driver] and [Database.DriverAs] always
// return the root driver, never a transaction driver.
//
// [Database.AfterCommit] registers a best-effort callback that runs once the
// outermost transaction commits, in registration order, and is discarded on
// rollback. Hooks of a nested transaction wait for the outermost commit and
// are discarded if the nested transaction rolls back. Without a transaction
// the callback runs immediately. Hooks are lost if the process dies after the
// commit, so use them for cache invalidation, broadcasts, and notifications,
// not for work that must not be lost. Every hook runs; their errors come back
// from the outermost WithTransaction wrapped with [ErrAfterCommit], which
// means the transaction committed and must not be retried.
// Drivers use the same marker for cleanup failures after a confirmed commit;
// committed events and hooks still run. An error returned by the body still
// requests rollback, even if it bears a marker from another transaction.
//
// The context is valid only while fn runs: using it afterwards fails with
// [database/sql.ErrTxDone]. Do not use it from several goroutines at once,
// because a transaction runs on one connection. Code that runs synchronously
// with the context, such as listeners that an event bus invokes before
// returning, joins the transaction too; hand work that must outlive it a
// fresh context.
//
// # Events
//
// With [Builder.Events], the outermost [Database.WithTransaction] publishes
// [TransactionCommitted] ("database.transaction.committed") after the commit
// and before the after-commit hooks, or [TransactionRolledBack]
// ("database.transaction.rolled-back") with the error. Listeners receive
// the context WithTransaction was called with, outside the transaction.
// Any enclosing session remains active. Cleanup failure after a confirmed
// commit publishes the committed event, not a rollback. Nested transactions
// publish nothing.
//
//	db := database.New(driver, func(connection *database.Builder) {
//		connection.Events(app.Events())
//	})
//
// New configures the facade with Builder policies, not a Config input. Policies
// run directly once in order on a fresh draft before system resolution. Nil
// entries panic; expanded nil/empty slices supply no edits. QueryText starts
// disabled and requires an explicit opt-in. Publication detaches settings, not
// the driver, codec, telemetry or events. Construction performs no lifecycle,
// transaction, session or query I/O.
package database
