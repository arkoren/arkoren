package account_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"
	sqlite "arkoren.dev/driver/sqlite/sqlitedatabase"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/account"
)

// newDirectory builds a migrated directory on db that records delivered
// messages.
func newDirectory(t *testing.T, db *database.Database, instance string, delivered *[]account.Message) *account.Directory {
	t.Helper()

	directory, err := account.NewDirectory(db, func(accounts *account.Builder) {
		accounts.Instance(instance)
		accounts.Deliver(func(_ context.Context, message account.Message) error {
			if delivered != nil {
				*delivered = append(*delivered, message)
			}

			return nil
		})
	})
	if err != nil {
		t.Fatalf("NewDirectory: %v", err)
	}
	migrateUp(t, db, directory.Migrations()...)

	return directory
}

// TestCreateFindRoundTrip verifies a created user is found by subject and by
// email, with a normalized email and the record shape the guard relies on.
func TestCreateFindRoundTrip(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, nil)
		subject, err := directory.Create(ctx, "  Ada@Example.COM ")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		found, err := directory.Lookup(ctx, "ada@example.com")
		if err != nil || found != subject {
			t.Fatalf("Lookup = %q, %v; want %q", found, err, subject)
		}
		record, err := directory.Find(ctx, subject)
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		user, ok := record.Value.(account.Account)
		if !ok || user.Email != "ada@example.com" || user.Verified() {
			t.Fatalf("record value = %#v", record.Value)
		}
		if record.Version != "0" || record.Claims["email_verified"] != false {
			t.Fatalf("record = %#v", record)
		}
	})
}

// TestCreateDuplicateEmailIsTaken verifies an address is unique after
// normalization and the second create is the 409 problem.
func TestCreateDuplicateEmailIsTaken(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, nil)
		if _, err := directory.Create(ctx, "ada@example.com"); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := directory.Create(ctx, "ADA@example.com"); !errors.Is(err, account.ErrEmailTaken) {
			t.Fatalf("second Create error = %v, want ErrEmailTaken", err)
		}
		if _, err := directory.CreateVerified(ctx, "ada@example.com"); !errors.Is(err, account.ErrEmailTaken) {
			t.Fatalf("CreateVerified error = %v, want ErrEmailTaken", err)
		}
	})
}

// TestCreateRejectsInvalidEmail verifies malformed addresses are refused and
// that Lookup reports them as an unknown user.
func TestCreateRejectsInvalidEmail(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, nil)
		for _, email := range []string{"", "nobody", "Ada <ada@example.com>", "a@b@c"} {
			if _, err := directory.Create(ctx, email); !errors.Is(err, account.ErrInvalidEmail) {
				t.Errorf("Create(%q) error = %v, want ErrInvalidEmail", email, err)
			}
			if _, err := directory.Lookup(ctx, email); !errors.Is(err, auth.ErrUserNotFound) {
				t.Errorf("Lookup(%q) error = %v, want ErrUserNotFound", email, err)
			}
		}
	})
}

// TestFindUnknownAndInvalidSubject verifies the driver contract errors.
func TestFindUnknownAndInvalidSubject(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, nil)
		if _, err := directory.Find(ctx, "missing"); !errors.Is(err, auth.ErrUserNotFound) {
			t.Errorf("Find(missing) error = %v, want ErrUserNotFound", err)
		}
		if _, err := directory.Find(ctx, ""); !errors.Is(err, auth.ErrInvalidSubject) {
			t.Errorf("Find(empty) error = %v, want ErrInvalidSubject", err)
		}
	})
}

// TestSubjectComparisonIsBytewise verifies subjects that differ only by case
// are different users, which a case-insensitive collation would merge.
func TestSubjectComparisonIsBytewise(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, nil)
		subject, err := directory.Create(ctx, "ada@example.com")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		upper := ""
		for _, char := range subject {
			if char >= 'a' && char <= 'f' {
				char -= 'a' - 'A'
			}
			upper += string(char)
		}
		if upper == subject {
			t.Skip("generated subject has no letters")
		}
		if _, err := directory.Find(ctx, upper); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("Find(%q) error = %v, want ErrUserNotFound", upper, err)
		}
	})
}

// TestRotateChangesVersion verifies Rotate bumps the record version, and that an
// unknown subject is an error.
func TestRotateChangesVersion(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, nil)
		subject, _ := directory.Create(ctx, "ada@example.com")
		before, _ := directory.Find(ctx, subject)
		if err := directory.Rotate(ctx, subject); err != nil {
			t.Fatalf("Rotate: %v", err)
		}
		after, _ := directory.Find(ctx, subject)
		if before.Version == after.Version {
			t.Fatalf("version did not change: %q", after.Version)
		}
		if err := directory.Rotate(ctx, "missing"); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("Rotate(missing) error = %v", err)
		}
	})
}

// TestDisabledUserIsDisabled verifies Disable and Enable toggle the driver
// error.
func TestDisabledUserIsDisabled(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, nil)
		subject, _ := directory.Create(ctx, "ada@example.com")
		if err := directory.Disable(ctx, subject); err != nil {
			t.Fatalf("Disable: %v", err)
		}
		if _, err := directory.Find(ctx, subject); !errors.Is(err, auth.ErrUserDisabled) {
			t.Fatalf("Find error = %v, want ErrUserDisabled", err)
		}
		if err := directory.Enable(ctx, subject); err != nil {
			t.Fatalf("Enable: %v", err)
		}
		if _, err := directory.Find(ctx, subject); err != nil {
			t.Fatalf("Find after Enable: %v", err)
		}
	})
}

// TestVerifyLifecycle verifies a token verifies the email once and then is
// spent.
func TestVerifyLifecycle(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		var delivered []account.Message
		directory := newDirectory(t, db, instance, &delivered)
		subject, _ := directory.Create(ctx, "ada@example.com")
		if err := directory.SendVerification(ctx, subject); err != nil {
			t.Fatalf("SendVerification: %v", err)
		}
		if len(delivered) != 1 || delivered[0].Email != "ada@example.com" {
			t.Fatalf("delivered = %#v", delivered)
		}
		got, err := directory.Verify(ctx, delivered[0].Token)
		if err != nil || got != subject {
			t.Fatalf("Verify = %q, %v", got, err)
		}
		record, _ := directory.Find(ctx, subject)
		if record.Claims["email_verified"] != true {
			t.Fatalf("claims = %#v", record.Claims)
		}
		if _, err := directory.Verify(ctx, delivered[0].Token); !errors.Is(err, account.ErrInvalidToken) {
			t.Fatalf("second Verify error = %v, want ErrInvalidToken", err)
		}
		if err := directory.SendVerification(ctx, subject); err != nil || len(delivered) != 1 {
			t.Fatalf("SendVerification for a verified user sent %d messages, err %v", len(delivered), err)
		}
	})
}

// TestVerifyTokenReplacedAndExpired verifies a newer token replaces the older
// one and an expired token is refused.
func TestVerifyTokenReplacedAndExpired(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		var delivered []account.Message
		directory := newDirectory(t, db, instance, &delivered)
		subject, _ := directory.Create(ctx, "ada@example.com")
		_ = directory.SendVerification(ctx, subject)
		_ = directory.SendVerification(ctx, subject)
		if _, err := directory.Verify(ctx, delivered[0].Token); !errors.Is(err, account.ErrInvalidToken) {
			t.Fatalf("replaced token error = %v, want ErrInvalidToken", err)
		}
		past := time.Now().Add(-time.Hour).Unix()
		_, err := db.Exec(ctx, query.Table(instance+"_email_verifications", func(statement *query.Builder) {
			statement.Unfiltered().Update(map[string]any{"expires_at": past})
		}))
		if err != nil {
			t.Fatalf("expire token: %v", err)
		}
		if _, err := directory.Verify(ctx, delivered[1].Token); !errors.Is(err, account.ErrInvalidToken) {
			t.Fatalf("expired token error = %v, want ErrInvalidToken", err)
		}
	})
}

// TestSendVerificationWithoutDelivery verifies the configuration error.
func TestSendVerificationWithoutDelivery(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		directory, err := account.NewDirectory(db, func(accounts *account.Builder) {
			accounts.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewDirectory: %v", err)
		}
		if err := directory.SendVerification(testContext(t), "x"); !errors.Is(err, account.ErrNoDelivery) {
			t.Fatalf("error = %v, want ErrNoDelivery", err)
		}
	})
}

// TestDeleteRunsForgetHooks verifies Delete removes the user and runs the hooks
// of the provider in the same transaction, and that a failing hook keeps the
// user.
func TestDeleteRunsForgetHooks(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, nil)
		subject, _ := directory.Create(ctx, "ada@example.com")
		var forgotten []string
		fail := true
		directory.Provider().OnForget(func(_ context.Context, subject string) error {
			if fail {
				return errors.New("hook failed")
			}
			forgotten = append(forgotten, subject)

			return nil
		})
		if err := directory.Delete(ctx, subject); err == nil {
			t.Fatal("Delete succeeded despite a failing hook")
		}
		if _, err := directory.Find(ctx, subject); err != nil {
			t.Fatalf("user vanished after a failed delete: %v", err)
		}
		fail = false
		if err := directory.Delete(ctx, subject); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if len(forgotten) != 1 || forgotten[0] != subject {
			t.Fatalf("forgotten = %v", forgotten)
		}
		if _, err := directory.Find(ctx, subject); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("Find after Delete error = %v", err)
		}
	})
}

// TestTwoInstancesSeparateTables verifies two instances over one database do
// not see each other's users.
func TestTwoInstancesSeparateTables(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		first := newDirectory(t, db, instance+"_a", nil)
		second := newDirectory(t, db, instance+"_b", nil)
		subject, _ := first.Create(ctx, "ada@example.com")
		if _, err := second.Find(ctx, subject); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("second instance found the user: %v", err)
		}
		if _, err := second.Create(ctx, "ada@example.com"); err != nil {
			t.Fatalf("same email in the second instance: %v", err)
		}
	})
}

// TestPingNamesMigrations verifies Ping fails with ErrNotMigrated before the
// migrations ran and succeeds after.
func TestPingNamesMigrations(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory, err := account.NewDirectory(db, func(accounts *account.Builder) {
			accounts.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewDirectory: %v", err)
		}
		err = directory.Ping(ctx)
		if !errors.Is(err, account.ErrNotMigrated) {
			t.Fatalf("Ping error = %v, want ErrNotMigrated", err)
		}
		migrateUp(t, db, directory.Migrations()...)
		if err := directory.Ping(ctx); err != nil {
			t.Fatalf("Ping after migrations: %v", err)
		}
	})
}

// TestNewDirectoryRejectsBadConfig verifies invalid instances and a nil
// database are configuration errors.
func TestNewDirectoryRejectsBadConfig(t *testing.T) {
	t.Parallel()

	if _, err := account.NewDirectory(nil); !errors.Is(err, account.ErrInvalidConfig) {
		t.Errorf("nil database error = %v", err)
	}
	driver, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	db := database.New(driver)
	for _, instance := range []string{"Upper", "1digit", "has-dash", "a23456789012345678901"} {
		_, err := account.NewDirectory(db, func(accounts *account.Builder) {
			accounts.Instance(instance)
		})
		if !errors.Is(err, account.ErrInvalidConfig) {
			t.Errorf("instance %q error = %v", instance, err)
		}
	}
}
