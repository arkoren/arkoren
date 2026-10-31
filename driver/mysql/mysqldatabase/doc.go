// Package mysqldatabase provides a MySQL [database.Driver].
//
// New opens a lazy database/sql pool; NewFrom transfers ownership of a
// caller-created pool. Start and Ping check connectivity, and Stop closes the
// root pool. SQL returns that root pool even on a scoped driver: native calls
// bypass transaction routing and retained-session safety.
//
// New and NewFrom accept Builder policies, applied once in order before
// validation. Nil entries panic; nil or empty slice expansions omit edits.
// Completed scalar settings detach from retained drafts. NewFrom applies only
// positive pool limits and takes ownership; Stop closes the supplied pool.
// New recognizes parseTime only in the native DSN parameter list; credential
// text cannot suppress its default, and explicit parseTime=false stays disabled.
// Invalid native DSNs match ErrInvalidConfig without printing supplied values.
// Unwrapped native parse causes remain available but are not safe to log.
//
// Use database.New for typed queries and context-routed scopes:
//
//	driver, err := mysqldatabase.New(dsn, func(connection *mysqldatabase.Builder) {
//		connection.MaxOpenConns(10)
//	})
//	if err != nil {
//		return err
//	}
//	db := database.New(driver)
//	err = db.WithSession(ctx, func(ctx context.Context) error {
//		_, err := db.Exec(ctx, query.Raw("UPDATE counters SET value = value + 1"))
//		return err
//	})
//
// WithSession retains one physical connection without beginning a transaction.
// Its queries, named locks, and explicit transactions never reconnect or replay
// on another session after failure. Nested sessions borrow the same connection
// but expire independently. Scoped cursors close before return; escaped session
// driver and cursor I/O fails with database.ErrSessionDone, while transaction
// handles fail with sql.ErrTxDone. Use each callback synchronously.
//
// GET_LOCK names longer than 64 bytes are hashed. Locks are not reentrant;
// transaction-owned locks survive savepoint rollback and release only after the
// outermost transaction finishes. Cleanup confirms RELEASE_LOCK returned 1 and
// discards uncertain sessions before pool reuse. A cleanup failure after a
// confirmed commit wraps database.ErrAfterCommit; the committed work must not
// be blindly retried. Panics retain their original value after cleanup.
// Only successful callback return authorizes commit; runtime.Goexit rolls back
// outer and nested transaction work rather than treating unwinding as success.
//
// Root lock waiters return contended connections between polling attempts.
// Successful raw locks still reserve one slot each. Sessions and transactions
// already reserve a slot and retain it while waiting; use TryLock and end a
// contended session when other work needs that capacity. No pool-size setting
// makes arbitrary combinations of held raw locks and pinned sessions deadlock-free.
//
// MySQL DDL can implicitly commit, ending transactions and savepoints. Neither
// rollback nor a failed session callback undoes previously committed DDL or
// autocommit statements, and schema changes and bookkeeping are not generally
// atomic. Arbitrary SQL session state is not reset by callback cleanup.
//
// Raw Driver.Select replaces previous destination elements; an empty result
// leaves an empty slice. A scanning failure can leave the successfully scanned
// prefix. The Database.Select facade instead creates a fresh typed result.
package mysqldatabase
