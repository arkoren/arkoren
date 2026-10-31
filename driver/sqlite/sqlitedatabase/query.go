package sqlitedatabase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
)

// executor is implemented by [sql.DB], [sql.Conn], and [sql.Tx], allowing
// scoped operations to reuse scanning without acquiring another connection.
type executor interface {
	// ExecContext executes a statement without returning rows.
	ExecContext(ctx context.Context, statement string, args ...any) (sql.Result, error)

	// QueryContext executes a statement that returns rows.
	QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error)
}

// rows adapts [sql.Rows] to [database.Rows], scanning with [query.Targets].
type rows struct {
	// Rows is the underlying cursor.
	*sql.Rows

	// columns are the result columns, read on the first Scan.
	columns []string

	// owner expires the cursor with its callback, or is nil for pool queries.
	owner *scope

	// closeOnce serializes explicit and automatic release, including concurrent
	// Close calls on root cursors, whose native sql.Rows supports that use.
	closeOnce sync.Once

	// closeErr preserves the first release result for idempotent Close calls.
	closeErr error
}

var _ database.Rows = (*rows)(nil)

// Exec executes a statement that modifies data and returns the number of rows
// it affected. Callback drivers use only their retained executor; an ended
// callback or canceled enclosing scope fails before native SQL is sent.
//
// Example:
//
//	changed, err := driver.Exec(ctx, "UPDATE counters SET value = value + 1 WHERE id = ?", id)
func (driver *Driver) Exec(ctx context.Context, statement string, args ...any) (int64, error) {
	if err := driver.check(ctx); err != nil {
		return 0, err
	}

	result, err := driver.db.ExecContext(ctx, statement, args...)

	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// Select scans every result row into dest, a pointer to a slice of structs,
// of pointers to structs, or of single-column values. Struct columns follow
// [query.Targets]. A successful query replaces existing elements; no rows leaves
// dest empty. A scan failure can leave the successfully scanned prefix. Invalid
// destinations and expired callbacks fail before any SQL is sent. Cursor-close
// failures are joined with scan/iteration errors and poison a retained session.
//
// Example:
//
//	ids := []int{99}
//	err := driver.Select(ctx, "SELECT id FROM reports ORDER BY id", &ids)
func (driver *Driver) Select(ctx context.Context, statement string, dest any, args ...any) (err error) {
	if err := driver.check(ctx); err != nil {
		return err
	}

	target := reflect.ValueOf(dest)

	if target.Kind() != reflect.Pointer || target.IsNil() || target.Elem().Kind() != reflect.Slice {
		return fmt.Errorf("select destination must be a non-nil pointer to a slice, got %T", dest)
	}

	result, err := driver.db.QueryContext(ctx, statement, args...)

	if err != nil {
		return err
	}

	defer func() {
		err = errors.Join(err, driver.closeResult(result))
	}()

	return scanRows(result, target.Elem())
}

// scanRows replaces a validated destination with the current native result,
// reusing its capacity. Pointer elements get independent pointees. If scanning
// fails, the destination retains only the prefix already scanned successfully.
func scanRows(result *sql.Rows, slice reflect.Value) error {
	columns, err := result.Columns()

	if err != nil {
		return err
	}

	itemType := slice.Type().Elem()
	pointers := itemType.Kind() == reflect.Pointer

	if pointers {
		itemType = itemType.Elem()
	}

	slice.SetLen(0)

	for result.Next() {
		item := reflect.New(itemType)

		targets, err := query.Targets(item.Interface(), columns)

		if err != nil {
			return err
		}

		if err := result.Scan(targets...); err != nil {
			return err
		}

		if pointers {
			slice.Set(reflect.Append(slice, item))
		} else {
			slice.Set(reflect.Append(slice, item.Elem()))
		}
	}

	return result.Err()
}

// Query executes a statement and returns a cursor over its rows. Close it when
// done because it retains connection capacity. Callback cursors are also closed
// automatically on return and reject later iteration with the scope's sentinel.
//
// Example:
//
//	rows, err := driver.Query(ctx, "SELECT value FROM counters ORDER BY id")
//	if err != nil {
//		return err
//	}
//	defer rows.Close()
//	for rows.Next() {
//		var value int
//		if err := rows.Scan(&value); err != nil {
//			return err
//		}
//	}
//	return rows.Err()
func (driver *Driver) Query(ctx context.Context, statement string, args ...any) (database.Rows, error) {
	if err := driver.check(ctx); err != nil {
		return nil, err
	}

	result, err := driver.db.QueryContext(ctx, statement, args...)

	if err != nil {
		return nil, err
	}

	cursor := &rows{
		Rows:  result,
		owner: driver.scope,
	}

	if driver.scope != nil {
		driver.scope.rows = append(driver.scope.rows, cursor)
	}

	return cursor, nil
}

// Find scans the first result row into dest, a pointer to a struct or to a
// single-column value. When no row matches, the returned error wraps both
// [sql.ErrNoRows] and [database.ErrNoRows]. Cursor-close failures are returned
// alongside scan or iteration errors, and make a retained session unsafe to reuse.
//
// Example:
//
//	var value int
//	if err := driver.Find(ctx, "SELECT value FROM counters WHERE id = ?", &value, id); err != nil {
//		return err
//	}
func (driver *Driver) Find(ctx context.Context, statement string, dest any, args ...any) (err error) {
	if err := driver.check(ctx); err != nil {
		return err
	}

	result, err := driver.db.QueryContext(ctx, statement, args...)

	if err != nil {
		return err
	}

	defer func() {
		err = errors.Join(err, driver.closeResult(result))
	}()

	if !result.Next() {
		if err := result.Err(); err != nil {
			return err
		}

		return errors.Join(sql.ErrNoRows, database.ErrNoRows)
	}

	cursor := rows{
		Rows: result,
	}

	if err := cursor.Scan(dest); err != nil {
		return err
	}

	return result.Err()
}

// closeResult preserves statement-finalization errors from convenience reads.
// A native cleanup failure poisons the owning physical session even if a callback
// ignores its immediate error; it must not commit or pool uncertain native state.
func (driver *Driver) closeResult(result *sql.Rows) error {
	err := result.Close()

	if err == nil {
		// Exhaustion can already have auto-closed the cursor and saved a
		// finalization error in Err; a repeated Close then returns nil.
		err = result.Err()
	}

	if err != nil && driver.scope != nil {
		driver.scope.poison(err)
	}

	return err
}

// Scan copies the current row into dest, a non-nil pointer to a struct or to
// a single-column value.
func (cursor *rows) Scan(dest any) error {
	if cursor.owner != nil {
		if err := cursor.owner.err(); err != nil {
			return err
		}
	}

	if cursor.columns == nil {
		columns, err := cursor.Columns()

		if err != nil {
			return err
		}

		cursor.columns = columns
	}

	targets, err := query.Targets(dest, cursor.columns)

	if err != nil {
		return err
	}

	return cursor.Rows.Scan(targets...)
}

// Next refuses iteration after its callback ends. Cleanup has already closed
// the underlying result before that connection can return to the pool.
func (cursor *rows) Next() bool {
	if cursor.owner != nil && cursor.owner.err() != nil {
		return false
	}

	return cursor.Rows.Next()
}

// Err reports callback expiration in preference to an exhausted native cursor,
// making an escaped cursor distinguishable from an ordinary empty result.
func (cursor *rows) Err() error {
	if cursor.owner != nil {
		if err := cursor.owner.err(); err != nil {
			return err
		}
	}

	return cursor.Rows.Err()
}

// Close releases the native result once and caches its outcome. It remains
// safe after automatic scope cleanup and never touches a later connection user.
func (cursor *rows) Close() error {
	cursor.closeOnce.Do(func() {
		cursor.closeErr = cursor.Rows.Close()
	})

	return cursor.closeErr
}
