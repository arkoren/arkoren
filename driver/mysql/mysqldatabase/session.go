package mysqldatabase

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"arkoren.dev/core/service/database"

	sqlmysql "github.com/go-sql-driver/mysql"
)

// cleanupTimeout bounds named-lock release independently of the caller's
// cancellation. An unsuccessful release poisons the whole physical session.
const cleanupTimeout = 5 * time.Second

// session retains a physical connection across callback and transaction
// boundaries. Callback drivers are synchronous; the native sql.Tx alone may
// roll back concurrently when its context ends.
type session struct {
	// conn stays checked out until every owned lock and cursor is cleaned up.
	conn *sql.Conn

	// active is the native transaction until completion has been requested.
	// Unlock must use it while non-nil: using Conn on an ErrBadConn path could
	// otherwise wait for the transaction's own retained connection read lock.
	active *sql.Tx

	// locks records one owner per canonical name. Checking it before GET_LOCK
	// prevents MySQL's reentrant lock counts from weakening the contract.
	locks map[string]*scope

	// failure prevents further scoped I/O after uncertain ownership or session
	// state. The connection owner closes its cursors before physical discard;
	// borrowed scopes cannot safely discard while an ancestor cursor is open.
	failure error
}

// scope is one callback lifetime, including a nested borrowed session. It
// expires independently of its parent without returning their shared connection.
type scope struct {
	// session owns the retained connection and all native named-lock state.
	session *session

	// parent preserves enclosing lifetime and cancellation checks.
	parent *scope

	// ctx cancels this callback even when its body ignores cancellation.
	ctx context.Context

	// tx is inherited by borrowed sessions and nested transactions, or nil for
	// autocommit work. Every statement, including GET_LOCK, uses this executor.
	tx *sql.Tx

	// transaction owns locks first acquired anywhere in the outer transaction.
	// It is nil outside transactions, where the acquiring session scope owns them.
	transaction *scope

	// depth changes only for explicit nested transactions, not borrowed sessions.
	depth int

	// done is the sentinel returned after this callback's lifetime ends.
	done error

	// ended prevents escaped drivers and cursors from using a returned connection.
	ended bool

	// cursors contains still-open streaming cursors created by this callback.
	cursors map[*rows]struct{}
}

// check rejects expired lifetimes before inspecting cancellation or touching
// a native executor. A poisoned retained session is never replaced or retried.
func (driver *Driver) check(ctx context.Context) error {
	for level := driver.scope; level != nil; level = level.parent {
		if level.ended {
			return level.done
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	for level := driver.scope; level != nil; level = level.parent {
		if err := level.ctx.Err(); err != nil {
			return err
		}
	}

	if driver.scope != nil {
		return driver.scope.session.failure
	}

	return nil
}

// recordFailure latches non-server execution errors on a retained session.
// Ordinary MySQL statement errors, such as duplicate keys, remain recoverable
// through a savepoint. Transport and cancellation errors cannot safely reuse
// the physical connection, even if the callback handles the immediate error.
func (driver *Driver) recordFailure(err error) {
	if err == nil || driver.scope == nil {
		return
	}

	var serverError *sqlmysql.MySQLError

	if !errors.As(err, &serverError) {
		driver.scope.session.poison(err)
	}
}

// poison retains the first uncertainty so later operations fail closed. Native
// discard waits for the connection-owning callback to close its cursors and
// finish any transaction, avoiding sql.Conn read/write ownership cycles.
func (session *session) poison(err error) {
	if session.failure == nil {
		session.failure = err
	}
}

// WithSession reserves one physical connection for fn without starting a
// transaction. Nested calls borrow it with independent callback lifetimes;
// calls inside a transaction preserve that transaction. No operation reconnects
// or replays fn after session loss. Previously committed SQL is not rolled back.
//
// Outstanding cursors and this callback's nontransactional locks are cleaned up
// before return, even after error, cancellation, or panic. Named-lock cleanup
// uses a fresh bounded context; uncertain sessions are discarded instead of
// pooled. Escaped driver and cursor I/O fails with [database.ErrSessionDone];
// closing an already cleaned-up cursor remains a no-op. Use fn synchronously.
// A reserved session consumes pool capacity even while waiting for a lock; use
// TryLock and end a contended session when other work needs that capacity.
//
// Example:
//
//	err := driver.WithSession(ctx, func(session database.Driver) error {
//		unlock, acquired, err := session.TryLock(ctx, "maintenance")
//		if err != nil || !acquired {
//			return err
//		}
//		defer unlock(context.WithoutCancel(ctx))
//		_, err = session.Exec(ctx, "UPDATE counters SET value = value + 1")
//		return err
//	})
func (driver *Driver) WithSession(ctx context.Context, fn func(database.Driver) error) (err error) {
	if err := driver.check(ctx); err != nil {
		return err
	}

	level := &scope{
		parent: driver.scope,
		ctx:    ctx,
		done:   database.ErrSessionDone,
	}

	if driver.scope == nil {
		conn, err := driver.pool.Conn(ctx)

		if err != nil {
			return err
		}

		level.session = &session{
			conn:  conn,
			locks: make(map[string]*scope),
		}
	} else {
		level.session = driver.scope.session
		level.tx = driver.scope.tx
		level.transaction = driver.scope.transaction
		level.depth = driver.scope.depth
	}

	wrapper := &Driver{
		db:    level.session.conn,
		pool:  driver.pool,
		scope: level,
	}

	if level.tx != nil {
		wrapper.db = level.tx
	}

	defer func() {
		panicValue := recover()
		level.ended = true

		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()

		err = errors.Join(err, ctx.Err(), level.closeRows(), level.releaseLocks(cleanupCtx))

		if level.session.failure != nil && err == nil {
			err = level.session.failure
		}

		if driver.scope == nil {
			// Borrowers may still have ancestor cursors retaining Conn's
			// read lock. Only the owner can close them before physical discard.
			err = errors.Join(err, level.session.finish())
		}

		if panicValue != nil {
			// Cleanup failures cannot replace the original callback panic.
			panic(panicValue)
		}
	}()

	return fn(wrapper)
}

// closeRows closes all streaming cursors before ending a native transaction or
// returning its connection. Closing uses the native cursor, not its expired
// public guard, and any error makes reuse of the session unsafe.
func (scope *scope) closeRows() error {
	var err error

	for cursor := range scope.cursors {
		err = errors.Join(err, cursor.Rows.Close())
		delete(scope.cursors, cursor)
	}

	if err != nil {
		scope.session.poison(err)
	}

	return err
}

// finish returns a known-clean connection or discards an uncertain session.
// Only the connection-owning callback calls it, after closing its cursors and
// requesting native transaction completion. A borrower must only poison state.
// ErrConnDone means an earlier bad-connection path already performed the native
// discard; it is not an additional cleanup failure.
func (session *session) finish() error {
	if session.failure != nil {
		return discardConn(session.conn)
	}

	err := session.conn.Close()

	if errors.Is(err, sql.ErrConnDone) {
		return nil
	}

	return err
}
