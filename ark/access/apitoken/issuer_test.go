package apitoken_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/apitoken"
)

// setColumn sets a column of every token row, to move a token's time without
// waiting for it.
func setColumn(t *testing.T, db *database.Database, instance, column string, value int64) {
	t.Helper()

	_, err := db.Exec(testContext(t), query.Table(instance+"_api_tokens", func(statement *query.Builder) {
		statement.Unfiltered().Update(map[string]any{column: value})
	}))
	if err != nil {
		t.Fatalf("set %s: %v", column, err)
	}
}

// TestIssueValidation verifies the invalid requests.
func TestIssueValidation(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, func(tokens *apitoken.Builder) {
			tokens.MaxTTL(time.Hour)
		})
		for name, request := range map[string]apitoken.Request{
			"empty name":    {Name: " "},
			"unknown scope": {Name: "a", Scopes: []string{"admin"}},
			"past expiry":   {Name: "a", ExpiresAt: time.Now().Add(-time.Minute)},
			"beyond max":    {Name: "a", ExpiresAt: time.Now().Add(2 * time.Hour)},
		} {
			if _, _, err := sys.issuer.Issue(ctx, "1", request); !errors.Is(err, apitoken.ErrInvalidRequest) {
				t.Errorf("%s error = %v, want ErrInvalidRequest", name, err)
			}
		}
		token, _, err := sys.issuer.Issue(ctx, "1", apitoken.Request{Name: "a", Scopes: []string{"posts:read", "posts:read"}})
		if err != nil || len(token.Scopes) != 1 || token.ExpiresAt.IsZero() {
			t.Fatalf("Issue = %+v, %v; want deduplicated scopes and the MaxTTL expiry", token, err)
		}
		if _, _, err := sys.issuer.Issue(ctx, "missing", apitoken.Request{Name: "a"}); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("Issue for an unknown user error = %v", err)
		}
	})
}

// TestDeviceTokenGetsCatalogScopes verifies a device token carries every scope
// and has no expiry.
func TestDeviceTokenGetsCatalogScopes(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		token, secret, err := sys.issuer.IssueDevice(testContext(t), "1", "Pixel 9")
		if err != nil || token.Kind != apitoken.KindDevice || !token.ExpiresAt.IsZero() {
			t.Fatalf("IssueDevice = %+v, %v", token, err)
		}
		reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret))
		if reply.Body != whoami("1", "posts:read,posts:write", 0, false) {
			t.Fatalf("whoami = %q", reply.Body)
		}
	})
}

// between returns the text between two markers.
func between(text, from, to string) string {
	_, rest, _ := strings.Cut(text, from)
	value, _, _ := strings.Cut(rest, to)

	return value
}

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

// TestForgetHookDeletesTokens verifies forgetting a user removes their tokens.
func TestForgetHookDeletesTokens(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		_, _, _ = sys.issuer.Issue(ctx, "1", apitoken.Request{Name: "a"})
		if err := sys.provider.Forget(ctx, "1"); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		if tokens, _ := sys.issuer.Tokens(ctx, "1"); len(tokens) != 0 {
			t.Fatalf("tokens after Forget = %+v", tokens)
		}
	})
}

// TestTwoInstancesSeparateTables verifies two instances keep separate tokens.
func TestTwoInstancesSeparateTables(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		first := newSystem(t, db, instance+"_a")
		second := newSystem(t, db, instance+"_b")
		_, secret, _ := first.issuer.Issue(ctx, "1", apitoken.Request{Name: "a"})
		if reply := second.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Status != http.StatusUnauthorized {
			t.Fatalf("the second instance accepted the first's token: %d", reply.Status)
		}
	})
}

// TestPingNamesMigrations verifies Ping reports a missing table.
func TestPingNamesMigrations(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance+"_x")
		provider, _ := auth.NewUserProvider(sys.users)
		issuer, err := apitoken.NewIssuer(provider, db, func(tokens *apitoken.Builder) {
			tokens.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewIssuer: %v", err)
		}
		if err := issuer.Ping(ctx); !errors.Is(err, apitoken.ErrNotMigrated) {
			t.Fatalf("Ping error = %v, want ErrNotMigrated", err)
		}
		migrateUp(t, db, issuer.Migrations()...)
		if err := issuer.Ping(ctx); err != nil {
			t.Fatalf("Ping after migrations: %v", err)
		}
	})
}

// TestNewIssuerRejectsBadConfig verifies the configuration errors.
func TestNewIssuerRejectsBadConfig(t *testing.T) {
	t.Parallel()

	if _, err := apitoken.NewIssuer(nil, nil); !errors.Is(err, apitoken.ErrInvalidConfig) {
		t.Fatalf("nil dependencies error = %v", err)
	}
}
