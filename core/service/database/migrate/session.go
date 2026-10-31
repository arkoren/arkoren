package migrate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"arkoren.dev/foundation/query"
)

const (
	// lockPollInterval separates refused acquisition attempts. A newly retained
	// session is released before this wait so contenders do not consume the pool.
	lockPollInterval = 20 * time.Millisecond

	// unlockTimeout bounds lock cleanup independently of caller cancellation.
	// The session driver discards its connection when release remains uncertain.
	unlockTimeout = 5 * time.Second
)

// locked acquires the migration lock before preparing or reading history, then
// runs fn on that same physical session. Each operation owns its history order:
// rollback follows batches/Add order, while status orders missing records by
// batch/time/id. Only refused TryLock attempts are retried, with their scope
// released before polling; any acquired-lock work or cleanup error is terminal.
// A prepared ledger freezes the operation's MySQL target and validates storage
// before history access. The handle is private to this retained callback scope.
func (migrator *Migrator) locked(ctx context.Context, fn func(context.Context, ledger, []record) error) error {
	if err := migrator.checkConfig(); err != nil {
		return err
	}

	// A deadline context would also expire the acquired session during work.
	// This watchdog can instead be stopped without canceling that lifetime.
	workCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var watchdog *time.Timer
	// deadlineAt bounds acquisition only; the watchdog stops once the lock is held.
	var deadlineAt time.Time

	if migrator.config.LockTimeout > 0 {
		deadlineAt = time.Now().Add(migrator.config.LockTimeout)
		watchdog = time.AfterFunc(migrator.config.LockTimeout, func() {
			cancel(context.DeadlineExceeded)
		})
		defer watchdog.Stop()
	}

	// lockError classifies only acquisition failures. The watchdog cancels with
	// a cause, so pool acquisition may return Canceled rather than that cause.
	lockError := func(err error) error {
		if ctx.Err() != nil {
			return fmt.Errorf("migrate lock: %w", errors.Join(ctx.Err(), err))
		}

		if errors.Is(context.Cause(workCtx), context.DeadlineExceeded) {
			return fmt.Errorf("migrate lock %q after %s: %w", migrator.config.LockName,
				migrator.config.LockTimeout, errors.Join(ErrLockTimeout, context.DeadlineExceeded, err))
		}

		return fmt.Errorf("migrate lock: %w", err)
	}

	for {
		if err := workCtx.Err(); err != nil {
			return lockError(err)
		}

		acquired := false

		err := migrator.db.WithSession(workCtx, func(scopedCtx context.Context) (e error) {
			unlock, held, err := migrator.db.TryLock(scopedCtx, migrator.config.LockName)

			if err != nil || !held {
				return err
			}

			acquired = true
			defer func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(scopedCtx), unlockTimeout)
				defer cleanupCancel()

				if err := unlock(cleanupCtx); err != nil {
					e = errors.Join(e, fmt.Errorf("migrate unlock: %w", err))
				}
			}()

			if watchdog != nil && (!watchdog.Stop() || !time.Now().Before(deadlineAt)) {
				cancel(context.DeadlineExceeded)

				return lockError(context.DeadlineExceeded)
			}

			if err := scopedCtx.Err(); err != nil {
				return lockError(err)
			}

			history, err := prepareLedger(scopedCtx, migrator.db, migrator.config.Table)

			if err != nil {
				return err
			}

			records, err := history.records(scopedCtx)

			if err != nil {
				return err
			}

			return fn(scopedCtx, history, records)
		})

		if acquired {
			return err
		}

		if err != nil {
			return lockError(err)
		}

		pause := time.NewTimer(lockPollInterval)

		select {
		case <-workCtx.Done():
			pause.Stop()

			return lockError(workCtx.Err())
		case <-pause.C:
		}
	}
}

// checkConfig validates the execution policy and bookkeeping identifier before
// a session is acquired. An unknown policy must not silently become warn/allow.
func (migrator *Migrator) checkConfig() error {
	if !slices.Contains([]OutOfOrder{OutOfOrderWarn, OutOfOrderAllow, OutOfOrderError}, migrator.config.OutOfOrder) {
		return fmt.Errorf("migrate out-of-order policy %q: %w", migrator.config.OutOfOrder, ErrInvalidMigration)
	}

	if _, err := query.Table(migrator.config.Table, func(statement *query.Builder) {
		statement.Select("id")
	}).Build(); err != nil {
		return fmt.Errorf("migrate table %q: %w", migrator.config.Table, errors.Join(ErrInvalidMigration, err))
	}

	return nil
}
