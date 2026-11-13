package gate_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/gate"
	"arkoren.dev/core/event"
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
	request = request.WithContext(withUser(request.Context(), "reader"))

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
	request = request.WithContext(withUser(request.Context(), "reader"))

	for _, protected := range []router.Handler{handler, middleware(next)} {
		if err := protected(httptest.NewRecorder(), request); !errors.Is(err, gate.ErrForbidden) {
			t.Fatalf("error = %v, want original rule denial", err)
		}
	}
}

// withUser returns ctx carrying an identity whose record holds value, the way
// an authenticator attaches the application user.
func withUser(ctx context.Context, value any) context.Context {
	return auth.NewContext(ctx, auth.NewIdentity("1", func(principal *auth.IdentityBuilder) {
		principal.Record(auth.UserRecord{Subject: "1", Value: value})
	}))
}

// TestAuthorizeIdentityRuleReceivesIdentity verifies that a rule over
// auth.Identity gets the identity itself, even without a record value.
func TestAuthorizeIdentityRuleReceivesIdentity(t *testing.T) {
	t.Parallel()

	ctx := auth.NewContext(t.Context(), auth.NewIdentity("svc"))
	isService := func(_ context.Context, identity auth.Identity) (bool, error) {
		return identity.Subject() == "svc", nil
	}

	if err := gate.Authorize(ctx, isService); err != nil {
		t.Fatalf("Authorize() = %v", err)
	}
}

// TestAuthorizeTypedRuleWithoutRecordIsUnauthenticated verifies an identity with
// no record value cannot satisfy a typed rule: it fails closed.
func TestAuthorizeTypedRuleWithoutRecordIsUnauthenticated(t *testing.T) {
	t.Parallel()

	ctx := auth.NewContext(t.Context(), auth.NewIdentity("svc"))

	if err := gate.Authorize(ctx, isAdmin); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("Authorize() = %v, want ErrUnauthenticated", err)
	}
}

// TestAuthorizeRecordTypeMismatchIsUnauthenticated verifies a record value of
// another type fails closed, and that without an identity both rule kinds do.
func TestAuthorizeRecordTypeMismatchIsUnauthenticated(t *testing.T) {
	t.Parallel()

	if err := gate.Authorize(withUser(t.Context(), &user{Admin: true}), isAdmin); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pointer record: Authorize() = %v, want ErrUnauthenticated", err)
	}

	identityRule := func(context.Context, auth.Identity) (bool, error) { return true, nil }

	if err := gate.Authorize(t.Context(), identityRule); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("no identity: Authorize() = %v, want ErrUnauthenticated", err)
	}
}

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

	ctx := withUser(t.Context(), "reader")
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

	if ok, err := available(withUser(t.Context(), "reader")); ok || err != nil {
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

// withScopes returns a context carrying an identity granted scopes.
func withScopes(scopes ...string) context.Context {
	return auth.NewContext(context.Background(), auth.NewIdentity("1", func(principal *auth.IdentityBuilder) {
		principal.Scopes(scopes...)
	}))
}

// TestHasScopesRequiresAll verifies every required scope must be granted and
// that no requirement allows any identity.
func TestHasScopesRequiresAll(t *testing.T) {
	t.Parallel()

	ctx := withScopes("read", "write")

	if err := gate.Authorize(ctx, gate.HasScopes("read")); err != nil {
		t.Fatalf("read: %v", err)
	}

	if err := gate.Authorize(ctx, gate.HasScopes("admin")); !errors.Is(err, gate.ErrForbidden) {
		t.Fatalf("admin: %v", err)
	}

	if err := gate.Authorize(ctx, gate.HasScopes("read", "admin")); !errors.Is(err, gate.ErrForbidden) {
		t.Fatalf("partial scopes: %v", err)
	}

	if err := gate.Authorize(withScopes(), gate.HasScopes()); err != nil {
		t.Fatalf("empty requirement: %v", err)
	}
}

// TestHasScopesCopiesRequired verifies later caller edits cannot weaken the rule.
func TestHasScopesCopiesRequired(t *testing.T) {
	t.Parallel()

	required := []string{"admin"}
	rule := gate.HasScopes(required...)
	required[0] = "read"

	if ok, _ := rule(t.Context(), auth.NewIdentity("1", func(principal *auth.IdentityBuilder) {
		principal.Scopes("read")
	})); ok {
		t.Fatal("rule followed caller edit")
	}
}

func ExampleBind() {
	available := gate.Bind(func(_ context.Context, identity auth.Identity) (bool, error) {
		return identity.Subject() == "analyst", nil
	})

	anonymous, _ := available(context.Background())
	analyst, _ := available(auth.NewContext(context.Background(), auth.NewIdentity("analyst")))

	fmt.Println(anonymous, analyst)
	// Output: false true
}

func ExampleHasScopes() {
	ctx := auth.NewContext(context.Background(), auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Scopes("posts:read")
	}))

	fmt.Println(gate.Authorize(ctx, gate.HasScopes("posts:read")))
	fmt.Println(errors.Is(gate.Authorize(ctx, gate.HasScopes("posts:write")), gate.ErrForbidden))
	// Output:
	// <nil>
	// true
}

func ExampleAuthorize() {
	isAdmin := func(_ context.Context, user struct{ Admin bool }) (bool, error) {
		return user.Admin, nil
	}
	ctx := auth.NewContext(context.Background(), auth.NewIdentity("1", func(principal *auth.IdentityBuilder) {
		principal.Record(auth.UserRecord{Value: struct{ Admin bool }{Admin: true}})
	}))

	fmt.Println(gate.Authorize(ctx, isAdmin))
	// Output: <nil>
}

func ExampleErrForbidden() {
	ctx := auth.NewContext(context.Background(), auth.NewIdentity("42"))
	err := gate.Authorize(ctx, func(context.Context, auth.Identity) (bool, error) { return false, nil })

	fmt.Println(errors.Is(err, gate.ErrForbidden))
	// Output: true
}

func ExampleNew() {
	policy := gate.New(func(access *gate.Builder) {
		access.Events(event.New())
	})

	fmt.Println(policy != nil)
	// Output: true
}

func ExampleGate() {
	bus := event.New()
	_, _ = bus.Listen(gate.Denied, func(_ context.Context, denied gate.DeniedEvent) error {
		fmt.Println("denied rule", denied.Rule)

		return nil
	})
	policy := gate.New(func(access *gate.Builder) {
		access.Events(bus)
	})
	ctx := auth.NewContext(context.Background(), auth.NewIdentity("42"))

	_ = policy.Authorize(ctx, gate.HasScopes("admin"))
	// Output: denied rule 0
}

func ExampleGate_Authorize() {
	policy := gate.New()
	ctx := auth.NewContext(context.Background(), auth.NewIdentity("42"))

	fmt.Println(policy.Authorize(ctx, gate.HasScopes()))
	// Output: <nil>
}

func ExampleGate_Require() {
	policy := gate.New()
	r := router.New()
	r.With(policy.Require(gate.HasScopes("posts:write"))).Post("/posts", func(http.ResponseWriter, *http.Request) error { return nil })

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/posts", nil))

	fmt.Println(response.Code)
	// Output: 401
}

func ExampleBuilder() {
	policy := gate.New(func(access *gate.Builder) {
		access.Events(event.New())
	})

	fmt.Println(policy != nil)
	// Output: true
}

func ExampleBuilder_Events() {
	bus := event.New()
	policy := gate.New(func(access *gate.Builder) {
		access.Events(bus)
	})

	fmt.Println(policy != nil)
	// Output: true
}

func ExampleBuilderFunc() {
	policy := gate.BuilderFunc(func(access *gate.Builder) {
		access.Events(event.New())
	})

	fmt.Println(gate.New(policy) != nil)
	// Output: true
}

func ExampleDenied() {
	bus := event.New()
	_, _ = bus.Listen(gate.Denied, func(_ context.Context, denied gate.DeniedEvent) error {
		fmt.Println(denied.Method, denied.Rule)

		return nil
	})

	bus.Emit(context.Background(), gate.Denied, gate.DeniedEvent{Method: http.MethodDelete, Rule: 1})
	// Output: DELETE 1
}

func ExampleDeniedEvent() {
	fmt.Println(gate.DeniedEvent{Method: http.MethodGet, Rule: 2})
	// Output: {GET 2}
}

func ExampleRequire() {
	r := router.New()
	r.With(gate.Require(gate.HasScopes("posts:write"))).Post("/posts", func(http.ResponseWriter, *http.Request) error { return nil })

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/posts", nil))

	fmt.Println(response.Code)
	// Output: 401
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

	return r.WithContext(withUser(r.Context(), *u))
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
