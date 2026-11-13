package auth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// exampleSession returns a fresh session for the examples.
func exampleSession() *session.Session {
	return session.New(time.Now().Add(time.Hour), map[string]any{})
}

func ExampleNewSessionIdentity() {
	identity := auth.NewSessionIdentity(func(_ context.Context, id int) (user, error) {
		return user{Name: "ann"}, nil
	})

	fmt.Println(identity.Err())
	// Output: <nil>
}

func ExampleSessionIdentity() {
	identity := auth.NewSessionIdentity(func(_ context.Context, id string) (user, error) {
		return user{Name: id}, nil
	})
	current := exampleSession()
	_ = identity.Login(current, "ann")

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(session.NewContext(request.Context(), current))
	resolved, err := identity.Authenticator()(request)

	fmt.Println(resolved.Subject(), err)
	// Output: ann <nil>
}

func ExampleSessionIdentity_Err() {
	var zero auth.SessionIdentity[int, user]

	fmt.Println(errors.Is(zero.Err(), auth.ErrInvalidSessionIdentity))
	// Output: true
}

func ExampleSessionIdentity_Session() {
	identity := auth.NewSessionIdentity(func(_ context.Context, id int) (user, error) {
		return user{}, nil
	})

	_, err := identity.Session(context.Background())

	fmt.Println(errors.Is(err, session.ErrNoSession))
	// Output: true
}

func ExampleSessionIdentity_Login() {
	identity := auth.NewSessionIdentity(func(_ context.Context, id int) (user, error) {
		return user{}, nil
	})
	current := exampleSession()
	before := current.ID()

	err := identity.Login(current, 7)

	fmt.Println(err, current.ID() != before)
	// Output: <nil> true
}

func ExampleSessionIdentity_Logout() {
	identity := auth.NewSessionIdentity(func(_ context.Context, id int) (user, error) {
		return user{}, nil
	})
	current := exampleSession()
	_ = identity.Login(current, 7)

	err := identity.Logout(current)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(session.NewContext(request.Context(), current))
	_, authErr := identity.Authenticator()(request)

	fmt.Println(err, errors.Is(authErr, auth.ErrNoCredentials))
	// Output: <nil> true
}

func ExampleSessionIdentity_Authenticator() {
	identity := auth.NewSessionIdentity(func(_ context.Context, id int) (user, error) {
		return user{Name: "ann"}, nil
	})

	_, err := identity.Authenticator()(httptest.NewRequest(http.MethodGet, "/", nil))

	fmt.Println(errors.Is(err, auth.ErrNoCredentials))
	// Output: true
}

func ExampleErrInvalidSessionIdentity() {
	identity := auth.NewSessionIdentity(func(context.Context, struct{ N int }) (user, error) {
		return user{}, nil
	})

	fmt.Println(errors.Is(identity.Err(), auth.ErrInvalidSessionIdentity))
	// Output: true
}

func ExampleSessionBuilder() {
	handle := session.NewHandle()
	identity := auth.NewSessionIdentity(func(_ context.Context, id int) (user, error) {
		return user{}, nil
	}, func(binding *auth.SessionBuilder) {
		binding.Handle(handle)
	})

	fmt.Println(identity.Err())
	// Output: <nil>
}

func ExampleSessionBuilder_Handle() {
	handle := session.NewHandle()
	identity := auth.NewSessionIdentity(func(_ context.Context, id int) (user, error) {
		return user{}, nil
	}, func(binding *auth.SessionBuilder) {
		binding.Handle(handle)
	})
	current := exampleSession()

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request = request.WithContext(session.NewContextFor(request.Context(), handle, current))
	found, err := identity.Session(request.Context())

	fmt.Println(found == current, err)
	// Output: true <nil>
}

func ExampleSessionBuilderFunc() {
	policy := auth.SessionBuilderFunc(func(binding *auth.SessionBuilder) {
		binding.Handle(session.NewHandle())
	})
	identity := auth.NewSessionIdentity(func(_ context.Context, id int) (user, error) {
		return user{}, nil
	}, policy)

	fmt.Println(identity.Err())
	// Output: <nil>
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

	if value, _ := got.Record().Value.(user); err != nil || value.Name != "u" || got.Subject() != "7" || got.Record().Subject != "7" {
		t.Fatalf("got %q %v, %v", got.Subject(), got.Record().Value, err)
	}
}

// TestSessionBridgeRoundTripsInt64AndStringIDs verifies the bridge formats each
// supported identifier kind as the identity subject and keeps the loaded user
// reachable through UserFromContext.
func TestSessionBridgeRoundTripsInt64AndStringIDs(t *testing.T) {
	t.Parallel()

	numbers := auth.NewSessionIdentity(func(_ context.Context, id int64) (user, error) {
		return user{"n"}, nil
	})
	words := auth.NewSessionIdentity(func(_ context.Context, id string) (user, error) {
		return user{"w"}, nil
	})
	sess := session.New(time.Now().Add(time.Hour), map[string]any{})

	if err := numbers.Login(sess, int64(1<<40)); err != nil {
		t.Fatal(err)
	}

	got, err := numbers.Authenticator()(sessionRequest(sess))

	if err != nil || got.Subject() != "1099511627776" {
		t.Fatalf("int64 subject = %q, %v", got.Subject(), err)
	}

	if value, ok := auth.UserFromContext[user](auth.NewContext(t.Context(), got)); !ok || value.Name != "n" {
		t.Fatalf("int64 user = %v, %v", value, ok)
	}

	if err := words.Login(sess, "ann"); err != nil {
		t.Fatal(err)
	}

	got, err = words.Authenticator()(sessionRequest(sess))

	if err != nil || got.Subject() != "ann" {
		t.Fatalf("string subject = %q, %v", got.Subject(), err)
	}
}

// TestSessionBridgeRejectsUnsupportedIDType verifies a K that cannot be a
// subject is a configuration error, not a credential fallback.
func TestSessionBridgeRejectsUnsupportedIDType(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(func(context.Context, struct{ N int }) (user, error) {
		return user{}, nil
	})

	if err := identity.Err(); !errors.Is(err, auth.ErrInvalidSessionIdentity) || !errors.Is(err, auth.ErrInvalidSubject) {
		t.Fatalf("Err() = %v", err)
	}

	if _, err := identity.Authenticator()(sessionRequest(session.New(time.Now().Add(time.Hour), map[string]any{}))); errors.Is(err, auth.ErrNoCredentials) || err == nil {
		t.Fatalf("authenticator error = %v, want a configuration error", err)
	}
}

// TestSessionBridgeFailsClosedOnUnformattableID verifies a stored empty string
// is not a valid subject and does not fall back to anonymous.
func TestSessionBridgeFailsClosedOnUnformattableID(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(func(context.Context, string) (user, error) {
		return user{"x"}, nil
	})
	sess := session.New(time.Now().Add(time.Hour), map[string]any{})

	if err := identity.Login(sess, ""); err != nil {
		t.Fatal(err)
	}

	if _, err := identity.Authenticator()(sessionRequest(sess)); !errors.Is(err, auth.ErrInvalidSubject) || errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("error = %v, want ErrInvalidSubject", err)
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
