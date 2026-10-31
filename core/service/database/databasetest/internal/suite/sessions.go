package suite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"arkoren.dev/core/service/database"
)

// sessionTests exercise the portable retained-connection and callback-lifetime
// contract. Every check runs against the same public Driver seam as transactions.
var sessionTests = []conformanceTest{
	{"Session/PhysicalConnection", sessionPhysicalConnection},
	{"Session/AutocommitSurvivesBodyError", sessionAutocommitSurvivesError},
	{"Session/TransactionsCommitIndividually", sessionTransactionsCommitIndividually},
	{"Session/NestedLifetimeAndLocks", sessionNestedLifetimeAndLocks},
	{"Session/EndedDriverAndRows", sessionEndedDriverAndRows},
	{"Session/TransactionOwnsBorrowedSessionLock", transactionOwnsBorrowedSessionLock},
	{"Session/ParentLockSurvivesTransaction", sessionLockSurvivesTransaction},
	{"Transaction/EndedNestedDriverAndRows", transactionEndedNestedDriverAndRows},
	{"Session/PanicReleasesLocks", sessionPanicReleasesLocks},
	{"Session/CancellationReleasesLocks", sessionCancellationReleasesLocks},
	{"Session/LocksAreNotReentrant", sessionLocksAreNotReentrant},
	{"Transaction/LocksAreNotReentrant", transactionLocksAreNotReentrant},
}

// sessionPhysicalConnection uses temporary state to observe physical identity,
// including scanning, cursors, borrowed sessions, and independent transactions.
func sessionPhysicalConnection(h *harness) {
	name := "tmp_" + h.table

	err := h.driver.WithSession(h.ctx(), func(session database.Driver) error {
		if _, err := session.Exec(h.ctx(), "CREATE TEMPORARY TABLE "+name+" (id INTEGER)"); err != nil {
			return err
		}

		for range 2 {
			if err := session.WithTransaction(h.ctx(), func(tx database.Driver) error {
				_, err := tx.Exec(h.ctx(), "INSERT INTO "+name+" VALUES (1)")

				return err
			}); err != nil {
				return err
			}
		}

		if err := session.WithSession(h.ctx(), func(inner database.Driver) error {
			var count int

			if err := inner.Find(h.ctx(), "SELECT COUNT(*) FROM "+name, &count); err != nil {
				return err
			}

			if count != 2 {
				return fmt.Errorf("temporary count = %d, want 2", count)
			}

			return nil
		}); err != nil {
			return err
		}

		var values []int

		if err := session.Select(h.ctx(), "SELECT id FROM "+name, &values); err != nil {
			return err
		}

		if len(values) != 2 {
			return fmt.Errorf("temporary rows = %v, want two rows", values)
		}

		rows, err := session.Query(h.ctx(), "SELECT id FROM "+name)

		if err != nil {
			return err
		}

		if !rows.Next() {
			return errors.Join(errors.New("temporary cursor has no row"), rows.Close())
		}

		var value int

		if err := rows.Scan(&value); err != nil {
			return errors.Join(err, rows.Close())
		}

		if err := rows.Close(); err != nil {
			return err
		}

		_, err = session.Exec(h.ctx(), "DROP TABLE "+name)

		return err
	})

	if err != nil {
		h.t.Fatal(err)
	}
}

// sessionAutocommitSurvivesError distinguishes a session from an implicit
// transaction: a callback error must not erase statements already committed.
func sessionAutocommitSurvivesError(h *harness) {
	err := h.driver.WithSession(h.ctx(), func(session database.Driver) error {
		h.seed(session, threeRows[0])

		return errRollback
	})

	if !errors.Is(err, errRollback) || len(h.rows(h.driver)) != 1 {
		h.t.Fatalf("session error = %v; expected retained autocommit row", err)
	}
}

// sessionTransactionsCommitIndividually preserves partial progress across a
// later failing transaction without returning the session connection in between.
func sessionTransactionsCommitIndividually(h *harness) {
	err := h.driver.WithSession(h.ctx(), func(session database.Driver) error {
		if err := session.WithTransaction(h.ctx(), func(tx database.Driver) error {
			h.seed(tx, threeRows[0])

			return nil
		}); err != nil {
			return err
		}

		return session.WithTransaction(h.ctx(), func(tx database.Driver) error {
			h.seed(tx, threeRows[1])

			return errRollback
		})
	})

	if !errors.Is(err, errRollback) {
		h.t.Fatal(err)
	}

	rows := h.rows(h.driver)

	if len(rows) != 1 || rows[0] != threeRows[0] {
		h.t.Fatalf("rows = %v, want only first transaction", rows)
	}
}

// sessionNestedLifetimeAndLocks checks lexical ownership, expired inner
// handles, and an old unlock's inability to release a new holder's lock.
func sessionNestedLifetimeAndLocks(h *harness) {
	parentName, childName := h.lockName("session-parent"), h.lockName("session-child")
	var escaped database.Driver
	var oldUnlock func(context.Context) error

	err := h.driver.WithSession(h.ctx(), func(session database.Driver) error {
		h.mustLock(session, parentName)

		if err := session.WithSession(h.ctx(), func(child database.Driver) error {
			escaped = child
			oldUnlock = h.mustLock(child, childName)
			h.expectNotAcquired(child, parentName)
			h.expectNotAcquired(h.driver, childName)

			return errRollback
		}); !errors.Is(err, errRollback) {
			return fmt.Errorf("nested session error = %w", err)
		}

		if _, err := escaped.Exec(h.ctx(), "SELECT 1"); !errors.Is(err, database.ErrSessionDone) {
			return fmt.Errorf("ended nested session: %w", err)
		}

		h.expectNotAcquired(h.driver, parentName)

		other := h.mustLock(h.driver, childName)
		h.mustUnlock(oldUnlock)
		h.expectNotAcquired(h.driver, childName)

		h.mustUnlock(other)

		return nil
	})

	if err != nil {
		h.t.Fatal(err)
	}

	h.expectFree(parentName)
	h.expectFree(childName)
}

// sessionEndedDriverAndRows leaves a streaming result open intentionally. Scope
// cleanup must close it without waiting for the caller and invalidate all I/O.
func sessionEndedDriverAndRows(h *harness) {
	var escaped database.Driver
	var cursor database.Rows

	err := h.driver.WithSession(h.ctx(), func(session database.Driver) error {
		escaped = session
		var err error
		cursor, err = session.Query(h.ctx(), "SELECT 1")

		return err
	})

	if err != nil {
		h.t.Fatal(err)
	}

	expectEndedDriver(h, escaped, database.ErrSessionDone)

	if cursor.Next() || !errors.Is(cursor.Err(), database.ErrSessionDone) {
		h.t.Fatalf("escaped rows error = %v", cursor.Err())
	}

	var one int

	if err := cursor.Scan(&one); !errors.Is(err, database.ErrSessionDone) {
		h.t.Fatalf("escaped Scan = %v", err)
	}

	if err := cursor.Close(); err != nil {
		h.t.Fatal(err)
	}

	h.rows(h.driver)
}

// expectEndedDriver checks every operation that could otherwise acquire a pool
// connection or act on a reused native session. It also rejects callback replay.
func expectEndedDriver(h *harness, driver database.Driver, want error) {
	h.t.Helper()

	var one int
	var values []int

	if _, err := driver.Exec(h.ctx(), "SELECT 1"); !errors.Is(err, want) {
		h.t.Fatalf("ended Exec = %v", err)
	}

	if err := driver.Find(h.ctx(), "SELECT 1", &one); !errors.Is(err, want) {
		h.t.Fatalf("ended Find = %v", err)
	}

	if err := driver.Select(h.ctx(), "SELECT 1", &values); !errors.Is(err, want) {
		h.t.Fatalf("ended Select = %v", err)
	}

	if err := closeRows(driver.Query(h.ctx(), "SELECT 1")); !errors.Is(err, want) {
		h.t.Fatalf("ended Query = %v", err)
	}

	if _, err := driver.Lock(h.ctx(), h.lockName("ended")); !errors.Is(err, want) {
		h.t.Fatalf("ended Lock = %v", err)
	}

	if _, _, err := driver.TryLock(h.ctx(), h.lockName("ended")); !errors.Is(err, want) {
		h.t.Fatalf("ended TryLock = %v", err)
	}

	callback := func(database.Driver) error {
		h.t.Fatal("ended driver invoked callback")

		return nil
	}

	if err := driver.WithSession(h.ctx(), callback); !errors.Is(err, want) {
		h.t.Fatalf("ended WithSession = %v", err)
	}

	if err := driver.WithTransaction(h.ctx(), callback); !errors.Is(err, want) {
		h.t.Fatalf("ended WithTransaction = %v", err)
	}
}

// transactionOwnsBorrowedSessionLock ensures a session nested in a transaction
// cannot prematurely release a lock first acquired after a savepoint.
func transactionOwnsBorrowedSessionLock(h *harness) {
	name := h.lockName("borrowed-tx")

	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		err := tx.WithTransaction(h.ctx(), func(inner database.Driver) error {
			if err := inner.WithSession(h.ctx(), func(session database.Driver) error {
				return session.WithTransaction(h.ctx(), func(nested database.Driver) error {
					h.mustLock(nested, name)

					return nil
				})
			}); err != nil {
				return err
			}

			return errRollback
		})

		if !errors.Is(err, errRollback) {
			return err
		}

		h.expectNotAcquired(h.driver, name)

		return nil
	})

	if err != nil {
		h.t.Fatal(err)
	}

	h.expectFree(name)
}

// sessionLockSurvivesTransaction protects an existing session acquisition from
// being adopted or released by a child transaction requesting the same key.
func sessionLockSurvivesTransaction(h *harness) {
	name := h.lockName("session-before-tx")

	err := h.driver.WithSession(h.ctx(), func(session database.Driver) error {
		h.mustLock(session, name)

		if err := session.WithTransaction(h.ctx(), func(tx database.Driver) error {
			h.expectNotAcquired(tx, name)

			return nil
		}); err != nil {
			return err
		}

		h.expectNotAcquired(h.driver, name)

		return nil
	})

	if err != nil {
		h.t.Fatal(err)
	}

	h.expectFree(name)
}

// transactionEndedNestedDriverAndRows checks savepoint callback expiration
// before the outer transaction finishes, rather than only after pool release.
func transactionEndedNestedDriverAndRows(h *harness) {
	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		var escaped database.Driver
		var cursor database.Rows

		if err := tx.WithTransaction(h.ctx(), func(inner database.Driver) error {
			escaped = inner
			var err error
			cursor, err = inner.Query(h.ctx(), "SELECT 1")

			return err
		}); err != nil {
			return err
		}

		expectEndedDriver(h, escaped, sql.ErrTxDone)

		if cursor.Next() || !errors.Is(cursor.Err(), sql.ErrTxDone) {
			return fmt.Errorf("ended transaction rows: %w", cursor.Err())
		}

		return nil
	})

	if err != nil {
		h.t.Fatal(err)
	}
}

// sessionPanicReleasesLocks recovers the original panic only after checking
// that the driver has completed its retained-connection cleanup.
func sessionPanicReleasesLocks(h *harness) {
	name := h.lockName("session-panic")
	func() {
		defer func() {
			if p := recover(); p != panicValue {
				h.t.Fatalf("panic = %v", p)
			}
		}()
		_ = h.driver.WithSession(h.ctx(), func(session database.Driver) error {
			h.mustLock(session, name)
			panic(panicValue)
		})
	}()
	h.expectFree(name)
}

// sessionCancellationReleasesLocks checks detached cleanup and cancellation
// reporting even when a callback ignores cancellation and returns nil.
func sessionCancellationReleasesLocks(h *harness) {
	name := h.lockName("session-cancel")
	ctx, cancel := context.WithCancel(h.ctx())
	defer cancel()
	err := h.driver.WithSession(ctx, func(session database.Driver) error {
		h.mustLock(session, name)
		cancel()

		return nil
	})

	if !errors.Is(err, context.Canceled) {
		h.t.Fatalf("canceled session = %v", err)
	}

	h.expectFree(name)
}

// sessionLocksAreNotReentrant checks same-session TryLock without relying on
// the database's native reentrancy behavior or taking a second pool connection.
func sessionLocksAreNotReentrant(h *harness) {
	err := h.driver.WithSession(h.ctx(), func(session database.Driver) error {
		name := h.lockName("session-repeat")

		h.mustLock(session, name)
		h.expectNotAcquired(session, name)

		ctx, cancel := context.WithTimeout(h.ctx(), 50*time.Millisecond)
		defer cancel()

		if _, err := session.Lock(ctx, name); !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("repeated session Lock = %w", err)
		}

		return nil
	})

	if err != nil {
		h.t.Fatal(err)
	}
}

// transactionLocksAreNotReentrant checks outer/nested ownership without adding
// a stacked native lock that would survive transaction cleanup.
func transactionLocksAreNotReentrant(h *harness) {
	name := h.lockName("tx-repeat")

	err := h.driver.WithTransaction(h.ctx(), func(tx database.Driver) error {
		h.mustTryLock(tx, name)
		h.expectNotAcquired(tx, name)

		ctx, cancel := context.WithTimeout(h.ctx(), 50*time.Millisecond)
		defer cancel()

		if _, err := tx.Lock(ctx, name); !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("repeated transaction Lock = %w", err)
		}

		return tx.WithTransaction(h.ctx(), func(inner database.Driver) error {
			h.expectNotAcquired(inner, name)

			return nil
		})
	})

	if err != nil {
		h.t.Fatal(err)
	}

	h.expectFree(name)
}
