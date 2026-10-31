package database

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"arkoren.dev/core/pagination"
	"arkoren.dev/foundation/collection"
	"arkoren.dev/foundation/query"
)

var (
	// ErrNoOrder reports that a paginated or batched query has no ORDER BY.
	// Stable pages need a total order, so add OrderBy with a unique
	// tiebreaker column last.
	ErrNoOrder = errors.New("paginated query requires an order")

	// ErrCursorColumn reports that an ordered column has no matching field
	// in the result type, so its value cannot be read for a cursor.
	ErrCursorColumn = errors.New("order column has no field in result type")

	// ErrCursorValue reports that a key value of a cursor row cannot be
	// used in a keyset comparison, for example because it is NULL.
	ErrCursorValue = errors.New("invalid cursor key value")

	// ErrPaged reports that the query already has a Limit or Offset.
	// Pagination sets both itself, so a preset value would be overwritten
	// or skip rows. Omit them when declaring the query.
	ErrPaged = errors.New("builder already has limit or offset")

	// ErrInvalidBatchSize reports a batch size below one in [Database.Each].
	ErrInvalidBatchSize = errors.New("batch size must be positive")
)

// Paginate counts the rows query selects and fetches the page p asks for. p is
// normalized with the default limits. query needs an ORDER BY so that pages are
// stable; otherwise it fails with [ErrNoOrder]. A query with a Limit or
// Offset fails with [ErrPaged]. The total comes from
// [query.Query.Count], so grouped and raw sources are counted correctly.
// Items is an eager collection of this page only. Local transformations do not
// execute SQL or recompute the count or page metadata.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrderBy("id")
//	})
//	page, err := db.Paginate[User](ctx, users, pagination.PageParams{PerPage: 20})
//	if err != nil {
//		return err
//	}
//	_ = page
func (database *Database) Paginate[T any](
	ctx context.Context,
	statement query.Query,
	p pagination.PageParams,
) (pagination.Page[T], error) {
	if !statement.IsSelect() {
		return pagination.Page[T]{}, fmt.Errorf("paginate: %w: %w", query.ErrInvalidQuery, errNotSelect)
	}

	if paged(statement) {
		return pagination.Page[T]{}, ErrPaged
	}

	if len(statement.Inspect().Orders) == 0 {
		return pagination.Page[T]{}, ErrNoOrder
	}

	p, err := p.Normalize()

	if err != nil {
		return pagination.Page[T]{}, err
	}

	total, err := database.Find[int64](ctx, statement.Count())

	if err != nil {
		return pagination.Page[T]{}, fmt.Errorf("paginate: count rows: %w", err)
	}

	items, err := database.Select[T](ctx, statement.Derive(func(statement *query.Builder) {
		statement.Limit(p.PerPage)
		statement.Offset(p.Offset())
	}))

	if err != nil {
		return pagination.Page[T]{}, fmt.Errorf("paginate: select rows: %w", err)
	}

	return pagination.Paginate(items, total, p.Page, p.PerPage), nil
}

// CursorPaginate fetches one keyset window of query. The cursor in p, when set,
// positions the window; p is normalized with the default limits. The
// ordered columns of query must exist as fields of T (matched by db tag or
// lowercase field name, as in [query.Values]). End the order with a unique
// tiebreaker; uniqueness is the caller's responsibility and is not checked.
// A malformed cursor or one for other columns or directions fails with
// [pagination.ErrCursorDecode]. Without [Builder.CursorCodec], positions are
// unsigned and can be forged. With a codec, unsigned, altered and wrongly
// signed cursors fail with ErrCursorDecode. A query with a Limit or Offset
// fails with [ErrPaged].
// Like Paginate, it requires a SELECT: row-returning mutations fail with
// query.ErrInvalidQuery before result-type reflection or SQL execution.
// Every ordered value in the selected dataset must be non-NULL; NULL ordering
// is unsupported. Use non-nullable keys or explicitly filter out NULL rows.
// All fetched rows, including lookahead, are checked for NULL keys and fail
// with [ErrCursorValue]. This cannot detect NULL rows that a SQL seek excludes.
// SQL nullable values are checked through driver.Valuer; non-NULL values keep
// that method set for binding while cursor JSON retains its existing shape.
// Items is an eager collection of this window only. Local transformations do
// not execute SQL or change tokens, which retain the original row boundaries.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrderBy("id")
//	})
//	page, err := db.CursorPaginate[User](ctx, users, pagination.CursorParams{PerPage: 20})
//	if err != nil {
//		return err
//	}
//	_ = page
func (database *Database) CursorPaginate[T any](
	ctx context.Context,
	statement query.Query,
	p pagination.CursorParams,
) (pagination.Cursor[T], error) {
	if paged(statement) {
		return pagination.Cursor[T]{}, ErrPaged
	}

	p, err := p.Normalize()

	if err != nil {
		return pagination.Cursor[T]{}, err
	}

	keys, err := newKeyset[T](statement)

	if err != nil {
		return pagination.Cursor[T]{}, err
	}

	keys.codec = database.config.CursorCodec

	fetch, backward, err := keys.seek(statement, p.Cursor)

	if err != nil {
		return pagination.Cursor[T]{}, err
	}

	rows, err := database.Select[T](ctx, fetch.Derive(func(statement *query.Builder) {
		statement.Limit(p.PerPage + 1)
	}))

	if err != nil {
		return pagination.Cursor[T]{}, fmt.Errorf("cursor paginate: select rows: %w", err)
	}

	items := rows.Items()

	first, last, err := keys.windowValues(ctx, items, p.PerPage)

	if err != nil {
		return pagination.Cursor[T]{}, err
	}

	more := len(items) > p.PerPage

	if more {
		items = items[:p.PerPage]
	}

	if backward {
		slices.Reverse(items)

		first, last = last, first
	}

	next, previous := more, p.Cursor != ""

	if backward {
		next, previous = true, more
	}

	return pagination.CursorPaginateFunc(
		items,
		p.PerPage,
		next,
		previous,
		func(_ T) (string, error) {
			return keys.encode(last, false)
		},
		func(_ T) (string, error) {
			return keys.encode(first, true)
		},
	)
}

// Each lazily iterates every row of query in batches of size rows using keyset
// pagination, so memory stays bounded and rows inserted behind the
// position are not revisited. query needs an ORDER BY, otherwise [ErrNoOrder]
// is yielded. End it with a unique tiebreaker; uniqueness is not checked.
// The ordered columns must exist
// as fields of T. A query with a Limit or Offset yields [ErrPaged].
// Rows are read in separate queries, not one snapshot, so
// a consistent view requires transaction isolation that preserves a snapshot
// across statements. A transaction alone, such as PostgreSQL's default read
// committed transaction, does not provide that guarantee. Row-returning
// mutations fail with query.ErrInvalidQuery before result-type reflection.
// Every ordered value in the selected dataset must be non-NULL, as for
// [Database.CursorPaginate]. Each checks every fetched row, including a short
// final batch, and yields [ErrCursorValue] for NULL keys. Cancellation is
// checked between buffered rows, not only between database queries.
//
// Example:
//
//	users := query.Table("users", func(statement *query.Builder) {
//		statement.OrderBy("id")
//	})
//	for user, err := range db.Each[User](ctx, users, 100).Iter() {
//		if err != nil {
//			return err
//		}
//		_ = user
//	}
func (database *Database) Each[T any](
	ctx context.Context,
	statement query.Query,
	size int,
) collection.TryLazySlice[T] {
	return collection.NewTryLazySlice(func(yield func(T, error) bool) {
		var zero T

		if size <= 0 {
			yield(zero, ErrInvalidBatchSize)

			return
		}

		if paged(statement) {
			yield(zero, ErrPaged)

			return
		}

		keys, err := newKeyset[T](statement)

		if err != nil {
			yield(zero, err)

			return
		}

		fetch := statement

		for {
			if err := ctx.Err(); err != nil {
				yield(zero, err)

				return
			}

			rows, err := database.Select[T](ctx, fetch.Derive(func(statement *query.Builder) {
				statement.Limit(size)
			}))

			if err != nil {
				yield(zero, fmt.Errorf("each: select rows: %w", err))

				return
			}

			items := rows.Items()
			values, continued := keys.yieldItems(ctx, items, yield)

			if !continued {
				return
			}

			if len(items) < size {
				return
			}

			fetch = statement.Seek(values, false)
		}
	})
}

// paged reports whether query already carries a Limit or Offset.
func paged(statement query.Query) bool {
	spec := statement.Inspect()

	return spec.Limit != nil || spec.Offset != nil
}
