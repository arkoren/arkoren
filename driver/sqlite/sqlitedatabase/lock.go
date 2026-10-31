package sqlitedatabase

import (
	"context"
	"fmt"
	"sync"
)

// localLock is an in-process lock whose waiters can give up through a context.
type localLock struct {
	// token holds one value while the lock is free.
	token chan struct{}

	// waiters counts the holders and waiters that reference the lock.
	waiters int
}

var (
	// localLocksMutex guards localLocks.
	localLocksMutex sync.Mutex

	// localLocks holds the in-process locks by name.
	localLocks = map[string]*localLock{}
)

// Lock blocks until the named lock is held or ctx ends. The returned unlock
// function releases the lock and is safe to call more than once.
//
// The lock is an in-process lock shared by all drivers in the process and,
// for a file-backed database on Linux, macOS, or a BSD, an
// operating system file lock on a sibling lock file, so the lock also spans
// processes. Inside a transaction the lock is held until the transaction ends
// and unlock does nothing.
// In an ordinary session callback, remaining locks are released on callback
// return. An inner session owns its new locks without adopting its parent's.
//
// Example:
//
//	unlock, err := driver.Lock(ctx, "nightly-report")
//	if err != nil {
//		return err
//	}
//	defer unlock(context.WithoutCancel(ctx))
func (driver *Driver) Lock(ctx context.Context, name string) (func(context.Context) error, error) {
	if err := driver.check(ctx); err != nil {
		return nil, err
	}

	unlock, _, err := driver.lock(ctx, name, true)

	if err != nil {
		return nil, err
	}

	return driver.ownLock(unlock), nil
}

// TryLock takes the named lock without waiting. It reports acquired false,
// with a nil error, when another holder has the lock. The returned unlock
// function releases the lock and is safe to call more than once.
//
// The lock is the one [Driver.Lock] takes, so the two exclude each other in
// the process and, where [Driver.Lock] spans processes, across them. Inside a
// transaction the lock is held until the transaction ends and unlock does
// nothing.
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

	unlock, acquired, err := driver.lock(ctx, name, false)

	if err != nil || !acquired {
		return nil, false, err
	}

	return driver.ownLock(unlock), true, nil
}

// ownLock transfers a successful acquisition to the outermost transaction, or
// this session callback when no transaction exists. The shared local/file lock
// primitives enforce non-reentrancy before ownership is recorded.
func (driver *Driver) ownLock(unlock func(context.Context) error) func(context.Context) error {
	if driver.scope == nil {
		return unlock
	}

	owner := driver.scope
	transactional := false

	for current := driver.scope; current != nil; current = current.parent {
		if current.tx != nil {
			owner, transactional = current, true
		}
	}

	release := onceUnlock(func(ctx context.Context) error {
		err := unlock(ctx)

		if err != nil {
			owner.poison(err)
		}

		return err
	})
	owner.held = append(owner.held, release)

	if transactional {
		return func(context.Context) error {
			return nil
		}
	}

	return release
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

// lockLocal takes the in-process lock called name. With wait it blocks until
// the lock is held or ctx ends; without it, it reports acquired false at once
// when the lock is held. A ctx that already ended is an error either way.
func lockLocal(ctx context.Context, name string, wait bool) (unlock func(context.Context) error, acquired bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("wait for lock %q: %w", name, err)
	}

	localLocksMutex.Lock()

	lock, ok := localLocks[name]

	if !ok {
		lock = &localLock{
			token: make(chan struct{}, 1),
		}
		lock.token <- struct{}{}
		localLocks[name] = lock
	}

	lock.waiters++
	localLocksMutex.Unlock()

	granted := func() (func(context.Context) error, bool, error) {
		return onceUnlock(func(context.Context) error {
			lock.token <- struct{}{}
			dropLocal(name, lock)

			return nil
		}), true, nil
	}

	if !wait {
		select {
		case <-lock.token:
			return granted()
		default:
			dropLocal(name, lock)

			return nil, false, nil
		}
	}

	select {
	case <-lock.token:
		return granted()
	case <-ctx.Done():
		dropLocal(name, lock)

		return nil, false, fmt.Errorf("wait for lock %q: %w", name, ctx.Err())
	}
}

// dropLocal removes one reference to lock and forgets it when unused.
func dropLocal(name string, lock *localLock) {
	localLocksMutex.Lock()
	defer localLocksMutex.Unlock()

	lock.waiters--

	if lock.waiters == 0 {
		delete(localLocks, name)
	}
}
