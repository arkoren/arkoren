package password_test

import (
	"testing"
	"time"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam/password"
)

// TestRefundOnMissingCounterKeepsAnExpiry verifies a refund of a counter that
// no longer exists (it expired, or the cache lost it) does not leave a counter
// that never expires.
func TestRefundOnMissingCounterKeepsAnExpiry(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		key := cache.NewKey[int64]("refund-on-missing-counter")

		password.Refund(sys.verifier, ctx, key)

		remaining, err := sys.store.TTL(ctx, key)
		if err != nil {
			t.Fatalf("TTL after refund: %v", err)
		}
		if remaining <= 0 || remaining > 15*time.Minute {
			t.Fatalf("remaining lifetime = %v, want within the 15 minute throttle window", remaining)
		}
	})
}
