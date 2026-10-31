package sqlitedatabase

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"time"

	"arkoren.dev/core/service/database"
)

// cleanupTimeout bounds savepoint cleanup independently of the canceled work
// context. SQLite's native transaction rollback and connection close are local.
const cleanupTimeout = 5 * time.Second

// scope owns one callback on a retained connection. A transaction frame carries
// tx; a session frame leaves it nil even when it borrows an enclosing transaction.
type scope struct {
	// ctx bounds the entire callback, even when an operation supplies a fresh
	// context. Cleanup uses its own detached context instead.
	ctx context.Context

	// parent keeps enclosing handle lifetimes and transaction ownership visible.
	parent *scope

	// conn remains checked out until the outermost session's cleanup finishes.
	conn *sql.Conn

	// tx identifies transaction frames and is shared by nested savepoints. Native
	// ownership keeps rollback synchronous instead of racing sql.Tx cancellation.
	// Every native transaction method runs inside conn.Raw's serialization boundary.
	tx sqldriver.Tx

	// depth supplies the savepoint suffix, starting at zero for an outer tx.
	depth int

	// done prevents escaped callback handles from reaching the native executor.
	done bool

	// failure poisons the physical owner after uncertain cleanup. Child frames
	// observe it through parent, so no later callback can resume stale work.
	failure error

	// rows holds streaming cursors to close before ending this callback.
	rows []*rows

	// held contains idempotent releases for locks owned by this callback.
	held []func(context.Context) error
}

// err checks every enclosing callback before native access. A borrowed session
// cannot remain usable after its enclosing transaction or physical owner ends.
func (scope *scope) err() error {
	for current := scope; current != nil; current = current.parent {
		if current.done {
			if current.tx != nil {
				return sql.ErrTxDone
			}

			return database.ErrSessionDone
		}

		if current.failure != nil {
			return current.failure
		}

		if err := current.ctx.Err(); err != nil {
			return err
		}
	}

	return nil
}

// check rejects a completed callback and an ended operation context before any
// SQL, file-lock request, or native connection acquisition can take place.
func (driver *Driver) check(ctx context.Context) error {
	if driver.scope != nil {
		if err := driver.scope.err(); err != nil {
			return errors.Join(err, ctx.Err())
		}
	}

	return ctx.Err()
}

// transaction returns the innermost active transaction frame, skipping borrowed
// sessions. The root and ordinary sessions have no transaction frame.
func (scope *scope) transaction() *scope {
	for current := scope; current != nil; current = current.parent {
		if current.tx != nil {
			return current
		}
	}

	return nil
}

// poison makes uncertain cleanup terminal for this physical session. The
// outermost owner will discard its connection instead of returning it for reuse.
func (scope *scope) poison(err error) {
	owner := scope.connectionOwner()
	owner.failure = errors.Join(owner.failure, err)
}

// connectionOwner finds the callback that acquired this physical connection.
// Only that frame may discard it after all descendant callbacks and cursors end.
func (scope *scope) connectionOwner() *scope {
	for scope.parent != nil {
		scope = scope.parent
	}

	return scope
}

// closeRows closes outstanding streaming results before a transaction boundary
// or sql.Conn.Close, which otherwise waits for abandoned result sets.
func (scope *scope) closeRows() error {
	var err error

	for _, cursor := range scope.rows {
		err = errors.Join(err, cursor.Close())
	}

	scope.rows = nil

	return err
}

// releaseLocks runs owned unlocks in reverse acquisition order after transaction
// completion. Uncertain child cleanup transfers locks to the physical owner so
// they cannot release before that owner discards a possibly active transaction.
func (scope *scope) releaseLocks(ctx context.Context) error {
	owner := scope.connectionOwner()

	if scope != owner && owner.failure != nil {
		owner.held = append(owner.held, scope.held...)
		scope.held = nil

		return owner.failure
	}

	var err error

	for index := len(scope.held) - 1; index >= 0; index-- {
		err = errors.Join(err, scope.held[index](ctx))
	}

	scope.held = nil

	return err
}

// cleanupSession closes cursors before physical release. A poisoned owner's
// connection is discarded before its locks release, whereas a healthy owner's
// locks release first and a failed unlock then forces discard instead of pooling.
// Borrowed scopes transfer uncertain locks without closing ancestor cursors.
func (scope *scope) cleanupSession(ctx context.Context) error {
	err := scope.closeRows()

	if err != nil {
		scope.poison(err)
	}

	if scope.parent != nil {
		return errors.Join(err, scope.releaseLocks(ctx))
	}

	if scope.failure != nil {
		return errors.Join(err, scope.closeConnection(), scope.releaseLocks(ctx))
	}

	locksErr := scope.releaseLocks(ctx)

	if locksErr != nil {
		scope.poison(locksErr)
	}

	return errors.Join(err, locksErr, scope.closeConnection())
}

// closeConnection returns a healthy physical session or discards a poisoned one.
// Only the acquiring callback calls it after closing all retained cursors; Raw's
// ErrBadConn signal cannot deadlock waiting for an ancestor cursor's read lock.
func (scope *scope) closeConnection() error {
	if scope.failure != nil {
		// Raw returns ErrBadConn after the requested discard, or ErrConnDone
		// if an earlier native failure already discarded it. Neither adds a
		// new cleanup failure to the original poisoned-session error.
		_ = scope.conn.Raw(func(any) error {
			return sqldriver.ErrBadConn
		})
	}

	err := scope.conn.Close()

	if errors.Is(err, sql.ErrConnDone) {
		return nil
	}

	return err
}

// WithSession retains one physical SQLite connection without starting a
// transaction. Nested calls borrow it but expire their own callback drivers and
// cursors on return. Errors do not undo committed statements, and failed cleanup
// discards the connection rather than allowing unrelated work to reuse it.
//
// Example:
//
//	err := driver.WithSession(ctx, func(session database.Driver) error {
//		_, err := session.Exec(ctx, "CREATE TEMPORARY TABLE selected (id INTEGER)")
//		return err
//	})
func (driver *Driver) WithSession(ctx context.Context, fn func(session database.Driver) error) (retErr error) {
	if err := driver.check(ctx); err != nil {
		return err
	}

	level := &scope{
		ctx:    ctx,
		parent: driver.scope,
	}
	child := &Driver{
		db:    driver.db,
		pool:  driver.pool,
		scope: level,
	}

	if driver.scope == nil {
		conn, err := driver.pool.Conn(ctx)

		if err != nil {
			return err
		}

		level.conn, child.db = conn, conn
	} else {
		level.conn = driver.scope.conn
	}

	defer func() {
		p := recover()
		level.done = true
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()

		cleanupErr := level.cleanupSession(endCtx)
		retErr = errors.Join(retErr, ctx.Err(), cleanupErr, level.failure)

		if p != nil {
			panic(p)
		}
	}()

	return fn(child)
}
