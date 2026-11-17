package apitoken_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/iam/apitoken"
)

// setRow sets a column of the token row with id, to move one token's time
// without waiting for it.
func setRow(t *testing.T, db *database.Database, instance, id, column string, value int64) {
	t.Helper()

	_, err := db.Exec(testContext(t), query.Table(instance+"_api_tokens", func(statement *query.Builder) {
		statement.Where("id", id).Update(map[string]any{column: value})
	}))
	if err != nil {
		t.Fatalf("set %s of %s: %v", column, id, err)
	}
}

// issueAged issues a token of kind for subject "1" named name, created
// createdDays ago and last used usedDays ago (zero means never used), and
// returns its id.
func issueAged(t *testing.T, db *database.Database, sys *system, instance string, kind apitoken.Kind, name string, createdDays, usedDays int) string {
	t.Helper()

	var token apitoken.Token
	var err error
	if kind == apitoken.KindDevice {
		token, _, err = sys.issuer.IssueDevice(testContext(t), "1", name)
	} else {
		token, _, err = sys.issuer.Issue(testContext(t), "1", apitoken.Request{Name: name})
	}
	if err != nil {
		t.Fatalf("issue %s: %v", name, err)
	}
	setRow(t, db, instance, token.ID, "created_at", daysAgo(createdDays))
	if usedDays > 0 {
		setRow(t, db, instance, token.ID, "last_used_at", daysAgo(usedDays))
	}

	return token.ID
}

// remaining returns the sorted names of the tokens left for subject "1".
func remaining(t *testing.T, sys *system) []string {
	t.Helper()

	tokens, err := sys.issuer.Tokens(testContext(t), "1")
	if err != nil {
		t.Fatalf("Tokens: %v", err)
	}
	names := make([]string, 0, len(tokens))
	for _, token := range tokens {
		names = append(names, token.Name)
	}
	slices.Sort(names)

	return names
}

// TestPruneDeletesIdleAndExpiredTokens verifies Prune removes the device tokens
// idle past the limit (by last use, or by creation when never used) and the
// expired tokens, and keeps every token that can still authenticate, including
// a personal token idle for a long time.
func TestPruneDeletesIdleAndExpiredTokens(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		issueAged(t, db, sys, instance, apitoken.KindDevice, "idle", 200, 91)
		issueAged(t, db, sys, instance, apitoken.KindDevice, "never-used-old", 91, 0)
		issueAged(t, db, sys, instance, apitoken.KindDevice, "recent", 400, 89)
		issueAged(t, db, sys, instance, apitoken.KindDevice, "never-used-new", 89, 0)
		issueAged(t, db, sys, instance, apitoken.KindPersonal, "personal-idle", 400, 300)
		expired := issueAged(t, db, sys, instance, apitoken.KindPersonal, "expired", 10, 0)
		setRow(t, db, instance, expired, "expires_at", daysAgo(1))
		if err := sys.issuer.Prune(testContext(t)); err != nil {
			t.Fatalf("Prune: %v", err)
		}
		want := []string{"never-used-new", "personal-idle", "recent"}
		if got := remaining(t, sys); !slices.Equal(got, want) {
			t.Fatalf("tokens after Prune = %v, want %v", got, want)
		}
	})
}

// TestPruneKeepsIdleDeviceTokensWithoutALimit verifies a negative DeviceIdleTTL
// skips the idle part of Prune but still removes expired tokens.
func TestPruneKeepsIdleDeviceTokensWithoutALimit(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, func(tokens *apitoken.Builder) { tokens.DeviceIdleTTL(-1) })
		issueAged(t, db, sys, instance, apitoken.KindDevice, "idle", 1000, 900)
		issueAged(t, db, sys, instance, apitoken.KindDevice, "never-used", 1000, 0)
		expired := issueAged(t, db, sys, instance, apitoken.KindPersonal, "expired", 10, 0)
		setRow(t, db, instance, expired, "expires_at", time.Now().Add(-time.Hour).Unix())
		if err := sys.issuer.Prune(testContext(t)); err != nil {
			t.Fatalf("Prune: %v", err)
		}
		want := []string{"idle", "never-used"}
		if got := remaining(t, sys); !slices.Equal(got, want) {
			t.Fatalf("tokens after Prune = %v, want %v", got, want)
		}
	})
}

// TestTokensHideTokensOfAnOlderUserVersion verifies the listing leaves out a
// token that a version change of the user ended, while the user's tokens of the
// current version stay.
func TestTokensHideTokensOfAnOlderUserVersion(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		if _, _, err := sys.issuer.Issue(testContext(t), "1", apitoken.Request{Name: "before"}); err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if err := sys.users.Put(auth.UserRecord{Subject: "1", Version: "1"}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if got := remaining(t, sys); len(got) != 0 {
			t.Fatalf("tokens after a version change = %v, want none", got)
		}
		if reply := sys.login(t, "1").Do(http.MethodGet, "/tokens", ""); strings.Contains(reply.Body, "before") {
			t.Fatalf("GET /tokens lists an ended token: %s", reply.Body)
		}
	})
}

// TestPruneDeletesTokensOfAnOlderUserVersion verifies Prune deletes the tokens
// a version change ended, for every user, and keeps the others. The version is
// put back afterwards so a token that survived Prune would show again.
func TestPruneDeletesTokensOfAnOlderUserVersion(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		ctx := testContext(t)
		for _, subject := range []string{"1", "2"} {
			if _, _, err := sys.issuer.Issue(ctx, subject, apitoken.Request{Name: "token-" + subject}); err != nil {
				t.Fatalf("Issue: %v", err)
			}
		}
		if err := sys.users.Put(auth.UserRecord{Subject: "1", Version: "1"}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := sys.issuer.Prune(ctx); err != nil {
			t.Fatalf("Prune: %v", err)
		}
		if err := sys.users.Put(auth.UserRecord{Subject: "1", Version: "0"}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if got := remaining(t, sys); len(got) != 0 {
			t.Fatalf("tokens of the user whose version changed = %v, want none after Prune", got)
		}
		if other, err := sys.issuer.Tokens(ctx, "2"); err != nil || len(other) != 1 {
			t.Fatalf("tokens of the other user = %v, %v; want one", other, err)
		}
	})
}
