package storage

import (
	"context"
	"time"
)

// sweepDue removes every expired entry when sweeping is enabled and the last
// sweep is at least cleanup old. Writes call it so abandoned keys are freed
// without a janitor. The mutex must be held.
func (driver *Store) sweepDue(now time.Time) {
	if driver.Cleanup < 0 || now.Sub(driver.LastSweep) < driver.Cleanup {
		return
	}

	driver.sweepLocked(now)
}

// Start launches the janitor unless Cleanup is negative. Starting an already
// started driver does nothing.
//
// Example:
//
//	driver := memorycache.New()
//	if err := driver.Start(ctx); err != nil {
//		return err
//	}
//	return driver.Stop(ctx)
func (driver *Store) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	driver.Lifecycle.Lock()
	defer driver.Lifecycle.Unlock()

	if driver.Cleanup < 0 || driver.Cancel != nil {
		return nil
	}

	loopCtx, cancel := context.WithCancel(context.Background())
	driver.Cancel = cancel
	driver.Done = make(chan struct{})

	go driver.janitor(loopCtx, driver.Done)

	return nil
}

// janitor removes expired entries every cleanup interval until ctx ends.
func (driver *Store) janitor(ctx context.Context, done chan<- struct{}) {
	defer close(done)

	ticker := time.NewTicker(driver.Cleanup)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			driver.sweep()
		}
	}
}

// sweep deletes every expired entry under the lock.
func (driver *Store) sweep() {
	driver.Mu.Lock()
	defer driver.Mu.Unlock()

	driver.sweepLocked(time.Now())
}

// sweepLocked deletes every expired entry and records now as the last sweep.
// The mutex must be held.
func (driver *Store) sweepLocked(now time.Time) {
	driver.LastSweep = now

	for key, stored := range driver.Entries {
		if stored.expired(now) {
			delete(driver.Entries, key)
		}
	}
}

// Stop ends the janitor and waits for it to exit or ctx to end. The driver
// stays usable afterwards; expired entries are then removed by access and by
// inline sweeps on writes.
//
// Example:
//
//	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
//	defer cancel()
//	if err := driver.Stop(cleanup); err != nil {
//		return err
//	}
func (driver *Store) Stop(ctx context.Context) error {
	driver.Lifecycle.Lock()

	cancel, done := driver.Cancel, driver.Done
	driver.Lifecycle.Unlock()

	if cancel == nil {
		return nil
	}

	cancel()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
