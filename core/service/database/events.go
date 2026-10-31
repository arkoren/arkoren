package database

import "arkoren.dev/core/event"

// TransactionEvent is the payload of [TransactionCommitted] and
// [TransactionRolledBack].
type TransactionEvent struct {
	// System is the db.system.name telemetry attribute of the database.
	System string

	// Namespace is [Builder.Namespace]; empty when it is not set.
	Namespace string

	// Err records the body, context, or commit failure. It is nil for a
	// confirmed commit, even when later lock cleanup or after-commit hooks
	// fail and WithTransaction returns ErrAfterCommit.
	Err error
}

var (
	// TransactionCommitted is published after a confirmed outermost commit,
	// before hooks. Post-commit driver cleanup errors do not change this event
	// to a rollback; the caller still receives ErrAfterCommit.
	TransactionCommitted = event.NewTopic[TransactionEvent]("database.transaction.committed")

	// TransactionRolledBack is published when an outermost transaction
	// fails before confirmed commit: its body failed, its context ended, or
	// the commit failed. A callback-origin ErrAfterCommit still counts as a
	// body failure rather than proof this transaction committed.
	TransactionRolledBack = event.NewTopic[TransactionEvent]("database.transaction.rolled-back")
)

// transactionEvent returns the payload for a transaction that ended with err.
func (database *Database) transactionEvent(err error) TransactionEvent {
	return TransactionEvent{
		System:    database.system,
		Namespace: database.config.Namespace,
		Err:       err,
	}
}
