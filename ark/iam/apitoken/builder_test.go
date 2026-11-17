package apitoken_test

import (
	"errors"
	"testing"

	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam/apitoken"
)

// newIssuerWith builds an issuer on the users and database of a test system
// with only configure applied, and returns the construction error.
func newIssuerWith(t *testing.T, db *database.Database, instance string, configure apitoken.BuilderFunc) (*apitoken.Issuer, error) {
	t.Helper()

	sys := newSystem(t, db, instance+"_x")

	return apitoken.NewIssuer(sys.provider, db, cache.New(memorycache.New()), configure)
}

// TestGrantsRequireAGrantFunctionWithACatalog verifies a catalog without a
// grant function is an invalid configuration.
func TestGrantsRequireAGrantFunctionWithACatalog(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		_, err := newIssuerWith(t, db, instance, func(tokens *apitoken.Builder) {
			tokens.Instance(instance).Grants(nil, "posts:read")
		})
		if !errors.Is(err, apitoken.ErrInvalidConfig) {
			t.Fatalf("catalog without a grant function error = %v, want ErrInvalidConfig", err)
		}
	})
}

// TestGrantsRejectInvalidCatalog verifies empty and duplicate names are
// invalid.
func TestGrantsRejectInvalidCatalog(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		for _, catalog := range [][]string{{""}, {"a", "a"}} {
			_, err := newIssuerWith(t, db, instance, func(tokens *apitoken.Builder) {
				tokens.Instance(instance).Grants(apitoken.AnyScope, catalog...)
			})
			if !errors.Is(err, apitoken.ErrInvalidConfig) {
				t.Errorf("catalog %q error = %v, want ErrInvalidConfig", catalog, err)
			}
		}
	})
}

// TestNoTokenKindIsInvalid verifies disabling both kinds is refused.
func TestNoTokenKindIsInvalid(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		_, err := newIssuerWith(t, db, instance, func(tokens *apitoken.Builder) {
			tokens.Instance(instance).PersonalTokens(false).DeviceTokens(false)
		})
		if !errors.Is(err, apitoken.ErrInvalidConfig) {
			t.Fatalf("no token kind error = %v, want ErrInvalidConfig", err)
		}
	})
}

// TestTokenKindsDefaultToEnabled verifies both kinds are on unless switched off.
func TestTokenKindsDefaultToEnabled(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		issuer, err := newIssuerWith(t, db, instance, func(tokens *apitoken.Builder) {
			tokens.Instance(instance)
		})
		if err != nil || !issuer.PersonalTokens() || !issuer.DeviceTokens() {
			t.Fatalf("NewIssuer = %v; want both kinds on", err)
		}
	})
}
