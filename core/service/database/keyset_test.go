package database_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"slices"
	"testing"

	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
)

// nullablePageRow models SQL NULL using the pointer representation supported by
// the database scanners. ID remains the unique tiebreaker for the ordering.
type nullablePageRow struct {
	// Rank is the nullable leading ordering key.
	Rank *int `db:"rank"`
	// ID breaks ties between ranks.
	ID int `db:"id"`
}

// valuerPageRow models the standard database/sql nullable representation. Its
// JSON representation must remain unchanged for existing cursor compatibility.
type valuerPageRow struct {
	// ID is both the ordering key and a driver.Valuer.
	ID sql.NullInt64 `db:"id"`
}

// pointerNullKey represents a nullable native value whose driver.Valuer method
// exists only on the pointer. Its ordinary JSON object is never JSON null.
type pointerNullKey struct {
	// Valid distinguishes a stored zero from SQL NULL.
	Valid bool
	// Number is the non-NULL SQL key value.
	Number int64
}

// Value returns a valid SQL scalar or SQL NULL, just as a native nullable type
// does. It intentionally has a pointer receiver to exercise that method set.
func (value *pointerNullKey) Value() (driver.Value, error) {
	if !value.Valid {
		return nil, nil
	}

	return value.Number, nil
}

// bindingPaginationDriver extends the existing facade fake with database/sql's
// real argument converter. It catches lost pointer-only driver.Valuer methods
// at the public SQL boundary without depending on a vendor driver.
type bindingPaginationDriver struct {
	// adapterStub supplies rows and the rest of the existing driver contract.
	adapterStub
}

// Select validates bound arguments exactly as a default database/sql driver
// does before delegating row population to the existing scripted fake.
func (connection bindingPaginationDriver) Select(
	ctx context.Context,
	statement string,
	dest any,
	args ...any,
) error {
	for _, value := range args {
		if _, err := driver.DefaultParameterConverter.ConvertValue(value); err != nil {
			return err
		}
	}

	return connection.adapterStub.Select(ctx, statement, dest, args...)
}

// TestCursorPaginateRetainsPointerValuerBinding verifies a non-NULL native key
// remains bindable after decoding its existing, dereferenced JSON cursor form.
func TestCursorPaginateRetainsPointerValuerBinding(t *testing.T) {
	t.Parallel()

	db := database.New(bindingPaginationDriver{adapterStub{selectFn: func(dest any) {
		*dest.(*[]pointerValuerPageRow) = []pointerValuerPageRow{
			{ID: &pointerNullKey{Number: 1, Valid: true}},
			{ID: &pointerNullKey{Number: 2, Valid: true}},
		}
	}}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	page, err := db.CursorPaginate[pointerValuerPageRow](t.Context(), statement, pagination.CursorParams{PerPage: 1})

	if err != nil {
		t.Fatal(err)
	}

	_, err = db.CursorPaginate[pointerValuerPageRow](t.Context(), statement, pagination.CursorParams{
		Cursor: page.NextCursor, PerPage: 1,
	})

	if err != nil {
		t.Fatal(err)
	}
}

// TestEachRetainsPointerValuerBinding verifies the continuation after a full
// batch keeps the pointer-only driver conversion rather than binding a struct.
func TestEachRetainsPointerValuerBinding(t *testing.T) {
	t.Parallel()

	calls := 0
	db := database.New(bindingPaginationDriver{adapterStub{selectFn: func(dest any) {
		calls++

		if calls == 1 {
			*dest.(*[]pointerValuerPageRow) = []pointerValuerPageRow{
				{ID: &pointerNullKey{Number: 1, Valid: true}},
				{ID: &pointerNullKey{Number: 2, Valid: true}},
			}
		}
	}}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	items, err := db.Each[pointerValuerPageRow](t.Context(), statement, 2).Items()

	if err != nil {
		t.Fatal(err)
	}

	if len(items) != 2 || calls != 2 {
		t.Fatalf("Each = %v, calls = %d", items, calls)
	}
}

// pointerValuerPageRow preserves the pointer needed by a native nullable key.
type pointerValuerPageRow struct {
	// ID carries the nullable ordering value.
	ID *pointerNullKey `db:"id"`
}

// bytePageRow models the nullable byte-slice representation emitted by SQL
// scanners. A nil slice and a present, empty byte string are distinct keys.
type bytePageRow struct {
	// ID is the ordering key; nil represents SQL NULL.
	ID []byte `db:"id"`
}

// TestCursorPaginateRejectsNilBytes checks typed nil containers, which are not
// nil when stored in any and therefore require a representation-aware check.
func TestCursorPaginateRejectsNilBytes(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]bytePageRow) = []bytePageRow{{}}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	_, err := db.CursorPaginate[bytePageRow](t.Context(), statement, pagination.CursorParams{})

	if !errors.Is(err, database.ErrCursorValue) {
		t.Fatalf("CursorPaginate() = %v, want ErrCursorValue", err)
	}
}

// TestCursorPaginateAcceptsPresentEmptyBytes protects the distinction between
// SQL NULL and a non-nil zero-length binary value during key validation.
func TestCursorPaginateAcceptsPresentEmptyBytes(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]bytePageRow) = []bytePageRow{{ID: []byte{}}}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	_, err := db.CursorPaginate[bytePageRow](t.Context(), statement, pagination.CursorParams{})

	if err != nil {
		t.Fatal(err)
	}
}

// TestCursorPaginateRejectsPointerNullValuer verifies dereferencing a key does
// not hide the driver's pointer-only Value method and accept SQL NULL.
func TestCursorPaginateRejectsPointerNullValuer(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]pointerValuerPageRow) = []pointerValuerPageRow{{ID: &pointerNullKey{}}}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	_, err := db.CursorPaginate[pointerValuerPageRow](t.Context(), statement, pagination.CursorParams{})

	if !errors.Is(err, database.ErrCursorValue) {
		t.Fatalf("CursorPaginate() = %v, want ErrCursorValue", err)
	}
}

// TestCursorPaginateRejectsNullInFinalWindow prevents a short final window from
// silently accepting an unsupported key just because no cursor is emitted.
func TestCursorPaginateRejectsNullInFinalWindow(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]nullablePageRow) = []nullablePageRow{{ID: 1}}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("rank")
		statement.OrderBy("id")
	})

	_, err := db.CursorPaginate[nullablePageRow](t.Context(), statement, pagination.CursorParams{PerPage: 2})

	if !errors.Is(err, database.ErrCursorValue) {
		t.Fatalf("CursorPaginate() = %v, want ErrCursorValue", err)
	}
}

// TestCursorPaginateRejectsNullLookahead checks the extra fetched row too. A
// non-NULL boundary cannot seek into a subsequent NULL partition in SQL.
func TestCursorPaginateRejectsNullLookahead(t *testing.T) {
	t.Parallel()

	rank := 1
	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]nullablePageRow) = []nullablePageRow{{Rank: &rank, ID: 1}, {ID: 2}}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("rank")
		statement.OrderBy("id")
	})

	_, err := db.CursorPaginate[nullablePageRow](t.Context(), statement, pagination.CursorParams{PerPage: 1})

	if !errors.Is(err, database.ErrCursorValue) {
		t.Fatalf("CursorPaginate() = %v, want ErrCursorValue", err)
	}
}

// TestCursorPaginateRejectsNullValuer verifies SQL NULL is not confused with
// the non-null JSON object produced by sql.NullInt64's default marshaling.
func TestCursorPaginateRejectsNullValuer(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]valuerPageRow) = []valuerPageRow{{ID: sql.NullInt64{}}}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	_, err := db.CursorPaginate[valuerPageRow](t.Context(), statement, pagination.CursorParams{})

	if !errors.Is(err, database.ErrCursorValue) {
		t.Fatalf("CursorPaginate() = %v, want ErrCursorValue", err)
	}
}

// TestCursorPaginateRejectsNullValuerCursor starts with a genuine issued cursor
// and changes only the nullable key. Unsigned input must still be SQL-safe.
func TestCursorPaginateRejectsNullValuerCursor(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]valuerPageRow) = []valuerPageRow{
			{ID: sql.NullInt64{Int64: 1, Valid: true}},
			{ID: sql.NullInt64{Int64: 2, Valid: true}},
		}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	page, err := db.CursorPaginate[valuerPageRow](t.Context(), statement, pagination.CursorParams{PerPage: 1})

	if err != nil {
		t.Fatal(err)
	}

	payload, err := pagination.CursorDecode[map[string]any](page.NextCursor)

	if err != nil {
		t.Fatal(err)
	}

	payload["v"] = []sql.NullInt64{{Int64: 1, Valid: false}}

	cursor, err := pagination.CursorEncode(payload)

	if err != nil {
		t.Fatal(err)
	}

	_, err = db.CursorPaginate[valuerPageRow](t.Context(), statement, pagination.CursorParams{Cursor: cursor})

	if !errors.Is(err, pagination.ErrCursorDecode) {
		t.Fatalf("CursorPaginate() = %v, want ErrCursorDecode", err)
	}
}

// TestEachRejectsNullInFinalBatch verifies Each cannot return successful partial
// results when no subsequent seek is needed to expose an invalid ordering key.
func TestEachRejectsNullInFinalBatch(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]nullablePageRow) = []nullablePageRow{{ID: 1}}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("rank")
		statement.OrderBy("id")
	})

	_, err := db.Each[nullablePageRow](t.Context(), statement, 2).Items()

	if !errors.Is(err, database.ErrCursorValue) {
		t.Fatalf("Each() = %v, want ErrCursorValue", err)
	}
}

// TestEachStopsCanceledBufferedBatch verifies cancellation is observed between
// rows already loaded into memory, rather than only before the next query.
func TestEachStopsCanceledBufferedBatch(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]paginateRow) = []paginateRow{{ID: 1}, {ID: 2}}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	count := 0
	canceled := false

	for _, err := range db.Each[paginateRow](ctx, statement, 3).Iter() {
		if err != nil {
			canceled = errors.Is(err, context.Canceled)
			break
		}

		count++
		cancel()
	}

	if count != 1 || !canceled {
		t.Fatalf("yielded %d rows, canceled = %v; want one row and cancellation", count, canceled)
	}
}

// TestCursorPaginatePreservesForwardBackwardBoundaries verifies the retained
// boundary keys, not lookahead keys, feed both cursor directions after reversal.
func TestCursorPaginatePreservesForwardBackwardBoundaries(t *testing.T) {
	t.Parallel()

	calls := 0
	db := database.New(adapterStub{selectFn: func(dest any) {
		calls++

		switch calls {
		case 1:
			*dest.(*[]paginateRow) = []paginateRow{{ID: 1}, {ID: 2}, {ID: 3}}
		case 2:
			*dest.(*[]paginateRow) = []paginateRow{{ID: 3}, {ID: 4}, {ID: 5}}
		case 3:
			*dest.(*[]paginateRow) = []paginateRow{{ID: 2}, {ID: 1}}
		default:
			t.Fatal("unexpected query")
		}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	first, err := db.CursorPaginate[paginateRow](t.Context(), statement, pagination.CursorParams{PerPage: 2})

	if err != nil {
		t.Fatal(err)
	}

	next, err := db.CursorPaginate[paginateRow](t.Context(), statement, pagination.CursorParams{
		Cursor: first.NextCursor, PerPage: 2,
	})

	if err != nil {
		t.Fatal(err)
	}

	previous, err := db.CursorPaginate[paginateRow](t.Context(), statement, pagination.CursorParams{
		Cursor: next.PrevCursor, PerPage: 2,
	})

	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(previous.Items, first.Items) || previous.NextCursor != first.NextCursor {
		t.Fatalf("backward page = %+v, want %+v", previous, first)
	}

	if previous.PrevCursor != "" || first.PrevCursor != "" {
		t.Fatal("first window advertised an earlier window")
	}
}

// TestCursorPaginateNullableValueRoundTrip retains the existing JSON object
// representation for a valid database/sql nullable key across continuation.
func TestCursorPaginateNullableValueRoundTrip(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]valuerPageRow) = []valuerPageRow{
			{ID: sql.NullInt64{Int64: 1, Valid: true}},
			{ID: sql.NullInt64{Int64: 2, Valid: true}},
		}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	first, err := db.CursorPaginate[valuerPageRow](t.Context(), statement, pagination.CursorParams{PerPage: 1})

	if err != nil {
		t.Fatal(err)
	}

	payload, err := pagination.CursorDecode[map[string]any](first.NextCursor)

	if err != nil {
		t.Fatal(err)
	}

	values := payload["v"].([]any)
	key := values[0].(map[string]any)

	if key["Int64"] != float64(1) || key["Valid"] != true {
		t.Fatalf("nullable cursor representation changed: %v", payload)
	}

	_, err = db.CursorPaginate[valuerPageRow](t.Context(), statement, pagination.CursorParams{
		Cursor: first.NextCursor, PerPage: 1,
	})

	if err != nil {
		t.Fatal(err)
	}
}

// TestPaginateMaximumSizeUsesDefaultCap verifies Database.Paginate still
// normalizes caller-supplied integer extremes before constructing its query.
func TestPaginateMaximumSizeUsesDefaultCap(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]paginateRow) = []paginateRow{{ID: 1}}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	page, err := db.Paginate[paginateRow](t.Context(), statement, pagination.PageParams{PerPage: math.MaxInt})

	if err != nil {
		t.Fatal(err)
	}

	if page.PerPage != 100 {
		t.Fatalf("PerPage = %d, want 100", page.PerPage)
	}
}

// TestCursorPaginateMaximumSizeUsesDefaultCap verifies the size+1 lookahead
// cannot overflow int even when a direct caller requests math.MaxInt rows.
func TestCursorPaginateMaximumSizeUsesDefaultCap(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]paginateRow) = []paginateRow{{ID: 1}}
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	page, err := db.CursorPaginate[paginateRow](t.Context(), statement, pagination.CursorParams{PerPage: math.MaxInt})

	if err != nil {
		t.Fatal(err)
	}

	if page.PerPage != 100 {
		t.Fatalf("PerPage = %d, want 100", page.PerPage)
	}
}

// TestCursorPaginateRejectsMutationBeforeResultType verifies keyset pagination
// rejects a row-returning mutation before resolving cursor fields or doing I/O.
func TestCursorPaginateRejectsMutationBeforeResultType(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(any) {
		t.Fatal("pagination executed a mutation")
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.Delete()
		statement.Returning("id")
		statement.OrderBy("id")
	})

	_, err := db.CursorPaginate[int](t.Context(), statement, pagination.CursorParams{})

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("CursorPaginate = %v, want ErrInvalidQuery", err)
	}
}

// TestEachRejectsMutationBeforeResultType verifies the lazy keyset entry point
// cannot reach Database.Select's intentional support for RETURNING writes.
func TestEachRejectsMutationBeforeResultType(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{selectFn: func(any) {
		t.Fatal("Each executed a mutation")
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.Delete()
		statement.Returning("id")
		statement.OrderBy("id")
	})

	_, err := db.Each[int](t.Context(), statement, 2).Items()

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("Each = %v, want ErrInvalidQuery", err)
	}
}
