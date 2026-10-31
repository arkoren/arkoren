//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package filelock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockPollInterval is how long Acquire sleeps between attempts.
const lockPollInterval = 20 * time.Millisecond

// Acquire takes an exclusive flock on path, creating the file when needed.
// With wait it polls with LOCK_NB so ctx ends the wait; without it, it
// reports acquired false at once when another process holds the lock. The kernel drops the lock when
// the process exits, so a crashed holder never blocks others. The returned
// release function unlocks and closes the file.
func Acquire(ctx context.Context, path string, wait bool) (release func() error, acquired bool, err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)

	if err != nil {
		return nil, false, fmt.Errorf("open lock file: %w", err)
	}

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)

		if err == nil {
			return func() error {
				return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
			}, true, nil
		}

		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return nil, false, errors.Join(fmt.Errorf("flock %s: %w", path, err), file.Close())
		}

		if !wait {
			return nil, false, file.Close()
		}

		timer.Reset(lockPollInterval)

		select {
		case <-ctx.Done():
			return nil, false, errors.Join(fmt.Errorf("wait for lock file: %w", ctx.Err()), file.Close())
		case <-timer.C:
		}
	}
}
