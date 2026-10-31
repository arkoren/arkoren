package suite

import (
	"errors"
	"slices"

	"arkoren.dev/core/service/database"
)

// queryTests cover Select and Find.
var queryTests = []conformanceTest{
	{"Select/ScansRows", selectScansRows},
	{"Select/NoRowsIsEmpty", selectNoRowsIsEmpty},
	{"Select/InvalidSQL", selectInvalidSQL},
	{"Select/ScansSingleColumnValues", selectScansSingleColumnValues},
	{"Find/ScansRow", findScansRow},
	{"Find/ScansScalar", findScansScalar},
	{"Find/NoRowsIsErrNoRows", findNoRows},
	{"Find/FirstOfSeveral", findFirstOfSeveral},
	{"Find/InvalidSQLIsNotErrNoRows", findInvalidSQL},
}

// selectScansRows checks positional Select into a slice of structs.
func selectScansRows(h *harness) {
	h.seed(h.driver, threeRows...)

	var rows []record

	if err := h.driver.Select(h.ctx(), h.sql("SELECT id, name FROM {t} WHERE id >= ? ORDER BY id"), &rows, 2); err != nil {
		h.t.Fatalf("Select() error = %v", err)
	}

	if want := threeRows[1:]; !slices.Equal(rows, want) {
		h.t.Errorf("Select() = %v, want %v", rows, want)
	}
}

// selectNoRowsIsEmpty checks that matching nothing is not an error.
func selectNoRowsIsEmpty(h *harness) {
	var rows []record

	if err := h.driver.Select(h.ctx(), h.sql("SELECT id, name FROM {t}"), &rows); err != nil {
		h.t.Fatalf("Select() error = %v", err)
	}

	if len(rows) != 0 {
		h.t.Errorf("Select() = %v, want no rows", rows)
	}
}

// selectInvalidSQL checks that a malformed query fails.
func selectInvalidSQL(h *harness) {
	var rows []record

	if err := h.driver.Select(h.ctx(), "NOT A QUERY", &rows); err == nil {
		h.t.Error("Select(invalid SQL) error = nil")
	}
}

// selectScansSingleColumnValues checks Select into slices of single values
// and of struct pointers.
func selectScansSingleColumnValues(h *harness) {
	h.seed(h.driver, threeRows...)

	var names []string

	if err := h.driver.Select(h.ctx(), h.sql("SELECT name FROM {t} ORDER BY id"), &names); err != nil {
		h.t.Fatalf("Select(strings) error = %v", err)
	}

	if want := []string{"one", "two", "three"}; !slices.Equal(names, want) {
		h.t.Errorf("Select(strings) = %v, want %v", names, want)
	}

	var pointers []*record

	if err := h.driver.Select(h.ctx(), h.sql("SELECT id, name FROM {t} WHERE id >= ? ORDER BY id"), &pointers, 2); err != nil {
		h.t.Fatalf("Select(pointers) error = %v", err)
	}

	if len(pointers) != 2 || *pointers[0] != threeRows[1] || *pointers[1] != threeRows[2] {
		h.t.Errorf("Select(pointers) = %v, want %v", pointers, threeRows[1:])
	}
}

// findScansRow checks Find into a struct.
func findScansRow(h *harness) {
	h.seed(h.driver, threeRows...)

	var got record

	if err := h.driver.Find(h.ctx(), h.sql("SELECT id, name FROM {t} WHERE id = ?"), &got, 2); err != nil {
		h.t.Fatalf("Find() error = %v", err)
	}

	if got != threeRows[1] {
		h.t.Errorf("Find() = %v, want %v", got, threeRows[1])
	}
}

// findScansScalar checks Find into a single-column value.
func findScansScalar(h *harness) {
	h.seed(h.driver, threeRows...)

	var name string

	if err := h.driver.Find(h.ctx(), h.sql("SELECT name FROM {t} WHERE id = ?"), &name, 3); err != nil {
		h.t.Fatalf("Find() error = %v", err)
	}

	if name != "three" {
		h.t.Errorf("Find() = %q, want %q", name, "three")
	}
}

// findNoRows checks the ErrNoRows mapping of Find.
func findNoRows(h *harness) {
	var got record

	err := h.driver.Find(h.ctx(), h.sql("SELECT id, name FROM {t} WHERE id = ?"), &got, 1)

	if !errors.Is(err, database.ErrNoRows) {
		h.t.Errorf("Find() error = %v, want ErrNoRows", err)
	}
}

// findFirstOfSeveral checks that Find scans the first of several rows.
func findFirstOfSeveral(h *harness) {
	h.seed(h.driver, threeRows...)

	var got record

	if err := h.driver.Find(h.ctx(), h.sql("SELECT id, name FROM {t} ORDER BY id"), &got); err != nil {
		h.t.Fatalf("Find() error = %v", err)
	}

	if got != threeRows[0] {
		h.t.Errorf("Find() = %v, want %v", got, threeRows[0])
	}
}

// findInvalidSQL checks that failures other than no rows keep their identity.
func findInvalidSQL(h *harness) {
	var got record

	err := h.driver.Find(h.ctx(), "NOT A QUERY", &got)

	if err == nil || errors.Is(err, database.ErrNoRows) {
		h.t.Errorf("Find(invalid SQL) error = %v, want a non-ErrNoRows error", err)
	}
}
