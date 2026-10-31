package mysqldatabase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"arkoren.dev/core/service/database"
)

// WithTransaction executes fn in a transaction on one retained connection.
// Commit or rollback completes before its named locks are released and the
// connection is returned. A transaction inside a session uses that session's
// connection; an explicit nested transaction uses a savepoint. Borrowed sessions
// do not introduce savepoints. Escaped callback drivers return [sql.ErrTxDone].
//
// Fresh named locks belong to the outermost transaction, including acquisitions
// in a nested callback that later rolls back or panics. Repeated names are not
// reentrant. Session locks acquired before the transaction remain session-owned.
// Uncertain rollback or unlock discards the physical session. Cleanup failure
// after a confirmed commit wraps [database.ErrAfterCommit]; do not retry that
// committed work. A callback panic is re-raised after rollback and cleanup.
// A callback that exits through runtime.Goexit also rolls back: only an actual
// successful return authorizes commit or release of successful nested work.
//
// MySQL DDL can implicitly commit and remove every savepoint. A rollback cannot
// undo those earlier schema effects or make their bookkeeping atomic.
//
// Example:
//
//	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
//		if _, err := tx.Lock(ctx, "invoice-numbers"); err != nil {
//			return err
//		}
//		_, err := tx.Exec(ctx, "UPDATE counters SET value = value + 1")
//		return err
//	})
func (driver *Driver) WithTransaction(ctx context.Context, fn func(database.Driver) error) (err error) {
	if err := driver.check(ctx); err != nil {
		return err
	}

	if driver.scope != nil && driver.scope.tx != nil {
		return driver.nestedTransaction(ctx, fn)
	}

	level := &scope{
		parent: driver.scope,
		ctx:    ctx,
		done:   sql.ErrTxDone,
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
	}

	tx, err := level.session.conn.BeginTx(ctx, nil)

	if err != nil {
		level.session.poison(err)

		if driver.scope == nil {
			err = errors.Join(err, level.session.finish())
		}

		return err
	}

	level.tx, level.transaction = tx, level
	level.session.active = tx

	wrapper := &Driver{
		db:    tx,
		pool:  driver.pool,
		scope: level,
	}

	return wrapper.executeTransaction(ctx, fn)
}

// executeTransaction owns callback completion and post-boundary cleanup after
// native BEGIN succeeded. The completion guard distinguishes a returned nil
// from panic/Goexit unwinding, where a named result otherwise also stays nil.
func (driver *Driver) executeTransaction(ctx context.Context, fn func(database.Driver) error) (err error) {
	level := driver.scope
	completed := false

	defer func() {
		panicValue := recover()
		level.ended = true

		if !completed {
			err = errors.Join(err, errors.New("transaction callback did not return"))
		}

		err = errors.Join(err, ctx.Err(), level.closeRows(), level.session.failure)
		committed, finishErr := level.endTransaction(err)
		err = finishErr
		level.session.active = nil

		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()

		cleanupErr := level.releaseLocks(cleanupCtx)

		if level.parent == nil {
			cleanupErr = errors.Join(cleanupErr, level.session.finish())
		}

		if committed && cleanupErr != nil {
			cleanupErr = fmt.Errorf("transaction cleanup: %w: %w", database.ErrAfterCommit, cleanupErr)
		}

		err = errors.Join(err, cleanupErr)

		if panicValue != nil {
			// Preserve the original panic even when cleanup also fails.
			panic(panicValue)
		}
	}()

	err = fn(driver)
	completed = true

	return err
}

// endTransaction commits only a successful callback and otherwise rolls back.
// ErrTxDone during rollback can precede native automatic rollback completion,
// so it still poisons the session for disposal by the physical connection owner.
// Failed commits are not marked confirmed and preserve their native causes.
func (scope *scope) endTransaction(cause error) (bool, error) {
	if cause == nil {
		err := scope.tx.Commit()

		if err == nil {
			return true, nil
		}

		scope.session.poison(err)
		cause = err
	}

	if err := scope.tx.Rollback(); err != nil {
		scope.session.poison(err)
		cause = errors.Join(cause, err)
	}

	return false, cause
}

// nestedTransaction brackets one callback with a savepoint on the existing
// native transaction. Callback lifetime and cursors end before savepoint cleanup;
// locks remain owned by the outer transaction even when this level fails.
func (driver *Driver) nestedTransaction(ctx context.Context, fn func(database.Driver) error) (err error) {
	parent := driver.scope
	level := &scope{
		session:     parent.session,
		parent:      parent,
		ctx:         ctx,
		tx:          parent.tx,
		transaction: parent.transaction,
		depth:       parent.depth + 1,
		done:        sql.ErrTxDone,
	}

	savepoint := fmt.Sprintf("arkoren_%d", level.depth)

	if _, err := driver.Exec(ctx, "SAVEPOINT "+savepoint); err != nil {
		return fmt.Errorf("%s: %w", savepoint, err)
	}

	wrapper := &Driver{
		db:    parent.tx,
		pool:  driver.pool,
		scope: level,
	}

	completed := false

	defer func() {
		panicValue := recover()
		level.ended = true

		if !completed {
			err = errors.Join(err, errors.New("transaction callback did not return"))
		}

		err = errors.Join(err, ctx.Err(), level.closeRows())

		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()

		if err != nil {
			if _, rollbackErr := parent.tx.ExecContext(cleanupCtx, "ROLLBACK TO SAVEPOINT "+savepoint); rollbackErr != nil {
				parent.session.poison(rollbackErr)
				err = errors.Join(err, rollbackErr)
			}
		}

		if _, releaseErr := parent.tx.ExecContext(cleanupCtx, "RELEASE SAVEPOINT "+savepoint); releaseErr != nil {
			parent.session.poison(releaseErr)
			err = errors.Join(err, releaseErr)
		}

		if panicValue != nil {
			// The original callback panic takes precedence over cleanup errors.
			panic(panicValue)
		}
	}()

	err = fn(wrapper)
	completed = true

	return err
}
