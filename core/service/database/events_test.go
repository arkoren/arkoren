package database_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/database"
)

// transactionEvents returns a database publishing on a bus and the slice of
// event names it records, "committed" or "rolled-back".
func transactionEvents(t *testing.T) (*database.Database, *[]string) {
	t.Helper()

	bus := event.New()
	var seen []string
	record := func(name string) func(context.Context, database.TransactionEvent) error {
		return func(context.Context, database.TransactionEvent) error {
			seen = append(seen, name)

			return nil
		}
	}

	if _, err := bus.Listen(database.TransactionCommitted, record("committed")); err != nil {
		t.Fatal(err)
	}

	if _, err := bus.Listen(database.TransactionRolledBack, record("rolled-back")); err != nil {
		t.Fatal(err)
	}

	var log []string

	return database.New(&txDriver{log: &log}, func(connection *database.Builder) {
		connection.Events(bus)
	}), &seen
}

// TestTransactionPublishesCommittedOnce verifies nested transactions produce
// only the outer transaction's committed event.
func TestTransactionPublishesCommittedOnce(t *testing.T) {
	t.Parallel()

	db, seen := transactionEvents(t)

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		return db.WithTransaction(ctx, func(context.Context) error {
			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	if len(*seen) != 1 || (*seen)[0] != "committed" {
		t.Fatalf("events = %v, want one committed", *seen)
	}
}

// TestTransactionPublishesRolledBack verifies failed work publishes rollback
// rather than a committed event.
func TestTransactionPublishesRolledBack(t *testing.T) {
	t.Parallel()

	db, seen := transactionEvents(t)

	err := db.WithTransaction(t.Context(), func(ctx context.Context) error {
		_ = db.WithTransaction(ctx, func(context.Context) error {
			return errBoom
		}) // The inner rollback is the point.

		return errBoom
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want errBoom", err)
	}

	if len(*seen) != 1 || (*seen)[0] != "rolled-back" {
		t.Fatalf("events = %v, want one rolled-back", *seen)
	}
}
