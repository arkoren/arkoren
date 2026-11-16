package access_test

import (
	"testing"

	"arkoren.dev/ark/access"
)

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
