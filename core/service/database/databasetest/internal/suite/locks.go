package suite

import (
	"context"
	"errors"
	"strings"
	"time"

	"arkoren.dev/core/service/database"
)

// lockWait is how long a check waits to prove a locker stays blocked.
const lockWait = 150 * time.Millisecond

// lockTests cover Lock and TryLock.
var lockTests = []conformanceTest{
	{"Lock/ReleasedByUnlock", lockReleasedByUnlock},
	{"Lock/UnlockIsIdempotent", lockUnlockIsIdempotent},
	{"Lock/RejectsEmptyName", lockRejectsEmptyName},
	{"Lock/AcceptsLongName", lockAcceptsLongName},
	{"Lock/DifferentNamesDoNotBlock", lockDifferentNames},
	{"Lock/WaiterHonoursContext", lockWaiterHonoursContext},
	{"Lock/WaiterAcquiresAfterUnlock", lockWaiterAcquires},
	{"Lock/HeldUntilTransactionCommits", lockHeldUntilTransactionEnds(nil)},
	{"Lock/HeldUntilTransactionRollsBack", lockHeldUntilTransactionEnds(errRollback)},
	{"Lock/UnlockInTransactionKeepsLock", lockUnlockInTransactionKeepsLock},
	{"TryLock/AcquiresFreeLock", tryLockAcquiresFreeLock},
	{"TryLock/NotAcquiredWhileLockHeld", tryLockNotAcquiredWhileLockHeld},
	{"TryLock/NotAcquiredWhileTryLockHeld", tryLockNotAcquiredWhileTryLockHeld},
	{"TryLock/BlocksLock", tryLockBlocksLock},
	{"TryLock/AcquirableAfterUnlock", tryLockAcquirableAfterUnlock},
	{"TryLock/UnlockIsIdempotent", tryLockUnlockIsIdempotent},
	{"TryLock/RejectsEmptyName", tryLockRejectsEmptyName},
	{"TryLock/AcceptsLongName", tryLockAcceptsLongName},
	{"TryLock/DifferentNamesDoNotConflict", tryLockDifferentNames},
	{"TryLock/NotAcquiredInTransactionWhileHeld", tryLockNotAcquiredInTransactionWhileHeld},
	{"TryLock/HeldUntilTransactionCommits", tryLockHeldUntilTransactionEnds(nil)},
	{"TryLock/HeldUntilTransactionRollsBack", tryLockHeldUntilTransactionEnds(errRollback)},
	{"TryLock/UnlockInTransactionKeepsLock", tryLockUnlockInTransactionKeepsLock},
	{"NestedLock/Rollback", nestedLockLifetime(false, errRollback, false, 1)},
	{"NestedTryLock/Rollback", nestedLockLifetime(true, errRollback, false, 1)},
	{"NestedLock/Panic", nestedLockLifetime(false, nil, true, 1)},
	{"NestedTryLock/Panic", nestedLockLifetime(true, nil, true, 1)},
	{"NestedLock/Success", nestedLockLifetime(false, nil, false, 1)},
	{"NestedTryLock/Success", nestedLockLifetime(true, nil, false, 1)},
	{"NestedLock/MultipleLevels", nestedLockLifetime(false, errRollback, false, 3)},
	{"NestedTryLock/MultipleLevels", nestedLockLifetime(true, errRollback, false, 3)},
	{"Lock/OuterPanicReleases", transactionLockPanic(false)},
	{"TryLock/OuterPanicReleases", transactionLockPanic(true)},
	{"Lock/OuterCancellationReleases", transactionLockCancellation(false)},
	{"TryLock/OuterCancellationReleases", transactionLockCancellation(true)},
	{"Lock/FreshNestedOuterRollback", transactionLockRollback(false)},
	{"TryLock/FreshNestedOuterRollback", transactionLockRollback(true)},
}

// nestedLockLifetime checks fresh acquisitions after savepoint creation. A
// competing root acquisition observes continuous ownership after inner unwind.
func nestedLockLifetime(try bool, result error, panics bool, depth int) func(*harness) {
	return func(h *harness) {
		name := h.lockName("fresh-nested")

		err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
			var enter func(database.Driver, int) error
			enter = func(current database.Driver, remaining int) error {
				return current.WithTransaction(h.ctx(), func(inner database.Driver) error {
					if remaining > 1 {
						return enter(inner, remaining-1)
					}

					if try {
						h.mustTryLock(inner, name)
					} else {
						h.mustLock(inner, name)
					}

					if panics {
						panic(panicValue)
					}

					return result
				})
			}
			func() {
				defer func() {
					if p := recover(); p != nil && (!panics || p != panicValue) {
						h.t.Fatalf("nested panic = %v", p)
					}
				}()

				if err := enter(tx, depth); !errors.Is(err, result) {
					h.t.Fatalf("nested result = %v, want %v", err, result)
				}

				if panics {
					h.t.Fatal("nested callback did not panic")
				}
			}()
			h.expectNotAcquired(h.driver, name)

			return nil
		})

		if err != nil {
			h.t.Fatal(err)
		}

		h.expectFree(name)
	}
}

// transactionLockPanic checks that fresh nested lock ownership is released
// after an uncaught outer panic, with the original panic value preserved.
func transactionLockPanic(try bool) func(*harness) {
	return func(h *harness) {
		name := h.lockName("outer-panic")
		func() {
			defer func() {
				if p := recover(); p != panicValue {
					h.t.Fatalf("outer panic = %v", p)
				}
			}()
			_ = h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
				if err := tx.WithTransaction(h.ctx(), func(inner database.Driver) error {
					if try {
						h.mustTryLock(inner, name)
					} else {
						h.mustLock(inner, name)
					}

					return nil
				}); err != nil {
					return err
				}

				panic(panicValue)
			})
		}()
		h.expectFree(name)
	}
}

// transactionLockCancellation checks lock cleanup after the transaction's own
// context ends, independently of whether the native SQL wrapper auto-rolls back.
func transactionLockCancellation(try bool) func(*harness) {
	return func(h *harness) {
		name := h.lockName("outer-cancel")
		ctx, cancel := context.WithCancel(h.ctx())
		defer cancel()
		err := h.driver.WithTransaction(ctx, func(tx database.Driver) error {
			if err := tx.WithTransaction(ctx, func(inner database.Driver) error {
				if try {
					h.mustTryLock(inner, name)
				} else {
					h.mustLock(inner, name)
				}

				return nil
			}); err != nil {
				return err
			}

			cancel()

			return nil
		})

		if !errors.Is(err, context.Canceled) {
			h.t.Fatalf("canceled transaction = %v", err)
		}

		h.expectFree(name)
	}
}

// transactionLockRollback verifies a successful fresh nested acquisition is
// released when its outermost transaction returns an ordinary body error.
func transactionLockRollback(try bool) func(*harness) {
	return func(h *harness) {
		name := h.lockName("fresh-outer-rollback")

		err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
			if err := tx.WithTransaction(h.ctx(), func(inner database.Driver) error {
				if try {
					h.mustTryLock(inner, name)
				} else {
					h.mustLock(inner, name)
				}

				return nil
			}); err != nil {
				return err
			}

			h.expectNotAcquired(h.driver, name)

			return errRollback
		})

		if !errors.Is(err, errRollback) {
			h.t.Fatalf("outer rollback = %v", err)
		}

		h.expectFree(name)
	}
}

// lockResult carries the outcome of a Lock call made on another goroutine.
type lockResult struct {
	// unlock releases the acquired lock.
	unlock func(context.Context) error

	// err is the Lock error.
	err error
}

// lockName returns a lock name unique to the harness.
func (harness *harness) lockName(name string) string {
	return "arkoren-dbtest-" + name + "-" + harness.table
}

// mustLock takes name and returns its unlock, failing the test on error.
func (harness *harness) mustLock(driver database.Driver, name string) func(context.Context) error {
	harness.t.Helper()

	unlock, err := driver.Lock(harness.ctx(), name)

	if err != nil {
		harness.t.Fatalf("Lock(%q) error = %v", name, err)
	}

	return unlock
}

// mustUnlock releases a lock, failing the test on error.
func (harness *harness) mustUnlock(unlock func(context.Context) error) {
	harness.t.Helper()

	if err := unlock(harness.ctx()); err != nil {
		harness.t.Fatalf("unlock error = %v", err)
	}
}

// expectBlocked checks that taking name does not succeed within lockWait and
// that the wait ends with the context deadline.
func (harness *harness) expectBlocked(name string) {
	harness.t.Helper()

	ctx, cancel := context.WithTimeout(harness.ctx(), lockWait)
	defer cancel()

	unlock, err := harness.driver.Lock(ctx, name)

	if err == nil {
		_ = unlock(harness.ctx())
		harness.t.Fatalf("Lock(%q) succeeded while the lock was held", name)
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		harness.t.Fatalf("Lock(%q) error = %v, want context.DeadlineExceeded", name, err)
	}
}

// expectFree checks that name can be taken and released.
func (harness *harness) expectFree(name string) {
	harness.t.Helper()

	harness.mustUnlock(harness.mustLock(harness.driver, name))
}

// lockAsync calls Lock on a goroutine and returns its result channel.
func (harness *harness) lockAsync(name string) <-chan lockResult {
	results := make(chan lockResult, 1)
	ctx := harness.ctx()

	go func() {
		unlock, err := harness.driver.Lock(ctx, name)
		results <- lockResult{unlock: unlock, err: err}
	}()

	return results
}

// expectPending checks that the asynchronous Lock has not returned yet.
func (harness *harness) expectPending(results <-chan lockResult) {
	harness.t.Helper()

	select {
	case result := <-results:
		harness.t.Fatalf("Lock() returned while the lock was held: error = %v", result.err)
	case <-time.After(lockWait):
	}
}

// expectAcquired waits for the asynchronous Lock and releases its lock.
func (harness *harness) expectAcquired(results <-chan lockResult) {
	harness.t.Helper()

	select {
	case result := <-results:
		if result.err != nil {
			harness.t.Fatalf("Lock() error = %v", result.err)
		}

		harness.mustUnlock(result.unlock)
	case <-harness.ctx().Done():
		harness.t.Fatal("Lock() never acquired the released lock")
	}
}

// lockReleasedByUnlock checks exclusion and release.
func lockReleasedByUnlock(h *harness) {
	name := h.lockName("release")
	unlock := h.mustLock(h.driver, name)

	h.expectBlocked(name)

	h.mustUnlock(unlock)
	h.expectFree(name)
}

// lockUnlockIsIdempotent checks that a second unlock succeeds and does not
// release a lock another holder took in between.
func lockUnlockIsIdempotent(h *harness) {
	name := h.lockName("twice")
	unlock := h.mustLock(h.driver, name)

	h.mustUnlock(unlock)

	other := h.mustLock(h.driver, name)
	h.mustUnlock(unlock)
	h.expectBlocked(name)

	h.mustUnlock(other)
}

// lockRejectsEmptyName checks ErrEmptyLockName.
func lockRejectsEmptyName(h *harness) {
	if _, err := h.driver.Lock(h.ctx(), ""); !errors.Is(err, database.ErrEmptyLockName) {
		h.t.Errorf("Lock(\"\") error = %v, want ErrEmptyLockName", err)
	}
}

// lockAcceptsLongName checks that name length is not limited.
func lockAcceptsLongName(h *harness) {
	name := h.lockName(strings.Repeat("long", 100))
	unlock := h.mustLock(h.driver, name)

	h.expectBlocked(name)

	h.mustUnlock(unlock)
}

// lockDifferentNames checks that unrelated names are independent.
func lockDifferentNames(h *harness) {
	unlock := h.mustLock(h.driver, h.lockName("a"))
	defer h.mustUnlock(unlock)

	h.expectFree(h.lockName("b"))
}

// lockWaiterHonoursContext checks that a canceled wait reports its context
// and leaves the lock with its holder.
func lockWaiterHonoursContext(h *harness) {
	name := h.lockName("cancel")
	unlock := h.mustLock(h.driver, name)

	ctx, cancel := context.WithCancel(h.ctx())
	time.AfterFunc(lockWait, cancel)

	if _, err := h.driver.Lock(ctx, name); !errors.Is(err, context.Canceled) {
		h.t.Errorf("Lock() error = %v, want context.Canceled", err)
	}

	h.expectBlocked(name)

	h.mustUnlock(unlock)
	h.expectFree(name)
}

// lockWaiterAcquires checks that a blocked locker proceeds on unlock.
func lockWaiterAcquires(h *harness) {
	name := h.lockName("handoff")
	unlock := h.mustLock(h.driver, name)

	results := h.lockAsync(name)
	h.expectPending(results)

	h.mustUnlock(unlock)
	h.expectAcquired(results)
}

// lockHeldUntilTransactionEnds returns a check that a lock taken inside a
// transaction excludes other lockers until the transaction ends with
// result.
func lockHeldUntilTransactionEnds(result error) func(h *harness) {
	return func(h *harness) {
		name := h.lockName("tx")

		var results <-chan lockResult

		err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
			h.mustLock(tx, name)

			results = h.lockAsync(name)
			h.expectPending(results)

			return result
		})

		if !errors.Is(err, result) {
			h.t.Fatalf("WithTransaction() error = %v, want %v", err, result)
		}

		h.expectAcquired(results)
	}
}

// lockUnlockInTransactionKeepsLock checks that calling unlock inside a
// transaction does not release the lock before the transaction ends.
func lockUnlockInTransactionKeepsLock(h *harness) {
	name := h.lockName("txunlock")

	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		unlock := h.mustLock(tx, name)
		h.mustUnlock(unlock)
		h.expectBlocked(name)

		return nil
	})

	if err != nil {
		h.t.Fatalf("WithTransaction() error = %v", err)
	}

	h.expectFree(name)
}

// mustTryLock takes name with TryLock and returns its unlock, failing the test
// when the call errors or the lock is not acquired.
func (harness *harness) mustTryLock(driver database.Driver, name string) func(context.Context) error {
	harness.t.Helper()

	unlock, acquired, err := driver.TryLock(harness.ctx(), name)

	if err != nil || !acquired {
		harness.t.Fatalf("TryLock(%q) = acquired %v, error %v, want acquired", name, acquired, err)
	}

	return unlock
}

// expectNotAcquired checks that TryLock returns at once, with no error, and
// without the lock.
func (harness *harness) expectNotAcquired(driver database.Driver, name string) {
	harness.t.Helper()

	unlock, acquired, err := driver.TryLock(harness.ctx(), name)

	if err != nil || acquired {
		if acquired {
			_ = unlock(harness.ctx())
		}

		harness.t.Fatalf("TryLock(%q) = acquired %v, error %v, want not acquired and nil error", name, acquired, err)
	}
}

// tryLockAcquiresFreeLock checks that a free lock is acquired and excludes
// others until released.
func tryLockAcquiresFreeLock(h *harness) {
	name := h.lockName("try")
	unlock := h.mustTryLock(h.driver, name)

	h.expectBlocked(name)

	h.mustUnlock(unlock)
	h.expectFree(name)
}

// tryLockNotAcquiredWhileLockHeld checks that TryLock does not wait for, or
// take, a lock held through Lock.
func tryLockNotAcquiredWhileLockHeld(h *harness) {
	name := h.lockName("tryheld")
	unlock := h.mustLock(h.driver, name)

	h.expectNotAcquired(h.driver, name)

	h.mustUnlock(unlock)
	h.mustUnlock(h.mustTryLock(h.driver, name))
}

// tryLockNotAcquiredWhileTryLockHeld checks that locks are not reentrant: a
// second TryLock of a held name is not acquired.
func tryLockNotAcquiredWhileTryLockHeld(h *harness) {
	name := h.lockName("trytwice")
	unlock := h.mustTryLock(h.driver, name)

	h.expectNotAcquired(h.driver, name)

	h.mustUnlock(unlock)
}

// tryLockBlocksLock checks that a lock taken by TryLock is the same lock Lock
// waits for, and that a failed TryLock leaves it with its holder.
func tryLockBlocksLock(h *harness) {
	name := h.lockName("tryblocks")
	unlock := h.mustTryLock(h.driver, name)

	h.expectNotAcquired(h.driver, name)

	results := h.lockAsync(name)
	h.expectPending(results)

	h.mustUnlock(unlock)
	h.expectAcquired(results)
}

// tryLockAcquirableAfterUnlock checks that a released lock can be tried again.
func tryLockAcquirableAfterUnlock(h *harness) {
	name := h.lockName("tryagain")

	h.mustUnlock(h.mustTryLock(h.driver, name))
	h.mustUnlock(h.mustTryLock(h.driver, name))
}

// tryLockUnlockIsIdempotent checks that a second unlock succeeds and does not
// release a lock another holder took in between.
func tryLockUnlockIsIdempotent(h *harness) {
	name := h.lockName("trytwiceunlock")
	unlock := h.mustTryLock(h.driver, name)

	h.mustUnlock(unlock)

	other := h.mustTryLock(h.driver, name)
	h.mustUnlock(unlock)
	h.expectNotAcquired(h.driver, name)

	h.mustUnlock(other)
}

// tryLockRejectsEmptyName checks ErrEmptyLockName.
func tryLockRejectsEmptyName(h *harness) {
	unlock, acquired, err := h.driver.TryLock(h.ctx(), "")

	if !errors.Is(err, database.ErrEmptyLockName) || acquired {
		h.t.Errorf("TryLock(\"\") = acquired %v, error %v, want ErrEmptyLockName", acquired, err)
	}

	if unlock != nil {
		h.t.Error("TryLock(\"\") returned an unlock")
	}
}

// tryLockAcceptsLongName checks that name length is not limited.
func tryLockAcceptsLongName(h *harness) {
	name := h.lockName(strings.Repeat("long", 100))
	unlock := h.mustTryLock(h.driver, name)

	h.expectNotAcquired(h.driver, name)

	h.mustUnlock(unlock)
}

// tryLockDifferentNames checks that unrelated names are independent.
func tryLockDifferentNames(h *harness) {
	unlock := h.mustTryLock(h.driver, h.lockName("a"))
	defer h.mustUnlock(unlock)

	h.mustUnlock(h.mustTryLock(h.driver, h.lockName("b")))
}

// tryLockNotAcquiredInTransactionWhileHeld checks that TryLock inside a
// transaction reports a lock held outside it as not acquired, and that the
// failed attempt does not disturb the transaction.
func tryLockNotAcquiredInTransactionWhileHeld(h *harness) {
	name := h.lockName("trytxheld")
	unlock := h.mustLock(h.driver, name)

	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		h.expectNotAcquired(tx, name)

		return nil
	})

	if err != nil {
		h.t.Fatalf("WithTransaction() error = %v", err)
	}

	h.mustUnlock(unlock)
	h.expectFree(name)
}

// tryLockHeldUntilTransactionEnds returns a check that a lock taken with
// TryLock inside a transaction excludes other lockers until the transaction
// ends with result.
func tryLockHeldUntilTransactionEnds(result error) func(h *harness) {
	return func(h *harness) {
		name := h.lockName("trytx")

		err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
			h.mustTryLock(tx, name)
			h.expectNotAcquired(h.driver, name)
			h.expectBlocked(name)

			return result
		})

		if !errors.Is(err, result) {
			h.t.Fatalf("WithTransaction() error = %v, want %v", err, result)
		}

		h.expectFree(name)
	}
}

// tryLockUnlockInTransactionKeepsLock checks that calling unlock inside a
// transaction does not release a TryLock before the transaction ends.
func tryLockUnlockInTransactionKeepsLock(h *harness) {
	name := h.lockName("trytxunlock")

	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		unlock := h.mustTryLock(tx, name)
		h.mustUnlock(unlock)
		h.expectNotAcquired(h.driver, name)

		return nil
	})

	if err != nil {
		h.t.Fatalf("WithTransaction() error = %v", err)
	}

	h.mustUnlock(h.mustTryLock(h.driver, name))
}
