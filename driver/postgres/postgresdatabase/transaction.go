package postgresdatabase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"arkoren.dev/core/service/database"
)

// transaction tracks one callback's native transaction boundary and cleanup
// ownership. The empty savepoint identifies an outer transaction; borrowed
// sessions stay acquired after it ends. Only a normally returned callback can
// commit, so panic and runtime.Goexit both retain completed's false value.
type transaction struct {
	// scope owns this callback's cursors and transaction-scoped advisory keys.
	scope *scope

	// savepoint names a nested boundary, or is empty for a native transaction.
	savepoint string

	// ownsSession records acquisition by WithTransaction rather than an
	// enclosing session callback, which must keep its physical connection.
	ownsSession bool

	// completed distinguishes a callback return from panic and runtime.Goexit.
	// A returned error still requests rollback through the finalizer's error.
	completed bool
}

// WithTransaction runs fn in a transaction on one retained physical session.
// A session callback starts it on its existing connection; a transaction callback
// starts a savepoint. Nested failure or panic rolls back only nested SQL, while
// fresh managed locks remain owned until the outermost transaction ends.
//
// Callback errors cause rollback, cancellation prevents commit, and an original
// panic propagates unchanged after bounded cleanup. The connection remains held
// through commit or rollback and subsequent session-lock cleanup. A cleanup
// failure after a confirmed outer commit wraps [database.ErrAfterCommit]: the
// data committed, so callers must not retry the transaction. An ambiguous commit
// or callback failure is never newly marked as a confirmed commit.
//
// Callback drivers and cursors expire on return with [sql.ErrTxDone]. Do not use
// them concurrently. Session-lock support requires direct PostgreSQL or session
// pooling, not PgBouncer transaction pooling. Keep nested transactions shallow:
// PostgreSQL savepoint overhead increases with many active subtransactions.
//
// Example:
//
//	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
//		if _, err := tx.Lock(ctx, "invoice-numbers"); err != nil {
//			return err
//		}
//		_, err := tx.Exec(ctx, "UPDATE counters SET value = value + 1 WHERE name = $1", "invoice")
//		return err
//	})
func (driver *Driver) WithTransaction(ctx context.Context, fn func(database.Driver) error) (err error) {
	if err := driver.check(ctx); err != nil {
		return err
	}

	work := transaction{ownsSession: driver.scope == nil}

	if work.ownsSession {
		driver, err = driver.acquire(ctx)

		if err != nil {
			return err
		}
	}

	parent := driver.scope
	work.scope = &scope{
		ctx:     ctx,
		session: parent.session,
		parent:  parent,
		outer:   parent.outer,
		tx:      parent.tx,
	}

	if parent.tx != nil {
		work.scope.depth = parent.depth + 1
		work.savepoint = fmt.Sprintf("arkoren_%d", work.scope.depth)

		_, err = driver.db.Exec(ctx, "SAVEPOINT "+work.savepoint)
	} else {
		work.scope.tx, err = parent.session.conn.Begin(ctx)
		work.scope.outer = work.scope
	}

	if err != nil {
		parent.session.err = fmt.Errorf("begin transaction: %w", err)

		if work.ownsSession {
			endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
			defer cancel()

			return errors.Join(mapError(err), parent.session.release(endCtx))
		}

		return mapError(err)
	}

	wrapper := &Driver{
		db:    work.scope.tx,
		pool:  driver.pool,
		scope: work.scope,
	}

	defer func() {
		panicValue := recover()

		err = work.finish(ctx, err)

		if panicValue != nil {
			panic(panicValue)
		}
	}()

	err = fn(wrapper)
	work.completed = true

	return err
}

// finish expires the callback before draining cursors, then ends its native
// transaction and releases its owned resources under one detached cleanup
// budget. Only a confirmed outer commit marks later cleanup as ErrAfterCommit;
// callback markers, failed commits, and savepoint release never establish it.
func (transaction *transaction) finish(ctx context.Context, err error) error {
	endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	level := transaction.scope
	level.err = sql.ErrTxDone

	err = errors.Join(err, ctx.Err(), level.closeRows(endCtx), level.session.err)

	committed := false

	if transaction.completed && err == nil {
		committed, err = transaction.commit(endCtx)
	} else {
		if rollbackErr := transaction.rollback(endCtx); rollbackErr != nil {
			err = errors.Join(err, rollbackErr)
		}
	}

	cleanupErr := level.cleanup(endCtx)

	if transaction.ownsSession {
		level.parent.err = database.ErrSessionDone
		cleanupErr = errors.Join(cleanupErr, level.session.release(endCtx))
	}

	if committed && cleanupErr != nil {
		cleanupErr = fmt.Errorf("%w: %w", database.ErrAfterCommit, cleanupErr)
	}

	return errors.Join(err, cleanupErr)
}

// commit releases a nested savepoint or commits the native outer transaction.
// Its boolean reports confirmed outer commit only, not nested success. Native
// failure poisons the shared session before resource cleanup can issue more SQL.
func (transaction *transaction) commit(ctx context.Context) (bool, error) {
	var err error
	committed := false

	if transaction.savepoint != "" {
		_, err = transaction.scope.tx.Exec(ctx, "RELEASE SAVEPOINT "+transaction.savepoint)
	} else {
		err = mapError(transaction.scope.tx.Commit(ctx))
		committed = err == nil
	}

	if err != nil {
		transaction.scope.session.err = err
	}

	return committed, err
}

// rollback undoes only this transaction boundary. A successfully rolled-back
// savepoint is released as before; a failed rollback skips release and poisons
// the physical session. The caller joins the contextual cause with body errors.
func (transaction *transaction) rollback(ctx context.Context) error {
	var err error

	if transaction.savepoint != "" {
		_, err = transaction.scope.tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+transaction.savepoint)

		if err == nil {
			_, err = transaction.scope.tx.Exec(ctx, "RELEASE SAVEPOINT "+transaction.savepoint)
		}
	} else {
		err = transaction.scope.tx.Rollback(ctx)
	}

	if err != nil {
		transaction.scope.session.err = fmt.Errorf("rollback transaction: %w", err)

		return transaction.scope.session.err
	}

	return nil
}
