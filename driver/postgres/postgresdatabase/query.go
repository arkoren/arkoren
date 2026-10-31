package postgresdatabase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// executor is implemented by the pool, a retained physical connection, and a
// transaction. Callback lifetime checks happen before either method is called.
type executor interface {
	// Exec executes a statement without returning rows.
	Exec(ctx context.Context, statement string, args ...any) (pgconn.CommandTag, error)

	// Query executes a statement that returns rows.
	Query(ctx context.Context, statement string, args ...any) (pgx.Rows, error)
}

// rows adapts [pgx.Rows] to [database.Rows], scanning with [query.Targets].
type rows struct {
	// result is the underlying cursor.
	result pgx.Rows

	// columns are the result columns, read on the first Scan.
	columns []string

	// scope retains the creating callback's lifetime, or nil for a root cursor.
	scope *scope

	// cancel releases the query context and interrupts a budget-limited drain.
	cancel context.CancelFunc
}

var _ database.Rows = (*rows)(nil)

// mapError translates pgx errors into the errors the database contract
// documents: a finished transaction wraps [sql.ErrTxDone], and no rows wraps
// [database.ErrNoRows]. The pgx error stays in the chain.
func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrTxClosed):
		return fmt.Errorf("%w: %w", sql.ErrTxDone, err)
	case errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, database.ErrNoRows):
		return errors.Join(err, database.ErrNoRows)
	default:
		return err
	}
}

// columnNames returns the names of the result columns of result.
func columnNames(result pgx.Rows) []string {
	descriptions := result.FieldDescriptions()
	columns := make([]string, len(descriptions))

	for index, description := range descriptions {
		columns[index] = description.Name
	}

	return columns
}

// Exec executes a statement that modifies data and returns the number of rows
// it affected. Callback drivers first check their lifetime, then use only their
// retained connection or transaction. An ended scope cannot acquire from the
// pool. Arguments use PostgreSQL's numbered placeholders.
//
// Example:
//
//	changed, err := driver.Exec(ctx, "UPDATE reports SET ready = true WHERE id = $1", id)
func (driver *Driver) Exec(ctx context.Context, statement string, args ...any) (int64, error) {
	if err := driver.check(ctx); err != nil {
		return 0, err
	}

	tag, err := driver.db.Exec(ctx, statement, args...)

	if err != nil {
		return 0, mapError(err)
	}

	return tag.RowsAffected(), nil
}

// Select scans every result row into dest, a pointer to a slice of structs,
// of pointers to structs, or of single-column values. Struct columns follow
// [query.Targets]. A successful query replaces existing elements; no rows leaves
// dest empty. A scan failure can leave a partial result. The cursor closes before
// return. Invalid destinations fail before SQL; expired callbacks fail before
// inspecting the destination or accessing a connection.
//
// Example:
//
//	var ids []int
//	err := driver.Select(ctx, "SELECT id FROM reports ORDER BY id", &ids)
func (driver *Driver) Select(ctx context.Context, statement string, dest any, args ...any) error {
	if err := driver.check(ctx); err != nil {
		return err
	}

	target := reflect.ValueOf(dest)

	if target.Kind() != reflect.Pointer || target.IsNil() || target.Elem().Kind() != reflect.Slice {
		return fmt.Errorf("select destination must be a non-nil pointer to a slice, got %T", dest)
	}

	result, err := driver.query(ctx, statement, args...)

	if err != nil {
		return mapError(err)
	}

	defer result.Close()

	return scanRows(result, target.Elem())
}

// scanRows replaces the validated slice with the current cursor's result. It
// preserves the slice's allocation but not its old length. Pointer elements get
// separately allocated pointees; a failure retains only rows already scanned.
func scanRows(result *rows, slice reflect.Value) error {
	itemType := slice.Type().Elem()
	pointers := itemType.Kind() == reflect.Pointer

	if pointers {
		itemType = itemType.Elem()
	}

	slice.SetLen(0)

	for result.Next() {
		item := reflect.New(itemType)

		if err := result.Scan(item.Interface()); err != nil {
			return mapError(err)
		}

		if pointers {
			slice.Set(reflect.Append(slice, item))
		} else {
			slice.Set(reflect.Append(slice, item.Elem()))
		}
	}

	return mapError(result.Err())
}

// Query executes a statement and returns a cursor over its rows. A root cursor
// retains a pool connection until closed; a callback cursor uses the already
// pinned connection and is closed automatically when its callback returns.
// After callback return, Next stops and Err/Scan report the scope's end sentinel.
// Close remains safe and idempotent after automatic cleanup.
//
// Example:
//
//	rows, err := driver.Query(ctx, "SELECT id FROM reports ORDER BY id")
//	if err != nil {
//		return err
//	}
//	defer rows.Close()
func (driver *Driver) Query(ctx context.Context, statement string, args ...any) (database.Rows, error) {
	if err := driver.check(ctx); err != nil {
		return nil, err
	}

	cursor, err := driver.query(ctx, statement, args...)

	if err != nil {
		return nil, err
	}

	return cursor, nil
}

// query gives each cursor its own cancelable query context and records it on
// its callback. Its public callers have already checked the callback and ctx.
// Cleanup can drain abandoned cursors without leaving a busy connection
// available to another pool borrower.
func (driver *Driver) query(ctx context.Context, statement string, args ...any) (*rows, error) {
	ctx, cancel := context.WithCancel(ctx)

	result, err := driver.db.Query(ctx, statement, args...)

	if err != nil {
		cancel()

		return nil, mapError(err)
	}

	cursor := &rows{
		result: result,
		scope:  driver.scope,
		cancel: cancel,
	}

	if driver.scope != nil {
		if driver.scope.rows == nil {
			driver.scope.rows = make(map[*rows]struct{})
		}

		driver.scope.rows[cursor] = struct{}{}
	}

	return cursor, nil
}

// Find scans the first result row into dest, a pointer to a struct or to a
// single-column value. When no row matches, the returned error wraps
// [pgx.ErrNoRows], which matches [sql.ErrNoRows], and [database.ErrNoRows]. Its
// cursor closes before return. Callback lifetime checks prevent a finished
// handle from querying a returned connection or falling back to the root pool.
//
// Example:
//
//	var pid int
//	err := driver.Find(ctx, "SELECT pg_backend_pid()", &pid)
func (driver *Driver) Find(ctx context.Context, statement string, dest any, args ...any) error {
	if err := driver.check(ctx); err != nil {
		return err
	}

	result, err := driver.query(ctx, statement, args...)

	if err != nil {
		return mapError(err)
	}

	defer result.Close()

	if !result.Next() {
		if err := result.Err(); err != nil {
			return mapError(err)
		}

		return mapError(pgx.ErrNoRows)
	}

	if err := result.Scan(dest); err != nil {
		return err
	}

	result.Close()

	return mapError(result.Err())
}

// Next advances the cursor to its next row.
func (cursor *rows) Next() bool {
	if cursor.scope != nil && cursor.scope.check() != nil {
		return false
	}

	if !cursor.result.Next() {
		cursor.close()

		return false
	}

	return true
}

// Scan copies the current row into dest, a non-nil pointer to a struct or to
// a single-column value.
func (cursor *rows) Scan(dest any) error {
	if cursor.scope != nil {
		if err := cursor.scope.check(); err != nil {
			return err
		}
	}

	if cursor.columns == nil {
		cursor.columns = columnNames(cursor.result)
	}

	targets, err := query.Targets(dest, cursor.columns)

	if err != nil {
		return err
	}

	return mapError(cursor.result.Scan(targets...))
}

// Err returns the error that ended iteration, or nil after the rows were
// exhausted.
func (cursor *rows) Err() error {
	if cursor.scope != nil {
		if err := cursor.scope.check(); err != nil {
			return err
		}
	}

	return mapError(cursor.result.Err())
}

// Close releases the connection of the cursor. It is safe to call more than
// once, including after callback cleanup has already closed it. An error that
// ended the iteration or expired its callback is reported by [rows.Err].
func (cursor *rows) Close() error {
	if cursor.scope != nil && cursor.scope.check() != nil {
		return nil
	}

	cursor.close()

	return nil
}

// close releases the pgx cursor before canceling its context, avoiding an
// unnecessary connection cancellation after ordinary successful iteration.
// Scope cleanup calls it directly because the callback guard has already ended.
func (cursor *rows) close() {
	cursor.result.Close()
	cursor.cancel()

	if cursor.scope != nil {
		delete(cursor.scope.rows, cursor)
	}
}
