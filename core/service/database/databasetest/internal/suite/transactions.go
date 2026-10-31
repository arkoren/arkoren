package suite

import (
	"context"
	"errors"
	"slices"

	"arkoren.dev/core/service/database"
)

// transactionTests cover WithTransaction.
var transactionTests = []conformanceTest{
	{"Transaction/CommitsOnNil", transactionCommits},
	{"Transaction/RollsBackOnError", transactionRollsBackOnError},
	{"Transaction/RollsBackOnPanic", transactionRollsBackOnPanic},
	{"Transaction/SeesOwnWrites", transactionSeesOwnWrites},
	{"Transaction/UsableAfterRollback", transactionUsableAfterRollback},
	{"Nested/InnerErrorRollsBackInner", nestedInnerErrorRollsBackInner},
	{"Nested/InnerFailedStatementKeepsOuterUsable", nestedInnerFailedStatementKeepsOuterUsable},
	{"Nested/InnerCommitsWithOuter", nestedInnerCommitsWithOuter},
	{"Nested/OuterRollbackDiscardsInner", nestedOuterRollbackDiscardsInner},
	{"Nested/InnerPanicRollsBackInner", nestedInnerPanicRollsBackInner},
	{"Nested/CanceledContextRollsBackAll", nestedCanceledContextRollsBackAll},
}

// errRollback is returned by transaction bodies that ask for a rollback.
var errRollback = errors.New("rollback requested")

// panicValue is the value transaction bodies panic with.
const panicValue = "databasetest: panic in transaction"

// transactionCommits checks that a nil return persists the writes.
func transactionCommits(h *harness) {
	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		h.seed(tx, threeRows...)

		return nil
	})

	if err != nil {
		h.t.Fatalf("WithTransaction() error = %v", err)
	}

	if got := h.rows(h.driver); !slices.Equal(got, threeRows) {
		h.t.Errorf("rows = %v, want %v", got, threeRows)
	}
}

// transactionRollsBackOnError checks that an error discards the writes and
// is returned to the caller.
func transactionRollsBackOnError(h *harness) {
	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		h.seed(tx, threeRows...)

		return errRollback
	})

	if !errors.Is(err, errRollback) {
		h.t.Errorf("WithTransaction() error = %v, want %v", err, errRollback)
	}

	if got := h.rows(h.driver); len(got) != 0 {
		h.t.Errorf("rows after rollback = %v, want none", got)
	}
}

// transactionRollsBackOnPanic checks that a panic discards the writes and
// propagates unchanged.
func transactionRollsBackOnPanic(h *harness) {
	var recovered any

	func() {
		defer func() {
			recovered = recover()
		}()

		_ = h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
			h.seed(tx, threeRows...)
			panic(panicValue)
		})
	}()

	if recovered != panicValue {
		h.t.Errorf("recovered = %v, want %v", recovered, panicValue)
	}

	if got := h.rows(h.driver); len(got) != 0 {
		h.t.Errorf("rows after panic = %v, want none", got)
	}
}

// transactionSeesOwnWrites checks reads inside the transaction.
func transactionSeesOwnWrites(h *harness) {
	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		h.seed(tx, threeRows[0])

		var got record

		if err := tx.Find(h.ctx(), h.sql("SELECT id, name FROM {t} WHERE id = ?"), &got, 1); err != nil {
			return err
		}

		if got != threeRows[0] {
			h.t.Errorf("Find() in transaction = %v, want %v", got, threeRows[0])
		}

		return nil
	})

	if err != nil {
		h.t.Fatalf("WithTransaction() error = %v", err)
	}
}

// transactionUsableAfterRollback checks that a rollback frees the driver.
func transactionUsableAfterRollback(h *harness) {
	_ = h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		h.seed(tx, threeRows[0])

		return errRollback
	})

	h.seed(h.driver, threeRows[0])

	if got := h.rows(h.driver); len(got) != 1 {
		h.t.Errorf("rows = %v, want 1 row", got)
	}
}

// nestedInnerErrorRollsBackInner checks that a failing nested transaction
// discards only its own writes and that the outer one can still commit.
func nestedInnerErrorRollsBackInner(h *harness) {
	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		h.seed(tx, threeRows[0])

		inner := tx.WithTransaction(h.ctx(), func(inner database.Driver) error {
			h.seed(inner, threeRows[1])

			return errRollback
		})

		if !errors.Is(inner, errRollback) {
			h.t.Errorf("nested WithTransaction() error = %v, want %v", inner, errRollback)
		}

		return nil
	})

	if err != nil {
		h.t.Fatalf("WithTransaction() error = %v", err)
	}

	if got, want := h.rows(h.driver), threeRows[:1]; !slices.Equal(got, want) {
		h.t.Errorf("rows = %v, want %v", got, want)
	}
}

// nestedInnerFailedStatementKeepsOuterUsable checks that a statement failing
// inside a nested transaction leaves the outer transaction usable. PostgreSQL
// aborts a transaction on any failed statement until it rolls back, so the
// nested rollback must recover it.
func nestedInnerFailedStatementKeepsOuterUsable(h *harness) {
	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		h.seed(tx, threeRows[0])

		inner := tx.WithTransaction(h.ctx(), func(inner database.Driver) error {
			_, err := inner.Exec(h.ctx(), h.sql("INSERT INTO {t} (id, name) VALUES (?, ?)"), threeRows[0].ID, threeRows[0].Name)

			return err
		})

		if inner == nil {
			h.t.Error("nested duplicate insert succeeded, want an error")
		}

		h.seed(tx, threeRows[1])

		return nil
	})

	if err != nil {
		h.t.Fatalf("WithTransaction() error = %v", err)
	}

	if got, want := h.rows(h.driver), threeRows[:2]; !slices.Equal(got, want) {
		h.t.Errorf("rows = %v, want %v", got, want)
	}
}

// nestedInnerCommitsWithOuter checks that a nested transaction's writes
// persist once the outer transaction commits, and are visible to it before.
func nestedInnerCommitsWithOuter(h *harness) {
	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		h.seed(tx, threeRows[0])

		inner := tx.WithTransaction(h.ctx(), func(inner database.Driver) error {
			h.seed(inner, threeRows[1])

			return nil
		})

		if inner != nil {
			return inner
		}

		if got, want := h.rows(tx), threeRows[:2]; !slices.Equal(got, want) {
			h.t.Errorf("rows in outer = %v, want %v", got, want)
		}

		return nil
	})

	if err != nil {
		h.t.Fatalf("WithTransaction() error = %v", err)
	}

	if got, want := h.rows(h.driver), threeRows[:2]; !slices.Equal(got, want) {
		h.t.Errorf("rows = %v, want %v", got, want)
	}
}

// nestedOuterRollbackDiscardsInner checks that a nested transaction that
// succeeded is still discarded when the outer one rolls back.
func nestedOuterRollbackDiscardsInner(h *harness) {
	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		h.seed(tx, threeRows[0])

		inner := tx.WithTransaction(h.ctx(), func(inner database.Driver) error {
			h.seed(inner, threeRows[1])

			return nil
		})

		if inner != nil {
			return inner
		}

		return errRollback
	})

	if !errors.Is(err, errRollback) {
		h.t.Fatalf("WithTransaction() error = %v, want %v", err, errRollback)
	}

	if got := h.rows(h.driver); len(got) != 0 {
		h.t.Errorf("rows after outer rollback = %v, want none", got)
	}
}

// nestedInnerPanicRollsBackInner checks that a panic in a nested transaction
// discards its writes and propagates, so the outer transaction sees it and
// can recover and commit its own work.
func nestedInnerPanicRollsBackInner(h *harness) {
	var recovered any

	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		h.seed(tx, threeRows[0])

		func() {
			defer func() {
				recovered = recover()
			}()

			_ = tx.WithTransaction(h.ctx(), func(inner database.Driver) error {
				h.seed(inner, threeRows[1])
				panic(panicValue)
			})
		}()

		return nil
	})

	if err != nil {
		h.t.Fatalf("WithTransaction() error = %v", err)
	}

	if recovered != panicValue {
		h.t.Errorf("recovered = %v, want %v", recovered, panicValue)
	}

	if got, want := h.rows(h.driver), threeRows[:1]; !slices.Equal(got, want) {
		h.t.Errorf("rows = %v, want %v", got, want)
	}
}

// nestedCanceledContextRollsBackAll checks that canceling the transaction's
// context inside a nested transaction discards all of it, even when every
// callback returns nil: the outermost WithTransaction must fail instead of
// committing.
func nestedCanceledContextRollsBackAll(h *harness) {
	ctx, cancel := context.WithCancel(h.ctx())
	defer cancel()

	err := h.driver.WithTransaction(ctx, func(tx database.Driver) error {
		h.seed(tx, threeRows[0])

		// The nested result is ignored on purpose: the outer commit must
		// fail on its own.
		_ = tx.WithTransaction(ctx, func(inner database.Driver) error {
			h.seed(inner, threeRows[1])
			cancel()

			return nil
		})

		return nil
	})

	if err == nil {
		h.t.Error("WithTransaction() committed after its context was canceled")
	}

	if got := h.rows(h.driver); len(got) != 0 {
		h.t.Errorf("rows after cancel = %v, want none", got)
	}
}
