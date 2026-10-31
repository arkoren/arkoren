package natsbroadcast

import (
	"log/slog"
	"testing"

	"github.com/nats-io/nats.go"
)

// TestNewFromUsesLogger checks borrowed connections still accept Logger without
// creating or reconfiguring a native connection.
func TestNewFromUsesLogger(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	driver, err := NewFrom(&nats.Conn{}, func(broker *Builder) {
		broker.Logger(logger)
	})

	if err != nil {
		t.Fatal(err)
	}

	if driver.logger != logger {
		t.Fatal("borrowed-connection construction ignored Logger")
	}
}

// TestBuilderCopiesOwnedConnectionSlices checks both input capture and
// publication, including direct edits through a retained construction root.
func TestBuilderCopiesOwnedConnectionSlices(t *testing.T) {
	t.Parallel()

	urls := []string{"nats://localhost:4222"}
	roots := []string{"root.pem"}
	var retained *Builder

	config := configureDriver(func(broker *Builder) {
		retained = broker
		broker.URLs(urls...)
		broker.RootCAs(roots...)

		urls[0] = "nats://localhost:4223"
		roots[0] = "other.pem"
	})

	retained.config.URLs[0] = "nats://localhost:4224"
	retained.config.RootCAs[0] = "retained.pem"

	if config.URLs[0] != "nats://localhost:4222" || config.RootCAs[0] != "root.pem" {
		t.Fatal("owned connection slices were not detached")
	}
}

// TestNewFromAppliesOnlyBorrowedConnectionSettings avoids SDK connection
// options, including invalid credentials, while preserving routing settings.
func TestNewFromAppliesOnlyBorrowedConnectionSettings(t *testing.T) {
	t.Parallel()

	connection := &nats.Conn{}
	calls := 0
	driver, err := NewFrom(connection, func(broker *Builder) {
		calls++
		broker.NKeySeed("invalid-seed-that-must-not-be-read")
		broker.RootCAs("missing-file-that-must-not-be-read")
		broker.Prefix("billing")
		broker.QueueGroup("workers")
	})

	if err != nil {
		t.Fatal(err)
	}

	if calls != 1 || driver.owned || driver.Connection() != connection {
		t.Fatal("borrowed connection ownership or callback execution changed")
	}

	if driver.prefix != "billing" || driver.queueGroup != "workers" {
		t.Fatal("borrowed connection routing settings were ignored")
	}
}

// TestNewValidatesCompletedConnectionSettings verifies later callbacks can
// replace invalid intermediate settings before ordinary validation runs.
func TestNewValidatesCompletedConnectionSettings(t *testing.T) {
	t.Parallel()

	var retained *Builder
	driver, err := New(func(broker *Builder) {
		retained = broker
		broker.NKeySeed("invalid")
	}, func(broker *Builder) {
		broker.NKeySeed("")
		broker.Prefix("billing")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Prefix("shipping")

	if driver.prefix != "billing" {
		t.Fatal("driver retained the mutable construction root")
	}
}
