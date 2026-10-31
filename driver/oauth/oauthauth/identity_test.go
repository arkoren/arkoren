package oauthauth

import (
	"testing"
)

// requireHasScopes verifies HasScopes(required...) allows an identity with the
// scopes "read" and "write" exactly when want is set.
func requireHasScopes(t *testing.T, required []string, want bool) {
	t.Helper()

	identity := Identity{
		Scopes: []string{"read", "write"},
	}

	got, err := HasScopes(required...)(t.Context(), identity)

	if err != nil || got != want {
		t.Errorf("HasScopes(%v) = %v, %v; want %v", required, got, err, want)
	}
}

// TestHasScopesAllPresent verifies a rule is met when every required scope is present.
func TestHasScopesAllPresent(t *testing.T) {
	t.Parallel()

	requireHasScopes(t, []string{"read", "write"}, true)
}

// TestHasScopesNone verifies a rule without required scopes is always met.
func TestHasScopesNone(t *testing.T) {
	t.Parallel()

	requireHasScopes(t, nil, true)
}

// TestHasScopesMissing verifies a rule is unmet when one required scope is missing.
func TestHasScopesMissing(t *testing.T) {
	t.Parallel()

	requireHasScopes(t, []string{"read", "admin"}, false)
}

// TestHasScopesOwnsRequirements keeps caller edits from weakening an already
// constructed authorization rule. Only the public rule result is observed.
func TestHasScopesOwnsRequirements(t *testing.T) {
	t.Parallel()

	required := []string{"admin"}
	rule := HasScopes(required...)

	required[0] = "read"

	allowed, err := rule(t.Context(), Identity{Scopes: []string{"read"}})

	if err != nil || allowed {
		t.Fatalf("rule after caller mutation = %t, %v; want false, nil", allowed, err)
	}
}
