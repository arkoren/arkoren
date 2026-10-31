package databasetest

import (
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/databasetest/internal/suite"
)

// defaultTimeout bounds each subtest's operations unless [WithTimeout]
// overrides it.
const defaultTimeout = 30 * time.Second

// settings holds the suite configuration built from [Option] values.
type settings struct {
	// timeout bounds the context handed to each subtest.
	timeout time.Duration
}

// Option configures [Run].
type Option func(*settings)

// WithTimeout bounds every subtest's operations, for slow remote databases.
// A zero or negative timeout keeps the default of 30 seconds.
func WithTimeout(timeout time.Duration) Option {
	return func(s *settings) {
		if timeout > 0 {
			s.timeout = timeout
		}
	}
}

// Run executes the conformance suite against drivers returned by newDriver.
//
// newDriver is called once per subtest and must return a driver connected to
// an empty or disposable database, registering any cleanup on t. Run creates
// and drops its own tables and never touches other ones.
//
// Run reports every deviation with t.Errorf or t.Fatalf in a subtest named
// after the behavior.
//
// Example:
//
//	databasetest.Run(t, func(t *testing.T) database.Driver {
//		return newTestDriver(t)
//	}, databasetest.WithTimeout(time.Minute))
func Run(t *testing.T, newDriver func(t *testing.T) database.Driver, opts ...Option) {
	t.Helper()

	config := settings{timeout: defaultTimeout}

	for _, opt := range opts {
		opt(&config)
	}

	suite.Run(t, newDriver, config.timeout)
}
