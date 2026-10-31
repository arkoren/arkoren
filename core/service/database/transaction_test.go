package database_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/pagination"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
)

// errBoom is returned by transaction bodies that ask for a rollback.
var errBoom = errors.New("boom")

// txDriver is a comparable fake that logs the operations it receives, tagged
// with the transaction level they ran on.
type txDriver struct {
	// log receives one entry per operation and transaction event.
	log *[]string

	// level is zero for the root driver and the nesting depth otherwise.
	level int
}

// event appends an entry tagged with the driver level.
func (driver *txDriver) event(name string) {
	*driver.log = append(*driver.log, name+"@"+string(rune('0'+driver.level)))
}

// Exec logs the call.
func (driver *txDriver) Exec(context.Context, string, ...any) (int64, error) {
	driver.event("exec")

	return 1, nil
}

// Select logs the call and returns no rows.
func (driver *txDriver) Select(context.Context, string, any, ...any) error {
	driver.event("select")

	return nil
}

// Query logs the call and ends the cursor with an error.
func (driver *txDriver) Query(context.Context, string, ...any) (database.Rows, error) {
	driver.event("query")

	return nil, errNoCursor
}

// Dialect reports the ANSI dialect.
func (driver *txDriver) Dialect() query.Dialect {
	return query.ANSI
}

// Find logs the call.
func (driver *txDriver) Find(context.Context, string, any, ...any) error {
	driver.event("find")

	return nil
}

// Lock logs the call and returns a no-op unlock.
func (driver *txDriver) Lock(context.Context, string) (func(context.Context) error, error) {
	driver.event("lock")

	return func(context.Context) error {
		return nil
	}, nil
}

// TryLock logs the call and reports the lock as acquired with a no-op unlock.
func (driver *txDriver) TryLock(context.Context, string) (func(context.Context) error, bool, error) {
	driver.event("trylock")

	return func(context.Context) error {
		return nil
	}, true, nil
}

// WithTransaction logs begin and commit or rollback around fn, which runs on
// a driver one level deeper. A panic in fn is logged as a rollback.
func (driver *txDriver) WithTransaction(_ context.Context, fn func(database.Driver) error) (err error) {
	inner := &txDriver{log: driver.log, level: driver.level + 1}
	inner.event("begin")

	defer func() {
		if recover := recover(); recover != nil {
			inner.event("rollback")
			panic(recover)
		}
	}()

	if err = fn(inner); err != nil {
		inner.event("rollback")

		return err
	}

	inner.event("commit")

	return nil
}

// uncomparableDriver embeds a slice, so comparing two values panics.
type uncomparableDriver struct {
	// txDriver supplies transaction behavior for an uncomparable wrapper value.
	*txDriver

	// _ makes the struct uncomparable.
	_ []int
}

// WithSession records reservation around fn without adding a transaction.
// Each callback gets a distinct driver level to make routing observable.
func (driver *txDriver) WithSession(_ context.Context, fn func(database.Driver) error) error {
	inner := &txDriver{log: driver.log, level: driver.level + 1}
	inner.event("session")
	defer inner.event("release")

	return fn(inner)
}

// TestWithTransactionCommitsAndRollsBackThroughContext checks successful work
// commits and the callback context routes operations to the transaction driver.
func TestWithTransactionCommitsAndRollsBackThroughContext(t *testing.T) {
	t.Parallel()

	var log []string
	db := database.New(&txDriver{log: &log})

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		if _, err := db.Exec(ctx, query.Raw("DELETE FROM t")); err != nil {
			t.Fatal(err)
		}

		return nil
	})

	if !errors.Is(err, nil) {
		t.Fatalf("%s: error = %v, want %v", "commit", err, nil)
	}

	want := []string{"begin@1", "exec@1", "commit@1"}

	if !slices.Equal(log, want) {
		t.Fatalf("%s: log = %v, want %v", "commit", log, want)
	}
}

// TestWithTransactionRollsBackThroughContext checks failed work rolls back
// after executing against the callback's transaction driver.
func TestWithTransactionRollsBackThroughContext(t *testing.T) {
	t.Parallel()

	var log []string
	db := database.New(&txDriver{log: &log})

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		if _, err := db.Exec(ctx, query.Raw("DELETE FROM t")); err != nil {
			t.Fatal(err)
		}

		return errBoom
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("%s: error = %v, want %v", "rollback", err, errBoom)
	}

	want := []string{"begin@1", "exec@1", "rollback@1"}

	if !slices.Equal(log, want) {
		t.Fatalf("%s: log = %v, want %v", "rollback", log, want)
	}
}

// TestOperationsOutsideTransactionUseRootDriver verifies a context without a
// transaction, and Driver, stay on the root driver.
func TestOperationsOutsideTransactionUseRootDriver(t *testing.T) {
	t.Parallel()

	var log []string

	root := &txDriver{log: &log}
	db := database.New(root)

	err := db.WithTransaction(t.Context(), func(context.Context) error {
		if _, err := db.Exec(t.Context(), query.Raw("DELETE FROM t")); err != nil {
			return err
		}

		if db.Driver() != database.Driver(root) {
			t.Error("Driver() did not return the root driver")
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"begin@1", "exec@0", "commit@1"}; !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// TestDatabasesOnOneDriverJoin verifies a second Database over the same
// driver joins the transaction, and one over another driver does not.
func TestDatabasesOnOneDriverJoin(t *testing.T) {
	t.Parallel()

	var log []string

	root := &txDriver{log: &log}
	first, second := database.New(root), database.New(root)
	other := database.New(&txDriver{log: &log})

	err := first.WithTransaction(t.Context(), func(ctx context.Context) error {
		if _, err := second.Exec(ctx, query.Raw("DELETE FROM a")); err != nil {
			return err
		}

		_, err := other.Exec(ctx, query.Raw("DELETE FROM b"))

		return err
	})

	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"begin@1", "exec@1", "exec@0", "commit@1"}; !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// TestUncomparableDriverJoinsThroughSameDatabase verifies an uncomparable
// driver does not panic and joins only through the same Database.
func TestUncomparableDriverJoinsThroughSameDatabase(t *testing.T) {
	t.Parallel()

	var log []string

	root := uncomparableDriver{txDriver: &txDriver{log: &log}}
	db := database.New(root)
	other := database.New(root)

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		if _, err := db.Exec(ctx, query.Raw("DELETE FROM a")); err != nil {
			return err
		}

		_, err := other.Exec(ctx, query.Raw("DELETE FROM b"))

		return err
	})

	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"begin@1", "exec@1", "exec@0", "commit@1"}; !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// TestNestedWithTransactionReachesTransactionDriver verifies a nested call
// starts a nested transaction on the transaction driver and its context joins.
func TestNestedWithTransactionReachesTransactionDriver(t *testing.T) {
	t.Parallel()

	var log []string

	db := database.New(&txDriver{log: &log})

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		inner := db.WithTransaction(ctx, func(ctx context.Context) error {
			_, err := db.Exec(ctx, query.Raw("DELETE FROM t"))

			return err
		})

		if inner != nil {
			return inner
		}

		_, err := db.Exec(ctx, query.Raw("DELETE FROM t"))

		return err
	})

	if err != nil {
		t.Fatal(err)
	}

	want := []string{"begin@1", "begin@2", "exec@2", "commit@2", "exec@1", "commit@1"}

	if !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// TestNestedPanicReachesOuter verifies a panic in a nested transaction is
// re-raised through WithTransaction.
func TestNestedPanicReachesOuter(t *testing.T) {
	t.Parallel()

	var log []string

	db := database.New(&txDriver{log: &log})

	defer func() {
		recovered, _ := recover().(error)

		if !errors.Is(recovered, errBoom) {
			t.Fatalf("recovered = %v, want %v", recovered, errBoom)
		}

		if want := []string{"begin@1", "begin@2", "rollback@2", "rollback@1"}; !slices.Equal(log, want) {
			t.Fatalf("log = %v, want %v", log, want)
		}
	}()

	_ = db.WithTransaction(t.Context(), func(ctx context.Context) error {
		return db.WithTransaction(ctx, func(context.Context) error {
			panic(errBoom)
		})
	})
}

// TestPaginateEachAndLockRunInTransaction verifies the paged reads and Lock
// resolve the transaction driver.
func TestPaginateEachAndLockRunInTransaction(t *testing.T) {
	t.Parallel()

	var log []string

	db := database.New(&txDriver{log: &log})

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		ordered := query.Table("rows", func(statement *query.Builder) {
			statement.OrderBy("id")
		})

		if _, err := db.Paginate[paginateRow](ctx, ordered, pagination.PageParams{}); err != nil {
			return err
		}

		if _, err := db.Each[paginateRow](ctx, ordered, 10).Items(); err != nil {
			return err
		}

		_, err := db.Lock(ctx, "name")

		return err
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range log[1 : len(log)-1] {
		if entry[len(entry)-1] != '1' {
			t.Fatalf("log = %v, want every operation on level 1", log)
		}
	}
}

// TestTransactionsOfDifferentDriversEncloseEachOther verifies that starting a
// transaction on another driver keeps the enclosing one in the context.
func TestTransactionsOfDifferentDriversEncloseEachOther(t *testing.T) {
	t.Parallel()

	var firstLog, otherLog []string

	first := database.New(&txDriver{log: &firstLog})
	other := database.New(&txDriver{log: &otherLog})

	err := first.WithTransaction(t.Context(), func(ctx context.Context) error {
		return other.WithTransaction(ctx, func(ctx context.Context) error {
			if _, err := first.Exec(ctx, query.Raw("DELETE FROM a")); err != nil {
				return err
			}

			_, err := other.Exec(ctx, query.Raw("DELETE FROM b"))

			return err
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"begin@1", "exec@1", "commit@1"}; !slices.Equal(firstLog, want) {
		t.Fatalf("first log = %v, want %v", firstLog, want)
	}

	if want := []string{"begin@1", "exec@1", "commit@1"}; !slices.Equal(otherLog, want) {
		t.Fatalf("other log = %v, want %v", otherLog, want)
	}
}

// hookLog returns a hook that appends name to log.
func hookLog(log *[]string, name string) func(context.Context) error {
	return func(context.Context) error {
		*log = append(*log, name)

		return nil
	}
}

// register calls AfterCommit and fails the test on an error.
func register(t *testing.T, db *database.Database, ctx context.Context, fn func(context.Context) error) {
	t.Helper()

	if err := db.AfterCommit(ctx, fn); err != nil {
		t.Fatal(err)
	}
}

// TestAfterCommitOutsideTransactionRunsImmediately verifies a context without
// a transaction runs the hook at once with that context.
func TestAfterCommitOutsideTransactionRunsImmediately(t *testing.T) {
	t.Parallel()

	var log []string

	db := database.New(&txDriver{log: &log})
	register(t, db, t.Context(), hookLog(&log, "hook"))

	if want := []string{"hook"}; !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// TestAfterCommitRunsAfterCommitInOrder verifies hooks wait for the commit and
// run in registration order before WithTransaction returns.
func TestAfterCommitRunsAfterCommitInOrder(t *testing.T) {
	t.Parallel()

	var log []string

	db := database.New(&txDriver{log: &log})

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		register(t, db, ctx, hookLog(&log, "first"))
		register(t, db, ctx, hookLog(&log, "second"))

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"begin@1", "commit@1", "first", "second"}; !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// TestAfterCommitDiscardedOnRollback verifies an error, a panic, and a failed
// commit discard the hooks.
func TestAfterCommitDiscardedOnRollback(t *testing.T) {
	t.Parallel()

	var log []string

	db := database.New(&txDriver{log: &log})

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		register(t, db, ctx, hookLog(&log, "error"))

		return errBoom
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want %v", err, errBoom)
	}

	func() {
		defer func() {
			if recovered, _ := recover().(error); !errors.Is(recovered, errBoom) {
				t.Fatalf("recovered = %v, want %v", recovered, errBoom)
			}
		}()

		_ = db.WithTransaction(t.Context(), func(ctx context.Context) error {
			register(t, db, ctx, hookLog(&log, "panic"))
			panic(errBoom)
		})
	}()

	failing := database.New(&failingCommitDriver{txDriver: &txDriver{log: &log}})

	err = failing.WithTransaction(t.Context(), func(ctx context.Context) error {
		register(t, failing, ctx, hookLog(&log, "commit"))

		return nil
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("commit error = %v, want %v", err, errBoom)
	}

	for _, entry := range log {
		if !strings.Contains(entry, "@") {
			t.Fatalf("log = %v, want no hook entries", log)
		}
	}
}

// failingCommitDriver runs the body on the embedded driver, then fails as if
// the commit failed.
type failingCommitDriver struct {
	// txDriver records transaction activity before the simulated commit failure.
	*txDriver
}

// WithTransaction returns errBoom after a body that returned nil.
func (driver *failingCommitDriver) WithTransaction(ctx context.Context, fn func(database.Driver) error) error {
	if err := driver.txDriver.WithTransaction(ctx, fn); err != nil {
		return err
	}

	return errBoom
}

// WithSession preserves commit-failure behavior in this non-I/O fake instead
// of passing the embedded plain driver to a session callback.
func (driver *failingCommitDriver) WithSession(_ context.Context, fn func(database.Driver) error) error {
	return fn(driver)
}

// TestAfterCommitNestedTransactions verifies nested hooks wait for the
// outermost commit and a nested rollback discards only its own hooks.
func TestAfterCommitNestedTransactions(t *testing.T) {
	t.Parallel()

	var log []string

	db := database.New(&txDriver{log: &log})

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		register(t, db, ctx, hookLog(&log, "outer-1"))

		err := db.WithTransaction(ctx, func(ctx context.Context) error {
			register(t, db, ctx, hookLog(&log, "nested-ok"))

			return nil
		})

		if err != nil {
			return err
		}

		err = db.WithTransaction(ctx, func(ctx context.Context) error {
			register(t, db, ctx, hookLog(&log, "nested-rolled-back"))

			return errBoom
		})

		if !errors.Is(err, errBoom) {
			t.Fatalf("nested error = %v, want %v", err, errBoom)
		}

		register(t, db, ctx, hookLog(&log, "outer-2"))

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"begin@1", "begin@2", "commit@2", "begin@2", "rollback@2", "commit@1",
		"outer-1", "nested-ok", "outer-2",
	}

	if !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// TestAfterCommitJoinsByDriver verifies a second Database over the same
// driver registers into the transaction and one over another driver runs
// the hook immediately.
func TestAfterCommitJoinsByDriver(t *testing.T) {
	t.Parallel()

	var log []string

	root := &txDriver{log: &log}
	first, second := database.New(root), database.New(root)
	other := database.New(&txDriver{log: &log})

	err := first.WithTransaction(t.Context(), func(ctx context.Context) error {
		register(t, second, ctx, hookLog(&log, "second"))
		register(t, other, ctx, hookLog(&log, "other"))

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"begin@1", "other", "commit@1", "second"}; !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// TestAfterCommitContextIsOutsideTransaction verifies database calls in a
// hook run on the root driver.
func TestAfterCommitContextIsOutsideTransaction(t *testing.T) {
	t.Parallel()

	var log []string

	db := database.New(&txDriver{log: &log})

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		register(t, db, ctx, func(ctx context.Context) error {
			_, err := db.Exec(ctx, query.Raw("DELETE FROM t"))

			return err
		})

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"begin@1", "commit@1", "exec@0"}; !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// TestAfterCommitPanicPropagatesAndSkipsLaterHooks verifies a panicking hook
// escapes the outermost WithTransaction after the commit.
func TestAfterCommitPanicPropagatesAndSkipsLaterHooks(t *testing.T) {
	t.Parallel()

	var log []string

	db := database.New(&txDriver{log: &log})

	defer func() {
		if recovered, _ := recover().(error); !errors.Is(recovered, errBoom) {
			t.Fatalf("recovered = %v, want %v", recovered, errBoom)
		}

		if want := []string{"begin@1", "commit@1"}; !slices.Equal(log, want) {
			t.Fatalf("log = %v, want %v", log, want)
		}
	}()

	_ = db.WithTransaction(t.Context(), func(ctx context.Context) error {
		register(t, db, ctx, func(context.Context) error {
			panic(errBoom)
		})
		register(t, db, ctx, hookLog(&log, "later"))

		return nil
	})
}

// TestAfterCommitErrorsAreJoinedAfterCommit verifies every hook runs and their
// errors come back from WithTransaction wrapped with ErrAfterCommit, after the
// commit.
func TestAfterCommitErrorsAreJoinedAfterCommit(t *testing.T) {
	t.Parallel()

	var log []string

	errOther := errors.New("other")
	db := database.New(&txDriver{log: &log})

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		register(t, db, ctx, func(context.Context) error {
			return errBoom
		})
		register(t, db, ctx, hookLog(&log, "later"))
		register(t, db, ctx, func(context.Context) error {
			return errOther
		})

		return nil
	})

	if !errors.Is(err, database.ErrAfterCommit) || !errors.Is(err, errBoom) || !errors.Is(err, errOther) {
		t.Fatalf("err = %v, want ErrAfterCommit wrapping both hook errors", err)
	}

	if want := []string{"begin@1", "commit@1", "later"}; !slices.Equal(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

// TestAfterCommitOutsideTransactionReturnsError verifies a hook run at once
// returns its error wrapped with ErrAfterCommit.
func TestAfterCommitOutsideTransactionReturnsError(t *testing.T) {
	t.Parallel()

	db := database.New(&txDriver{log: new([]string)})

	err := db.AfterCommit(t.Context(), func(context.Context) error {
		return errBoom
	})

	if !errors.Is(err, database.ErrAfterCommit) || !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want ErrAfterCommit wrapping %v", err, errBoom)
	}
}
