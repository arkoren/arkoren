package migrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"arkoren.dev/core/service/database"
)

// Status describes one migration for [Migrator.Status].
type Status struct {
	// ID is the migration id.
	ID string

	// Group is the migration's group, or the empty string for the default
	// group and for a Missing migration.
	Group string

	// Ran reports whether the migration is recorded as applied.
	Ran bool

	// Batch is the batch that applied the migration, or zero.
	Batch int

	// RanAt is when the migration was applied, in UTC, or zero.
	RanAt time.Time

	// OutOfOrder marks a pending migration that precedes an applied one of
	// the same group.
	OutOfOrder bool

	// Missing marks a recorded migration that is not added to the migrator.
	Missing bool
}

// RollbackOption changes how [Migrator.Rollback] chooses migrations.
type RollbackOption func(*rollbackConfig)

// rollbackConfig holds the rollback settings.
type rollbackConfig struct {
	// steps is the number of migrations to revert, or zero for the last batch.
	steps int

	// stepsSet records that [Steps] was used.
	stepsSet bool
}

// Steps reverts the last n applied migrations instead of the last batch. An n
// below one makes Rollback fail with [ErrInvalidMigration].
func Steps(n int) RollbackOption {
	return func(config *rollbackConfig) {
		config.steps = n
		config.stepsSet = true
	}
}

// Migrator applies migrations to a database. Create one with [New].
type Migrator struct {
	// db is the database that migrations run on.
	db *database.Database

	// config holds the settings.
	config settings

	// mu guards migrations.
	mu sync.Mutex

	// migrations holds the added definitions in Add order.
	migrations []Definition
}

// New creates a Migrator for db. It performs no I/O.
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
//
// Example:
//
//	migrate.New(database, func(migrator *migrate.Builder) {
//		migrator.LockTimeout(time.Minute)
//	})
func New(db *database.Database, configure ...BuilderFunc) *Migrator {
	return &Migrator{db: db, config: newSettings(configure...)}
}

// Database returns the database the migrator runs on.
func (migrator *Migrator) Database() *database.Database {
	return migrator.db
}

// Add registers migrations in order. Add order is run order. It adds all of
// them or none. It returns [ErrInvalidMigration] for an invalid definition and
// [ErrDuplicateID] for an id that is already added. A definition restricted
// by [MigrationBuilder.Only] to dialects that exclude the database's dialect is
// invalid: Add reports it as [ErrInvalidMigration] naming the id, the allowed
// dialects, and the database dialect, before anything is registered or run.
func (migrator *Migrator) Add(migrations ...Definition) error {
	migrator.mu.Lock()
	defer migrator.mu.Unlock()

	dialect := migrator.db.Dialect()
	seen := map[string]bool{}

	for _, migration := range migrations {
		if err := migration.Err(); err != nil {
			return fmt.Errorf("add migration: %w", err)
		}

		dialects := migration.Dialects()

		if dialects != nil && !slices.Contains(dialects, dialect) {
			return fmt.Errorf("add migration %s: %w: only for %v, database is %s",
				migration.ID(), ErrInvalidMigration, dialects, dialect)
		}

		taken := seen[migration.ID()] || slices.ContainsFunc(migrator.migrations, func(other Definition) bool {
			return other.ID() == migration.ID()
		})

		if taken {
			return fmt.Errorf("add migration %s: %w", migration.ID(), ErrDuplicateID)
		}

		seen[migration.ID()] = true
	}

	migrator.migrations = append(migrator.migrations, migrations...)

	return nil
}

// registered returns a copy of the added definitions.
func (migrator *Migrator) registered() []Definition {
	migrator.mu.Lock()
	defer migrator.mu.Unlock()

	return slices.Clone(migrator.migrations)
}

// Up applies pending migrations in Add order while retaining the lock-owning
// physical session. It stops at the first failure without reconnecting or
// replaying work; earlier migration commits stay recorded. Each transactional
// migration commits separately unless ctx already carries an outer transaction.
// See [Builder.OutOfOrder] for pending migrations preceding applied ones.
// MySQL verifies canonical binary ledger storage before history or callbacks;
// unsupported layouts return ErrInvalidMigration without automatic conversion.
//
// Example:
//
//	err := migrator.Up(ctx)
//	if err != nil {
//		return err // Inspect the cause; never blindly replay nontransactional work.
//	}
func (migrator *Migrator) Up(ctx context.Context) error {
	definitions := migrator.registered()

	return migrator.locked(ctx, func(ctx context.Context, history ledger, records []record) error {
		applied := map[string]bool{}
		batch := 0

		for _, existing := range records {
			applied[existing.ID] = true
			batch = max(batch, existing.Batch)
		}

		batch++

		late := lateMigrations(definitions, applied)

		var pending, outOfOrder []Definition

		for index, definition := range definitions {
			if applied[definition.ID()] {
				continue
			}

			pending = append(pending, definition)

			if late[index] {
				outOfOrder = append(outOfOrder, definition)
			}
		}

		if err := migrator.checkOutOfOrder(ctx, outOfOrder); err != nil {
			return err
		}

		for _, definition := range pending {
			startedAt := time.Now()

			completed, err := migrator.runUp(ctx, history, definition, batch)
			migrator.emit(ctx, Applied, definition, batch, false, time.Since(startedAt), completed, err)

			if err != nil {
				return err
			}

			migrator.config.Logger.InfoContext(ctx, "migration applied",
				slog.String("id", definition.ID()), slog.Duration("duration", time.Since(startedAt)))
		}

		return nil
	})
}

// inTransaction runs fn in a child transaction on the pinned session when DDL
// is transactional and the definition requests it. completed distinguishes a
// successful body and confirmed commit with post-commit errors from a rolled-back
// body that itself returned [database.ErrAfterCommit]. A caller's outer transaction
// controls final commit; otherwise each migration commits independently.
func (migrator *Migrator) inTransaction(ctx context.Context, definition Definition, fn func(context.Context) error) (bool, error) {
	if !definition.Transactional() || !migrator.db.Dialect().TransactionalDDL() {
		err := fn(ctx)

		return err == nil, err
	}

	bodyCompleted := false

	err := migrator.db.WithTransaction(ctx, func(ctx context.Context) error {
		err := fn(ctx)

		bodyCompleted = err == nil

		return err
	})

	return err == nil || bodyCompleted && errors.Is(err, database.ErrAfterCommit), err
}

// runSteps invokes steps in order with the scoped context and wraps failures
// with their step number. Cancellation prevents invoking subsequent callbacks,
// including callbacks that would otherwise do no SQL and ignore cancellation.
func runSteps(ctx context.Context, db *database.Database, steps []Func) error {
	for index, step := range steps {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("step %d: %w", index+1, err)
		}

		if err := step(ctx, db); err != nil {
			return fmt.Errorf("step %d: %w", index+1, err)
		}
	}

	return nil
}

// runUp applies one migration and records it on the scoped executor. completed
// remains true after confirmed commit even if hooks or commit cleanup fail, so
// the caller can publish the actual outcome while still stopping on the error.
func (migrator *Migrator) runUp(ctx context.Context, history ledger, definition Definition, batch int) (bool, error) {
	completed, err := migrator.inTransaction(ctx, definition, func(ctx context.Context) error {
		if err := runSteps(ctx, migrator.db, definition.up); err != nil {
			return err
		}

		return history.insert(ctx, definition.ID(), batch)
	})

	if err != nil {
		return completed, fmt.Errorf("migrate up %s: %w", definition.ID(), err)
	}

	return completed, nil
}

// Rollback reverts the last batch, or the last n migrations with [Steps], in
// reverse application order under the migration lock. It validates every
// target before running anything: [ErrUnknownMigration] for a recorded
// migration that is not added, [ErrIrreversible] for one without Down steps.
// History, planning, steps, and bookkeeping share the lock-owning session; a
// lost session stops the run without replay. Earlier Down commits remain final
// unless ctx carries a surrounding transaction.
// Batches are reverted newest first; within a batch the registered Add order
// is reversed, independent of timestamp precision or clock changes. Preserve
// the original relative Add order of applied definitions. Unknown records in
// a batch take precedence over known targets and fail with ErrUnknownMigration:
// re-register their definitions rather than guessing their application order.
// MySQL validates actual binary ledger metadata before any Down callback or
// deletion, and never converts an existing unsupported ledger automatically.
//
// Example:
//
//	err := migrator.Rollback(ctx, migrate.Steps(1))
//	if err != nil {
//		return err
//	}
func (migrator *Migrator) Rollback(ctx context.Context, opts ...RollbackOption) error {
	var config rollbackConfig

	for _, opt := range opts {
		opt(&config)
	}

	if config.stepsSet && config.steps < 1 {
		return fmt.Errorf("migrate rollback: %w: steps must be greater than zero", ErrInvalidMigration)
	}

	registered := migrator.registered()
	definitions := map[string]Definition{}

	for _, definition := range registered {
		definitions[definition.ID()] = definition
	}

	return migrator.locked(ctx, func(ctx context.Context, history ledger, records []record) error {
		orderRollback(records, registered)

		targets := records

		if config.steps > 0 {
			targets = records[:min(config.steps, len(records))]
		} else if len(records) > 0 {
			count := 0

			for count < len(records) && records[count].Batch == records[0].Batch {
				count++
			}

			targets = records[:count]
		}

		for _, target := range targets {
			definition, ok := definitions[target.ID]

			if !ok {
				return fmt.Errorf("migrate rollback %s: %w", target.ID, ErrUnknownMigration)
			}

			if !definition.Reversible() {
				return fmt.Errorf("migrate rollback %s: %w", target.ID, ErrIrreversible)
			}
		}

		for _, target := range targets {
			startedAt := time.Now()

			completed, err := migrator.runDown(ctx, history, definitions[target.ID])
			migrator.emit(ctx, RolledBack, definitions[target.ID], target.Batch, true, time.Since(startedAt), completed, err)

			if err != nil {
				return err
			}

			migrator.config.Logger.InfoContext(ctx, "migration rolled back",
				slog.String("id", target.ID), slog.Duration("duration", time.Since(startedAt)))
		}

		return nil
	})
}

// runDown reverts one migration and deletes its record on the pinned executor.
// Its completion result has the same confirmed-commit meaning as [Migrator.runUp], including
// an error after the schema and ledger change already became final.
func (migrator *Migrator) runDown(ctx context.Context, history ledger, definition Definition) (bool, error) {
	completed, err := migrator.inTransaction(ctx, definition, func(ctx context.Context) error {
		if err := runSteps(ctx, migrator.db, definition.down); err != nil {
			return err
		}

		return history.delete(ctx, definition.ID())
	})

	if err != nil {
		return completed, fmt.Errorf("migrate down %s: %w", definition.ID(), err)
	}

	return completed, nil
}

// Status lists the registered migrations in Add order, then recorded
// migrations that are not registered, marked Missing.
// Like [Migrator.Up], it takes the migration lock and creates the
// bookkeeping table when it does not exist, so it writes to a fresh database.
// Both preparation and history reads use the lock-owning session. Acquisition,
// session loss, or cleanup failures return a nil result rather than a snapshot
// whose protected scope did not complete successfully.
// On MySQL this also verifies canonical binary ledger storage. An unsupported
// or unverifiable existing table returns nil and an error, never an ALTER.
//
// Example:
//
//	statuses, err := migrator.Status(ctx)
//	if err != nil {
//		return err
//	}
//	for _, status := range statuses {
//		fmt.Println(status.ID, status.Ran)
//	}
func (migrator *Migrator) Status(ctx context.Context) ([]Status, error) {
	definitions := migrator.registered()

	var statuses []Status

	err := migrator.locked(ctx, func(_ context.Context, _ ledger, records []record) error {
		orderStatusHistory(records)

		byID := map[string]record{}
		applied := map[string]bool{}

		for _, existing := range records {
			byID[existing.ID] = existing
			applied[existing.ID] = true
		}

		late := lateMigrations(definitions, applied)

		known := map[string]bool{}

		for index, definition := range definitions {
			known[definition.ID()] = true

			existing, ran := byID[definition.ID()]
			statuses = append(statuses, Status{
				ID:         definition.ID(),
				Group:      definition.Group(),
				Ran:        ran,
				Batch:      existing.Batch,
				RanAt:      parseTime(existing.RanAt),
				OutOfOrder: late[index],
			})
		}

		for _, existing := range records {
			if !known[existing.ID] {
				statuses = append(statuses, Status{
					ID: existing.ID, Ran: true, Batch: existing.Batch,
					RanAt: parseTime(existing.RanAt), Missing: true,
				})
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return statuses, nil
}
