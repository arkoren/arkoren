package password_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/bcrypt/bcrypthash"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/iam/password"
)

// rehashDriver interrupts replacement hashing after Check verified the old
// password, making credential changes at that boundary deterministic.
type rehashDriver struct {
	hash.Driver

	// beforeHash runs once before the replacement hash is produced.
	beforeHash func() error
}

// Hash runs the pending interruption before delegating to the real hasher.
func (driver *rehashDriver) Hash(value []byte) ([]byte, error) {
	if driver.beforeHash != nil {
		before := driver.beforeHash
		driver.beforeHash = nil
		if err := before(); err != nil {
			return nil, err
		}
	}

	return driver.Driver.Hash(value)
}

// TestRehashPreservesResetPassword verifies an in-flight upgrade cannot restore
// a compromised password after a password reset has committed.
func TestRehashPreservesResetPassword(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		subject, err := sys.users.Create(ctx, "ada@example.com")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := sys.verifier.Set(ctx, subject, "old password"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := sys.verifier.SendReset(ctx, password.ResetRequest{Identifier: "ada@example.com"}); err != nil {
			t.Fatalf("SendReset: %v", err)
		}
		signed := (*sys.sent)[0].Signed
		driver := &rehashDriver{
			Driver: bcrypthash.New(func(driver *bcrypthash.Builder) {
				driver.Cost(5)
			}),
			beforeHash: func() error {
				_, err := sys.verifier.Reset(ctx, signed, "reset password")

				return err
			},
		}
		upgraded, err := password.NewVerifier(sys.provider, db, sys.store, testClientIP, hash.New(driver), password.Users{
			Lookup: sys.users.Lookup,
			Rotate: sys.users.Rotate,
		}, func(passwords *password.Builder) {
			passwords.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		if err := upgraded.Check(ctx, subject, "old password"); err != nil {
			t.Fatalf("in-flight Check: %v", err)
		}
		if err := sys.verifier.Check(ctx, subject, "old password"); !errors.Is(err, password.ErrInvalidCredentials) {
			t.Fatalf("old password after reset = %v, want ErrInvalidCredentials", err)
		}
		if err := sys.verifier.Check(ctx, subject, "reset password"); err != nil {
			t.Fatalf("reset password rejected: %v", err)
		}
	})
}

// TestRehashPreservesChangedPassword verifies an upgrade cannot overwrite a
// replacement password stored while the old password was being checked.
func TestRehashPreservesChangedPassword(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		if err := sys.verifier.Set(ctx, "1", "old password"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		driver := &rehashDriver{
			Driver: bcrypthash.New(func(driver *bcrypthash.Builder) {
				driver.Cost(5)
			}),
			beforeHash: func() error {
				return sys.verifier.Set(ctx, "1", "changed password")
			},
		}
		upgraded, err := password.NewVerifier(sys.provider, db, sys.store, testClientIP, hash.New(driver), password.Users{
			Lookup: sys.users.Lookup,
			Rotate: sys.users.Rotate,
		}, func(passwords *password.Builder) {
			passwords.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		if err := upgraded.Check(ctx, "1", "old password"); err != nil {
			t.Fatalf("in-flight Check: %v", err)
		}
		if err := sys.verifier.Check(ctx, "1", "old password"); !errors.Is(err, password.ErrInvalidCredentials) {
			t.Fatalf("old password after change = %v, want ErrInvalidCredentials", err)
		}
		if err := sys.verifier.Check(ctx, "1", "changed password"); err != nil {
			t.Fatalf("changed password rejected: %v", err)
		}
	})
}

// TestRehashDoesNotRecreateRemovedPassword verifies an upgrade never inserts a
// password that a concurrent recovery, takeover or administrative action removed.
func TestRehashDoesNotRecreateRemovedPassword(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		if err := sys.verifier.Set(ctx, "1", "old password"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		driver := &rehashDriver{
			Driver: bcrypthash.New(func(driver *bcrypthash.Builder) {
				driver.Cost(5)
			}),
			beforeHash: func() error {
				return sys.verifier.Remove(ctx, "1")
			},
		}
		upgraded, err := password.NewVerifier(sys.provider, db, sys.store, testClientIP, hash.New(driver), password.Users{
			Lookup: sys.users.Lookup,
			Rotate: sys.users.Rotate,
		}, func(passwords *password.Builder) {
			passwords.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		if err := upgraded.Check(ctx, "1", "old password"); err != nil {
			t.Fatalf("in-flight Check: %v", err)
		}
		if has, err := sys.verifier.Has(ctx, "1"); err != nil || has {
			t.Fatalf("Has after removal = %v, %v; want false, nil", has, err)
		}
	})
}

// storedHash reads a verifier's encoded hash to check upgrade persistence without
// another verification changing that stored value.
func storedHash(t *testing.T, ctx context.Context, db *database.Database, instance string) []byte {
	t.Helper()

	row, err := db.Find[struct {
		Hash string `db:"hash"`
	}](ctx, query.Table(instance+"_password_hashes", func(statement *query.Builder) {
		statement.Select("hash").Where("subject", "1")
	}))
	if err != nil {
		t.Fatalf("load stored hash: %v", err)
	}
	stored, err := base64.StdEncoding.DecodeString(row.Hash)
	if err != nil {
		t.Fatalf("decode stored hash: %v", err)
	}

	return stored
}

// TestRehashUpgradesUnchangedPassword verifies successful checks still upgrade an
// outdated hash when no credential change raced with verification.
func TestRehashUpgradesUnchangedPassword(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		if err := sys.verifier.Set(ctx, "1", "old password"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		hasher := hash.New(bcrypthash.New(func(driver *bcrypthash.Builder) {
			driver.Cost(5)
		}))
		if !hasher.NeedsRehash(storedHash(t, ctx, db, instance)) {
			t.Fatal("fixture hash does not need an upgrade")
		}
		upgraded, err := password.NewVerifier(sys.provider, db, sys.store, testClientIP, hasher, password.Users{
			Lookup: sys.users.Lookup,
			Rotate: sys.users.Rotate,
		}, func(passwords *password.Builder) {
			passwords.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		if err := upgraded.Check(ctx, "1", "old password"); err != nil {
			t.Fatalf("Check: %v", err)
		}
		if hasher.NeedsRehash(storedHash(t, ctx, db, instance)) {
			t.Fatal("successful verification did not upgrade the hash")
		}
	})
}

// TestRehashFailureDoesNotFailCheck verifies replacement hashing remains best
// effort and leaves the valid original hash intact when hashing fails.
func TestRehashFailureDoesNotFailCheck(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		if err := sys.verifier.Set(ctx, "1", "old password"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		before := string(storedHash(t, ctx, db, instance))
		driver := &rehashDriver{
			Driver: bcrypthash.New(func(driver *bcrypthash.Builder) {
				driver.Cost(5)
			}),
			beforeHash: func() error {
				return errors.New("hashing unavailable")
			},
		}
		upgraded, err := password.NewVerifier(sys.provider, db, sys.store, testClientIP, hash.New(driver), password.Users{
			Lookup: sys.users.Lookup,
			Rotate: sys.users.Rotate,
		}, func(passwords *password.Builder) {
			passwords.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		if err := upgraded.Check(ctx, "1", "old password"); err != nil {
			t.Fatalf("Check with failed optimization: %v", err)
		}
		if after := string(storedHash(t, ctx, db, instance)); after != before {
			t.Fatal("failed rehash changed the stored hash")
		}
	})
}

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

// TestForgetHookDeletesRows verifies forgetting a user removes the hash.
func TestForgetHookDeletesRows(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		if err := sys.provider.Forget(ctx, "1"); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		if has, err := sys.verifier.Has(ctx, "1"); err != nil || has {
			t.Fatalf("Has after forget = %v, %v", has, err)
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
