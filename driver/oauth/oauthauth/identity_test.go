package oauthauth

import (
	"testing"

	"arkoren.dev/core/http/auth"
)

// TestIdentityIsIdentifiable verifies an application without local users can
// use Identity as its own session identifier.
func TestIdentityIsIdentifiable(t *testing.T) {
	t.Parallel()

	var _ auth.Identifiable[Identity] = Identity{}

	identity := Identity{Issuer: "https://id.example.com", Subject: "user-42"}

	if got := identity.AuthID(); got.Issuer != identity.Issuer || got.Subject != identity.Subject {
		t.Fatalf("AuthID = %+v", got)
	}
}

// TestIdentityIsScoped verifies the granted scopes are exposed for scope rules.
func TestIdentityIsScoped(t *testing.T) {
	t.Parallel()

	var _ auth.Scoped = Identity{}

	if got := (Identity{Scopes: []string{"read", "write"}}).AuthScopes(); len(got) != 2 || got[0] != "read" || got[1] != "write" {
		t.Fatalf("AuthScopes = %v", got)
	}
}
