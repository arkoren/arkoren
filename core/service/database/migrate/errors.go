package migrate

import "errors"

var (
	// ErrInvalidMigration reports an invalid definition or argument.
	ErrInvalidMigration = errors.New("invalid migration")

	// ErrDuplicateID reports a migration id that is already added.
	ErrDuplicateID = errors.New("duplicate migration id")

	// ErrOutOfOrder reports pending migrations that precede applied ones.
	ErrOutOfOrder = errors.New("out-of-order migrations")

	// ErrIrreversible reports a migration without a Down step.
	ErrIrreversible = errors.New("irreversible migration")

	// ErrLockTimeout reports that session acquisition or lock contention used
	// the [Builder.LockTimeout] budget before protected work began. The error
	// also wraps [context.DeadlineExceeded]. Caller-owned cancellation and
	// deadlines are returned without this marker; work is never replayed after
	// successful acquisition, even if its session or cleanup fails.
	ErrLockTimeout = errors.New("migration lock timeout")

	// ErrUnknownMigration reports a recorded migration that is not added.
	ErrUnknownMigration = errors.New("unknown migration")
)
