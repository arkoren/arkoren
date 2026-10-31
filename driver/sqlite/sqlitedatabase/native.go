package sqlitedatabase

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
)

// SQL returns the underlying database/sql pool, accepted by any Go SQL
// library. It is always the pool, also when called on the driver of a
// transaction, so queries made through it run outside any transaction.
// Native calls also bypass callback lifetime guards and facade telemetry; keep
// protected work on the callback driver or its facade context instead.
//
// Example:
//
//	stats := driver.SQL().Stats()
//	openConnections := stats.OpenConnections
func (driver *Driver) SQL() *sql.DB {
	return driver.pool
}

// beginTransaction starts the native transaction under the retained sql.Conn's
// Raw boundary, without giving sql.Tx an independent cancellation/rollback
// worker. BEGIN keeps its original context; detaching it could hide blocked I/O.
// Capturing native errors avoids sql.Conn discarding underneath ancestor cursors.
// ConnBeginTx is required, as implemented by the pinned modernc SQLite driver;
// falling back to contextless Begin would lose that cancellation guarantee.
func (scope *scope) beginTransaction(ctx context.Context) error {
	var nativeErr error
	err := scope.conn.Raw(func(native any) error {
		connection, ok := native.(sqldriver.ConnBeginTx)

		if !ok {
			nativeErr = fmt.Errorf("native SQLite connection requires driver.ConnBeginTx: %w", errors.ErrUnsupported)

			return nil
		}

		scope.tx, nativeErr = connection.BeginTx(ctx, sqldriver.TxOptions{})

		return nil
	})

	if err != nil {
		scope.poison(err)
	}

	if errors.Is(nativeErr, sqldriver.ErrBadConn) {
		scope.poison(nativeErr)
	}

	return errors.Join(err, nativeErr)
}

// endTransaction chooses the native outer boundary or the nested savepoint.
// A native completion error poisons the physical owner; it is never handed back
// for reuse under an uncertain transaction. Raw serializes native Tx methods
// without starting an automatic rollback worker or discarding ancestor cursors.
func (scope *scope) endTransaction(ctx context.Context, cause error) (bool, error) {
	if scope.depth > 0 {
		return false, scope.endSavepoint(ctx, cause)
	}

	finish := scope.tx.Commit

	if cause != nil {
		finish = scope.tx.Rollback
	}

	var nativeErr error
	err := scope.conn.Raw(func(any) error {
		nativeErr = finish()

		return nil
	})
	err = errors.Join(err, nativeErr)

	if err != nil {
		scope.poison(err)
	}

	return cause == nil && err == nil, errors.Join(cause, err)
}

// endSavepoint rolls back failed nested work before releasing its savepoint.
// Transaction-owned locks are unaffected; the outer transaction retains them.
// Either failed native command poisons the physical owner before any later SQL.
func (scope *scope) endSavepoint(ctx context.Context, cause error) error {
	savepoint := fmt.Sprintf("arkoren_%d", scope.depth)

	if cause != nil {
		_, err := scope.conn.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint)
		cause = errors.Join(cause, err)

		if err != nil {
			scope.poison(err)
		}
	}

	_, err := scope.conn.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint)

	if err != nil {
		scope.poison(err)
	}

	return errors.Join(cause, err)
}
