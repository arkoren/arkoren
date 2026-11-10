package gate_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/auth/gate"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// TestRequireSnapshotsRuleSlice verifies that replacing an expanded rule cannot
// permit a previously denied user. Require must snapshot at construction, not
// when the returned middleware wraps an individual handler.
func TestRequireSnapshotsRuleSlice(t *testing.T) {
	t.Parallel()

	rules := []func(context.Context, string) (bool, error){
		func(context.Context, string) (bool, error) {
			return false, nil
		},
	}

	next := func(http.ResponseWriter, *http.Request) error {
		return nil
	}

	middleware := gate.Require(rules...)
	handler := middleware(next)
	rules[0] = func(context.Context, string) (bool, error) {
		return true, nil
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(auth.NewContext(request.Context(), "reader"))

	for _, protected := range []router.Handler{handler, middleware(next)} {
		if err := protected(httptest.NewRecorder(), request); !errors.Is(err, gate.ErrForbidden) {
			t.Fatalf("error = %v, want original rule denial", err)
		}
	}
}

// TestGateRequireSnapshotsRuleSlice verifies that the event-publishing method
// owns its rule container too. Replacing the caller's entry must not change
// handlers wrapped either before or after that replacement.
func TestGateRequireSnapshotsRuleSlice(t *testing.T) {
	t.Parallel()

	rules := []func(context.Context, string) (bool, error){
		func(context.Context, string) (bool, error) {
			return false, nil
		},
	}

	next := func(http.ResponseWriter, *http.Request) error {
		return nil
	}

	policy := gate.New()
	middleware := policy.Require(rules...)
	handler := middleware(next)
	rules[0] = func(context.Context, string) (bool, error) {
		return true, nil
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(auth.NewContext(request.Context(), "reader"))

	for _, protected := range []router.Handler{handler, middleware(next)} {
		if err := protected(httptest.NewRecorder(), request); !errors.Is(err, gate.ErrForbidden) {
			t.Fatalf("error = %v, want original rule denial", err)
		}
	}
}

// user is the test user type.
type user struct {
	// Admin controls the authorization rule independently of authentication.
	Admin bool
}

// isAdmin allows administrators.
func isAdmin(_ context.Context, u user) (bool, error) {
	return u.Admin, nil
}

// newRequest returns a request whose context carries u, or none when nil.
func newRequest(u *user) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	if u == nil {
		return r
	}

	return r.WithContext(auth.NewContext(r.Context(), *u))
}

// hasStatus reports whether err is a problem with the given status.
func hasStatus(err error, status int) bool {
	details, ok := errors.AsType[problem.Details](err)

	return ok && details.Status() == status
}

// TestAuthorizeWithoutUserIsUnauthenticated verifies the 401 case.
func TestAuthorizeWithoutUserIsUnauthenticated(t *testing.T) {
	t.Parallel()

	if err := gate.Authorize(newRequest(nil).Context(), isAdmin); !hasStatus(err, http.StatusUnauthorized) || !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}
}

// TestAuthorizeDeniesWithForbidden verifies a false rule.
func TestAuthorizeDeniesWithForbidden(t *testing.T) {
	t.Parallel()

	if err := gate.Authorize(newRequest(&user{}).Context(), isAdmin); !hasStatus(err, http.StatusForbidden) || !errors.Is(err, gate.ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
}

// TestAuthorizeAllowsWhenAllRulesPass verifies success, including no rules.
func TestAuthorizeAllowsWhenAllRulesPass(t *testing.T) {
	t.Parallel()

	ctx := newRequest(&user{
		Admin: true,
	}).Context()

	if err := gate.Authorize(ctx, isAdmin, isAdmin); err != nil {
		t.Fatal(err)
	}

	if err := gate.Authorize[user](ctx); err != nil {
		t.Fatal(err)
	}
}

// TestAuthorizeStopsAtFirstDenialOrError verifies short-circuiting.
func TestAuthorizeStopsAtFirstDenialOrError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	calls := 0
	later := func(context.Context, user) (bool, error) {
		calls++

		return true, nil
	}

	failing := func(context.Context, user) (bool, error) {
		return false, boom
	}

	ctx := newRequest(&user{}).Context()

	if err := gate.Authorize(ctx, failing, later); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}

	if err := gate.Authorize(ctx, isAdmin, later); !hasStatus(err, http.StatusForbidden) {
		t.Fatalf("err = %v", err)
	}

	if calls != 0 {
		t.Fatalf("later rule ran %d times", calls)
	}
}

// checkRequireStatus runs a request for u through gate.Require(isAdmin) and
// fails the test unless the response status is want.
func checkRequireStatus(t *testing.T, u *user, want int) {
	t.Helper()

	next := func(http.ResponseWriter, *http.Request) error {
		return nil
	}

	handler := gate.Require(isAdmin)(next)

	if got := router.Handler(handler).Record(newRequest(u)).StatusCode; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

// TestRequireRejectsAnonymous verifies the middleware answers 401 without a
// user.
func TestRequireRejectsAnonymous(t *testing.T) {
	t.Parallel()

	checkRequireStatus(t, nil, http.StatusUnauthorized)
}

// TestRequireDeniesUnauthorizedUser verifies the middleware answers 403 when
// the rule fails.
func TestRequireDeniesUnauthorizedUser(t *testing.T) {
	t.Parallel()

	checkRequireStatus(t, &user{}, http.StatusForbidden)
}

// TestRequireAllowsAuthorizedUser verifies the middleware calls through when
// the rule passes.
func TestRequireAllowsAuthorizedUser(t *testing.T) {
	t.Parallel()

	checkRequireStatus(t, &user{
		Admin: true,
	}, http.StatusNoContent)
}

// TestRequirePanicsWithoutRules verifies the programming-error guard.
func TestRequirePanicsWithoutRules(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	gate.Require[user]()
}

// scoped is a test user carrying granted scopes.
type scoped struct{ scopes []string }

// AuthScopes reports the granted scopes.
func (user scoped) AuthScopes() []string { return user.scopes }

// TestBindWithoutUserIsUnavailable verifies an anonymous context is hidden
// without an error and without running any rule.
func TestBindWithoutUserIsUnavailable(t *testing.T) {
	t.Parallel()

	available := gate.Bind(func(context.Context, string) (bool, error) {
		t.Fatal("rule ran without a user")

		return true, nil
	})

	if ok, err := available(t.Context()); ok || err != nil {
		t.Fatalf("available = %v, error = %v", ok, err)
	}
}

// TestBindRunsRulesInOrder verifies allow, deny and error outcomes and that a
// denial skips later rules.
func TestBindRunsRulesInOrder(t *testing.T) {
	t.Parallel()

	ctx := auth.NewContext(t.Context(), "reader")
	allow := func(context.Context, string) (bool, error) { return true, nil }
	deny := func(context.Context, string) (bool, error) { return false, nil }
	fail := errors.New("policy backend down")

	if ok, err := gate.Bind(allow, allow)(ctx); !ok || err != nil {
		t.Fatalf("allowed = %v, error = %v", ok, err)
	}

	if ok, err := gate.Bind(allow, deny, func(context.Context, string) (bool, error) {
		t.Fatal("rule ran after denial")

		return true, nil
	})(ctx); ok || err != nil {
		t.Fatalf("denied = %v, error = %v", ok, err)
	}

	if ok, err := gate.Bind(allow, func(context.Context, string) (bool, error) {
		return true, fail
	})(ctx); ok || !errors.Is(err, fail) {
		t.Fatalf("failed = %v, error = %v", ok, err)
	}
}

// TestBindSnapshotsRuleSlice verifies replacing a caller-owned entry cannot
// change the availability decision.
func TestBindSnapshotsRuleSlice(t *testing.T) {
	t.Parallel()

	rules := []func(context.Context, string) (bool, error){
		func(context.Context, string) (bool, error) { return false, nil },
	}
	available := gate.Bind(rules...)
	rules[0] = func(context.Context, string) (bool, error) { return true, nil }

	if ok, err := available(auth.NewContext(t.Context(), "reader")); ok || err != nil {
		t.Fatalf("available = %v, error = %v", ok, err)
	}
}

// TestBindWithoutRulesPanics verifies the same misuse rule as Require.
func TestBindWithoutRulesPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("Bind without rules did not panic")
		}
	}()

	gate.Bind[string]()
}

// TestHasScopesRequiresAll verifies every required scope must be granted and
// that no requirement allows any user.
func TestHasScopesRequiresAll(t *testing.T) {
	t.Parallel()

	user := scoped{[]string{"read", "write"}}

	for required, want := range map[string]bool{"read": true, "admin": false} {
		if ok, err := gate.HasScopes[scoped](required)(t.Context(), user); ok != want || err != nil {
			t.Fatalf("scope %q = %v, error = %v", required, ok, err)
		}
	}

	if ok, _ := gate.HasScopes[scoped]("read", "admin")(t.Context(), user); ok {
		t.Fatal("partial scopes allowed")
	}

	if ok, _ := gate.HasScopes[scoped]()(t.Context(), scoped{}); !ok {
		t.Fatal("empty requirement denied")
	}
}

// TestHasScopesCopiesRequired verifies later caller edits cannot weaken the rule.
func TestHasScopesCopiesRequired(t *testing.T) {
	t.Parallel()

	required := []string{"admin"}
	rule := gate.HasScopes[scoped](required...)
	required[0] = "read"

	if ok, _ := rule(t.Context(), scoped{[]string{"read"}}); ok {
		t.Fatal("rule followed caller edit")
	}
}
