package auth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/foundation/problem"
)

// TestMiddlewareSnapshotsAuthenticatorSlice verifies that replacing an expanded
// slice entry cannot bypass authentication, whether a handler was wrapped before
// or after the replacement. The snapshot must belong to Middleware itself.
func TestMiddlewareSnapshotsAuthenticatorSlice(t *testing.T) {
	t.Parallel()

	authenticators := []auth.Authenticator{
		func(*http.Request) (auth.Identity, error) {
			return auth.Identity{}, auth.ErrUnauthenticated
		},
	}

	next := func(http.ResponseWriter, *http.Request) error {
		return nil
	}

	middleware := auth.Middleware(authenticators...)
	handler := middleware(next)
	authenticators[0] = func(*http.Request) (auth.Identity, error) {
		return auth.NewIdentity("unexpected-user"), nil
	}

	for _, protected := range []router.Handler{handler, middleware(next)} {
		err := protected(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

		if !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("error = %v, want original authentication denial", err)
		}
	}
}

// user is the test user type.
type user struct {
	// Name identifies the authenticated test user recorded by the handler.
	Name string
}

// ok is a handler that records the user it sees in seen.
func ok(seen *[]string) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		name := "anonymous"

		if u, found := auth.UserFromContext[user](r.Context()); found {
			name = u.Name
		}

		*seen = append(*seen, name)

		return nil
	}
}

// returns builds an authenticator returning an identity of the user name and err.
func returns(name string, err error) auth.Authenticator {
	return func(*http.Request) (auth.Identity, error) {
		return auth.NewIdentity(name, func(principal *auth.IdentityBuilder) {
			principal.Record(auth.UserRecord{Subject: name, Value: user{name}})
		}), err
	}
}

// status runs handler and returns the response.
func status(handler router.Handler, r *http.Request) *http.Response {
	return handler.Record(r)
}

// TestMiddlewareUsesFirstSuccessInOrder verifies chain order.
func TestMiddlewareUsesFirstSuccessInOrder(t *testing.T) {
	t.Parallel()

	var seen []string
	handler := auth.Middleware(returns("", auth.ErrNoCredentials), returns("b", nil), returns("c", nil))(ok(&seen))
	status(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if len(seen) != 1 || seen[0] != "b" {
		t.Fatalf("seen = %v, want [b]", seen)
	}
}

// TestMiddlewareContinuesAnonymously verifies passthrough without credentials.
func TestMiddlewareContinuesAnonymously(t *testing.T) {
	t.Parallel()

	var seen []string
	handler := auth.Middleware(returns("", auth.ErrNoCredentials))(ok(&seen))
	status(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if len(seen) != 1 || seen[0] != "anonymous" {
		t.Fatalf("seen = %v, want [anonymous]", seen)
	}
}

// TestMiddlewareReturnsOtherErrors verifies errors stop the chain.
func TestMiddlewareReturnsOtherErrors(t *testing.T) {
	t.Parallel()

	var seen []string
	handler := auth.Middleware(returns("", errors.New("boom")), returns("b", nil))(ok(&seen))
	response := status(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if len(seen) != 0 || response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("seen = %v, status = %d", seen, response.StatusCode)
	}
}

// TestRequireRejectsAnonymousWithChallenge verifies the default 401.
func TestRequireRejectsAnonymousWithChallenge(t *testing.T) {
	t.Parallel()

	var seen []string
	response := status(auth.Require()(ok(&seen)), httptest.NewRequest(http.MethodGet, "/", nil))

	if response.StatusCode != http.StatusUnauthorized || response.Header.Get("WWW-Authenticate") != "Bearer" || len(seen) != 0 {
		t.Fatalf("status = %d, header = %q", response.StatusCode, response.Header.Get("WWW-Authenticate"))
	}
}

// TestRequireAllowsUser verifies authenticated requests pass.
func TestRequireAllowsUser(t *testing.T) {
	t.Parallel()

	var seen []string
	handler := auth.Middleware(returns("a", nil))(auth.Require()(ok(&seen)))

	if response := status(handler, httptest.NewRequest(http.MethodGet, "/", nil)); response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

// TestUserFromContextFailsClosedOnTypeMismatch verifies that a record value of
// another type, or none at all, is reported as absent.
func TestUserFromContextFailsClosedOnTypeMismatch(t *testing.T) {
	t.Parallel()

	ctx := auth.NewContext(t.Context(), auth.NewIdentity("a", func(principal *auth.IdentityBuilder) {
		principal.Record(auth.UserRecord{Value: user{"a"}})
	}))

	if _, ok := auth.UserFromContext[*user](ctx); ok {
		t.Fatal("*user found for a user value")
	}

	if got, ok := auth.UserFromContext[user](ctx); !ok || got.Name != "a" {
		t.Fatalf("user = %v, %v", got, ok)
	}

	if _, ok := auth.UserFromContext[user](auth.NewContext(t.Context(), auth.NewIdentity("a"))); ok {
		t.Fatal("user found for an identity without a record value")
	}

	if _, ok := auth.UserFromContext[user](t.Context()); ok {
		t.Fatal("user found without an identity")
	}
}

// TestRequireAllowsIdentityWithoutUser verifies Require only needs an identity,
// not an application user.
func TestRequireAllowsIdentityWithoutUser(t *testing.T) {
	t.Parallel()

	authenticate := func(*http.Request) (auth.Identity, error) {
		return auth.NewIdentity("service"), nil
	}

	var seen []string
	handler := auth.Middleware(authenticate)(auth.Require()(ok(&seen)))

	if response := status(handler, httptest.NewRequest(http.MethodGet, "/", nil)); response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

// TestMiddlewareFallsBackAcrossAuthenticators verifies a chain of two
// authenticators of different origins: the first has no credentials, so the
// second one's identity is stored.
func TestMiddlewareFallsBackAcrossAuthenticators(t *testing.T) {
	t.Parallel()

	session := func(*http.Request) (auth.Identity, error) {
		return auth.Identity{}, auth.ErrNoCredentials
	}
	key := func(*http.Request) (auth.Identity, error) {
		return auth.NewIdentity("key-1", func(principal *auth.IdentityBuilder) {
			principal.Scopes("read")
		}), nil
	}

	var got auth.Identity
	handler := auth.Middleware(session, key)(func(_ http.ResponseWriter, r *http.Request) error {
		got, _ = auth.FromContext(r.Context())

		return nil
	})
	handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if got.Subject() != "key-1" || len(got.Scopes()) != 1 {
		t.Fatalf("identity = %q %v", got.Subject(), got.Scopes())
	}
}

// TestRequireUsesCustomErrorResponse verifies MiddlewareBuilder.ErrorResponse.
func TestRequireUsesCustomErrorResponse(t *testing.T) {
	t.Parallel()

	var seen []string
	handler := auth.Require(func(authenticator *auth.MiddlewareBuilder) {
		authenticator.ErrorResponse(problem.Unauthorized("Basic"))
	})(ok(&seen))

	response := status(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Header.Get("WWW-Authenticate") != "Basic" {
		t.Fatalf("header = %q", response.Header.Get("WWW-Authenticate"))
	}
}

func ExampleAuthenticator() {
	var authenticate auth.Authenticator = func(r *http.Request) (auth.Identity, error) {
		if r.Header.Get("X-Demo-Key") != "secret" {
			return auth.Identity{}, auth.ErrNoCredentials
		}

		return auth.NewIdentity("service-1"), nil
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err := authenticate(r)

	fmt.Println(errors.Is(err, auth.ErrNoCredentials))
	// Output: true
}

func ExampleNewContext() {
	ctx := auth.NewContext(context.Background(), auth.NewIdentity("42"))

	identity, ok := auth.FromContext(ctx)

	fmt.Println(identity.Subject(), ok)
	// Output: 42 true
}

func ExampleFromContext() {
	_, ok := auth.FromContext(context.Background())

	fmt.Println(ok)
	// Output: false
}

func ExampleUserFromContext() {
	identity := auth.NewIdentity("42", func(principal *auth.IdentityBuilder) {
		principal.Record(auth.UserRecord{Subject: "42", Value: user{Name: "ann"}})
	})
	ctx := auth.NewContext(context.Background(), identity)

	found, ok := auth.UserFromContext[user](ctx)
	_, wrong := auth.UserFromContext[*user](ctx)

	fmt.Println(found.Name, ok, wrong)
	// Output: ann true false
}

func ExampleMiddleware() {
	apiKey := func(r *http.Request) (auth.Identity, error) {
		if r.Header.Get("X-Demo-Key") != "secret" {
			return auth.Identity{}, auth.ErrNoCredentials
		}

		return auth.NewIdentity("service-1"), nil
	}

	r := router.New()
	r.Use(auth.Middleware(apiKey))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) error {
		identity, _ := auth.FromContext(r.Context())
		_, err := fmt.Fprintf(w, "subject=%q", identity.Subject())

		return err
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Demo-Key", "secret")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)

	fmt.Println(response.Body.String())
	// Output: subject="service-1"
}

func ExampleRequire() {
	r := router.New()
	r.With(auth.Require()).Get("/", func(http.ResponseWriter, *http.Request) error { return nil })

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	fmt.Println(response.Code, response.Header().Get("WWW-Authenticate"))
	// Output: 401 Bearer
}

func ExampleBearer() {
	authenticate := auth.Bearer(func(_ context.Context, token string) (auth.Identity, error) {
		if token != "good" {
			return auth.Identity{}, errors.New("unknown token")
		}

		return auth.NewIdentity("service-1"), nil
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer good")
	identity, err := authenticate(request)

	fmt.Println(identity.Subject(), err)
	// Output: service-1 <nil>
}

func ExampleMiddlewareBuilder() {
	middleware := auth.Require(func(authenticator *auth.MiddlewareBuilder) {
		authenticator.ErrorResponse(problem.Unauthorized("Basic"))
	})

	fmt.Println(middleware != nil)
	// Output: true
}

func ExampleMiddlewareBuilder_ErrorResponse() {
	r := router.New()
	r.With(auth.Require(func(authenticator *auth.MiddlewareBuilder) {
		authenticator.ErrorResponse(problem.Unauthorized("Basic"))
	})).Get("/", func(http.ResponseWriter, *http.Request) error { return nil })

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	fmt.Println(response.Header().Get("WWW-Authenticate"))
	// Output: Basic
}

func ExampleMiddlewareBuilder_Events() {
	bus := event.New()
	_, _ = bus.Listen(auth.Rejected, func(_ context.Context, rejected auth.RejectedEvent) error {
		fmt.Println("rejected", rejected.Method)

		return nil
	})

	r := router.New()
	r.With(auth.Require(func(authenticator *auth.MiddlewareBuilder) {
		authenticator.Events(bus)
	})).Get("/", func(http.ResponseWriter, *http.Request) error { return nil })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	// Output: rejected GET
}

func ExampleMiddlewareBuilderFunc() {
	policy := auth.MiddlewareBuilderFunc(func(authenticator *auth.MiddlewareBuilder) {
		authenticator.ErrorResponse(problem.Forbidden())
	})

	fmt.Println(auth.Require(policy) != nil)
	// Output: true
}

func ExampleErrNoCredentials() {
	authenticate := func(*http.Request) (auth.Identity, error) {
		return auth.Identity{}, auth.ErrNoCredentials
	}

	_, err := authenticate(httptest.NewRequest(http.MethodGet, "/", nil))

	fmt.Println(errors.Is(err, auth.ErrNoCredentials))
	// Output: true
}

func ExampleErrUnauthenticated() {
	var err error = auth.ErrUnauthenticated

	fmt.Println(errors.Is(err, auth.ErrUnauthenticated))
	// Output: true
}

func ExampleRejected() {
	bus := event.New()
	_, _ = bus.Listen(auth.Rejected, func(_ context.Context, rejected auth.RejectedEvent) error {
		fmt.Println(rejected.Method)

		return nil
	})

	bus.Emit(context.Background(), auth.Rejected, auth.RejectedEvent{Method: http.MethodPost})
	// Output: POST
}

func ExampleRejectedEvent() {
	fmt.Println(auth.RejectedEvent{Method: http.MethodGet}.Method)
	// Output: GET
}

// sessionRequest returns a request carrying sess.
func sessionRequest(sess *session.Session) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	return r.WithContext(session.NewContext(r.Context(), sess))
}

// bearerRequest returns a request with the given Authorization values.
func bearerRequest(values ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header["Authorization"] = values

	return r
}

// bearerVerify is the token verifier used by the bearer header tests: "bad"
// fails verification, "gone" reports no credentials, and any other token
// authenticates a user of that name.
func bearerVerify(_ context.Context, token string) (auth.Identity, error) {
	switch token {
	case "bad":
		return auth.Identity{}, errors.New("bad token")
	case "gone":
		return auth.Identity{}, auth.ErrNoCredentials
	}

	return auth.NewIdentity(token, func(principal *auth.IdentityBuilder) {
		principal.Record(auth.UserRecord{Subject: token, Value: user{token}})
	}), nil
}

// checkBearerUser sends a request with the given Authorization values through
// auth.Bearer and fails the test unless the handler saw exactly wantUser.
func checkBearerUser(t *testing.T, wantUser string, values ...string) {
	t.Helper()

	var seen []string
	handler := auth.Middleware(auth.Bearer(bearerVerify))(ok(&seen))
	status(handler, bearerRequest(values...))

	if len(seen) != 1 || seen[0] != wantUser {
		t.Fatalf("seen = %v, want %s", seen, wantUser)
	}
}

// checkBearerRejected sends a request with the given Authorization values
// through auth.Bearer and fails the test unless it is answered with 401 and an
// invalid_token challenge.
func checkBearerRejected(t *testing.T, values ...string) {
	t.Helper()

	var seen []string
	handler := auth.Middleware(auth.Bearer(bearerVerify))(ok(&seen))
	response := status(handler, bearerRequest(values...))

	if response.StatusCode != http.StatusUnauthorized || response.Header.Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
		t.Fatalf("status = %d, header = %q", response.StatusCode, response.Header.Get("WWW-Authenticate"))
	}
}

// TestBearerMissingHeaderIsAnonymous verifies a request without credentials.
func TestBearerMissingHeaderIsAnonymous(t *testing.T) {
	t.Parallel()

	checkBearerUser(t, "anonymous")
}

// TestBearerValidToken verifies a well-formed bearer header.
func TestBearerValidToken(t *testing.T) {
	t.Parallel()

	checkBearerUser(t, "abc", "Bearer abc")
}

// TestBearerSchemeIsCaseInsensitive verifies the scheme match ignores case.
func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	checkBearerUser(t, "abc", "bEaReR abc")
}

// TestBearerOtherSchemeRejected verifies a non-bearer scheme is rejected.
func TestBearerOtherSchemeRejected(t *testing.T) {
	t.Parallel()

	checkBearerRejected(t, "Basic abc")
}

// TestBearerNoTokenRejected verifies a scheme without a token is rejected.
func TestBearerNoTokenRejected(t *testing.T) {
	t.Parallel()

	checkBearerRejected(t, "Bearer")
}

// TestBearerEmptyTokenRejected verifies an empty token is rejected.
func TestBearerEmptyTokenRejected(t *testing.T) {
	t.Parallel()

	checkBearerRejected(t, "Bearer ")
}

// TestBearerTokenWithSpacesRejected verifies a token containing a space is
// rejected.
func TestBearerTokenWithSpacesRejected(t *testing.T) {
	t.Parallel()

	checkBearerRejected(t, "Bearer a b")
}

// TestBearerTwoHeadersRejected verifies repeated Authorization headers are
// rejected.
func TestBearerTwoHeadersRejected(t *testing.T) {
	t.Parallel()

	checkBearerRejected(t, "Bearer a", "Bearer b")
}

// TestBearerVerifyErrorRejected verifies a verifier failure is rejected.
func TestBearerVerifyErrorRejected(t *testing.T) {
	t.Parallel()

	checkBearerRejected(t, "Bearer bad")
}

// TestBearerVerifyNoCredentialsIsAnonymous verifies a verifier reporting no
// credentials yields an anonymous request.
func TestBearerVerifyNoCredentialsIsAnonymous(t *testing.T) {
	t.Parallel()

	checkBearerUser(t, "anonymous", "Bearer gone")
}

// TestErrUnauthenticatedMatchesDerivedProblems verifies errors.Is matching.
func TestErrUnauthenticatedMatchesDerivedProblems(t *testing.T) {
	t.Parallel()

	err := auth.ErrUnauthenticated.Derive(func(details *problem.Builder) {
		details.Error(errors.New("boom"))
		details.Extension("k", "v")
	})

	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}
}
