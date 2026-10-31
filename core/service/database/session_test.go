package database_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
)

// TestSessionContextExpires verifies callback lifetime is enforced by the
// facade even when a test driver does not invalidate its callback driver.
func TestSessionContextExpires(t *testing.T) {
	t.Parallel()

	db := database.New(&txDriver{log: new([]string)})
	var escaped context.Context

	if err := db.WithSession(t.Context(), func(ctx context.Context) error {
		escaped = ctx

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(escaped, query.Raw("DELETE FROM t")); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("Exec = %v, want ErrSessionDone", err)
	}

	if err := db.AfterCommit(escaped, func(context.Context) error {
		t.Fatal("hook ran on an ended session")

		return nil
	}); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("AfterCommit = %v, want ErrSessionDone", err)
	}
}

// TestSessionPanicExpiresContext verifies the facade's lifetime guard unwinds
// before a driver's panic escapes, without changing the original panic value.
func TestSessionPanicExpiresContext(t *testing.T) {
	t.Parallel()

	db := database.New(&txDriver{log: new([]string)})
	var escaped context.Context
	func() {
		defer func() {
			p, ok := recover().(error)

			if !ok || !errors.Is(p, errBoom) {
				t.Fatalf("panic = %v", p)
			}
		}()
		_ = db.WithSession(t.Context(), func(ctx context.Context) error {
			escaped = ctx
			panic(errBoom)
		})
	}()

	if _, err := db.Exec(escaped, query.Raw("DELETE FROM t")); !errors.Is(err, database.ErrSessionDone) {
		t.Fatalf("escaped panic context = %v", err)
	}
}

// TestSessionRoutesEveryOperation checks matching facades and lazy operations
// use the callback driver while unrelated roots keep their own binding.
func TestSessionRoutesEveryOperation(t *testing.T) {
	t.Parallel()

	var log []string
	root := &txDriver{log: &log}
	db, joined := database.New(root), database.New(root)

	err := db.WithSession(t.Context(), func(ctx context.Context) error {
		if _, err := joined.Exec(ctx, query.Raw("DELETE FROM t")); err != nil {
			return err
		}

		if _, err := db.Select[int](ctx, query.Raw("SELECT 1")); err != nil {
			return err
		}

		if _, err := db.Find[int](ctx, query.Raw("SELECT 1")); err != nil {
			return err
		}

		if _, err := db.Cursor[int](ctx, query.Raw("SELECT 1")).Items(); !errors.Is(err, errNoCursor) {
			t.Fatalf("cursor error = %v", err)
		}

		ordered := query.Table("t", func(statement *query.Builder) {
			statement.OrderBy("id")
		})

		if _, err := db.Paginate[paginateRow](ctx, ordered, pagination.PageParams{}); err != nil {
			return err
		}

		if _, err := db.CursorPaginate[paginateRow](ctx, ordered, pagination.CursorParams{}); err != nil {
			return err
		}

		if _, err := db.Each[paginateRow](ctx, ordered, 10).Items(); err != nil {
			return err
		}

		if _, err := db.Lock(ctx, "one"); err != nil {
			return err
		}

		if _, _, err := db.TryLock(ctx, "two"); err != nil {
			return err
		}

		if db.Driver() != root {
			t.Fatal("Driver accessor did not retain root")
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range log {
		if entry[len(entry)-1] != '1' {
			t.Fatalf("operation bypassed session: %v", log)
		}
	}
}

// TestSessionTransactionOrdering verifies borrowed sessions preserve nesting,
// hooks see the live enclosing session, and finished inner contexts stay dead.
func TestSessionTransactionOrdering(t *testing.T) {
	t.Parallel()

	var log []string
	db := database.New(&txDriver{log: &log})

	err := db.WithSession(t.Context(), func(ctx context.Context) error {
		var ended context.Context

		if err := db.WithTransaction(ctx, func(ctx context.Context) error {
			ended = ctx

			return db.WithSession(ctx, func(ctx context.Context) error {
				return db.AfterCommit(ctx, func(ctx context.Context) error {
					_, err := db.Exec(ctx, query.Raw("DELETE FROM t"))

					return err
				})
			})
		}); err != nil {
			return err
		}

		if _, err := db.Exec(ended, query.Raw("DELETE FROM t")); !errors.Is(err, sql.ErrTxDone) {
			t.Fatalf("ended transaction = %v", err)
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	want := []string{"session@1", "begin@2", "session@3", "release@3", "commit@2", "exec@1", "release@1"}

	if !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// TestSessionMatchingPreservesOtherRoots checks lexical interleaving does not
// replace an earlier driver's scope and uncomparable drivers keep facade identity.
func TestSessionMatchingPreservesOtherRoots(t *testing.T) {
	t.Parallel()

	var log []string
	root := uncomparableDriver{txDriver: &txDriver{log: &log}}
	first, notJoined := database.New(root), database.New(root)
	other := database.New(&txDriver{log: &log})

	err := first.WithSession(t.Context(), func(ctx context.Context) error {
		return other.WithTransaction(ctx, func(ctx context.Context) error {
			if _, err := first.Exec(ctx, query.Raw("DELETE FROM a")); err != nil {
				return err
			}

			_, err := notJoined.Exec(ctx, query.Raw("DELETE FROM b"))

			return err
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	want := []string{"session@1", "begin@1", "exec@1", "exec@0", "commit@1", "release@1"}

	if !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// postCommitDriver simulates successful commit followed by lock cleanup failure.
// It keeps the callback's own failure distinct from this post-commit outcome.
type postCommitDriver struct {
	// txDriver records the actual callback outcome and native commit ordering.
	*txDriver
}

// WithTransaction returns a post-commit marker only after the body succeeds.
// A body-provided marker remains a rollback request through the embedded fake.
func (driver *postCommitDriver) WithTransaction(ctx context.Context, fn func(database.Driver) error) error {
	if err := driver.txDriver.WithTransaction(ctx, fn); err != nil {
		return err
	}

	return fmt.Errorf("%w: %w", database.ErrAfterCommit, errBoom)
}

// WithSession keeps the post-commit failure wrapper visible in callback work.
// This fake has no native connection or session resource to release.
func (driver *postCommitDriver) WithSession(_ context.Context, fn func(database.Driver) error) error {
	return fn(driver)
}

// TestPostCommitCleanupRunsHooksAndCommittedEvent verifies the driver cleanup
// marker does not turn committed data into a rollback event or discard hooks.
func TestPostCommitCleanupRunsHooksAndCommittedEvent(t *testing.T) {
	t.Parallel()

	var log []string
	bus := event.New()

	if _, err := bus.Listen(database.TransactionCommitted, func(context.Context, database.TransactionEvent) error {
		log = append(log, "event")

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := bus.Listen(database.TransactionRolledBack, func(context.Context, database.TransactionEvent) error {
		t.Fatal("committed cleanup emitted rollback")

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	db := database.New(&postCommitDriver{
		txDriver: &txDriver{log: &log},
	}, func(connection *database.Builder) {
		connection.Events(bus)
	})

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		return db.AfterCommit(ctx, hookLog(&log, "hook"))
	})

	if !errors.Is(err, database.ErrAfterCommit) || !errors.Is(err, errBoom) {
		t.Fatalf("postcommit error = %v", err)
	}

	want := []string{"begin@1", "commit@1", "event", "hook"}

	if !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// TestBodyAfterCommitMarkerStillRollsBack prevents an error originating from
// another transaction's hook from being mistaken for this transaction's commit.
func TestBodyAfterCommitMarkerStillRollsBack(t *testing.T) {
	t.Parallel()

	db, seen := transactionEvents(t)

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		if err := db.AfterCommit(ctx, func(context.Context) error {
			t.Fatal("rollback ran hook")

			return nil
		}); err != nil {
			return err
		}

		return fmt.Errorf("%w: %w", database.ErrAfterCommit, errBoom)
	})

	if !errors.Is(err, errBoom) || len(*seen) != 1 || (*seen)[0] != "rolled-back" {
		t.Fatalf("error = %v, events = %v", err, *seen)
	}
}

// TestEndedNestedSessionDoesNotFallBack verifies lexical completion blocks all
// facade paths while the enclosing session is still usable with its own context.
func TestEndedNestedSessionDoesNotFallBack(t *testing.T) {
	t.Parallel()

	db := database.New(&txDriver{log: new([]string)})

	err := db.WithSession(t.Context(), func(ctx context.Context) error {
		var ended context.Context

		if err := db.WithSession(ctx, func(ctx context.Context) error {
			ended = ctx

			return nil
		}); err != nil {
			return err
		}

		if _, err := db.Cursor[int](ended, query.Raw("SELECT 1")).Items(); !errors.Is(err, database.ErrSessionDone) {
			t.Fatalf("ended lazy cursor = %v", err)
		}

		if _, err := db.Lock(ended, "name"); !errors.Is(err, database.ErrSessionDone) {
			t.Fatalf("ended Lock = %v", err)
		}

		if _, _, err := db.TryLock(ended, "name"); !errors.Is(err, database.ErrSessionDone) {
			t.Fatalf("ended TryLock = %v", err)
		}

		callback := func(context.Context) error {
			t.Fatal("ended context invoked callback")

			return nil
		}

		if err := db.WithSession(ended, callback); !errors.Is(err, database.ErrSessionDone) {
			t.Fatalf("ended WithSession = %v", err)
		}

		if err := db.WithTransaction(ended, callback); !errors.Is(err, database.ErrSessionDone) {
			t.Fatalf("ended WithTransaction = %v", err)
		}

		_, err := db.Exec(ctx, query.Raw("DELETE FROM t"))

		return err
	})

	if err != nil {
		t.Fatal(err)
	}
}
