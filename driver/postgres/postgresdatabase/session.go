package postgresdatabase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arkoren.dev/core/service/database"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// cleanupTimeout bounds rollback, cursor draining, unlock, and physical close
// after the caller's context has ended. An exhausted budget discards the session
// instead of returning uncertain lock or transaction state to the pool.
const cleanupTimeout = 5 * time.Second

// session owns one acquired physical connection and all managed advisory keys
// on it. A poison error forbids further application SQL until final disposal;
// nested scopes never replace its connection or independently return it.
type session struct {
	// conn remains acquired through transaction end and advisory-lock cleanup.
	conn *pgxpool.Conn

	// locks indexes server acquisitions by canonical key, not input spelling.
	locks map[int64]*advisoryLock

	// err records uncertain acquisition, rollback, or cleanup ownership.
	err error
}

// scope is one callback's lifetime and resource ownership on a shared session.
// Parents let a borrowed session fail closed when an enclosing callback ends.
// The callback context and drivers must not be used concurrently.
type scope struct {
	// ctx bounds this callback even when an operation supplies a fresh context.
	// Finalization deliberately bypasses it with a detached cleanup context.
	ctx context.Context

	// session is the physical owner shared by every enclosing callback.
	session *session

	// parent is the enclosing callback, or nil for the physical owner's scope.
	parent *scope

	// tx is the current outer pgx transaction, also retained by borrowed sessions.
	tx pgx.Tx

	// outer owns all locks first acquired anywhere inside its transaction.
	outer *scope

	// depth names savepoints; borrowed sessions preserve their enclosing depth.
	depth int

	// err is nil while active, then ErrSessionDone or sql.ErrTxDone on return.
	err error

	// rows contains only outstanding cursors created by this callback.
	rows map[*rows]struct{}
}

// check rejects ended callbacks and poisoned sessions before any pool or native
// operation. It also preserves the contract's context error on valid scopes.
func (driver *Driver) check(ctx context.Context) error {
	if driver.scope != nil {
		if err := driver.scope.check(); err != nil {
			return err
		}
	}

	return ctx.Err()
}

// check follows all callback lifetimes before cancellation and physical
// ownership. An expired child reports its own sentinel even if its parent later
// ends; a live child cannot bypass an enclosing callback's cancellation.
func (scope *scope) check() error {
	for current := scope; current != nil; current = current.parent {
		if current.err != nil {
			return current.err
		}
	}

	for current := scope; current != nil; current = current.parent {
		if err := current.ctx.Err(); err != nil {
			return err
		}
	}

	return scope.session.err
}

// acquire reserves a physical session from the original pool. Only root entry
// points call it; scoped operations never reacquire after connection loss.
func (driver *Driver) acquire(ctx context.Context) (*Driver, error) {
	conn, err := driver.pool.Acquire(ctx)

	if err != nil {
		return nil, fmt.Errorf("acquire PostgreSQL session: %w", err)
	}

	return &Driver{
		db:   conn.Conn(),
		pool: driver.pool,
		scope: &scope{
			ctx: ctx,
			session: &session{
				conn:  conn,
				locks: make(map[int64]*advisoryLock),
			},
		},
	}, nil
}

// WithSession runs fn once on a retained physical connection without beginning
// a transaction. Transactions started by fn use that same connection, and loss
// of it never reconnects or replays the callback. Nested sessions borrow the
// connection but their handles and cursors expire independently when fn returns.
// Inside a transaction, the borrowed session remains in that transaction.
// Canceling this callback's context also rejects operations supplied with fresh
// contexts; an operation cannot resume a canceled enclosing scope.
//
// Callback errors and bounded cleanup errors are joined. A panic is re-raised
// unchanged after cleanup. Outstanding cursors and owned session locks are
// closed before the physical connection can return to the pool. Do not use the
// callback driver concurrently or retain it after return; later calls fail with
// [database.ErrSessionDone]. Native [Driver.DB] access bypasses this scope.
//
// Example:
//
//	err := driver.WithSession(ctx, func(session database.Driver) error {
//		_, err := session.Exec(ctx, "SET application_name = 'migration'")
//		return err
//	})
func (driver *Driver) WithSession(ctx context.Context, fn func(database.Driver) error) (err error) {
	if err := driver.check(ctx); err != nil {
		return err
	}

	root := driver.scope == nil

	if root {
		driver, err = driver.acquire(ctx)

		if err != nil {
			return err
		}
	} else {
		parent := driver.scope
		driver = &Driver{
			db:   driver.db,
			pool: driver.pool,
			scope: &scope{
				ctx:     ctx,
				session: parent.session,
				parent:  parent,
				tx:      parent.tx,
				outer:   parent.outer,
				depth:   parent.depth,
			},
		}
	}

	defer func() {
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()

		driver.scope.err = database.ErrSessionDone
		err = errors.Join(err, ctx.Err(), driver.scope.cleanup(endCtx))

		if root {
			err = errors.Join(err, driver.scope.session.release(endCtx))
		}
	}()

	return fn(driver)
}

// closeRows drains outstanding cursors under the cleanup budget. Query-specific
// cancellation interrupts a blocked drain and makes pgx close the connection;
// no extra goroutine performs SQL or outlives the caller's cleanup.
func (scope *scope) closeRows(ctx context.Context) error {
	var err error

	for cursor := range scope.rows {
		stop := context.AfterFunc(ctx, cursor.cancel)
		cursor.close()
		stop()
		err = errors.Join(err, mapError(cursor.result.Err()))
	}

	if err != nil {
		scope.session.err = errors.Join(scope.session.err, err)
	}

	return err
}

// cleanup closes this callback's cursors and releases only its owned keys.
// Transaction acquisitions belong to outer, not to shorter borrowed sessions.
// Poisoned sessions skip SQL and are closed by the physical owner's release.
func (scope *scope) cleanup(ctx context.Context) error {
	err := scope.closeRows(ctx)

	if scope.session.err == nil {
		for _, lock := range scope.session.locks {
			if lock.owner == scope {
				err = errors.Join(err, lock.release(ctx))
			}

			if scope.session.err != nil {
				break
			}
		}
	}

	return errors.Join(err, scope.session.err)
}

// release returns a healthy, unlocked session to its exact original pool.
// Uncertain cleanup closes the physical connection first; pgxpool then destroys
// the closed resource and accounts for asynchronous network cleanup itself.
func (session *session) release(ctx context.Context) error {
	var err error

	if session.err != nil {
		err = session.conn.Conn().Close(ctx)
	}

	session.conn.Release()

	return err
}
