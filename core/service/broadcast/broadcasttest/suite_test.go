package broadcasttest_test

import (
	"testing"

	"arkoren.dev/core/service/broadcast/broadcasttest"
)

// TestFakeConformance runs the broadcast conformance suite against the Fake.
func TestFakeConformance(t *testing.T) {
	t.Parallel()

	broadcasttest.Run(t, func(*testing.T) broadcasttest.Driver {
		fake := broadcasttest.New()

		return broadcasttest.Driver{
			Publisher: fake,
			Consumer:  fake,
		}
	}, broadcasttest.WithSynctest())
}
