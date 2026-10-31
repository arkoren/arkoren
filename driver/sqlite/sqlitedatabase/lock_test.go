package sqlitedatabase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
)

// lockTestWait is how long a test waits to prove a locker stays blocked.
const lockTestWait = 100 * time.Millisecond

// newLockDatabase opens an in-memory SQLite database for lock tests.
func newLockDatabase(t *testing.T) *database.Database {
	t.Helper()

	driver, connection := newSQLiteDriver(t)
	registerSQLiteConnectionCleanup(t, connection)

	return database.New(driver)
}

// lockTestName returns a lock name unique to the test.
func lockTestName(t *testing.T) string {
	t.Helper()

	return "lock-test:" + t.Name()
}

// TestSQLiteLockAndUnlock verifies a lock can be taken and released twice in turn.
func TestSQLiteLockAndUnlock(t *testing.T) {
	t.Parallel()

	databaseClient := newLockDatabase(t)
	ctx := sqliteTestContext(t)
	name := lockTestName(t)

	for range 2 {
		unlock, err := databaseClient.Lock(ctx, name)

		if err != nil {
			t.Fatalf("Lock() error = %v", err)
		}

		if err := unlock(ctx); err != nil {
			t.Fatalf("unlock error = %v", err)
		}
	}
}

// TestSQLiteLockBlocksUntilUnlock verifies a second locker waits for the first.
func TestSQLiteLockBlocksUntilUnlock(t *testing.T) {
	t.Parallel()

	databaseClient := newLockDatabase(t)
	ctx := sqliteTestContext(t)
	name := lockTestName(t)

	first, err := databaseClient.Lock(ctx, name)

	if err != nil {
		t.Fatalf("first Lock() error = %v", err)
	}

	acquired := make(chan error, 1)

	go func() {
		second, err := databaseClient.Lock(ctx, name)

		if err == nil {
			err = second(ctx)
		}

		acquired <- err
	}()

	select {
	case err := <-acquired:
		t.Fatalf("second Lock() returned before unlock: %v", err)
	case <-time.After(lockTestWait):
	}

	if err := first(ctx); err != nil {
		t.Fatalf("unlock error = %v", err)
	}

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second Lock() error = %v", err)
		}
	case <-time.After(sqliteTestTimeout):
		t.Fatal("second Lock() did not return after unlock")
	}
}

// TestSQLiteLockReturnsContextErrorWhileWaiting verifies cancellation ends the wait.
func TestSQLiteLockReturnsContextErrorWhileWaiting(t *testing.T) {
	t.Parallel()

	databaseClient := newLockDatabase(t)
	ctx := sqliteTestContext(t)
	name := lockTestName(t)

	unlock, err := databaseClient.Lock(ctx, name)

	if err != nil {
		t.Fatalf("Lock() error = %v", err)
	}

	t.Cleanup(func() {
		if err := unlock(ctx); err != nil {
			t.Errorf("unlock error = %v", err)
		}
	})

	waitContext, cancel := context.WithTimeout(ctx, lockTestWait)
	t.Cleanup(cancel)

	_, err = databaseClient.Lock(waitContext, name)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock() error = %v, want %v", err, context.DeadlineExceeded)
	}
}

// TestSQLiteLockReturnsCanceledError verifies a canceled context returns context.Canceled.
func TestSQLiteLockReturnsCanceledError(t *testing.T) {
	t.Parallel()

	databaseClient := newLockDatabase(t)
	ctx := sqliteTestContext(t)
	name := lockTestName(t)

	unlock, err := databaseClient.Lock(ctx, name)

	if err != nil {
		t.Fatalf("Lock() error = %v", err)
	}

	t.Cleanup(func() {
		if err := unlock(ctx); err != nil {
			t.Errorf("unlock error = %v", err)
		}
	})

	waitContext, cancel := context.WithCancel(ctx)
	cancel()

	_, err = databaseClient.Lock(waitContext, name)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Lock() error = %v, want %v", err, context.Canceled)
	}
}

// TestSQLiteLockDoubleUnlockIsSafe verifies a second unlock does not release another holder's lock.
func TestSQLiteLockDoubleUnlockIsSafe(t *testing.T) {
	t.Parallel()

	databaseClient := newLockDatabase(t)
	ctx := sqliteTestContext(t)
	name := lockTestName(t)

	first, err := databaseClient.Lock(ctx, name)

	if err != nil {
		t.Fatalf("first Lock() error = %v", err)
	}

	if err := first(ctx); err != nil {
		t.Fatalf("first unlock error = %v", err)
	}

	second, err := databaseClient.Lock(ctx, name)

	if err != nil {
		t.Fatalf("second Lock() error = %v", err)
	}

	t.Cleanup(func() {
		if err := second(ctx); err != nil {
			t.Errorf("second unlock error = %v", err)
		}
	})

	if err := first(ctx); err != nil {
		t.Fatalf("repeated unlock error = %v", err)
	}

	waitContext, cancel := context.WithTimeout(ctx, lockTestWait)
	t.Cleanup(cancel)

	if _, err := databaseClient.Lock(waitContext, name); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock() error = %v, want %v: repeated unlock freed the lock", err, context.DeadlineExceeded)
	}
}

// TestSQLiteLockRejectsEmptyName verifies an empty name is an error.
func TestSQLiteLockRejectsEmptyName(t *testing.T) {
	t.Parallel()

	databaseClient := newLockDatabase(t)

	if _, err := databaseClient.Lock(sqliteTestContext(t), ""); err == nil {
		t.Fatal("Lock() error = nil, want error for empty name")
	}
}

// TestSQLiteLockWorksInsideTransaction verifies Lock inside WithTransaction.
func TestSQLiteLockWorksInsideTransaction(t *testing.T) {
	t.Parallel()

	databaseClient := newLockDatabase(t)
	ctx := sqliteTestContext(t)
	name := lockTestName(t)

	err := databaseClient.WithTransaction(ctx, func(txCtx context.Context) error {
		unlock, err := databaseClient.Lock(txCtx, name)

		if err != nil {
			return err
		}

		return unlock(ctx)
	})

	if err != nil {
		t.Fatalf("WithTransaction() error = %v", err)
	}
}

// TestSQLiteLockInsideTransactionBlocksOutsideLocker verifies transaction locks exclude other lockers.
func TestSQLiteLockInsideTransactionBlocksOutsideLocker(t *testing.T) {
	t.Parallel()

	databaseClient := newLockDatabase(t)
	ctx := sqliteTestContext(t)
	name := lockTestName(t)

	err := databaseClient.WithTransaction(ctx, func(txCtx context.Context) error {
		unlock, err := databaseClient.Lock(txCtx, name)

		if err != nil {
			return err
		}

		waitContext, cancel := context.WithTimeout(ctx, lockTestWait)
		defer cancel()

		if _, err := databaseClient.Lock(waitContext, name); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Lock() error = %v, want %v", err, context.DeadlineExceeded)
		}

		return unlock(ctx)
	})

	if err != nil {
		t.Fatalf("WithTransaction() error = %v", err)
	}
}
