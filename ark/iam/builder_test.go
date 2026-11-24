package iam_test

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/totp"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/account"
	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/ark/iam/internal/apptest"
	"arkoren.dev/ark/iam/password"
	"arkoren.dev/ark/iam/permission"
)

// ownUsersWithoutCreate returns the application's users without a Create
// function, so nothing can create a user.
func ownUsersWithoutCreate(t *testing.T) iam.Users {
	t.Helper()

	users := newOwnUsers(t)
	provider, err := auth.NewUserProvider(users.driver)
	if err != nil {
		t.Fatalf("NewUserProvider: %v", err)
	}

	return iam.Users{Provider: provider, Lookup: users.lookup, Rotate: users.rotate}
}

// TestRegistrationWithoutCreateIsIgnored verifies Registration(true) over users
// that cannot be created is ignored with a warning, and POST /register is not
// mounted.
func TestRegistrationWithoutCreateIsIgnored(t *testing.T) {
	warnings := apptest.CaptureWarnings(t)

	sys := newAppWith(t, memoryDatabase(t), newInstance(t), wire{reset: true}, func(identity *iam.Builder) {
		identity.Users(ownUsersWithoutCreate(t)).Password(hasher(), registration)
	})
	absent(t, sys, "POST /register")
	if got := warnings.Settings(); !slices.Contains(got, "password.Builder.Registration") {
		t.Fatalf("warnings = %v, want one naming password.Builder.Registration", got)
	}
}

// TestSignUpWithoutCreateIsIgnored verifies SignUp(true) over users that cannot
// be created is ignored with a warning, and a first login finds no account.
func TestSignUpWithoutCreateIsIgnored(t *testing.T) {
	warnings := apptest.CaptureWarnings(t)

	sys := newAppWith(t, memoryDatabase(t), newInstance(t), wire{reset: true}, func(identity *iam.Builder) {
		identity.Users(ownUsersWithoutCreate(t)).Provider(fakeProvider{name: "fake"}, signUp)
	})
	if got := warnings.Settings(); !slices.ContainsFunc(got, func(setting string) bool { return strings.HasPrefix(setting, "ProviderBuilder.SignUp") }) {
		t.Fatalf("warnings = %v, want one naming ProviderBuilder.SignUp", got)
	}
	if !sys.router.Has(http.MethodGet, "/fake") {
		t.Fatal("GET /fake is not mounted, want the provider kept without sign-up")
	}
}

// TestSignUpOffNeedsNoCreate verifies providers work over users that cannot be
// created while sign-up stays off.
func TestSignUpOffNeedsNoCreate(t *testing.T) {
	t.Parallel()

	if _, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) {
		identity.Users(ownUsersWithoutCreate(t)).Provider(fakeProvider{name: "fake"})
	}); err != nil {
		t.Fatalf("New: %v", err)
	}
}

// TestDirectoryWithUsersIsInvalid verifies configuring the built-in users next
// to the application's own is refused.
func TestDirectoryWithUsersIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) {
		identity.Users(ownUsersWithoutCreate(t)).Directory(func(accounts *account.Builder) { accounts.VerifyTTL(time.Hour) })
	})
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestTwoFactorWithNilCipherIsInvalid verifies a nil cipher no longer leaves
// two-factor silently off.
func TestTwoFactorWithNilCipherIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) {
		identity.TwoFactor(totp.New("Example"), nil)
	})
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestTokensWithNoKindIsInvalid verifies enabling tokens and switching both
// kinds off is refused.
func TestTokensWithNoKindIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) {
		identity.Tokens(func(tokens *apitoken.Builder) { tokens.PersonalTokens(false).DeviceTokens(false) })
	})
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestTokenCatalogWithoutGrantsIsInvalid verifies a scope catalog needs its
// grant function.
func TestTokenCatalogWithoutGrantsIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) {
		identity.Tokens(func(tokens *apitoken.Builder) { tokens.Grants(nil, "posts:read") })
	})
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestDirectoryCallbacksReachTheBuiltInUsers verifies Directory configures the
// built-in users: a shorter verification lifetime shows in the delivered link.
func TestDirectoryCallbacksReachTheBuiltInUsers(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Directory(func(accounts *account.Builder) { accounts.VerifyTTL(time.Hour) })
		})
		subject, err := sys.identity.Directory().Create(ctx, "ada@example.com")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := sys.identity.Directory().SendVerification(ctx, account.VerifyRequest{Subject: subject}); err != nil {
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.Password(hasher())
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

	if _, err := newFacade(t, func(identity *iam.Builder) {
		identity.TwoFactor(totp.New("Example"), nil)
		identity.TwoFactor(totp.New("Example"), cipher(t))
	}); err != nil {
		t.Fatalf("New with a valid last call = %v, want nil", err)
	}
	_, err := newFacade(t, func(identity *iam.Builder) {
		identity.TwoFactor(totp.New("Example"), cipher(t))
		identity.TwoFactor(totp.New("Example"), nil)
	})
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("New with an invalid last call = %v, want ErrInvalidConfig", err)
	}
}

// TestTokensLaterCallReplacesTheEarlier verifies the configure callbacks of an
// earlier Tokens call are dropped: personal tokens an earlier call turned off
// work again.
func TestTokensLaterCallReplacesTheEarlier(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Tokens(func(tokens *apitoken.Builder) { tokens.PersonalTokens(false) })
			identity.Tokens()
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher())
		})
		if reply := sys.client.Post("/register", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusNotFound {
			t.Fatalf("register = %d, want 404 while registration is off", reply.Status)
		}
		subject, err := sys.identity.Directory().CreateVerified(ctx, "ada@example.com")
		if err != nil {
			t.Fatalf("CreateVerified: %v", err)
		}
		if err := sys.identity.Verifier().Set(ctx, subject, "correct horse"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if reply := sys.client.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("login = %d %s", reply.Status, reply.Body)
		}
		if me := sys.client.Do(http.MethodGet, "/whoami", ""); !strings.HasPrefix(me.Body, subject+"|") {
			t.Fatalf("/me = %q, want %s", me.Body, subject)
		}
	})
}

// absent fails the test for each "METHOD /path" route that is mounted on sys.
func absent(t *testing.T, sys *app, routes ...string) {
	t.Helper()

	for _, route := range routes {
		method, path, _ := strings.Cut(route, " ")
		if sys.router.Has(method, path) {
			t.Errorf("%s is mounted, want it absent", route)
		}
	}
}

// TestPasswordNotCalledMountsNothing verifies that without a Password call no
// password route is mounted and no verifier exists.
func TestPasswordNotCalledMountsNothing(t *testing.T) {
	t.Parallel()

	sys := newAppWith(t, memoryDatabase(t), newInstance(t), wire{}, func(identity *iam.Builder) {})
	absent(t, sys, "POST /login", "POST /register", "PUT /password", "POST /password/forgot")
	if sys.identity.Verifier() != nil {
		t.Fatal("Verifier is not nil without a Password call")
	}
}

// TestPasswordWithoutHasherIsInvalid verifies a Password call needs a hasher.
func TestPasswordWithoutHasherIsInvalid(t *testing.T) {
	t.Parallel()

	_, err := newFacade(t, func(identity *iam.Builder) { identity.Password(nil) })
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestTwoFactorNotCalledMountsNothing verifies that without a TwoFactor call no
// two-factor route is mounted.
func TestTwoFactorNotCalledMountsNothing(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), newInstance(t))
	absent(t, sys, "POST /two-factor/setup", "POST /two-factor", "GET /login/pending")
	if sys.identity.Factors() != nil {
		t.Fatal("Factors is not nil without a TwoFactor call")
	}
}

// TestTokensNotCalledMountsNothing verifies that without a Tokens call no token
// route is mounted and no issuer exists.
func TestTokensNotCalledMountsNothing(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), newInstance(t))
	absent(t, sys, "GET /tokens", "POST /tokens", "POST /device-tokens")
	if sys.identity.Issuer() != nil {
		t.Fatal("Issuer is not nil without a Tokens call")
	}
}

// TestPermissionsNotCalledMountsNothing verifies that without a Permissions
// call no permission route is mounted and no store exists.
func TestPermissionsNotCalledMountsNothing(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), newInstance(t))
	absent(t, sys, "GET /me/permissions")
	if sys.identity.Permissions() != nil {
		t.Fatal("Permissions is not nil without a Permissions call")
	}
}

// TestProviderNotCalledMountsNoProvider verifies that without a Provider call
// no provider route and no connection store exist, and that a called provider
// is mounted.
func TestProviderNotCalledMountsNoProvider(t *testing.T) {
	t.Parallel()

	without := newApp(t, memoryDatabase(t), newInstance(t))
	if without.router.Has(http.MethodGet, "/fake") || without.identity.Links() != nil {
		t.Fatal("a provider is mounted without a Provider call")
	}
	with := newApp(t, memoryDatabase(t), newInstance(t), func(identity *iam.Builder) {
		identity.Provider(fakeProvider{name: "fake"})
	})
	if !with.router.Has(http.MethodGet, "/fake") {
		t.Fatal("GET /fake is not mounted for the called provider")
	}
}

// TestResetNotCalledMountsNoRecoveryRoutes verifies that without a Reset call
// neither forgot nor reset is mounted.
func TestResetNotCalledMountsNoRecoveryRoutes(t *testing.T) {
	t.Parallel()

	sys := newAppWith(t, memoryDatabase(t), newInstance(t), wire{}, func(identity *iam.Builder) {
		identity.Password(hasher(), func(passwords *password.Builder) { passwords.Registration(false) })
	})
	absent(t, sys, "POST /password/forgot", "POST /password/reset")
}

// TestVerificationNotCalledMountsNoEmailRoutes verifies that without a
// Verification call no email route is mounted.
func TestVerificationNotCalledMountsNoEmailRoutes(t *testing.T) {
	t.Parallel()

	sys := newAppWith(t, memoryDatabase(t), newInstance(t), wire{}, func(identity *iam.Builder) {
		identity.Directory(func(accounts *account.Builder) { accounts.VerifyTTL(time.Hour) })
	})
	absent(t, sys, "POST /email/verify", "POST /email/verify/resend")
}

// TestManagementNotCalledMountsNoUserRoutes verifies that without a Management
// call no /users route is mounted.
func TestManagementNotCalledMountsNoUserRoutes(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), newInstance(t), func(identity *iam.Builder) {
		identity.Permissions(func(policy *permission.Builder) { policy.SelfService(true) })
	})
	absent(t, sys, "GET /users/abc/permissions", "PUT /users/abc/roles/editor")
}
