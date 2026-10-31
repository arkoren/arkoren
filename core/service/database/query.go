package database

import (
	"context"
	"errors"
	"fmt"

	"arkoren.dev/foundation/collection"
	"arkoren.dev/foundation/query"
)

// errNotSelect rejects builders that are not SELECT statements in methods
// that scan rows.
var errNotSelect = errors.New("builder is not a select statement")

// Dialect returns the dialect reported by [Driver.Dialect]. The query-query
// methods on [Database] compile for it.
func (database *Database) Dialect() query.Dialect {
	return database.driver.Dialect()
}

// buildRows compiles s for the driver dialect. A [query.Query] must
// return rows, so it is rejected unless it is a SELECT or an INSERT,
// UPDATE, or DELETE with RETURNING. Other statements, such as
// [query.RawStatement], are opaque and always accepted.
func (database *Database) buildRows(s query.Statementer) (query.Statement, error) {
	if statement, ok := s.(query.Query); ok && !returnsRows(statement) {
		return query.Statement{}, fmt.Errorf("build query: %w: %w", query.ErrInvalidQuery, errNotSelect)
	}

	return database.build(s)
}

// returnsRows reports whether query compiles to a statement that
// returns rows: a SELECT, or a write with a RETURNING clause.
func returnsRows(statement query.Query) bool {
	return statement.IsSelect() || statement.Inspect().Returning
}

// build compiles s for the driver dialect.
func (database *Database) build(s query.Statementer) (query.Statement, error) {
	statement, err := s.BuildFor(database.Dialect())

	if err != nil {
		return query.Statement{}, fmt.Errorf("build query: %w", err)
	}

	return statement, nil
}

// Exec compiles s for the driver dialect, executes it, and returns the
// number of affected rows.
func (database *Database) Exec(ctx context.Context, s query.Statementer) (int64, error) {
	statement, err := database.build(s)

	if err != nil {
		return 0, err
	}

	ctx, op := database.begin(ctx, s, statement.SQL())

	driver, err := database.driverFor(ctx)

	if err != nil {
		op.end(ctx, err)

		return 0, err
	}

	affected, err := driver.Exec(ctx, statement.SQL(), statement.Args()...)
	op.end(ctx, err)

	return affected, err
}

// Select compiles s for the driver dialect and scans every result row into
// a collection.Slice. A [query.Query] must be a SELECT or have RETURNING;
// otherwise it fails with [query.ErrInvalidQuery].
func (database *Database) Select[T any](ctx context.Context, s query.Statementer) (collection.Slice[T], error) {
	statement, err := database.buildRows(s)

	if err != nil {
		return collection.Slice[T]{}, err
	}

	ctx, op := database.begin(ctx, s, statement.SQL())
	items := make([]T, 0)

	driver, err := database.driverFor(ctx)

	if err != nil {
		op.end(ctx, err)

		return collection.Slice[T]{}, err
	}

	err = driver.Select(ctx, statement.SQL(), &items, statement.Args()...)
	op.end(ctx, err)

	if err != nil {
		return collection.Slice[T]{}, err
	}

	return collection.NewSlice(items), nil
}

// Find compiles s for the driver dialect and scans the single expected row.
// A [query.Query] must be a SELECT or have RETURNING; otherwise it fails
// with [query.ErrInvalidQuery].
func (database *Database) Find[T any](ctx context.Context, s query.Statementer) (T, error) {
	var result T

	statement, err := database.buildRows(s)

	if err != nil {
		return result, err
	}

	ctx, op := database.begin(ctx, s, statement.SQL())

	driver, err := database.driverFor(ctx)

	if err != nil {
		op.end(ctx, err)

		return result, err
	}

	err = driver.Find(ctx, statement.SQL(), &result, statement.Args()...)
	op.end(ctx, err)

	if err != nil {
		return result, err
	}

	return result, nil
}

// Cursor compiles s for the driver dialect and returns a lazy cursor over
// its results. A build error, including a [query.Query] that returns no
// rows, is yielded by the cursor.
// Exhaustion and scan failures close the rows before yielding a final error;
// errors.Is matches both the original failure and any Close error. Stopping
// iteration early still closes rows, but Close failures can only be recorded
// in telemetry because an iterator cannot yield again after the consumer stops.
// Use Driver.Query and explicitly handle Rows.Close when early-stop cleanup
// failures must be returned to application code.
//
// Example:
//
//	for user, err := range db.Cursor[User](ctx, query.Table("users")).Iter() {
//		if err != nil {
//			return err
//		}
//		_ = user
//	}
func (database *Database) Cursor[T any](ctx context.Context, s query.Statementer) collection.TryLazySlice[T] {
	statement, err := database.buildRows(s)

	return database.cursor[T](ctx, s, statement, err)
}
