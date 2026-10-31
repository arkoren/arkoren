package mysqldatabase_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	mysql "arkoren.dev/driver/mysql/mysqldatabase"
)

// boundedMySQL opens an independently owned pool with exactly the requested
// capacity. Tests use a second pool for observations so that inspecting a lock
// never supplies spare capacity to the pool whose progress is being tested.
func boundedMySQL(t *testing.T, capacity int) *mysql.Driver {
	t.Helper()

	driver, err := mysql.New(integrationDSN(t), func(databaseClient *mysql.Builder) {
		databaseClient.MaxOpenConns(capacity)
		databaseClient.MaxIdleConns(capacity)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return driver
}

// TestMySQLTransactionLockUsesSingleConnection verifies that named-lock
// ownership and transactional SQL share a physical server session. A deadline
// turns an accidental second pool borrow into a bounded failure.
func TestMySQLTransactionLockUsesSingleConnection(t *testing.T) {
	t.Parallel()

	driver, observer := boundedMySQL(t, 1), boundedMySQL(t, 1)
	ctx, cancel := context.WithTimeout(testContext(t), 2*time.Second)
	t.Cleanup(cancel)
	name := uniqueName(t, "tx_lock")

	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
		var id int64

		if err := tx.Find(ctx, "SELECT CONNECTION_ID()", &id); err != nil {
			return err
		}

		if _, err := tx.Lock(ctx, name); err != nil {
			return err
		}

		var owner sql.NullInt64

		if err := observer.SQL().QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", name).Scan(&owner); err != nil {
			return err
		}

		if !owner.Valid || owner.Int64 != id {
			return fmt.Errorf("lock session = %v, transaction session = %d", owner, id)
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	assertMySQLLockFree(t, observer, name)
}

// TestMySQLTransactionTryLockUsesSingleConnection covers the nonwaiting path
// separately: an uncontended TryLock must not wait for its own transaction to
// release the only pooled connection.
func TestMySQLTransactionTryLockUsesSingleConnection(t *testing.T) {
	t.Parallel()

	driver, observer := boundedMySQL(t, 1), boundedMySQL(t, 1)
	ctx, cancel := context.WithTimeout(testContext(t), 2*time.Second)
	t.Cleanup(cancel)
	name := uniqueName(t, "tx_try_lock")

	err := driver.WithTransaction(ctx, func(tx database.Driver) error {
		var id int64

		if err := tx.Find(ctx, "SELECT CONNECTION_ID()", &id); err != nil {
			return err
		}

		_, acquired, err := tx.TryLock(ctx, name)

		if err != nil {
			return err
		}

		if !acquired {
			return errors.New("uncontended transaction lock was not acquired")
		}

		var owner sql.NullInt64

		if err := observer.SQL().QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", name).Scan(&owner); err != nil {
			return err
		}

		if !owner.Valid || owner.Int64 != id {
			return fmt.Errorf("lock session = %v, transaction session = %d", owner, id)
		}

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	assertMySQLLockFree(t, observer, name)
}

// TestMySQLFullPoolTransactionsAcquireDistinctLocks starts both transactions
// before either requests a free named lock. Server-side lock contention cannot
// explain a failure because the names are different.
func TestMySQLFullPoolTransactionsAcquireDistinctLocks(t *testing.T) {
	t.Parallel()

	driver := boundedMySQL(t, 2)
	ctx, cancel := context.WithTimeout(testContext(t), 3*time.Second)
	t.Cleanup(cancel)
	ready, start := make(chan struct{}, 2), make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup

	for range 2 {
		name := uniqueName(t, "full_pool")
		group.Go(func() {
			results <- driver.WithTransaction(ctx, func(tx database.Driver) error {
				ready <- struct{}{}

				select {
				case <-start:
				case <-ctx.Done():
					return ctx.Err()
				}

				_, acquired, err := tx.TryLock(ctx, name)

				if err == nil && !acquired {
					return errors.New("distinct free lock was not acquired")
				}

				return err
			})
		})
	}

	t.Cleanup(func() {
		cancel()
		group.Wait()
	})

	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}

	close(start)
	group.Wait()

	for range 2 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}

// TestMySQLRootWaiterLeavesCapacityForHolderQuery forces the competing locker
// to receive the last spare connection before the holder issues its query.
// Returning an unsuccessful polling connection, not waiter cancellation or a
// larger pool, must let the holder finish its protected work.
func TestMySQLRootWaiterLeavesCapacityForHolderQuery(t *testing.T) {
	t.Parallel()

	driver := boundedMySQL(t, 2)
	ctx, cancel := context.WithTimeout(testContext(t), 3*time.Second)
	t.Cleanup(cancel)
	name := uniqueName(t, "holder_waiter")
	unlock, err := driver.Lock(ctx, name)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unlock(context.Background()); err != nil {
			t.Error(err)
		}
	})

	gate, err := driver.SQL().Conn(ctx)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := gate.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
			t.Error(err)
		}
	})

	before := driver.SQL().Stats().WaitCount
	var group sync.WaitGroup

	group.Go(func() {
		other, err := driver.Lock(ctx, name)

		if err == nil {
			// Cancellation may race with acquisition during test cleanup.
			if err := other(context.Background()); err != nil {
				t.Error(err)
			}
		} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Error(err)
		}
	})

	t.Cleanup(func() {
		cancel()
		group.Wait()
	})

	for driver.SQL().Stats().WaitCount == before {
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}

		runtime.Gosched()
	}

	if err := gate.Close(); err != nil {
		t.Fatal(err)
	}

	var value int

	if err := driver.Find(ctx, "SELECT 1", &value); err != nil {
		t.Fatalf("holder query while waiter is active: %v", err)
	}

	if value != 1 || ctx.Err() != nil {
		t.Fatalf("holder query = %d, context error = %v", value, ctx.Err())
	}
}

// assertMySQLLockFree checks backend release from an independent session and
// releases its own successful acquisition. The bounded wait allows MySQL to
// finish processing a discarded TCP session's COM_QUIT; it never permits an
// indefinitely leaked session lock or an unbalanced native GET_LOCK count.
func assertMySQLLockFree(t *testing.T, observer *mysql.Driver, name string) {
	t.Helper()

	unlock, err := observer.Lock(testContext(t), name)

	if err != nil {
		t.Fatalf("lock %q remains held: %v", name, err)
	}

	if err := unlock(testContext(t)); err != nil {
		t.Fatal(err)
	}
}
