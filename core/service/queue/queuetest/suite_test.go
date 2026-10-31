package queuetest_test

import (
	"testing"

	"arkoren.dev/core/service/queue/queuetest"
)

// TestFakeConformance runs the queue conformance suite against the Fake.
func TestFakeConformance(t *testing.T) {
	t.Parallel()

	queuetest.Run(t, func(*testing.T) queuetest.Driver {
		fake := queuetest.New()

		return queuetest.Driver{
			Dispatcher: fake,
			Consumer:   fake,
		}
	}, queuetest.WithSynctest())
}
