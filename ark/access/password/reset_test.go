package password_test

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/password"
)

// TestResetTokensKeepThreeAndDieTogether verifies a repeat request does not
// invalidate a link already sent, only the fourth drops the oldest, and one
// completed reset ends the rest.
func TestResetTokensKeepThreeAndDieTogether(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		for range 4 {
			if err := sys.verifier.SendReset(ctx, "ada@example.com"); err != nil {
				t.Fatalf("SendReset: %v", err)
			}
		}
		tokens := make([]string, 0, 4)
		for _, message := range *sys.sent {
			tokens = append(tokens, message.Token)
		}
		if _, err := sys.verifier.Reset(ctx, tokens[0], "reset horse ok"); !errors.Is(err, password.ErrInvalidToken) {
			t.Fatalf("the oldest of four tokens error = %v, want ErrInvalidToken", err)
		}
		if _, err := sys.verifier.Reset(ctx, tokens[1], "reset horse ok"); err != nil {
			t.Fatalf("an earlier live token was invalidated: %v", err)
		}
		for _, token := range tokens[2:] {
			if _, err := sys.verifier.Reset(ctx, token, "other horse ok"); !errors.Is(err, password.ErrInvalidToken) {
				t.Fatalf("a token outlived a completed reset: %v", err)
			}
		}
	})
}

// TestResetClearsTheIdentifierLockout verifies a completed reset clears the
// failures counted against the address across all clients.
func TestResetClearsTheIdentifierLockout(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		login := func(secret, ip string) int {
			return sys.client.NewDevice().Do(http.MethodPost, "/login", `{"email":"ada@example.com","password":"`+secret+`"}`, "X-Test-IP: "+ip).Status
		}
		for i := range 50 {
			login("wrong horse!", "10.0."+strconv.Itoa(i/10)+"."+strconv.Itoa(i%10))
		}
		if got := login("correct horse", "10.1.0.1"); got != http.StatusTooManyRequests {
			t.Fatalf("login after the failures = %d, want 429", got)
		}
		if err := sys.verifier.SendReset(ctx, "ada@example.com"); err != nil {
			t.Fatalf("SendReset: %v", err)
		}
		if _, err := sys.verifier.Reset(ctx, (*sys.sent)[0].Token, "reset horse ok"); err != nil {
			t.Fatalf("Reset: %v", err)
		}
		if got := login("reset horse ok", "10.1.0.2"); got != http.StatusNoContent {
			t.Fatalf("login after the reset = %d, want 204", got)
		}
	})
}

// TestResetTokenExpired verifies expiry.
func TestResetTokenExpired(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, "ada@example.com")
		second := (*sys.sent)[0].Token
		past := time.Now().Add(-time.Hour).Unix()
		_, err := db.Exec(ctx, query.Table(instance+"_password_resets", func(statement *query.Builder) {
			statement.Unfiltered().Update(map[string]any{"expires_at": past})
		}))
		if err != nil {
			t.Fatalf("expire: %v", err)
		}
		if _, err := sys.verifier.Reset(ctx, second, "reset horse ok"); !errors.Is(err, password.ErrInvalidToken) {
			t.Fatalf("expired token error = %v", err)
		}
	})
}

// TestConcurrentResetOneWins verifies two requests with one token do not both
// succeed.
func TestConcurrentResetOneWins(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, "ada@example.com")
		token := (*sys.sent)[0].Token
		results := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := sys.verifier.Reset(ctx, token, "reset horse ok")
				results <- err
			}()
		}
		succeeded := 0
		for range 2 {
			if err := <-results; err == nil {
				succeeded++
			}
		}
		if succeeded != 1 {
			t.Fatalf("%d resets succeeded, want 1", succeeded)
		}
	})
}
