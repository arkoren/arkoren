package apitoken_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"

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
