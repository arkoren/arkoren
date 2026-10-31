package mysqldatabase

import (
	"context"
	"crypto/sha256"
	"database/sql"
	sqldriver "database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"arkoren.dev/core/service/database"
)

// maxLockName is the longest lock name MySQL accepts, in bytes.
const maxLockName = 64

// lockPollInterval yields query capacity between unsuccessful GET_LOCK tries.
// It bounds polling frequency without retaining a connection during the wait.
const lockPollInterval = 10 * time.Millisecond

// ErrLockNotAcquired reports a native NULL result from GET_LOCK: acquisition
// failed, rather than ordinary contention. TryLock reports contention as false
// with no error. A failed acquisition makes a retained session unsafe to reuse.
//
// Example:
//
//	unlock, acquired, err := driver.TryLock(ctx, "nightly-report")
//	if errors.Is(err, mysqldatabase.ErrLockNotAcquired) {
//		return fmt.Errorf("database could not establish lock ownership: %w", err)
//	}
//	if err != nil || !acquired {
//		return err
//	}
//	defer unlock(context.WithoutCancel(ctx))
var ErrLockNotAcquired = errors.New("lock was not acquired")

// Lock polls GET_LOCK with a zero timeout until the name is available or ctx
// ends. Root attempts return contended connections before waiting, so waiters
// do not retain the spare query capacity needed by a holder. A successful raw
// lock still retains one connection until unlock. Names over 64 bytes are hashed.
//
// Scoped locks use the session's existing physical connection. A session keeps
// its connection even while waiting; callers needing to yield it must TryLock
// and end a contended session. Locks are not reentrant, including inside one
// session. Transaction locks survive nested rollback and end only with the
// outermost transaction; their unlock is a no-op. Other scoped locks are also
// released automatically when their acquiring callback returns.
//
// Example:
//
//	unlock, err := driver.Lock(ctx, "nightly-report")
//	if err != nil {
//		return err
//	}
//	defer unlock(context.WithoutCancel(ctx))
func (driver *Driver) Lock(ctx context.Context, name string) (func(context.Context) error, error) {
	for {
		unlock, acquired, err := driver.TryLock(ctx, name)

		if err != nil || acquired {
			return unlock, err
		}

		timer := time.NewTimer(lockPollInterval)

		select {
		case <-ctx.Done():
			timer.Stop()

			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// TryLock takes the named lock without waiting. It reports acquired false,
// with a nil error, when another session holds the lock, and then returns the
// connection to the pool. The returned unlock function releases the lock and
// is safe to call more than once.
//
// TryLock uses the same canonical names and ownership rules as [Driver.Lock].
// An already-owned name returns false instead of stacking MySQL's native lock
// count. Session cleanup releases only its own acquisitions; transaction locks
// belong to the outermost transaction and their unlock does nothing.
//
// Example:
//
//	unlock, acquired, err := driver.TryLock(ctx, "nightly-report")
//	if err != nil || !acquired {
//		return err
//	}
//	defer unlock(ctx)
func (driver *Driver) TryLock(ctx context.Context, name string) (func(context.Context) error, bool, error) {
	if err := driver.check(ctx); err != nil {
		return nil, false, err
	}

	if name == "" {
		return nil, false, database.ErrEmptyLockName
	}

	lockName := mysqlLockName(name)

	if driver.scope != nil {
		state := driver.scope.session

		if state.locks[lockName] != nil {
			return nil, false, nil
		}

		granted, err := acquire(ctx, driver.db, lockName)

		if err != nil {
			// The server may have granted the lock before its response was
			// lost. Do not pool this session even if the callback handles err.
			state.poison(err)

			return nil, false, err
		}

		if !granted {
			return nil, false, nil
		}

		owner := driver.scope

		if owner.transaction != nil {
			owner = owner.transaction
		}

		state.locks[lockName] = owner

		if driver.scope.tx != nil {
			return func(context.Context) error {
				return nil
			}, true, nil
		}

		return onceUnlock(func(ctx context.Context) error {
			if state.locks[lockName] != owner {
				return nil
			}

			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
			defer cancel()

			return state.release(cleanupCtx, lockName)
		}), true, nil
	}

	conn, err := driver.pool.Conn(ctx)

	if err != nil {
		return nil, false, fmt.Errorf("open lock connection: %w", err)
	}

	granted, err := acquire(ctx, conn, lockName)

	if err != nil {
		return nil, false, errors.Join(err, discardConn(conn))
	}

	if !granted {
		return nil, false, conn.Close()
	}

	return unlockConn(conn, lockName), true, nil
}

// unlockConn returns the idempotent unlock for lockName held on conn. It
// releases the lock and the connection, or discards the connection when the
// release fails so a session that may still hold the lock is never reused.
func unlockConn(conn *sql.Conn, lockName string) func(context.Context) error {
	return onceUnlock(func(ctx context.Context) error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()

		if err := release(cleanupCtx, conn, lockName); err != nil {
			return errors.Join(err, discardConn(conn))
		}

		return conn.Close()
	})
}

// mysqlLockName shortens name to the length MySQL accepts.
func mysqlLockName(name string) string {
	if len(name) <= maxLockName {
		return name
	}

	sum := sha256.Sum256([]byte(name))

	return hex.EncodeToString(sum[:])[:maxLockName]
}

// onceUnlock wraps unlock so that it runs at most once; later calls return
// the first result.
func onceUnlock(unlock func(context.Context) error) func(context.Context) error {
	var (
		once   sync.Once
		result error
	)

	return func(ctx context.Context) error {
		once.Do(func() {
			result = unlock(ctx)
		})

		return result
	}
}

// acquire makes one nonblocking GET_LOCK attempt on the caller's executor.
// MySQL answering NULL means acquisition failed, not ordinary contention; the
// error wraps ErrLockNotAcquired and ownership must be treated as uncertain.
func acquire(ctx context.Context, conn executor, lockName string) (bool, error) {
	var granted sql.NullInt64

	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", lockName).Scan(&granted); err != nil {
		// Native cancellation can discard the connection and report ErrBadConn
		// instead of the deadline. Preserve both causes without trusting ownership.
		return false, fmt.Errorf("acquire lock %q: %w", lockName, errors.Join(err, ctx.Err()))
	}

	if !granted.Valid {
		return false, fmt.Errorf("acquire lock %q: %w", lockName, ErrLockNotAcquired)
	}

	return granted.Int64 == 1, nil
}

// release requires RELEASE_LOCK to confirm ownership with result 1. Zero or
// NULL is an unexpected ownership loss, not successful cleanup; callers must
// discard the physical session rather than assuming a successful query suffices.
func release(ctx context.Context, conn executor, lockName string) error {
	var released sql.NullInt64

	if err := conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released); err != nil {
		return fmt.Errorf("release lock %q: %w", lockName, err)
	}

	if !released.Valid || released.Int64 != 1 {
		return fmt.Errorf("release lock %q: ownership was not confirmed", lockName)
	}

	return nil
}

// discardConn closes conn and makes the pool drop it, so a session that may
// still hold a lock is never reused.
func discardConn(conn *sql.Conn) error {
	err := conn.Raw(func(any) error {
		return sqldriver.ErrBadConn
	})

	if errors.Is(err, sqldriver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		// Raw has already discarded the connection, or an earlier native
		// bad-connection path did. Calling Close again adds ErrConnDone.
		return nil
	}

	return err
}

// release releases a tracked native lock on the active executor and removes
// ownership only after confirmation. It never discards an active transaction's
// retained Conn, which would wait for that same transaction to finish.
func (session *session) release(ctx context.Context, name string) error {
	var conn executor = session.conn

	if session.active != nil {
		conn = session.active
	}

	if err := release(ctx, conn, name); err != nil {
		session.poison(err)

		return err
	}

	delete(session.locks, name)

	return nil
}

// releaseLocks releases only this callback's acquisitions. Nested transaction
// locks are registered to the outer transaction, so savepoint rollback cannot
// release them. A poisoned session skips SQL and is discarded after its native
// transaction has ended instead of returning possibly locked state to the pool.
func (scope *scope) releaseLocks(ctx context.Context) error {
	if scope.session.failure != nil {
		return nil
	}

	for name, owner := range scope.session.locks {
		if owner == scope {
			if err := scope.session.release(ctx, name); err != nil {
				return err
			}
		}
	}

	return nil
}
