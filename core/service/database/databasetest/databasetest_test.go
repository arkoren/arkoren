package databasetest_test

import (
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/databasetest"
)

// ExampleRun shows a driver test running the conformance suite. The factory
// connects to a disposable database and registers its cleanup with t.Cleanup.
func ExampleRun() {
	_ = func(t *testing.T, factory func(*testing.T) database.Driver) {
		databasetest.Run(t, factory)
	}
}

// ExampleWithTimeout shows how to give a slow remote database more time.
func ExampleWithTimeout() {
	_ = func(t *testing.T, factory func(*testing.T) database.Driver) {
		databasetest.Run(t, factory, databasetest.WithTimeout(2*time.Minute))
	}
}
