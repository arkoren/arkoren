// Package postgresdatabase provides a PostgreSQL [database.Driver] using pgxpool.
// Construct a driver with [New], or transfer an existing pool to [NewFrom], then
// start it, wrap it with [database.New], and stop it at application shutdown.
// New applies Builder policies once in order before validation; nil entries
// panic and nil or empty slice expansions omit edits. Completed scalar settings
// detach from retained drafts. NewFrom accepts no policies: set native pool
// limits before transferring ownership. Stop still closes the supplied pool.
// To keep construction free of network I/O, New rejects nonzero pool_min_conns
// and pool_min_idle_conns. Create pools needing eager minima at the application's
// startup boundary and transfer them with NewFrom. Native health-check periods
// must be positive. Invalid DSN messages omit supplied values while retaining
// native parse causes for explicit inspection; unwrapped causes are not redacted.
//
//	driver, err := postgresdatabase.New(dsn, func(connection *postgresdatabase.Builder) {
//		connection.MaxOpenConns(10)
//	})
//	if err != nil {
//		return err
//	}
//	db := database.New(driver)
//
// # Retained sessions and transactions
//
// [Driver.WithSession] pins one physical connection without starting a
// transaction. Every operation on its callback driver uses that connection;
// connection loss fails subsequent SQL instead of reconnecting or replaying
// work. Nested sessions borrow it with independently expiring handles and
// cursors. [Driver.WithTransaction] starts a transaction on the retained session,
// or reserves one when called on the root driver. Nested transactions use
// savepoints, and a session borrowed inside a transaction stays inside it.
//
// Example:
//
//	err := driver.WithSession(ctx, func(session database.Driver) error {
//		return session.WithTransaction(ctx, func(tx database.Driver) error {
//			_, err := tx.Exec(ctx, "INSERT INTO audit_log(message) VALUES ($1)", "started")
//			return err
//		})
//	})
//
// Callback drivers must not be used concurrently or after return. Ended session
// calls and cursors fail with [database.ErrSessionDone]; ended transactions fail
// with [database/sql.ErrTxDone]. Native [Driver.DB] calls always bypass scopes.
// Canceling an enclosing callback also prevents operations with fresh contexts.
// Cleanup closes outstanding cursors, finalizes transactions, and releases
// managed locks before returning the connection. It uses a detached five-second
// budget and discards uncertain sessions. A cleanup error after a confirmed
// outer commit wraps [database.ErrAfterCommit]; do not retry committed work.
//
// # Advisory locks and pooling
//
// [Driver.Lock] and [Driver.TryLock] use session advisory locks. Locks are not
// reentrant, including across managed scopes sharing a connection. Fresh locks
// inside transactions survive all nested rollbacks and panics until the
// outermost transaction ends; their explicit unlock does nothing. Session locks
// outside transactions end at explicit unlock or their owning callback's end.
// An enclosing session's locks survive its child transactions.
//
// Connect directly or use session pooling. PgBouncer transaction pooling cannot
// preserve these session locks, even when a client connection stays acquired.
// Root blocking-lock polling releases refused attempts between tries, but every
// successfully held root lock still consumes a pool slot. Root queries require
// capacity beyond those held locks. A pinned waiter can still exhaust capacity
// when mixed with raw root holders that need another connection: try a lock in a
// short session and leave the session on refusal instead. Raw advisory-lock or
// transaction-control SQL is outside managed ownership and cleanup guarantees.
//
// Raw Driver.Select replaces previous destination elements; empty results leave
// the slice empty. Scanning errors can leave the successfully read prefix. The
// typed Database.Select facade creates a new result rather than reusing a slice.
package postgresdatabase
