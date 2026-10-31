package postgresdatabase

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"arkoren.dev/core/service/database"
)

// lockPollInterval bounds acquisition frequency without retaining a refused
// root attempt's pool connection between tries. No FIFO ordering is promised.
const lockPollInterval = 10 * time.Millisecond

// advisoryLock records one native acquisition and its owning callback. The
// session map prevents native reentrancy; pointer identity makes stale unlock
// closures harmless after another scope acquires the same canonical key.
type advisoryLock struct {
	// owner determines cleanup lifetime, including outermost transaction locks.
	owner *scope

	// key is the exact signed 64-bit identifier passed to PostgreSQL.
	key int64
}

// Lock waits until the named advisory lock is held or ctx ends. Outside a
// session, refused attempts return their connections before polling; a successful
// acquisition retains one pool slot until unlock. Session and transaction calls
// use their existing connection and never acquire an additional slot.
//
// Locks are not reentrant, including on the same physical session. A transaction
// owns fresh acquisitions until its outermost end, across nested rollback and
// panic; its unlock is a no-op. Other callback acquisitions end at unlock or
// callback return. Use direct or session-pooled PostgreSQL, not transaction-mode
// PgBouncer, because all managed locks are session-level advisory locks.
//
// Example:
//
//	unlock, err := driver.Lock(ctx, "nightly-report")
//	if err != nil {
//		return err
//	}
//	defer unlock(ctx)
func (driver *Driver) Lock(ctx context.Context, name string) (func(context.Context) error, error) {
	for {
		unlock, acquired, err := driver.TryLock(ctx, name)

		if err != nil {
			return nil, err
		}

		if acquired {
			return unlock, nil
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

// TryLock asks once for the named lock without waiting for lock ownership.
// Pool acquisition and network I/O still obey ctx. An empty name fails with
// [database.ErrEmptyLockName]; refusal returns nil, false, nil, including when
// another managed scope on this physical session owns the canonical key.
//
// The returned unlock is idempotent. Session cleanup uses a detached five-second
// budget even if the supplied context has ended. An uncertain acquisition or
// unlock poisons the session so it cannot return to the pool as healthy.
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

	if driver.scope == nil {
		pinned, err := driver.acquire(ctx)

		if err != nil {
			return nil, false, err
		}

		_, acquired, err := pinned.TryLock(ctx, name)

		if err != nil || !acquired {
			endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
			defer cancel()

			return nil, false, errors.Join(err, pinned.scope.session.release(endCtx))
		}

		var once sync.Once
		var result error

		return func(ctx context.Context) error {
			once.Do(func() {
				endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
				defer cancel()

				pinned.scope.err = database.ErrSessionDone
				result = errors.Join(pinned.scope.cleanup(endCtx), pinned.scope.session.release(endCtx))
			})

			return result
		}, true, nil
	}

	session := driver.scope.session
	key := lockKey(name)

	if _, held := session.locks[key]; held {
		return nil, false, nil
	}

	var acquired bool

	if err := driver.Find(ctx, "SELECT pg_try_advisory_lock($1)", &acquired, key); err != nil {
		session.err = fmt.Errorf("try advisory lock: %w", err)

		return nil, false, session.err
	}

	if !acquired {
		return nil, false, nil
	}

	owner := driver.scope

	if owner.outer != nil {
		owner = owner.outer
	}

	lock := &advisoryLock{
		owner: owner,
		key:   key,
	}
	session.locks[key] = lock

	if owner.tx != nil {
		return func(context.Context) error {
			return nil
		}, true, nil
	}

	var once sync.Once
	var result error

	return func(ctx context.Context) error {
		once.Do(func() {
			endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
			defer cancel()

			result = lock.release(endCtx)
		})

		return result
	}, true, nil
}

// release unlocks exactly this acquisition, never a later owner of the key.
// Native false is an ownership violation, not a successful cleanup. Poisoning
// defers physical disposal until the active transaction has been finalized.
func (lock *advisoryLock) release(ctx context.Context) error {
	session := lock.owner.session

	if session.locks[lock.key] != lock {
		return nil
	}

	if session.err != nil {
		return session.err
	}

	var released bool

	err := session.conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", lock.key).Scan(&released)

	if err == nil && !released {
		err = errors.New("advisory lock ownership was lost")
	}

	if err != nil {
		session.err = fmt.Errorf("release advisory lock: %w", err)

		return session.err
	}

	delete(session.locks, lock.key)

	return nil
}

// lockKey hashes arbitrary names to PostgreSQL's signed 64-bit advisory key.
// Managed ownership uses this key too, so a hash collision cannot stack locks.
func lockKey(name string) int64 {
	sum := sha256.Sum256([]byte(name))

	return int64(binary.BigEndian.Uint64(sum[:8]))
}
