package sqlitedatabase

import (
	"context"
	"errors"
	"fmt"

	"arkoren.dev/core/service/database"
)

// WithTransaction begins on a retained physical connection, or uses a
// savepoint when an enclosing transaction exists. Nested locks belong to the
// outermost transaction and survive savepoint rollback. Callback drivers and
// cursors expire on return, even while an enclosing transaction remains live.
//
// A body error rolls back. Panic cleanup re-raises the original panic. Cleanup
// errors after a confirmed commit wrap database.ErrAfterCommit, so callers must
// not retry committed work. Session-owned locks predating this transaction are
// not released by its cleanup.
// The driver owns native transaction completion: caller cancellation requests
// synchronous rollback before managed locks are released, rather than relying
// on database/sql's asynchronous sql.Tx cancellation worker. Native BEGIN still
// uses the caller context. A callback that exits via runtime.Goexit rolls back.
// Native Commit and Rollback have no context parameter and can outlive the
// savepoint cleanup budget; this driver does not claim to interrupt those calls.
//
// Example:
//
//	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
//		_, err := tx.Exec(ctx, "UPDATE counters SET value = value + 1")
//		return err
//	})
func (driver *Driver) WithTransaction(ctx context.Context, fn func(tx database.Driver) error) error {
	if err := driver.check(ctx); err != nil {
		return err
	}

	if driver.scope == nil {
		committed := false
		err := driver.WithSession(ctx, func(session database.Driver) error {
			err := session.WithTransaction(ctx, fn)
			committed = err == nil

			return err
		})

		if committed && err != nil {
			return fmt.Errorf("%w: %w", database.ErrAfterCommit, err)
		}

		return err
	}

	level := &scope{
		ctx:    ctx,
		parent: driver.scope,
		conn:   driver.scope.conn,
	}
	enclosing := driver.scope.transaction()

	if enclosing != nil {
		level.tx, level.depth = enclosing.tx, enclosing.depth+1

		if _, err := driver.Exec(ctx, fmt.Sprintf("SAVEPOINT arkoren_%d", level.depth)); err != nil {
			return err
		}
	} else {
		if err := level.beginTransaction(ctx); err != nil {
			return err
		}
	}

	child := &Driver{
		db:    level.conn,
		pool:  driver.pool,
		scope: level,
	}

	return child.executeTransaction(ctx, fn)
}

// executeTransaction closes callback cursors before the boundary, ends its SQL
// transaction/savepoint, and then releases only this level's owned locks. Native
// cleanup bypasses expired public handles but never uses the root pool.
func (driver *Driver) executeTransaction(ctx context.Context, fn func(database.Driver) error) (retErr error) {
	level := driver.scope
	completed := false

	defer func() {
		p := recover()
		level.done = true
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()

		rowsErr := level.closeRows()

		if rowsErr != nil {
			level.poison(rowsErr)
		}

		if !completed {
			retErr = errors.Join(retErr, errors.New("transaction callback did not return"))
		}

		retErr = errors.Join(retErr, ctx.Err(), rowsErr, level.parent.err())
		committed, err := level.endTransaction(endCtx, retErr)
		retErr = err

		if err := level.releaseLocks(endCtx); err != nil {
			level.poison(err)
			retErr = errors.Join(retErr, err)

			if committed {
				retErr = fmt.Errorf("%w: %w", database.ErrAfterCommit, retErr)
			}
		}

		if p != nil {
			panic(p)
		}
	}()

	retErr = fn(driver)
	completed = true

	return retErr
}
