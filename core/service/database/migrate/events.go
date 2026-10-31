package migrate

import (
	"context"
	"time"

	"arkoren.dev/core/event"
)

// MigrationEvent is the payload of [Applied], [RolledBack], and [Failed].
type MigrationEvent struct {
	// ID is the migration identifier.
	ID string

	// Group is the migration group; empty for the application's own.
	Group string

	// Batch is the batch the migration was applied in.
	Batch int

	// Rollback reports whether the migration was being reverted.
	Rollback bool

	// Duration is how long the migration ran.
	Duration time.Duration

	// Err is the error that failed the migration; nil for [Applied] and
	// [RolledBack].
	Err error
}

var (
	// Applied is published after [Migrator.Up] applies and records one
	// migration.
	Applied = event.NewTopic[MigrationEvent]("database.migration.applied")

	// RolledBack is published after [Migrator.Rollback] reverts one
	// migration and deletes its record.
	RolledBack = event.NewTopic[MigrationEvent]("database.migration.rolled-back")

	// Failed is published when applying or reverting a migration fails; the
	// run stops there.
	Failed = event.NewTopic[MigrationEvent]("database.migration.failed")
)

// emit reports the migration's actual completion, not merely its returned error.
// A confirmed commit followed by cleanup or hook failure publishes done with a
// nil payload error; incomplete work publishes Failed with the underlying error.
// The caller still stops the run on any error, including post-commit failures.
func (migrator *Migrator) emit(ctx context.Context, done event.Topic[MigrationEvent], definition Definition, batch int, rollback bool, duration time.Duration, completed bool, err error) {
	payload := MigrationEvent{
		ID:       definition.ID(),
		Group:    definition.Group(),
		Batch:    batch,
		Rollback: rollback,
		Duration: duration,
	}

	if !completed {
		payload.Err = err

		migrator.config.Events.Emit(ctx, Failed, payload)

		return
	}

	migrator.config.Events.Emit(ctx, done, payload)
}
