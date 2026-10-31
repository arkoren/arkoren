package mysqldatabase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
)

// executor is implemented by [sql.DB], [sql.Conn], and [sql.Tx]. Lock queries
// use the same executor as application SQL, never an extra pool connection.
type executor interface {
	// ExecContext executes a statement without returning rows.
	ExecContext(ctx context.Context, statement string, args ...any) (sql.Result, error)

	// QueryContext executes a statement that returns rows.
	QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error)

	// QueryRowContext returns one row for native named-lock operations.
	QueryRowContext(ctx context.Context, statement string, args ...any) *sql.Row
}

// rows adapts [sql.Rows] to [database.Rows], scanning with [query.Targets].
type rows struct {
	// Rows is the underlying cursor.
	*sql.Rows

	// columns are the result columns, read on the first Scan.
	columns []string

	// driver guards the lifetime of the callback that opened this cursor.
	// It is nil for root queries whose lifetime is managed by sql.Rows itself.
	driver *Driver
}

var _ database.Rows = (*rows)(nil)

// Exec executes statement and returns its affected-row count. Scoped calls use
// their retained session or transaction and fail before I/O after callback exit.
// Transport failures poison a scoped session instead of retrying on the pool.
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
		driver.recordFailure(err)

		return 0, err
	}

	return result.RowsAffected()
}

// Select scans every result row into dest, a pointer to a slice of structs,
// of pointers to structs, or of single-column values. Struct columns follow
// [query.Targets]. A successful query replaces existing elements; no rows leaves
// dest empty. A scan failure can leave a partial result. It closes its cursor
// before returning and rejects an ended callback before inspecting dest or SQL.
//
// Example:
//
//	var ids []int64
//	err := driver.Select(ctx, "SELECT id FROM counters ORDER BY id", &ids)
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
		driver.recordFailure(err)

		return err
	}

	defer func() {
		closeErr := result.Close()
		driver.recordFailure(closeErr)
		err = errors.Join(err, closeErr)
	}()

	err = scanRows(result, target.Elem())
	driver.recordFailure(result.Err())

	return err
}

// scanRows replaces a validated destination with native results, preserving its
// allocation but not previous elements. Pointer elements receive new pointees;
// errors leave the successfully scanned prefix rather than restoring old data.
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

// Query returns a streaming cursor over statement's rows. Close it when done;
// a scoped callback also closes outstanding cursors before ending its native
// transaction or returning the connection. Escaped cursors report that callback's
// lifetime sentinel rather than accessing a later connection borrower.
//
// Example:
//
//	rows, err := driver.Query(ctx, "SELECT id FROM counters ORDER BY id")
//	if err != nil {
//		return err
//	}
//	defer rows.Close()
//	for rows.Next() {
//		var id int64
//		if err := rows.Scan(&id); err != nil {
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
		driver.recordFailure(err)

		return nil, err
	}

	cursor := &rows{
		Rows: result,
	}

	if driver.scope != nil {
		cursor.driver = driver

		if driver.scope.cursors == nil {
			driver.scope.cursors = make(map[*rows]struct{})
		}

		driver.scope.cursors[cursor] = struct{}{}
	}

	return cursor, nil
}

// Find scans the first result row into dest, a pointer to a struct or to a
// single-column value. When no row matches, the returned error wraps both
// [sql.ErrNoRows] and [database.ErrNoRows]. Its cursor closes before return;
// an ended callback fails before SQL and never obtains a new connection.
//
// Example:
//
//	var value int64
//	err := driver.Find(ctx, "SELECT value FROM counters WHERE id = ?", &value, id)
func (driver *Driver) Find(ctx context.Context, statement string, dest any, args ...any) (err error) {
	if err := driver.check(ctx); err != nil {
		return err
	}

	result, err := driver.db.QueryContext(ctx, statement, args...)

	if err != nil {
		driver.recordFailure(err)

		return err
	}

	defer func() {
		closeErr := result.Close()
		driver.recordFailure(closeErr)
		err = errors.Join(err, closeErr)
	}()

	if !result.Next() {
		if err := result.Err(); err != nil {
			driver.recordFailure(err)

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

	err = result.Err()
	driver.recordFailure(err)

	return err
}

// Next refuses to advance an expired callback cursor. Err exposes the lifetime
// sentinel, rather than accessing a native cursor whose session may be reused.
func (cursor *rows) Next() bool {
	if cursor.driver != nil && cursor.driver.check(context.Background()) != nil {
		return false
	}

	return cursor.Rows.Next()
}

// Err reports the callback's lifetime or cancellation error before the native
// iteration error. A transport failure also poisons a still-active session.
func (cursor *rows) Err() error {
	if cursor.driver != nil {
		if err := cursor.driver.check(context.Background()); err != nil {
			return err
		}

		cursor.driver.recordFailure(cursor.Rows.Err())
	}

	return cursor.Rows.Err()
}

// Close releases native resources even after cancellation, unregistering an
// active cursor from automatic scope cleanup. An expired cursor is already
// closed, so Close returns nil without touching a later connection borrower.
func (cursor *rows) Close() error {
	if cursor.driver == nil {
		return cursor.Rows.Close()
	}

	err := cursor.driver.check(context.Background())

	if cursor.driver.scope.ended {
		return nil
	}

	delete(cursor.driver.scope.cursors, cursor)
	closeErr := cursor.Rows.Close()
	cursor.driver.recordFailure(closeErr)

	return errors.Join(err, closeErr)
}

// Scan copies the current row into a non-nil pointer to a struct or scalar,
// mapping columns with query.Targets. An expired callback is rejected before
// inspecting the native cursor or modifying dest.
func (cursor *rows) Scan(dest any) error {
	if cursor.driver != nil {
		if err := cursor.driver.check(context.Background()); err != nil {
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
