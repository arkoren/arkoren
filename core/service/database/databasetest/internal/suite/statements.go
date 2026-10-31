package suite

import (
	"context"
	"errors"

	"arkoren.dev/core/service/database"
)

// statementTests cover Exec and context cancellation.
var statementTests = []conformanceTest{
	{"Exec/ReportsRowsAffected", execReportsRowsAffected},
	{"Exec/InvalidSQL", execInvalidSQL},
	{"Context/CanceledBeforeCall", contextCanceledBeforeCall},
}

// execReportsRowsAffected checks the count for insert, update, and delete.
func execReportsRowsAffected(h *harness) {
	h.seed(h.driver, threeRows...)

	update, err := h.driver.Exec(h.ctx(), h.sql("UPDATE {t} SET name = ?"), "changed")

	if err != nil || update != 3 {
		h.t.Errorf("update all: affected = %d, error = %v, want 3", update, err)
	}

	none, err := h.driver.Exec(h.ctx(), h.sql("UPDATE {t} SET name = ? WHERE id = ?"), "x", 99)

	if err != nil || none != 0 {
		h.t.Errorf("update none: affected = %d, error = %v, want 0", none, err)
	}

	deleted, err := h.driver.Exec(h.ctx(), h.sql("DELETE FROM {t} WHERE id > ?"), 1)

	if err != nil || deleted != 2 {
		h.t.Errorf("delete: affected = %d, error = %v, want 2", deleted, err)
	}
}

// execInvalidSQL checks that a malformed statement fails.
func execInvalidSQL(h *harness) {
	if _, err := h.driver.Exec(h.ctx(), "NOT A STATEMENT"); err == nil {
		h.t.Error("Exec(invalid SQL) error = nil")
	}
}

// contextCanceledBeforeCall checks that every operation reports a context
// that ended before the call, and that Find does not mistake it for no rows.
func contextCanceledBeforeCall(h *harness) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var rows []record

	var one record

	selectAll := h.sql("SELECT id, name FROM {t}")

	calls := map[string]func() error{
		"Exec": func() error {
			_, err := h.driver.Exec(ctx, h.sql("DELETE FROM {t}"))

			return err
		},
		"Select": func() error {
			return h.driver.Select(ctx, selectAll, &rows)
		},
		"Find": func() error {
			return h.driver.Find(ctx, selectAll, &one)
		},
		"Query": func() error {
			return closeRows(h.driver.Query(ctx, selectAll))
		},
		"WithTransaction": func() error {
			return h.driver.WithTransaction(ctx, func(database.Driver) error {
				return nil
			})
		},
		"WithSession": func() error {
			return h.driver.WithSession(ctx, func(database.Driver) error {
				return nil
			})
		},
		"Lock": func() error {
			_, err := h.driver.Lock(ctx, h.lockName("canceled"))

			return err
		},
		"TryLock": func() error {
			_, _, err := h.driver.TryLock(ctx, h.lockName("trycanceled"))

			return err
		},
	}

	for name, call := range calls {
		err := call()

		if !errors.Is(err, context.Canceled) {
			h.t.Errorf("%s with canceled context: error = %v, want context.Canceled", name, err)
		}

		if errors.Is(err, database.ErrNoRows) {
			h.t.Errorf("%s with canceled context: error matches ErrNoRows", name)
		}
	}
}

// closeRows closes rows when the query succeeded and returns the query error
// or, failing that, the close error.
func closeRows(rows database.Rows, err error) error {
	if err != nil {
		return err
	}

	return rows.Close()
}
