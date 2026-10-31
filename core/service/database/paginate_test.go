package database_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/key"
	"arkoren.dev/foundation/query"
)

// paginateRow is a result type for pagination unit tests.
type paginateRow struct {
	// ID is the unique key.
	ID int `db:"id"`
}

// TestPaginateNoOrder verifies Paginate requires an order.
func TestPaginateNoOrder(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})

	_, err := db.Paginate[paginateRow](context.Background(), query.Table("rows"), pagination.PageParams{})

	if !errors.Is(err, database.ErrNoOrder) {
		t.Fatalf("Paginate() error = %v, want ErrNoOrder", err)
	}
}

// TestPaginateNotSelect verifies Paginate rejects non-select builders.
func TestPaginateNotSelect(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})
	b := query.Table("rows", func(statement *query.Builder) {
		statement.Delete()
		statement.OrderBy("id")
	})

	_, err := db.Paginate[paginateRow](context.Background(), b, pagination.PageParams{})

	if !errors.Is(err, query.ErrInvalidQuery) {
		t.Fatalf("Paginate() error = %v, want ErrInvalidQuery", err)
	}
}

// TestCursorPaginateNoOrder verifies CursorPaginate requires an order.
func TestCursorPaginateNoOrder(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})

	_, err := db.CursorPaginate[paginateRow](context.Background(), query.Table("rows"), pagination.CursorParams{})

	if !errors.Is(err, database.ErrNoOrder) {
		t.Fatalf("CursorPaginate() error = %v, want ErrNoOrder", err)
	}
}

// TestCursorPaginateMissingColumn verifies an unknown order column fails.
func TestCursorPaginateMissingColumn(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})
	b := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("missing")
	})

	_, err := db.CursorPaginate[paginateRow](context.Background(), b, pagination.CursorParams{})

	if !errors.Is(err, database.ErrCursorColumn) {
		t.Fatalf("CursorPaginate() error = %v, want ErrCursorColumn", err)
	}
}

// TestCursorPaginateMismatchedCursor verifies a foreign cursor is rejected.
func TestCursorPaginateMismatchedCursor(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})

	cursor, err := pagination.CursorEncode(map[string]any{"v": []int{1}, "k": "other"})

	if err != nil {
		t.Fatalf("CursorEncode() error = %v", err)
	}

	b := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	_, err = db.CursorPaginate[paginateRow](context.Background(), b, pagination.CursorParams{Cursor: cursor})

	if !errors.Is(err, pagination.ErrCursorDecode) {
		t.Fatalf("CursorPaginate() error = %v, want ErrCursorDecode", err)
	}
}

// TestEachNoOrder verifies Each yields ErrNoOrder.
func TestEachNoOrder(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})

	_, err := db.Each[paginateRow](context.Background(), query.Table("rows"), 10).Items()

	if !errors.Is(err, database.ErrNoOrder) {
		t.Fatalf("Each() error = %v, want ErrNoOrder", err)
	}
}

// TestEachInvalidSize verifies Each rejects a non-positive size.
func TestEachInvalidSize(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})

	_, err := db.Each[paginateRow](context.Background(), query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	}), 0).Items()

	if !errors.Is(err, database.ErrInvalidBatchSize) {
		t.Fatalf("Each() error = %v, want ErrInvalidBatchSize", err)
	}
}

// TestPaginateLimitPreset verifies Paginate rejects a builder with a Limit.
func TestPaginateLimitPreset(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})
	b := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
		statement.Limit(5)
	})

	_, err := db.Paginate[paginateRow](context.Background(), b, pagination.PageParams{})

	if !errors.Is(err, database.ErrPaged) {
		t.Fatalf("Paginate() error = %v, want ErrPaged", err)
	}
}

// TestPaginateOffsetPreset verifies Paginate rejects a builder with a Offset.
func TestPaginateOffsetPreset(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})
	b := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
		statement.Offset(5)
	})

	_, err := db.Paginate[paginateRow](context.Background(), b, pagination.PageParams{})

	if !errors.Is(err, database.ErrPaged) {
		t.Fatalf("Paginate() error = %v, want ErrPaged", err)
	}
}

// TestCursorPaginateLimitPreset verifies CursorPaginate rejects a builder with a Limit.
func TestCursorPaginateLimitPreset(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})
	b := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
		statement.Limit(5)
	})

	_, err := db.CursorPaginate[paginateRow](context.Background(), b, pagination.CursorParams{})

	if !errors.Is(err, database.ErrPaged) {
		t.Fatalf("CursorPaginate() error = %v, want ErrPaged", err)
	}
}

// TestCursorPaginateOffsetPreset verifies CursorPaginate rejects a builder with a Offset.
func TestCursorPaginateOffsetPreset(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})
	b := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
		statement.Offset(5)
	})

	_, err := db.CursorPaginate[paginateRow](context.Background(), b, pagination.CursorParams{})

	if !errors.Is(err, database.ErrPaged) {
		t.Fatalf("CursorPaginate() error = %v, want ErrPaged", err)
	}
}

// TestEachLimitPreset verifies Each yields ErrPaged for a builder with a Limit.
func TestEachLimitPreset(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})
	b := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
		statement.Limit(5)
	})

	count := 0

	for _, err := range db.Each[paginateRow](context.Background(), b, 2).Iter() {
		count++

		if !errors.Is(err, database.ErrPaged) {
			t.Fatalf("Each() error = %v, want ErrPaged", err)
		}
	}

	if count != 1 {
		t.Fatalf("Each() yielded %d items, want 1", count)
	}
}

// TestEachOffsetPreset verifies Each yields ErrPaged for a builder with a Offset.
func TestEachOffsetPreset(t *testing.T) {
	t.Parallel()

	db := database.New(adapterStub{})
	b := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
		statement.Offset(5)
	})

	count := 0

	for _, err := range db.Each[paginateRow](context.Background(), b, 2).Iter() {
		count++

		if !errors.Is(err, database.ErrPaged) {
			t.Fatalf("Each() error = %v, want ErrPaged", err)
		}
	}

	if count != 1 {
		t.Fatalf("Each() yielded %d items, want 1", count)
	}
}

// signedCursorDB returns a database whose Select returns ids as rows and that
// signs cursors with a codec built from secret.
func signedCursorDB(t *testing.T, secret byte, ids ...int) *database.Database {
	t.Helper()

	material, err := key.NewShared(bytes.Repeat([]byte{secret}, pagination.MinSecretLength))

	if err != nil {
		t.Fatal(err)
	}

	codec, err := pagination.NewCursorCodec(material)

	if err != nil {
		t.Fatalf("NewCursorCodec() error = %v", err)
	}

	stub := adapterStub{selectFn: func(dest any) {
		rows := dest.(*[]paginateRow)

		for _, id := range ids {
			*rows = append(*rows, paginateRow{ID: id})
		}
	}}

	return database.New(stub, func(connection *database.Builder) {
		connection.CursorCodec(codec)
	})
}

// TestCursorPaginateSignedRoundTrip verifies a signed cursor is accepted by the same codec.
func TestCursorPaginateSignedRoundTrip(t *testing.T) {
	t.Parallel()

	db := signedCursorDB(t, 1, 1, 2, 3)
	b := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	page, err := db.CursorPaginate[paginateRow](context.Background(), b, pagination.CursorParams{PerPage: 2})

	if err != nil {
		t.Fatalf("CursorPaginate() error = %v", err)
	}

	if !strings.Contains(page.NextCursor, ".") {
		t.Fatalf("NextCursor = %q, want a signed cursor", page.NextCursor)
	}

	if _, err := db.CursorPaginate[paginateRow](context.Background(), b, pagination.CursorParams{Cursor: page.NextCursor}); err != nil {
		t.Fatalf("CursorPaginate(signed cursor) error = %v", err)
	}
}

// TestCursorPaginateSignedRejectsUnsignedCursor verifies a codec refuses plain cursors.
func TestCursorPaginateSignedRejectsUnsignedCursor(t *testing.T) {
	t.Parallel()

	plain := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]paginateRow) = []paginateRow{{ID: 1}, {ID: 2}, {ID: 3}}
	}})

	b := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	page, err := plain.CursorPaginate[paginateRow](context.Background(), b, pagination.CursorParams{PerPage: 2})

	if err != nil {
		t.Fatalf("CursorPaginate() error = %v", err)
	}

	signed := signedCursorDB(t, 1, 1, 2, 3)

	_, err = signed.CursorPaginate[paginateRow](context.Background(), b, pagination.CursorParams{Cursor: page.NextCursor})

	if !errors.Is(err, pagination.ErrCursorDecode) {
		t.Fatalf("CursorPaginate() error = %v, want ErrCursorDecode", err)
	}
}

// TestCursorPaginateSignedRejectsOtherSecret verifies a cursor signed elsewhere is refused.
func TestCursorPaginateSignedRejectsOtherSecret(t *testing.T) {
	t.Parallel()

	b := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	page, err := signedCursorDB(t, 1, 1, 2, 3).CursorPaginate[paginateRow](context.Background(), b, pagination.CursorParams{PerPage: 2})

	if err != nil {
		t.Fatalf("CursorPaginate() error = %v", err)
	}

	_, err = signedCursorDB(t, 2).CursorPaginate[paginateRow](context.Background(), b, pagination.CursorParams{Cursor: page.NextCursor})

	if !errors.Is(err, pagination.ErrCursorDecode) {
		t.Fatalf("CursorPaginate() error = %v, want ErrCursorDecode", err)
	}
}

// TestPaginateCollectionItems keeps native operations on the returned window
// detached from driver storage without adjusting SQL-derived metadata.
func TestPaginateCollectionItems(t *testing.T) {
	t.Parallel()

	input := []paginateRow{{ID: 3}, {ID: 4}}
	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]paginateRow) = input
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	page, err := db.Paginate[paginateRow](t.Context(), statement, pagination.PageParams{Page: 2, PerPage: 2})

	if err != nil {
		t.Fatal(err)
	}

	ids := page.Items.Map(func(_ int, row paginateRow) int {
		return row.ID
	})

	input[0].ID = 99

	if !slices.Equal(ids, []int{3, 4}) || page.Items[0].ID != 3 {
		t.Fatalf("items = %v, IDs = %v", page.Items, ids)
	}

	if page.CurrentPage != 2 || page.PerPage != 2 {
		t.Fatalf("metadata = %+v", page)
	}
}

// TestCursorPaginateCollectionKeepsSQLAnchor checks local filtering cannot
// move the next SQL seek boundary, even when it removes the final visible row.
func TestCursorPaginateCollectionKeepsSQLAnchor(t *testing.T) {
	t.Parallel()

	input := []paginateRow{{ID: 3}, {ID: 4}, {ID: 5}}
	db := database.New(adapterStub{selectFn: func(dest any) {
		*dest.(*[]paginateRow) = input
	}})

	statement := query.Table("rows", func(statement *query.Builder) {
		statement.OrderBy("id")
	})

	page, err := db.CursorPaginate[paginateRow](t.Context(), statement, pagination.CursorParams{PerPage: 2})

	if err != nil {
		t.Fatal(err)
	}

	page.Items = page.Items.Filter(func(_ int, row paginateRow) bool {
		return row.ID == 3
	})

	payload, err := pagination.CursorDecode[map[string]any](page.NextCursor)

	if err != nil {
		t.Fatal(err)
	}

	values, ok := payload["v"].([]any)

	if !ok || len(values) != 1 {
		t.Fatalf("cursor payload = %v", payload)
	}

	if values[0] != float64(4) || page.PerPage != 2 {
		t.Fatalf("anchor = %v, page = %+v", values, page)
	}

	input[0].ID = 99

	if len(page.Items) != 1 || page.Items[0].ID != 3 {
		t.Fatalf("local items = %v", page.Items)
	}
}
