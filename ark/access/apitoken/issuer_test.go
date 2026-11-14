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

// TestIssueAuthenticateRoundTrip verifies a created token authenticates with
// its scopes and no factors, and is not a session.
func TestIssueAuthenticateRoundTrip(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		browser := sys.login(t, "1")
		_, secret := createToken(t, browser, `{"name":"CI","scopes":["posts:write"]}`)
		if !strings.HasPrefix(secret, "ark_") {
			t.Fatalf("secret = %q, want the ark_ prefix", secret)
		}
		reply := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", bearer(secret))
		if reply.Body != whoami("1", "posts:write", 0, false) {
			t.Fatalf("whoami = %d %q", reply.Status, reply.Body)
		}
	})
}

// TestOtherPrefixFallsThrough verifies a bearer token with another prefix is
// anonymous and not rejected, so a second authenticator can take it.
func TestOtherPrefixFallsThrough(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer("eyJhbGciOi.jwt.text"))
		if reply.Body != "anonymous" || reply.Header.Get("WWW-Authenticate") != "" {
			t.Fatalf("whoami = %d %q %q", reply.Status, reply.Body, reply.Header.Get("WWW-Authenticate"))
		}
	})
}

// TestMalformedUnknownTamperedExpired401 verifies every bad token of this
// issuer is 401 with a Bearer challenge.
func TestMalformedUnknownTamperedExpired401(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		browser := sys.login(t, "1")
		_, secret := createToken(t, browser, `{"name":"CI","scopes":[]}`)
		tampered := secret[:len(secret)-1] + "A"
		if tampered == secret {
			tampered = secret[:len(secret)-1] + "B"
		}
		for name, text := range map[string]string{
			"malformed": "ark_nope",
			"unknown":   "ark_AAAAAAAAAAAA_AAAAAAAAAAAAAAAAAAAAAAAAAA",
			"tampered":  tampered,
		} {
			reply := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", bearer(text))
			if reply.Status != http.StatusUnauthorized || !strings.Contains(reply.Header.Get("WWW-Authenticate"), "invalid_token") {
				t.Errorf("%s = %d %q", name, reply.Status, reply.Header.Get("WWW-Authenticate"))
			}
		}
	})
}

// TestExpiredTokenIs401 verifies a token that outlived its lifetime is
// rejected.
func TestExpiredTokenIs401(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		_, secret, err := sys.issuer.Issue(ctx, "1", apitoken.Request{Name: "short", ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); !strings.HasPrefix(reply.Body, "1|") {
			t.Fatalf("whoami before expiry = %d %q", reply.Status, reply.Body)
		}
		setColumn(t, db, instance, "expires_at", time.Now().Add(-time.Minute).Unix())
		if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Status != http.StatusUnauthorized {
			t.Fatalf("whoami after expiry = %d", reply.Status)
		}
	})
}

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

// TestDisabledOrDeletedUserIs401 verifies a token dies with its user.
func TestDisabledOrDeletedUserIs401(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		_, disabled, _ := sys.issuer.Issue(ctx, "1", apitoken.Request{Name: "a"})
		_, deleted, _ := sys.issuer.Issue(ctx, "2", apitoken.Request{Name: "b"})
		_ = sys.users.Disable("1")
		sys.users.Delete("2")
		for _, text := range []string{disabled, deleted} {
			if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(text)); reply.Status != http.StatusUnauthorized {
				t.Errorf("whoami = %d, want 401", reply.Status)
			}
		}
	})
}

// TestVersionChangeRejectsToken verifies a user version change ends the token
// and an unchanged version keeps it.
func TestVersionChangeRejectsToken(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		_, secret, _ := sys.issuer.Issue(ctx, "1", apitoken.Request{Name: "a"})
		_ = sys.users.Put(auth.UserRecord{Subject: "1", Version: "0", Claims: map[string]any{"role": "x"}})
		if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Status != http.StatusOK {
			t.Fatalf("whoami with an unchanged version = %d", reply.Status)
		}
		_ = sys.users.Put(auth.UserRecord{Subject: "1", Version: "1"})
		if reply := sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Status != http.StatusUnauthorized {
			t.Fatalf("whoami after a version change = %d, want 401", reply.Status)
		}
	})
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

// TestExchangeEndsSessionLogin verifies the exchange trades a fresh login for a
// token and ends the session.
func TestExchangeEndsSessionLogin(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		device := sys.login(t, "1")
		reply := device.Post("/device-tokens", `{"device":"Pixel 9"}`)
		if reply.Status != http.StatusCreated {
			t.Fatalf("exchange = %d %s", reply.Status, reply.Body)
		}
		if after := device.Do(http.MethodGet, "/whoami", ""); after.Body != "anonymous" {
			t.Fatalf("whoami after exchange = %q, want the session ended", after.Body)
		}
		secret := between(reply.Body, `"secret":"`, `"`)
		if who := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", bearer(secret)); !strings.HasPrefix(who.Body, "1|") {
			t.Fatalf("whoami with the device token = %q", who.Body)
		}
	})
}

// between returns the text between two markers.
func between(text, from, to string) string {
	_, rest, _ := strings.Cut(text, from)
	value, _, _ := strings.Cut(rest, to)

	return value
}

// TestExchangeRequiresSessionLogin verifies a token cannot be traded for a
// device token and an anonymous request is refused.
func TestExchangeRequiresSessionLogin(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		_, secret, _ := sys.issuer.Issue(ctx, "1", apitoken.Request{Name: "a"})
		if reply := sys.client.NewDevice().Do(http.MethodPost, "/device-tokens", `{"device":"x"}`, bearer(secret)); reply.Status != http.StatusUnauthorized {
			t.Fatalf("exchange with a token = %d, want 401", reply.Status)
		}
		if reply := sys.client.NewDevice().Post("/device-tokens", `{"device":"x"}`); reply.Status != http.StatusUnauthorized {
			t.Fatalf("anonymous exchange = %d, want 401", reply.Status)
		}
	})
}

// TestDeleteCurrentRevokesPresentedToken verifies a device logs out by revoking
// its own token and a session cannot use the route.
func TestDeleteCurrentRevokesPresentedToken(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		_, secret, _ := sys.issuer.IssueDevice(ctx, "1", "Pixel 9")
		_, other, _ := sys.issuer.IssueDevice(ctx, "1", "iPad")
		if reply := sys.login(t, "1").Do(http.MethodDelete, "/device-tokens/current", ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("session DELETE current = %d, want 401", reply.Status)
		}
		if reply := sys.client.NewDevice().Do(http.MethodDelete, "/device-tokens/current", "", bearer(secret)); reply.Status != http.StatusNoContent {
			t.Fatalf("DELETE current = %d", reply.Status)
		}
		if reply := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Status != http.StatusUnauthorized {
			t.Fatalf("revoked token whoami = %d", reply.Status)
		}
		if reply := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", bearer(other)); reply.Status != http.StatusOK {
			t.Fatalf("the other device's token whoami = %d", reply.Status)
		}
	})
}

// TestListAndDeleteOwnTokens verifies the list, delete and the 404 for
// another user's token.
func TestListAndDeleteOwnTokens(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		first := sys.login(t, "1")
		second := sys.login(t, "2")
		id, _ := createToken(t, first, `{"name":"mine","scopes":["posts:read"]}`)
		if reply := first.Do(http.MethodGet, "/tokens", ""); !strings.Contains(reply.Body, `"name":"mine"`) || strings.Contains(reply.Body, "secret") {
			t.Fatalf("list = %d %s", reply.Status, reply.Body)
		}
		if reply := second.Do(http.MethodDelete, "/tokens/"+id, ""); reply.Status != http.StatusNotFound {
			t.Fatalf("deleting another user's token = %d, want 404", reply.Status)
		}
		if reply := first.Do(http.MethodDelete, "/tokens/"+id, ""); reply.Status != http.StatusNoContent {
			t.Fatalf("delete = %d", reply.Status)
		}
		if reply := first.Do(http.MethodDelete, "/tokens/"+id, ""); reply.Status != http.StatusNotFound {
			t.Fatalf("second delete = %d, want 404", reply.Status)
		}
	})
}

// TestHandlersRejectTokenIdentity verifies a token can neither list, create nor
// delete tokens, and is never fresh.
func TestHandlersRejectTokenIdentity(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		token, secret, _ := sys.issuer.Issue(ctx, "1", apitoken.Request{Name: "a", Scopes: []string{"posts:write"}})
		header := bearer(secret)
		for _, route := range []struct{ method, path, body string }{
			{http.MethodGet, "/tokens", ""},
			{http.MethodPost, "/tokens", `{"name":"b"}`},
			{http.MethodDelete, "/tokens/" + token.ID, ""},
			{http.MethodGet, "/fresh", ""},
		} {
			if reply := sys.client.NewDevice().Do(route.method, route.path, route.body, header); reply.Status < 400 {
				t.Errorf("%s %s with a token = %d, want an error", route.method, route.path, reply.Status)
			}
		}
	})
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

// TestLastUsedTouchedOnce verifies last_used_at is written on first use, left
// alone within the touch interval and written again after it.
func TestLastUsedTouchedOnce(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		_, secret, _ := sys.issuer.Issue(ctx, "1", apitoken.Request{Name: "a"})
		use := func() time.Time {
			sys.client.Do(http.MethodGet, "/whoami", "", bearer(secret))
			tokens, _ := sys.issuer.Tokens(ctx, "1")

			return tokens[0].LastUsedAt
		}
		if use().IsZero() {
			t.Fatal("last_used_at was not written on first use")
		}
		recent := time.Now().Add(-time.Minute).Unix()
		setColumn(t, db, instance, "last_used_at", recent)
		if got := use(); got.Unix() != recent {
			t.Fatalf("last_used_at rewritten within the interval: %v", got)
		}
		setColumn(t, db, instance, "last_used_at", time.Now().Add(-time.Hour).Unix())
		if got := use(); time.Since(got) > time.Minute {
			t.Fatalf("last_used_at not refreshed after the interval: %v", got)
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
