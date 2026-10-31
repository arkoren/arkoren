package suite

import (
	"slices"

	"arkoren.dev/core/service/database"
)

// cursorTests cover Query.
var cursorTests = []conformanceTest{
	{"Query/IteratesRows", queryIteratesRows},
	{"Query/ScansScalar", queryScansScalar},
	{"Query/EmptyResult", queryEmptyResult},
	{"Query/InvalidSQL", queryInvalidSQL},
	{"Query/CloseIsIdempotent", queryCloseIsIdempotent},
	{"Query/EarlyCloseReleasesConnection", queryEarlyClose},
}

// collect drains rows into records, closing them, and checks the cursor
// contract: Err is nil after exhaustion, Close succeeds, and Next stays
// false afterwards.
func collect(h *harness, rows database.Rows) []record {
	h.t.Helper()

	var got []record

	for rows.Next() {
		var row record

		if err := rows.Scan(&row); err != nil {
			h.t.Fatalf("Scan() error = %v", err)
		}

		got = append(got, row)
	}

	if err := rows.Err(); err != nil {
		h.t.Errorf("Err() after exhaustion = %v, want nil", err)
	}

	if err := rows.Close(); err != nil {
		h.t.Errorf("Close() error = %v", err)
	}

	if rows.Next() {
		h.t.Error("Next() = true after Close")
	}

	return got
}

// queryIteratesRows checks positional cursors.
func queryIteratesRows(h *harness) {
	h.seed(h.driver, threeRows...)

	rows, err := h.driver.Query(h.ctx(), h.sql("SELECT id, name FROM {t} WHERE id <= ? ORDER BY id"), 2)

	if err != nil {
		h.t.Fatalf("Query() error = %v", err)
	}

	if got, want := collect(h, rows), threeRows[:2]; !slices.Equal(got, want) {
		h.t.Errorf("Query() rows = %v, want %v", got, want)
	}
}

// queryScansScalar checks Scan into a single-column value.
func queryScansScalar(h *harness) {
	h.seed(h.driver, threeRows...)

	rows, err := h.driver.Query(h.ctx(), h.sql("SELECT name FROM {t} ORDER BY id"))

	if err != nil {
		h.t.Fatalf("Query() error = %v", err)
	}

	defer rows.Close()

	var names []string

	for rows.Next() {
		var name string

		if err := rows.Scan(&name); err != nil {
			h.t.Fatalf("Scan() error = %v", err)
		}

		names = append(names, name)
	}

	if want := []string{"one", "two", "three"}; !slices.Equal(names, want) {
		h.t.Errorf("names = %v, want %v", names, want)
	}
}

// queryEmptyResult checks a cursor over no rows.
func queryEmptyResult(h *harness) {
	rows, err := h.driver.Query(h.ctx(), h.sql("SELECT id, name FROM {t}"))

	if err != nil {
		h.t.Fatalf("Query() error = %v", err)
	}

	if got := collect(h, rows); len(got) != 0 {
		h.t.Errorf("Query() rows = %v, want none", got)
	}
}

// queryInvalidSQL checks that a malformed query fails, at Query or at Err.
func queryInvalidSQL(h *harness) {
	rows, err := h.driver.Query(h.ctx(), "NOT A QUERY")

	if err != nil {
		return
	}

	defer rows.Close()

	if rows.Next() || rows.Err() == nil {
		h.t.Error("Query(invalid SQL) reported no error")
	}
}

// queryCloseIsIdempotent checks that Close can be repeated.
func queryCloseIsIdempotent(h *harness) {
	h.seed(h.driver, threeRows...)

	rows, err := h.driver.Query(h.ctx(), h.sql("SELECT id, name FROM {t}"))

	if err != nil {
		h.t.Fatalf("Query() error = %v", err)
	}

	for i := range 2 {
		if err := rows.Close(); err != nil {
			h.t.Errorf("Close() call %d error = %v", i+1, err)
		}
	}

	if rows.Next() {
		h.t.Error("Next() = true after Close")
	}
}

// queryEarlyClose checks that a cursor abandoned mid-way leaves the driver
// usable.
func queryEarlyClose(h *harness) {
	h.seed(h.driver, threeRows...)

	rows, err := h.driver.Query(h.ctx(), h.sql("SELECT id, name FROM {t} ORDER BY id"))

	if err != nil {
		h.t.Fatalf("Query() error = %v", err)
	}

	if !rows.Next() {
		h.t.Fatalf("Next() = false, Err() = %v", rows.Err())
	}

	if err := rows.Close(); err != nil {
		h.t.Fatalf("Close() error = %v", err)
	}

	if got := h.rows(h.driver); len(got) != 3 {
		h.t.Errorf("rows after early Close = %v, want 3 rows", got)
	}
}
