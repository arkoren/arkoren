package access_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/totp"

	"arkoren.dev/ark/access"
	"arkoren.dev/ark/access/account"
	"arkoren.dev/ark/access/apitoken"
)

// ownUsersWithoutCreate returns the application's users without a Create
// function, so nothing can create a user.
func ownUsersWithoutCreate(t *testing.T) access.Users {
	t.Helper()

	users := newOwnUsers(t)
	provider, err := auth.NewUserProvider(users.driver)
	if err != nil {
		t.Fatalf("NewUserProvider: %v", err)
	}

	return access.Users{Provider: provider, Lookup: users.lookup, Rotate: users.rotate}
}

// TestRegistrationWithoutCreateIsInvalid verifies Registration(true) over users
// that cannot be created is refused at construction.
func TestRegistrationWithoutCreateIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := newFacadeWith(t, wire{reset: true}, func(iam *access.Builder) {
		iam.Users(ownUsersWithoutCreate(t)).Password(hasher(), registration)
	})
	if !errors.Is(err, access.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestSignUpWithoutCreateIsInvalid verifies SignUp(true) over users that cannot
// be created is refused at construction.
func TestSignUpWithoutCreateIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := newFacadeWith(t, wire{reset: true}, func(iam *access.Builder) {
		iam.Users(ownUsersWithoutCreate(t)).Provider(fakeProvider{name: "fake"}, signUp)
	})
	if !errors.Is(err, access.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestSignUpOffNeedsNoCreate verifies providers work over users that cannot be
// created while sign-up stays off.
func TestSignUpOffNeedsNoCreate(t *testing.T) {
	t.Parallel()

	if _, err := newFacadeWith(t, wire{reset: true}, func(iam *access.Builder) {
		iam.Users(ownUsersWithoutCreate(t)).Provider(fakeProvider{name: "fake"})
	}); err != nil {
		t.Fatalf("New: %v", err)
	}
}

// TestDirectoryWithUsersIsInvalid verifies configuring the built-in users next
// to the application's own is refused.
func TestDirectoryWithUsersIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := newFacadeWith(t, wire{reset: true}, func(iam *access.Builder) {
		iam.Users(ownUsersWithoutCreate(t)).Directory(func(accounts *account.Builder) { accounts.VerifyTTL(time.Hour) })
	})
	if !errors.Is(err, access.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestTwoFactorWithNilCipherIsInvalid verifies a nil cipher no longer leaves
// two-factor silently off.
func TestTwoFactorWithNilCipherIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := newFacadeWith(t, wire{reset: true}, func(iam *access.Builder) {
		iam.TwoFactor(totp.New("Example"), nil)
	})
	if !errors.Is(err, access.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestTokensWithNoKindIsInvalid verifies enabling tokens and switching both
// kinds off is refused.
func TestTokensWithNoKindIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := newFacadeWith(t, wire{reset: true}, func(iam *access.Builder) {
		iam.Tokens(func(tokens *apitoken.Builder) { tokens.PersonalTokens(false).DeviceTokens(false) })
	})
	if !errors.Is(err, access.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestTokenCatalogWithoutGrantsIsInvalid verifies a scope catalog needs its
// grant function.
func TestTokenCatalogWithoutGrantsIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := newFacadeWith(t, wire{reset: true}, func(iam *access.Builder) {
		iam.Tokens(func(tokens *apitoken.Builder) { tokens.Grants(nil, "posts:read") })
	})
	if !errors.Is(err, access.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestDirectoryCallbacksReachTheBuiltInUsers verifies Directory configures the
// built-in users: a shorter verification lifetime shows in the delivered link.
func TestDirectoryCallbacksReachTheBuiltInUsers(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Directory(func(accounts *account.Builder) { accounts.VerifyTTL(time.Hour) })
		})
		subject, err := sys.iam.Directory().Create(ctx, "ada@example.com")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := sys.iam.Directory().SendVerification(ctx, account.VerifyRequest{Subject: subject}); err != nil {
			t.Fatalf("SendVerification: %v", err)
		}
		if left := time.Until(sys.box.lastVerify(t).ExpiresAt); left <= 0 || left > time.Hour {
			t.Fatalf("verification link expires in %v, want at most an hour", left)
		}
	})
}

// TestPasswordLaterCallReplacesTheEarlier verifies the configuration of the
// last Password call is the whole configuration: the registration an earlier
// call enabled is gone.
func TestPasswordLaterCallReplacesTheEarlier(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Password(hasher())
		})
		if sys.router.Has(http.MethodPost, "/register") {
			t.Fatal("POST /register is mounted, want the second Password call to replace the first")
		}
	})
}

// TestTwoFactorLaterCallReplacesTheEarlier verifies the last TwoFactor call
// replaces the cipher of the earlier one, in both directions.
func TestTwoFactorLaterCallReplacesTheEarlier(t *testing.T) {
	t.Parallel()

	if _, err := newFacade(t, func(iam *access.Builder) {
		iam.TwoFactor(totp.New("Example"), nil)
		iam.TwoFactor(totp.New("Example"), cipher(t))
	}); err != nil {
		t.Fatalf("New with a valid last call = %v, want nil", err)
	}
	_, err := newFacade(t, func(iam *access.Builder) {
		iam.TwoFactor(totp.New("Example"), cipher(t))
		iam.TwoFactor(totp.New("Example"), nil)
	})
	if !errors.Is(err, access.ErrInvalidConfig) {
		t.Fatalf("New with an invalid last call = %v, want ErrInvalidConfig", err)
	}
}

// TestTokensLaterCallReplacesTheEarlier verifies the configure callbacks of an
// earlier Tokens call are dropped: personal tokens an earlier call turned off
// work again.
func TestTokensLaterCallReplacesTheEarlier(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Tokens(func(tokens *apitoken.Builder) { tokens.PersonalTokens(false) })
			iam.Tokens()
		})
		if !sys.router.Has(http.MethodPost, "/tokens") {
			t.Fatal("POST /tokens is not mounted, want the second Tokens call to replace the first")
		}
	})
}

// TestUsersAddedThroughTheDirectoryLogIn verifies the way to add users with
// registration off: create them in the directory and give them a password.
func TestUsersAddedThroughTheDirectoryLogIn(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
		})
		if reply := sys.client.Post("/register", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusNotFound {
			t.Fatalf("register = %d, want 404 while registration is off", reply.Status)
		}
		subject, err := sys.iam.Directory().CreateVerified(ctx, "ada@example.com")
		if err != nil {
			t.Fatalf("CreateVerified: %v", err)
		}
		if err := sys.iam.Verifier().Set(ctx, subject, "correct horse"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if reply := sys.client.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("login = %d %s", reply.Status, reply.Body)
		}
		if me := sys.client.Do(http.MethodGet, "/me", ""); !strings.HasPrefix(me.Body, subject+"|") {
			t.Fatalf("/me = %q, want %s", me.Body, subject)
		}
	})
}
