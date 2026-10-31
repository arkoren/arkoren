package authtest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/auth"
)

// Option changes one conformance suite setting.
type Option[U any] func(*suite[U])

// WithInvalid supplies a factory for a request carrying credentials the
// authenticator rejects, such as an expired or forged token. It enables the
// check that rejected credentials yield [auth.ErrUnauthenticated]. Leave it
// out for authenticators without a notion of invalid credentials, such as
// [auth.SessionIdentity.Authenticator].
func WithInvalid[U any](newRequest func(t *testing.T) *http.Request) Option[U] {
	return func(s *suite[U]) {
		s.newInvalid = newRequest
	}
}

// WithUser supplies a check run on the user resolved from the valid request,
// for example comparing its identifier.
func WithUser[U any](check func(t *testing.T, user U)) Option[U] {
	return func(s *suite[U]) {
		s.checkUser = check
	}
}

// suite holds the settings and request factories of one [Run].
type suite[U any] struct {
	// authenticate is the authenticator under test.
	authenticate auth.Authenticator[U]

	// newAnonymous creates a request without credentials.
	newAnonymous func(t *testing.T) *http.Request

	// newValid creates a request with valid credentials.
	newValid func(t *testing.T) *http.Request

	// newInvalid optionally creates a request with rejected credentials.
	newInvalid func(t *testing.T) *http.Request

	// checkUser optionally checks the user resolved from a valid request.
	checkUser func(t *testing.T, user U)
}

// Run checks that authenticate follows the [auth.Authenticator] contract:
// a request without credentials yields [auth.ErrNoCredentials] and passes
// [auth.Middleware] anonymously, a request with valid credentials yields a
// user that the middleware stores in the context, and, with [WithInvalid],
// rejected credentials yield [auth.ErrUnauthenticated] and stop the request.
//
// newAnonymous and newValid are called once per subtest; each request must
// carry whatever context the authenticator reads, such as a session.
//
// # Example
//
//	func TestBearer(t *testing.T) {
//		authenticate := auth.Bearer(verify)
//		authtest.Run(t, authenticate, anonymousRequest, validRequest,
//			authtest.WithInvalid[User](forgedRequest))
//	}
func Run[U any](t *testing.T, authenticate auth.Authenticator[U], newAnonymous, newValid func(t *testing.T) *http.Request, opts ...Option[U]) {
	s := &suite[U]{
		authenticate: authenticate,
		newAnonymous: newAnonymous,
		newValid:     newValid,
	}

	for _, opt := range opts {
		opt(s)
	}

	t.Run("AnonymousHasNoCredentials", s.testAnonymousHasNoCredentials)
	t.Run("AnonymousPassesMiddleware", s.testAnonymousPassesMiddleware)
	t.Run("ValidResolvesUser", s.testValidResolvesUser)
	t.Run("ValidStoresUser", s.testValidStoresUser)
	t.Run("InvalidIsUnauthenticated", s.testInvalidIsUnauthenticated)
	t.Run("InvalidStopsMiddleware", s.testInvalidStopsMiddleware)
}

// serve runs request through [auth.Middleware] with the authenticator under
// test. It reports whether the next handler ran, whether that handler saw a
// user of type U, and the middleware's error.
func (suite *suite[U]) serve(request *http.Request) (called, hasUser bool, err error) {
	handler := auth.Middleware(suite.authenticate)(func(_ http.ResponseWriter, r *http.Request) error {
		called = true
		_, hasUser = auth.FromContext[U](r.Context())

		return nil
	})

	err = handler(httptest.NewRecorder(), request)

	return called, hasUser, err
}

// testAnonymousHasNoCredentials verifies a request without credentials yields
// auth.ErrNoCredentials, so the next authenticator may try.
func (suite *suite[U]) testAnonymousHasNoCredentials(t *testing.T) {
	if _, err := suite.authenticate(suite.newAnonymous(t)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("authenticate() error = %v, want ErrNoCredentials", err)
	}
}

// testAnonymousPassesMiddleware verifies the middleware lets a request
// without credentials through without a user.
func (suite *suite[U]) testAnonymousPassesMiddleware(t *testing.T) {
	called, hasUser, err := suite.serve(suite.newAnonymous(t))

	if err != nil || !called || hasUser {
		t.Fatalf("Middleware: error = %v, called = %v, user = %v; want nil, true, false", err, called, hasUser)
	}
}

// testValidResolvesUser verifies a request with valid credentials yields a
// user and no error.
func (suite *suite[U]) testValidResolvesUser(t *testing.T) {
	user, err := suite.authenticate(suite.newValid(t))

	if err != nil {
		t.Fatalf("authenticate() error = %v", err)
	}

	if suite.checkUser != nil {
		suite.checkUser(t, user)
	}
}

// testValidStoresUser verifies the middleware stores the user of a valid
// request in the context.
func (suite *suite[U]) testValidStoresUser(t *testing.T) {
	called, hasUser, err := suite.serve(suite.newValid(t))

	if err != nil || !called || !hasUser {
		t.Fatalf("Middleware: error = %v, called = %v, user = %v; want nil, true, true", err, called, hasUser)
	}
}

// testInvalidIsUnauthenticated verifies rejected credentials yield
// auth.ErrUnauthenticated, which renders as 401.
func (suite *suite[U]) testInvalidIsUnauthenticated(t *testing.T) {
	if suite.newInvalid == nil {
		t.Skip("no invalid request; see WithInvalid")
	}

	if _, err := suite.authenticate(suite.newInvalid(t)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("authenticate() error = %v, want ErrUnauthenticated", err)
	}
}

// testInvalidStopsMiddleware verifies the middleware returns the error of
// rejected credentials and does not call the next handler.
func (suite *suite[U]) testInvalidStopsMiddleware(t *testing.T) {
	if suite.newInvalid == nil {
		t.Skip("no invalid request; see WithInvalid")
	}

	called, _, err := suite.serve(suite.newInvalid(t))

	if err == nil || called {
		t.Fatalf("Middleware: error = %v, called = %v; want an error and false", err, called)
	}
}
