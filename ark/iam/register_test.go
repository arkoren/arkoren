package iam_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/totp"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/apitoken"
)

// TestRegisterOnlyRegistersEnabledFeatures verifies the route table per feature set.
func TestRegisterOnlyRegistersEnabledFeatures(t *testing.T) {
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
	features := map[string]iam.BuilderFunc{
		"password":  func(identity *iam.Builder) { identity.Password(hasher(), registration) },
		"twofactor": func(identity *iam.Builder) { identity.TwoFactor(totp.New("Example"), cipher(t)) },
		"tokens":    func(identity *iam.Builder) { identity.Tokens() },
		"providers": func(identity *iam.Builder) { identity.Provider(fakeProvider{name: "fake"}) },
		"always":    func(identity *iam.Builder) {},
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

// TestRegisterNeverPanicsOnAcceptedConfiguration verifies Register succeeds for every
// feature combination New accepts, and mounts a route only when its requirement
// holds: forgot and reset need Reset, the email routes need Verification,
// register needs Registration, and each token kind's routes need that kind.
func TestRegisterNeverPanicsOnAcceptedConfiguration(t *testing.T) {
	t.Parallel()

	ownProvider := func(t *testing.T) (*auth.UserProvider, *ownUsers) {
		t.Helper()
		users := newOwnUsers(t)
		provider, err := auth.NewUserProvider(users.driver)
		if err != nil {
			t.Fatalf("NewUserProvider: %v", err)
		}

		return provider, users
	}
	verified := func(context.Context, string) (bool, error) { return true, nil }
	proved := func(context.Context, string) (bool, error) { return false, nil }
	emailRoutes := []string{"POST /email/verify", "POST /email/verify/resend"}
	resetRoutes := []string{"POST /password/forgot", "POST /password/reset"}
	tests := []struct {
		name      string
		wire      wire
		configure func(t *testing.T, identity *iam.Builder)
		// absent lists routes that must not be mounted.
		absent []string
	}{
		{"nothing", wire{}, func(*testing.T, *iam.Builder) {}, slices.Concat(emailRoutes, resetRoutes)},
		{"password with neither reset nor verification", wire{}, func(_ *testing.T, identity *iam.Builder) { identity.Password(hasher()) },
			slices.Concat([]string{"POST /register"}, emailRoutes, resetRoutes)},
		{"password without registration", both, func(_ *testing.T, identity *iam.Builder) { identity.Password(hasher(), reset) },
			[]string{"POST /register"}},
		{"password with registration and reset only", wire{reset: true}, func(_ *testing.T, identity *iam.Builder) { identity.Password(hasher(), registration) },
			emailRoutes},
		{"password with registration, reset and verification", both, func(_ *testing.T, identity *iam.Builder) { identity.Password(hasher(), registration) }, nil},
		{"twofactor", both, func(t *testing.T, identity *iam.Builder) { identity.TwoFactor(totp.New("Example"), cipher(t)) }, resetRoutes},
		{"tokens", both, func(_ *testing.T, identity *iam.Builder) { identity.Tokens() }, resetRoutes},
		{"personal tokens only", both, func(_ *testing.T, identity *iam.Builder) {
			identity.Tokens(func(tokens *apitoken.Builder) { tokens.DeviceTokens(false) })
		}, []string{"POST /device-tokens", "DELETE /device-tokens/current"}},
		{"device tokens only", both, func(_ *testing.T, identity *iam.Builder) {
			identity.Tokens(func(tokens *apitoken.Builder) { tokens.PersonalTokens(false) })
		}, []string{"POST /tokens"}},
		{"token catalog with a grant function", both, func(_ *testing.T, identity *iam.Builder) {
			identity.Tokens(func(tokens *apitoken.Builder) { tokens.Grants(apitoken.AnyScope, "posts:read").GrantTTL(time.Minute) })
		}, nil},
		{"providers", both, func(_ *testing.T, identity *iam.Builder) { identity.Provider(fakeProvider{name: "fake"}) }, nil},
		{"providers with sign-up", both, func(_ *testing.T, identity *iam.Builder) { identity.Provider(fakeProvider{name: "fake"}, signUp) }, nil},
		{"everything", both, func(t *testing.T, identity *iam.Builder) {
			identity.Password(hasher(), registration).TwoFactor(totp.New("Example"), cipher(t)).Tokens().Provider(fakeProvider{name: "fake"}, signUp)
		}, nil},
		{"own users without create", wire{}, func(t *testing.T, identity *iam.Builder) {
			provider, users := ownProvider(t)
			identity.Users(iam.Users{Provider: provider, Lookup: users.lookup, Rotate: users.rotate}).Password(hasher())
		}, slices.Concat([]string{"POST /register", "DELETE /account"}, emailRoutes, resetRoutes)},
		{"own users without create, with reset", wire{reset: true}, func(t *testing.T, identity *iam.Builder) {
			provider, users := ownProvider(t)
			identity.Users(iam.Users{Provider: provider, Lookup: users.lookup, Rotate: users.rotate}).Password(hasher(), reset)
		}, slices.Concat([]string{"POST /register", "DELETE /account"}, emailRoutes)},
		{"own users with create and registration", wire{}, func(t *testing.T, identity *iam.Builder) {
			provider, users := ownProvider(t)
			identity.Users(iam.Users{Provider: provider, Lookup: users.lookup, Create: users.create, Rotate: users.rotate, Verified: verified, ProveEmail: proved}).Password(hasher(), registration)
		}, slices.Concat([]string{"DELETE /account"}, emailRoutes, resetRoutes)},
		{"own users with create, without registration", wire{}, func(t *testing.T, identity *iam.Builder) {
			provider, users := ownProvider(t)
			identity.Users(iam.Users{Provider: provider, Lookup: users.lookup, Create: users.create, Rotate: users.rotate, Verified: verified, ProveEmail: proved}).Password(hasher())
		}, []string{"POST /register", "DELETE /account"}},
		{"own users with everything", wire{reset: true}, func(t *testing.T, identity *iam.Builder) {
			provider, users := ownProvider(t)
			identity.Users(iam.Users{Provider: provider, Lookup: users.lookup, Create: users.create, Rotate: users.rotate, Verified: verified, ProveEmail: proved})
			identity.Password(hasher(), registration).TwoFactor(totp.New("Example"), cipher(t)).Tokens().Provider(fakeProvider{name: "fake"}, signUp)
		}, slices.Concat([]string{"DELETE /account"}, emailRoutes)},
	}
	for _, test := range tests {
		identity, err := newFacadeWith(t, test.wire, func(identity *iam.Builder) { test.configure(t, identity) })
		if err != nil {
			t.Fatalf("%s: New: %v", test.name, err)
		}
		r := router.New()
		if panics(func() { identity.Register(r) }) {
			t.Errorf("%s: Register panicked", test.name)
		}
		for _, route := range test.absent {
			method, path, _ := strings.Cut(route, " ")
			if r.Has(method, path) {
				t.Errorf("%s: %s is mounted without its requirement", test.name, route)
			}
		}
	}
}
