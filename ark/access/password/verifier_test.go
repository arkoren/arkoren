package password_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/password"
)

// TestAuthenticateFailuresLookAlike verifies unknown, wrong, hashless and
// disabled users all return the one error.
func TestAuthenticateFailuresLookAlike(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		subject, _ := sys.users.Create(ctx, "ada@example.com")
		_, _ = sys.users.Create(ctx, "bob@example.com")
		if err := sys.verifier.Set(ctx, subject, "correct horse"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		for name, attempt := range map[string][2]string{
			"unknown":  {"nobody@example.com", "correct horse"},
			"wrong":    {"ada@example.com", "wrong horse!"},
			"hashless": {"bob@example.com", "correct horse"},
		} {
			if _, err := sys.verifier.Authenticate(ctx, attempt[0], attempt[1]); !errors.Is(err, password.ErrInvalidCredentials) {
				t.Errorf("%s error = %v, want ErrInvalidCredentials", name, err)
			}
		}
		if got, err := sys.verifier.Authenticate(ctx, "ada@example.com", "correct horse"); err != nil || got != subject {
			t.Fatalf("Authenticate = %q, %v", got, err)
		}
		if err := sys.users.driver.Disable(subject); err != nil {
			t.Fatalf("Disable: %v", err)
		}
		if _, err := sys.verifier.Authenticate(ctx, "ada@example.com", "correct horse"); !errors.Is(err, password.ErrInvalidCredentials) {
			t.Errorf("disabled error = %v, want ErrInvalidCredentials", err)
		}
	})
}

// TestSetEnforcesPolicy verifies the default policy and that a stored hash
// round-trips.
func TestSetEnforcesPolicy(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		for _, weak := range []string{"", "short", strings.Repeat("x", 73)} {
			if err := sys.verifier.Set(ctx, "1", weak); !errors.Is(err, password.ErrInvalidPassword) {
				t.Errorf("Set(%d bytes) error = %v, want ErrInvalidPassword", len(weak), err)
			}
		}
		if err := sys.verifier.Set(ctx, "1", "first password"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := sys.verifier.Set(ctx, "1", "second password"); err != nil {
			t.Fatalf("second Set: %v", err)
		}
		if err := sys.verifier.Check(ctx, "1", "first password"); !errors.Is(err, password.ErrInvalidCredentials) {
			t.Errorf("old password still works: %v", err)
		}
		if err := sys.verifier.Check(ctx, "1", "second password"); err != nil {
			t.Errorf("new password rejected: %v", err)
		}
	})
}

// TestConfirmWithPasswordCredential verifies the credential works through the
// core confirm route and a wrong password is core's 403.
func TestConfirmWithPasswordCredential(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		if reply := sys.client.Post("/confirm", `{"method":"pwd","password":"wrong horse!"}`); reply.Status != http.StatusForbidden {
			t.Fatalf("wrong confirm = %d %s", reply.Status, reply.Body)
		}
		if reply := sys.client.Post("/confirm", `{"method":"pwd","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("confirm = %d %s", reply.Status, reply.Body)
		}
	})
}

// TestForgetHookDeletesRows verifies forgetting a user removes the hash and the
// pending reset.
func TestForgetHookDeletesRows(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, "ada@example.com")
		if err := sys.provider.Forget(ctx, "1"); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		if has, err := sys.verifier.Has(ctx, "1"); err != nil || has {
			t.Fatalf("Has after forget = %v, %v", has, err)
		}
		rows, err := db.Select[int](ctx, query.Table(instance+"_password_resets", func(statement *query.Builder) {
			statement.SelectRaw("COUNT(*)")
		}))
		if err != nil || rows[0] != 0 {
			t.Fatalf("resets left = %v, %v", rows, err)
		}
	})
}

// TestTwoInstancesSeparateTables verifies two verifiers over one database keep
// separate hashes.
func TestTwoInstancesSeparateTables(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		first := newSystem(t, db, instance+"_a")
		second := newSystem(t, db, instance+"_b")
		if err := first.verifier.Set(ctx, "1", "first password"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if has, _ := second.verifier.Has(ctx, "1"); has {
			t.Fatal("the second instance sees the first instance's hash")
		}
	})
}

// TestPingNamesMigrations verifies Ping reports missing tables.
func TestPingNamesMigrations(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		users := newFakeUsers()
		provider, _ := auth.NewUserProvider(users.driver)
		verifier, err := password.NewVerifier(provider, db, newSystem(t, db, instance+"_x").store, testClientIP, newSystem(t, db, instance+"_y").hasher, password.Users{
			Lookup: users.Lookup,
			Rotate: users.Rotate,
		}, func(passwords *password.Builder) {
			passwords.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		if err := verifier.Ping(ctx); !errors.Is(err, password.ErrNotMigrated) {
			t.Fatalf("Ping error = %v, want ErrNotMigrated", err)
		}
		migrateUp(t, db, verifier.Migrations()...)
		if err := verifier.Ping(ctx); err != nil {
			t.Fatalf("Ping after migrations: %v", err)
		}
	})
}

// TestNewVerifierRejectsBadConfig verifies the configuration errors.
func TestNewVerifierRejectsBadConfig(t *testing.T) {
	t.Parallel()

	if _, err := password.NewVerifier(nil, nil, nil, nil, nil, password.Users{}); !errors.Is(err, password.ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
}
