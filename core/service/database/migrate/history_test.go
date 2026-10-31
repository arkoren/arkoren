package migrate

import (
	"errors"
	"slices"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"
)

// TestRollbackUsesAddOrderWhenTimestampsTie exercises the public rollback seam
// using the existing history fake, without asserting private planning helpers.
func TestRollbackUsesAddOrderWhenTimestampsTie(t *testing.T) {
	t.Parallel()

	state := &routingState{records: []record{{ID: "z", Batch: 1}, {ID: "a", Batch: 1}}}
	migrator := New(database.New(&routingDriver{state: state}))

	if err := migrator.Add(NewMigration("z", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-z")))
		migration.Down(Exec(query.Raw("down-z")))
	}), NewMigration("a", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-a")))
		migration.Down(Exec(query.Raw("down-a")))
	})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Rollback(t.Context(), Steps(1)); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(state.calls, "transaction exec down-a") {
		t.Fatalf("Rollback(Steps(1)) calls = %v, want down-a", state.calls)
	}
}

// TestRollbackUsesAddOrderAfterClockMovesBackward verifies wall-clock readings
// cannot change which migration Steps(1) reverts within a single Up batch.
func TestRollbackUsesAddOrderAfterClockMovesBackward(t *testing.T) {
	t.Parallel()

	state := &routingState{records: []record{
		{ID: "first", Batch: 1, RanAt: time.Unix(2, 0)},
		{ID: "last", Batch: 1, RanAt: time.Unix(1, 0)},
	}}
	migrator := New(database.New(&routingDriver{state: state}))

	if err := migrator.Add(NewMigration("first", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-first")))
		migration.Down(Exec(query.Raw("down-first")))
	}), NewMigration("last", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-last")))
		migration.Down(Exec(query.Raw("down-last")))
	})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Rollback(t.Context(), Steps(1)); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(state.calls, "transaction exec down-last") {
		t.Fatalf("Rollback(Steps(1)) calls = %v, want down-last", state.calls)
	}
}

// TestRollbackKeepsBatchPriorityOverRegistrationOrder verifies a migration
// inserted earlier in the list by an out-of-order release is still newer when
// its recorded application batch is newer.
func TestRollbackKeepsBatchPriorityOverRegistrationOrder(t *testing.T) {
	t.Parallel()

	state := &routingState{records: []record{{ID: "newer", Batch: 2}, {ID: "older", Batch: 1}}}
	migrator := New(database.New(&routingDriver{state: state}))

	if err := migrator.Add(NewMigration("newer", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-newer")))
		migration.Down(Exec(query.Raw("down-newer")))
	}), NewMigration("older", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-older")))
		migration.Down(Exec(query.Raw("down-older")))
	})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Rollback(t.Context(), Steps(1)); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(state.calls, "transaction exec down-newer") {
		t.Fatalf("Rollback(Steps(1)) calls = %v, want down-newer", state.calls)
	}
}

// TestRollbackUnknownRecordBlocksAmbiguousBatch verifies Steps cannot guess a
// known target's order relative to an unregistered definition in that batch.
func TestRollbackUnknownRecordBlocksAmbiguousBatch(t *testing.T) {
	t.Parallel()

	state := &routingState{records: []record{
		{ID: "unknown", Batch: 1, RanAt: time.Unix(1, 0)},
		{ID: "known", Batch: 1, RanAt: time.Unix(2, 0)},
	}}
	migrator := New(database.New(&routingDriver{state: state}))

	if err := migrator.Add(NewMigration("known", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-known")))
		migration.Down(Exec(query.Raw("down-known")))
	})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Rollback(t.Context(), Steps(1)); !errors.Is(err, ErrUnknownMigration) {
		t.Fatalf("Rollback = %v, want ErrUnknownMigration", err)
	}

	if slices.Contains(state.calls, "transaction exec down-known") {
		t.Fatal("ambiguous rollback executed a known step")
	}
}

// TestUpAppendedGroupMigrationDoesNotConflictWithApplicationHistory verifies
// interleaved groups have independent append-only sequences, not global order.
func TestUpAppendedGroupMigrationDoesNotConflictWithApplicationHistory(t *testing.T) {
	t.Parallel()

	state := &routingState{records: []record{{ID: "package/first", Batch: 1}, {ID: "app", Batch: 1}}}
	migrator := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
		migrator.OutOfOrder(OutOfOrderError)
	})

	if err := migrator.Add(NewGroup("package").Migrations(
		NewMigration("first", func(migration *MigrationBuilder) {
			migration.Up(Exec(query.Raw("up-first")))
		}),
		NewMigration("second", func(migration *MigrationBuilder) {
			migration.Up(Exec(query.Raw("up-second")))
		}),
	)...); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Add(NewMigration("app", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-app")))
	})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Up(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(state.calls, "transaction exec up-second") {
		t.Fatalf("Up calls = %v, want up-second", state.calls)
	}
}

// TestUpRejectsEarlierPendingMigrationWithinGroup verifies the error policy
// stops before executing any step, while Status exposes the same classification.
func TestUpRejectsEarlierPendingMigrationWithinGroup(t *testing.T) {
	t.Parallel()

	state := &routingState{records: []record{{ID: "package/second", Batch: 1}}}
	migrator := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
		migrator.OutOfOrder(OutOfOrderError)
	})

	if err := migrator.Add(NewGroup("package").Migrations(
		NewMigration("first", func(migration *MigrationBuilder) {
			migration.Up(Exec(query.Raw("up-first")))
		}),
		NewMigration("second", func(migration *MigrationBuilder) {
			migration.Up(Exec(query.Raw("up-second")))
		}),
	)...); err != nil {
		t.Fatal(err)
	}

	statuses, err := migrator.Status(t.Context())

	if err != nil {
		t.Fatal(err)
	}

	if !statuses[0].OutOfOrder || statuses[1].OutOfOrder {
		t.Fatalf("Status = %+v", statuses)
	}

	if err := migrator.Up(t.Context()); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("Up = %v, want ErrOutOfOrder", err)
	}

	if slices.Contains(state.calls, "transaction exec up-first") {
		t.Fatal("out-of-order step executed")
	}
}

// TestUpRejectsUnknownOutOfOrderPolicy verifies a misspelled safety policy
// fails before acquiring a session or running the pending migration.
func TestUpRejectsUnknownOutOfOrderPolicy(t *testing.T) {
	t.Parallel()

	state := &routingState{}
	migrator := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
		migrator.OutOfOrder("erorr")
	})

	if err := migrator.Add(NewMigration("pending", func(migration *MigrationBuilder) {
		migration.Up(Exec(query.Raw("up-pending")))
	})); err != nil {
		t.Fatal(err)
	}

	if err := migrator.Up(t.Context()); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("Up = %v, want ErrInvalidMigration", err)
	}

	if state.sessions != 0 {
		t.Fatal("invalid policy acquired a session")
	}
}

// TestStatusRejectsUnknownOutOfOrderPolicy verifies inspection does not create
// a bookkeeping table when the supplied execution policy is invalid.
func TestStatusRejectsUnknownOutOfOrderPolicy(t *testing.T) {
	t.Parallel()

	state := &routingState{}
	migrator := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
		migrator.OutOfOrder("unknown")
	})

	_, err := migrator.Status(t.Context())

	if !errors.Is(err, ErrInvalidMigration) || state.sessions != 0 {
		t.Fatalf("Status = %v, sessions = %d", err, state.sessions)
	}
}

// TestRollbackRejectsUnknownOutOfOrderPolicy checks destructive operations use
// the same pre-I/O validation as Up and Status, even with empty history.
func TestRollbackRejectsUnknownOutOfOrderPolicy(t *testing.T) {
	t.Parallel()

	state := &routingState{}
	migrator := New(database.New(&routingDriver{state: state}), func(migrator *Builder) {
		migrator.OutOfOrder("unknown")
	})

	if err := migrator.Rollback(t.Context()); !errors.Is(err, ErrInvalidMigration) {
		t.Fatalf("Rollback = %v, want ErrInvalidMigration", err)
	}

	if state.sessions != 0 {
		t.Fatal("invalid policy acquired a session")
	}
}
