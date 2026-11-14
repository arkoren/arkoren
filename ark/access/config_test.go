package access_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access"
)

// newFacade composes a facade over an in-memory database without serving it.
func newFacade(t *testing.T, configure ...access.BuilderFunc) (*access.Access, error) {
	t.Helper()

	return access.New(memoryDatabase(t), cache.New(memorycache.New()), configure...)
}

// TestNewRejectsBadConfig verifies the configuration errors.
func TestNewRejectsBadConfig(t *testing.T) {
	t.Parallel()

	if _, err := access.New(nil, nil); !errors.Is(err, access.ErrInvalidConfig) {
		t.Errorf("missing dependencies error = %v", err)
	}
	if _, err := newFacade(t, func(iam *access.Builder) { iam.Instance("Bad-Name") }); !errors.Is(err, access.ErrInvalidConfig) {
		t.Errorf("invalid instance error = %v", err)
	}
	if _, err := newFacade(t, func(iam *access.Builder) { iam.SessionLifetime(-1) }); !errors.Is(err, access.ErrInvalidConfig) {
		t.Errorf("negative lifetime error = %v", err)
	}
	if _, err := newFacade(t, func(iam *access.Builder) { iam.TwoFactor(cipher(t), "bad:issuer") }); !errors.Is(err, access.ErrInvalidConfig) {
		t.Errorf("invalid issuer error = %v", err)
	}
}

// TestNewRejectsDuplicateProviderNames verifies two providers cannot share a
// mounted path.
func TestNewRejectsDuplicateProviderNames(t *testing.T) {
	t.Parallel()

	_, err := newFacade(t, func(iam *access.Builder) {
		iam.Provider(fakeProvider{name: "oauth"}, fakeProvider{name: "oauth"})
	})
	if !errors.Is(err, access.ErrInvalidConfig) || !strings.Contains(err.Error(), "oauth") {
		t.Fatalf("error = %v, want a duplicate-name rejection", err)
	}
}

// TestHandlersPanicWhenFeatureDisabled verifies each handler of a disabled
// feature fails at composition.
func TestHandlersPanicWhenFeatureDisabled(t *testing.T) {
	t.Parallel()

	iam, err := newFacade(t)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for name, build := range map[string]func(){
		"RegisterHandler":           func() { iam.RegisterHandler() },
		"LoginHandler":              func() { iam.LoginHandler() },
		"ChallengeHandler":          func() { iam.ChallengeHandler() },
		"PendingHandler":            func() { iam.PendingHandler() },
		"ConfirmHandler":            func() { iam.ConfirmHandler() },
		"VerifyEmailHandler":        func() { iam.VerifyEmailHandler() },
		"ResendVerificationHandler": func() { iam.ResendVerificationHandler() },
		"ForgotPasswordHandler":     func() { iam.ForgotPasswordHandler() },
		"ResetPasswordHandler":      func() { iam.ResetPasswordHandler() },
		"ChangePasswordHandler":     func() { iam.ChangePasswordHandler() },
		"SetupTwoFactorHandler":     func() { iam.SetupTwoFactorHandler() },
		"EnableTwoFactorHandler":    func() { iam.EnableTwoFactorHandler() },
		"DisableTwoFactorHandler":   func() { iam.DisableTwoFactorHandler() },
		"RecoveryCodesHandler":      func() { iam.RecoveryCodesHandler() },
		"ListTokensHandler":         func() { iam.ListTokensHandler() },
		"CreateTokenHandler":        func() { iam.CreateTokenHandler() },
		"DeleteTokenHandler":        func() { iam.DeleteTokenHandler() },
		"ExchangeHandler":           func() { iam.ExchangeHandler() },
		"DeleteDeviceTokenHandler":  func() { iam.DeleteDeviceTokenHandler() },
		"ListConnectionsHandler":    func() { iam.ListConnectionsHandler() },
		"ConnectHandler":            func() { iam.ConnectHandler() },
		"DeleteConnectionHandler":   func() { iam.DeleteConnectionHandler() },
	} {
		if !panics(build) {
			t.Errorf("%s did not panic with its feature disabled", name)
		}
	}
	// These work without any feature.
	iam.LogoutHandler()
	iam.ListSessionsHandler()
	iam.DeleteSessionHandler()
	iam.DeleteOtherSessionsHandler()
	iam.DeleteAccountHandler()
	iam.RequireVerified()
}

// panics reports whether f panics.
func panics(f func()) (panicked bool) {
	defer func() {
		panicked = recover() != nil
	}()
	f()

	return false
}

// TestDeliverIsRequiredForForgotAndVerify verifies the composition-time panics
// for a missing delivery function.
func TestDeliverIsRequiredForForgotAndVerify(t *testing.T) {
	t.Parallel()

	iam, err := newFacade(t, func(iam *access.Builder) { iam.Password(hasher()) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for name, build := range map[string]func(){
		"ForgotPasswordHandler":     func() { iam.ForgotPasswordHandler() },
		"VerifyEmailHandler":        func() { iam.VerifyEmailHandler() },
		"ResendVerificationHandler": func() { iam.ResendVerificationHandler() },
		"Mount":                     func() { iam.Mount(router.New()) },
	} {
		if !panics(build) {
			t.Errorf("%s did not panic without Deliver", name)
		}
	}
}

// TestMustAcceptPanicsForUnregisteredMethod verifies the startup check.
func TestMustAcceptPanicsForUnregisteredMethod(t *testing.T) {
	t.Parallel()

	iam, err := newFacade(t, func(iam *access.Builder) {
		iam.Password(hasher())
		iam.TwoFactor(cipher(t), "Example")
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	iam.MustAccept(auth.MethodPassword, auth.MethodOTP, "recovery")
	if !panics(func() { iam.MustAccept("passkey") }) {
		t.Fatal("MustAccept accepted an unregistered method")
	}
}

// TestDefaultRegistriesAreOrdered verifies the second-factor methods are listed
// totp then recovery, and step-up is pwd then totp.
func TestDefaultRegistriesAreOrdered(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
			iam.TwoFactor(cipher(t), "Example")
		})
		register(t, sys.client, "ada@example.com")
		setup := sys.client.Post("/two-factor/setup", "")
		sys.client.Post("/two-factor", `{"code":"`+code(t, secretOf(setup.Body), 0)+`"}`)
		stale := sys.client.NewDevice()
		reply := stale.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
		if !strings.Contains(reply.Body, `["otp","recovery"]`) {
			t.Fatalf("second-factor methods = %s, want otp then recovery", reply.Body)
		}
	})
}

// TestMountOnlyMountsEnabledFeatures verifies the route table per feature set.
func TestMountOnlyMountsEnabledFeatures(t *testing.T) {
	t.Parallel()

	type route struct{ method, path string }
	all := map[string][]route{
		"password": {{"POST", "/register"}, {"POST", "/login"}, {"PUT", "/password"}, {"POST", "/password/forgot"}, {"POST", "/password/reset"}},
		"twofactor": {{"POST", "/two-factor/setup"}, {"POST", "/two-factor"}, {"DELETE", "/two-factor"}, {"POST", "/two-factor/recovery-codes"},
			{"POST", "/login/challenge"}, {"GET", "/login/pending"}},
		"tokens":    {{"GET", "/tokens"}, {"POST", "/tokens"}, {"DELETE", "/tokens/abc"}, {"POST", "/device-tokens"}, {"DELETE", "/device-tokens/current"}},
		"providers": {{"GET", "/auth/fake"}, {"GET", "/auth/fake/callback"}, {"GET", "/connections"}, {"POST", "/connections/fake"}, {"DELETE", "/connections/abc"}},
		"always":    {{"POST", "/logout"}, {"GET", "/sessions"}, {"DELETE", "/sessions"}, {"DELETE", "/sessions/abc"}, {"DELETE", "/account"}, {"POST", "/email/verify"}, {"POST", "/email/verify/resend"}},
	}
	features := map[string]access.BuilderFunc{
		"password":  func(iam *access.Builder) { iam.Password(hasher()) },
		"twofactor": func(iam *access.Builder) { iam.TwoFactor(cipher(t), "Example") },
		"tokens":    func(iam *access.Builder) { iam.Tokens() },
		"providers": func(iam *access.Builder) { iam.Provider(fakeProvider{name: "fake"}) },
		"always":    func(iam *access.Builder) {},
	}
	for enabled := range all {
		sys := newApp(t, memoryDatabase(t), newInstance(t), features[enabled])
		for feature, routes := range all {
			want := feature == enabled || feature == "always"
			for _, route := range routes {
				if got := sys.router.Has(route.method, route.path); got != want {
					t.Errorf("with only %s: %s %s mounted = %v, want %v", enabled, route.method, route.path, got, want)
				}
			}
		}
	}
}

// TestMigrationsOnlyForEnabledFeatures verifies Migrations lists the groups of
// the enabled features and Ping names the migrations when they have not run.
func TestMigrationsOnlyForEnabledFeatures(t *testing.T) {
	t.Parallel()

	groups := func(iam *access.Access) string {
		seen := []string{}
		for _, definition := range iam.Migrations() {
			group := strings.TrimPrefix(definition.Group(), "arkoren.dev:ark/access:")
			if len(seen) == 0 || seen[len(seen)-1] != group {
				seen = append(seen, group)
			}
		}

		return strings.Join(seen, ",")
	}
	all, err := newFacade(t, func(iam *access.Builder) {
		iam.Password(hasher())
		iam.TwoFactor(cipher(t), "Example")
		iam.Tokens()
		iam.Provider(fakeProvider{name: "fake"})
		iam.Instance("tenant_a")
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := groups(all), "account:tenant_a,password:tenant_a,twofactor:tenant_a,apitoken:tenant_a,federation:tenant_a"; got != want {
		t.Errorf("groups = %s, want %s", got, want)
	}
	passwordOnly, _ := newFacade(t, func(iam *access.Builder) { iam.Password(hasher()) })
	if got, want := groups(passwordOnly), "account,password"; got != want {
		t.Errorf("groups = %s, want %s", got, want)
	}
	if err := passwordOnly.Ping(context.Background()); !errors.Is(err, access.ErrNotMigrated) {
		t.Errorf("Ping before migrating error = %v, want ErrNotMigrated", err)
	}
}

// ownUsers is the application's own user store, as the facade sees it.
type ownUsers struct {
	mu       sync.Mutex
	driver   *memoryauth.Driver
	subjects map[string]string
	versions map[string]int
}

// newOwnUsers returns a store with one user, "ada@example.com".
func newOwnUsers(t *testing.T) *ownUsers {
	t.Helper()

	users := &ownUsers{driver: memoryauth.New(), subjects: map[string]string{}, versions: map[string]int{}}
	if _, err := users.create(context.Background(), "ada@example.com"); err != nil {
		t.Fatalf("create: %v", err)
	}

	return users
}

// create adds a user.
func (users *ownUsers) create(_ context.Context, identifier string) (string, error) {
	users.mu.Lock()
	defer users.mu.Unlock()

	subject := "u" + strconv.Itoa(len(users.subjects)+1)
	users.subjects[identifier] = subject

	return subject, users.driver.Put(auth.UserRecord{Subject: subject, Version: "0"})
}

// lookup finds a user by identifier.
func (users *ownUsers) lookup(_ context.Context, identifier string) (string, error) {
	users.mu.Lock()
	defer users.mu.Unlock()

	subject, ok := users.subjects[identifier]
	if !ok {
		return "", fmt.Errorf("own users: %w", auth.ErrUserNotFound)
	}

	return subject, nil
}

// rotate bumps a user's version.
func (users *ownUsers) rotate(_ context.Context, subject string) error {
	users.mu.Lock()
	defer users.mu.Unlock()

	users.versions[subject]++

	return users.driver.Put(auth.UserRecord{Subject: subject, Version: strconv.Itoa(users.versions[subject])})
}

// TestOwnUsersPasswordLoginAndReset verifies the facade works over the
// application's own users: no users table, login, and a reset that rotates.
func TestOwnUsersPasswordLoginAndReset(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		users := newOwnUsers(t)
		provider, err := auth.NewUserProvider(users.driver)
		if err != nil {
			t.Fatalf("NewUserProvider: %v", err)
		}
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Users(access.Users{Provider: provider, Lookup: users.lookup, Create: users.create, Rotate: users.rotate})
			iam.Password(hasher())
		})
		if sys.iam.Directory() != nil {
			t.Fatal("the built-in directory was built next to Users")
		}
		for _, definition := range sys.iam.Migrations() {
			if strings.Contains(definition.Group(), ":account") {
				t.Fatalf("a users table is migrated: %s", definition.ID())
			}
		}
		if err := sys.iam.Verifier().Set(ctx, "u1", "correct horse"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		device := sys.client.NewDevice()
		if reply := device.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("login = %d %s", reply.Status, reply.Body)
		}
		if reply := device.Do(http.MethodGet, "/me", ""); !strings.HasPrefix(reply.Body, "u1|1|") {
			t.Fatalf("/me = %q", reply.Body)
		}
		sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`)
		token := sys.box.last(t, access.KindReset).Token
		sys.client.NewDevice().Post("/password/reset", `{"token":"`+token+`","password":"reset horse ok"}`)
		if users.versions["u1"] != 1 {
			t.Fatalf("the reset rotated %d times, want once", users.versions["u1"])
		}
		if reply := device.Do(http.MethodGet, "/me", ""); reply.Body != "anonymous" {
			t.Fatalf("the old login survived the rotation: %q", reply.Body)
		}
		for _, route := range [][2]string{{"POST", "/email/verify"}, {"POST", "/email/verify/resend"}, {"DELETE", "/account"}} {
			if sys.router.Has(route[0], route[1]) {
				t.Errorf("%s %s is mounted without the built-in users", route[0], route[1])
			}
		}
	})
}

// TestOwnUsersValidation verifies the Users requirements.
func TestOwnUsersValidation(t *testing.T) {
	t.Parallel()

	users := newOwnUsers(t)
	provider, _ := auth.NewUserProvider(users.driver)
	if _, err := newFacade(t, func(iam *access.Builder) { iam.Users(access.Users{}) }); !errors.Is(err, access.ErrInvalidConfig) {
		t.Errorf("Users without a provider error = %v", err)
	}
	_, err := newFacade(t, func(iam *access.Builder) {
		iam.Users(access.Users{Provider: provider})
		iam.Password(hasher())
	})
	if !errors.Is(err, access.ErrInvalidConfig) {
		t.Errorf("Password without Lookup and Rotate error = %v", err)
	}
	iam, err := newFacade(t, func(iam *access.Builder) { iam.Users(access.Users{Provider: provider}) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !panics(func() { iam.DeleteAccountHandler() }) || !panics(func() { iam.RequireVerified() }) {
		t.Error("the directory-only handlers did not panic with the application's own users")
	}
}

// TestSessionAndGuardShareTheLifetime verifies SessionLifetime reaches both.
func TestSessionAndGuardShareTheLifetime(t *testing.T) {
	t.Parallel()

	if _, err := newFacade(t, func(iam *access.Builder) {
		iam.SessionLifetime(48 * time.Hour)
		iam.Session(func(cookies *session.Builder) { cookies.Name("app_session") })
	}); err != nil {
		t.Fatalf("New: %v", err)
	}
}

// TestPackagesDoNotImportEachOther verifies the public packages of the module
// import only core, foundation, internal helpers and the standard library, and
// never one another or the root.
func TestPackagesDoNotImportEachOther(t *testing.T) {
	t.Parallel()

	output, err := exec.Command("go", "list", "-f", "{{.ImportPath}}: {{join .Imports \" \"}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	const module = "arkoren.dev/ark/access"
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		path, imports, _ := strings.Cut(line, ": ")
		if strings.Contains(path, "/internal/") {
			continue
		}
		for imported := range strings.FieldsSeq(imports) {
			switch {
			case strings.HasPrefix(imported, "arkoren.dev/ark/") && !strings.HasPrefix(imported, module):
				t.Errorf("%s imports another ark: %s", path, imported)
			case strings.HasPrefix(imported, module) && path != module && !strings.Contains(imported, "/internal/"):
				t.Errorf("%s imports a sibling package: %s", path, imported)
			}
		}
	}
}
