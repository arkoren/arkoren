package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/foundation/problem"
)

// TestMiddlewareSnapshotsAuthenticatorSlice verifies that replacing an expanded
// slice entry cannot bypass authentication, whether a handler was wrapped before
// or after the replacement. The snapshot must belong to Middleware itself.
func TestMiddlewareSnapshotsAuthenticatorSlice(t *testing.T) {
	t.Parallel()

	authenticators := []auth.Authenticator[string]{
		func(*http.Request) (string, error) {
			return "", auth.ErrUnauthenticated
		},
	}

	next := func(http.ResponseWriter, *http.Request) error {
		return nil
	}

	middleware := auth.Middleware(authenticators...)
	handler := middleware(next)
	authenticators[0] = func(*http.Request) (string, error) {
		return "unexpected-user", nil
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

		if u, found := auth.FromContext[user](r.Context()); found {
			name = u.Name
		}

		*seen = append(*seen, name)

		return nil
	}
}

// returns builds an authenticator returning name and err.
func returns(name string, err error) auth.Authenticator[user] {
	return func(*http.Request) (user, error) {
		return user{
			name,
		}, err
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
	response := status(auth.Require[user]()(ok(&seen)), httptest.NewRequest(http.MethodGet, "/", nil))

	if response.StatusCode != http.StatusUnauthorized || response.Header.Get("WWW-Authenticate") != "Bearer" || len(seen) != 0 {
		t.Fatalf("status = %d, header = %q", response.StatusCode, response.Header.Get("WWW-Authenticate"))
	}
}

// TestRequireAllowsUser verifies authenticated requests pass.
func TestRequireAllowsUser(t *testing.T) {
	t.Parallel()

	var seen []string
	handler := auth.Middleware(returns("a", nil))(auth.Require[user]()(ok(&seen)))

	if response := status(handler, httptest.NewRequest(http.MethodGet, "/", nil)); response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

// TestRequireFailsClosedOnTypeMismatch verifies a different user type is absent.
func TestRequireFailsClosedOnTypeMismatch(t *testing.T) {
	t.Parallel()

	var seen []string
	handler := auth.Middleware(returns("a", nil))(auth.Require[*user]()(ok(&seen)))

	if response := status(handler, httptest.NewRequest(http.MethodGet, "/", nil)); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

// TestRequireUsesCustomErrorResponse verifies Builder.ErrorResponse.
func TestRequireUsesCustomErrorResponse(t *testing.T) {
	t.Parallel()

	var seen []string
	handler := auth.Require[user](func(authenticator *auth.Builder) {
		authenticator.ErrorResponse(problem.Unauthorized("Basic"))
	})(ok(&seen))

	response := status(handler, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Header.Get("WWW-Authenticate") != "Basic" {
		t.Fatalf("header = %q", response.Header.Get("WWW-Authenticate"))
	}
}

// sessionRequest returns a request carrying sess.
func sessionRequest(sess *session.Session) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	return r.WithContext(session.NewContext(r.Context(), sess))
}

// loadByID loads a user, treating id 0 as deleted and -1 as failing.
func loadByID(_ context.Context, id int) (user, error) {
	switch id {
	case 0:
		return user{}, auth.ErrNoCredentials
	case -1:
		return user{}, errors.New("db down")
	}

	return user{
		"u",
	}, nil
}

// TestSessionLoadsLoggedInUser verifies Login then Session resolves the user.
func TestSessionLoadsLoggedInUser(t *testing.T) {
	t.Parallel()

	sess := session.New(time.Now().Add(time.Hour), map[string]any{})
	before := sess.ID()
	identity := auth.NewSessionIdentity(loadByID)

	if err := identity.Login(sess, 7); err != nil {
		t.Fatal(err)
	}

	if sess.ID() == before {
		t.Fatal("session id not regenerated")
	}

	got, err := identity.Authenticator()(sessionRequest(sess))

	if err != nil || got.Name != "u" {
		t.Fatalf("got %v, %v", got, err)
	}
}

// TestSessionWithoutSessionOrKeyHasNoCredentials verifies missing state.
func TestSessionWithoutSessionOrKeyHasNoCredentials(t *testing.T) {
	t.Parallel()

	authenticate := auth.NewSessionIdentity(loadByID).Authenticator()

	if _, err := authenticate(httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("no session: %v", err)
	}

	if _, err := authenticate(sessionRequest(session.New(time.Now().Add(time.Hour), map[string]any{}))); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("no key: %v", err)
	}
}

// TestSessionPassesLoadResults verifies deleted users and load errors.
func TestSessionPassesLoadResults(t *testing.T) {
	t.Parallel()

	deleted := session.New(time.Now().Add(time.Hour), map[string]any{})
	identity := auth.NewSessionIdentity(loadByID)

	if err := identity.Login(deleted, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := identity.Authenticator()(sessionRequest(deleted)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("id 0: %v", err)
	}

	sess := session.New(time.Now().Add(time.Hour), map[string]any{})

	if err := identity.Login(sess, -1); err != nil {
		t.Fatal(err)
	}

	if _, err := identity.Authenticator()(sessionRequest(sess)); err == nil || errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("load error lost: %v", err)
	}
}

// TestSessionHonoursHandle verifies the definition selects its session bridge.
func TestSessionHonoursHandle(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	identity := auth.NewSessionIdentity(loadByID, func(authenticator *auth.SessionBuilder) {
		authenticator.Handle(handle)
	})

	sess := session.New(time.Now().Add(time.Hour), map[string]any{})

	if err := identity.Login(sess, 7); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(session.NewContextFor(r.Context(), handle, sess))

	if _, err := identity.Authenticator()(r); err != nil {
		t.Fatal(err)
	}
}

// TestLogoutRemovesUserAndRegenerates verifies Logout.
func TestLogoutRemovesUserAndRegenerates(t *testing.T) {
	t.Parallel()

	sess := session.New(time.Now().Add(time.Hour), map[string]any{})
	identity := auth.NewSessionIdentity(loadByID)

	if err := identity.Login(sess, 7); err != nil {
		t.Fatal(err)
	}

	before := sess.ID()

	if err := identity.Logout(sess); err != nil {
		t.Fatal(err)
	}

	if sess.ID() == before {
		t.Fatal("session id not regenerated")
	}

	if _, err := identity.Authenticator()(sessionRequest(sess)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("err = %v", err)
	}
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
func bearerVerify(_ context.Context, token string) (user, error) {
	switch token {
	case "bad":
		return user{}, errors.New("bad token")
	case "gone":
		return user{}, auth.ErrNoCredentials
	}

	return user{
		token,
	}, nil
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
