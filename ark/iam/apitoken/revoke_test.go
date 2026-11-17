package apitoken_test

import (
	"testing"

	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam/apitoken"
)

// TestRevokeIgnoresUnknownAndRevokeAll verifies the Go API.
func TestRevokeIgnoresUnknownAndRevokeAll(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		first, _, _ := sys.issuer.Issue(ctx, "1", apitoken.Request{Name: "a"})
		_, _, _ = sys.issuer.Issue(ctx, "1", apitoken.Request{Name: "b"})
		if err := sys.issuer.Revoke(ctx, "1", "unknown", first.ID); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		if tokens, _ := sys.issuer.Tokens(ctx, "1"); len(tokens) != 1 {
			t.Fatalf("tokens = %+v, want one", tokens)
		}
		if err := sys.issuer.RevokeAll(ctx, "1"); err != nil {
			t.Fatalf("RevokeAll: %v", err)
		}
		if tokens, _ := sys.issuer.Tokens(ctx, "1"); len(tokens) != 0 {
			t.Fatalf("tokens after RevokeAll = %+v", tokens)
		}
	})
}
