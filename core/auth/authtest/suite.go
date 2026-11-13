package authtest

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
)

// defaultTimeout bounds the context of each subtest unless [WithTimeout]
// overrides it.
const defaultTimeout = 30 * time.Second

const (
	// foundSubject names the enabled fixture user.
	foundSubject = "1"

	// disabledSubject names the disabled fixture user.
	disabledSubject = "2"

	// missingSubject is a valid subject no fixture defines.
	missingSubject = "3"
)

// User is a fixture the suite asks a driver under test to contain.
//
// Example:
//
//	authtest.User{Subject: "1", Version: "v1", Claims: map[string]any{"role": "admin"}}
type User struct {
	// Subject identifies the user. Fixture subjects are decimal digits.
	Subject string

	// Version must come back as [auth.UserRecord.Version].
	Version string

	// Claims must come back as [auth.UserRecord.Claims].
	Claims map[string]any

	// Disabled users must make Find return [auth.ErrUserDisabled].
	Disabled bool
}

// settings holds the suite configuration built from [Option] values. Each
// suite reads only the fields that apply to it.
type settings struct {
	// timeout bounds the context handed to each [RunUserDriver] subtest.
	timeout time.Duration

	// newInvalid optionally creates a request with rejected credentials for [Run].
	newInvalid func(t *testing.T) *http.Request

	// checkIdentity optionally checks the identity [Run] resolves from a valid request.
	checkIdentity func(t *testing.T, identity auth.Identity)
}

// Option configures [Run] or [RunUserDriver]; create one with [WithTimeout],
// [WithInvalid] or [WithIdentity]. A suite ignores the options it does not
// use: [WithTimeout] only affects [RunUserDriver], the other two only [Run].
//
// Example:
//
//	option := authtest.WithTimeout(time.Minute)
//	authtest.RunUserDriver(t, newDriver, option)
type Option func(*settings)

// WithTimeout bounds every subtest's context, for slow remote drivers. A zero
// or negative timeout keeps the default of 30 seconds.
//
// Example:
//
//	authtest.RunUserDriver(t, newDriver, authtest.WithTimeout(time.Minute))
func WithTimeout(timeout time.Duration) Option {
	return func(config *settings) {
		if timeout > 0 {
			config.timeout = timeout
		}
	}
}

// RunUserDriver executes the conformance suite against drivers returned by
// newDriver. newDriver is called once per subtest with the fixtures the driver
// must contain (one enabled user with a version and claims, one disabled user)
// and registers any cleanup on t. The suite reports every deviation with
// t.Errorf or t.Fatalf in a subtest named after the behavior.
//
// Example:
//
//	func TestMemoryDriver(t *testing.T) {
//		authtest.RunUserDriver(t, func(t *testing.T, users []authtest.User) auth.UserDriver {
//			driver := memoryauth.New()
//			for _, user := range users {
//				_ = driver.Put(auth.UserRecord{Subject: user.Subject, Version: user.Version, Claims: user.Claims})
//				if user.Disabled {
//					_ = driver.Disable(user.Subject)
//				}
//			}
//			return driver
//		})
//	}
func RunUserDriver(t *testing.T, newDriver func(t *testing.T, users []User) auth.UserDriver, opts ...Option) {
	t.Helper()

	config := settings{timeout: defaultTimeout}

	for _, opt := range opts {
		opt(&config)
	}

	claims := map[string]any{"role": "admin"}
	fixtures := []User{
		{Subject: foundSubject, Version: "v7", Claims: claims},
		{Subject: disabledSubject, Version: "v1", Disabled: true},
	}

	run := func(name string, test func(t *testing.T, driver auth.UserDriver)) {
		t.Run(name, func(t *testing.T) {
			driver := newDriver(t, fixtures)

			test(t, driver)
		})
	}

	find := func(t *testing.T, driver auth.UserDriver, subject string) (auth.UserRecord, error) {
		t.Helper()

		ctx, cancel := context.WithTimeout(t.Context(), config.timeout)
		defer cancel()

		return driver.Find(ctx, subject)
	}

	run("Found", func(t *testing.T, driver auth.UserDriver) {
		record, err := find(t, driver, foundSubject)

		if err != nil {
			t.Fatalf("Find(%q) = %v", foundSubject, err)
		}

		if record.Subject != foundSubject {
			t.Errorf("Subject = %q, want %q", record.Subject, foundSubject)
		}
	})

	run("VersionPassthrough", func(t *testing.T, driver auth.UserDriver) {
		record, err := find(t, driver, foundSubject)

		if err != nil || record.Version != "v7" {
			t.Fatalf("Find(%q) = version %q, %v, want v7", foundSubject, record.Version, err)
		}
	})

	run("ClaimsPassthrough", func(t *testing.T, driver auth.UserDriver) {
		record, err := find(t, driver, foundSubject)

		if err != nil || !maps.Equal(record.Claims, claims) {
			t.Fatalf("Find(%q) = claims %v, %v, want %v", foundSubject, record.Claims, err, claims)
		}
	})

	run("NotFound", func(t *testing.T, driver auth.UserDriver) {
		if _, err := find(t, driver, missingSubject); !errors.Is(err, auth.ErrUserNotFound) {
			t.Errorf("Find(%q) error = %v, want ErrUserNotFound", missingSubject, err)
		}
	})

	run("Disabled", func(t *testing.T, driver auth.UserDriver) {
		_, err := find(t, driver, disabledSubject)

		if !errors.Is(err, auth.ErrUserDisabled) || errors.Is(err, auth.ErrUserNotFound) {
			t.Errorf("Find(%q) error = %v, want ErrUserDisabled only", disabledSubject, err)
		}
	})

	run("InvalidSubject", func(t *testing.T, driver auth.UserDriver) {
		for _, subject := range []string{"", strings.Repeat("1", 256), "1 2", "1\x7f", "é1"} {
			if _, err := find(t, driver, subject); !errors.Is(err, auth.ErrInvalidSubject) || errors.Is(err, auth.ErrUserNotFound) {
				t.Errorf("Find(%q) error = %v, want ErrInvalidSubject only", subject, err)
			}
		}
	})
}

// WithInvalid supplies a factory for a request carrying credentials the
// authenticator rejects, such as an expired or forged token. It enables the
// check that rejected credentials yield [auth.ErrUnauthenticated]. Leave it
// out for authenticators without a notion of invalid credentials, such as
// [auth.Guard.Authenticator]. It only affects [Run].
//
// Example:
//
//	authtest.Run(t, authenticate, anonymousRequest, validRequest,
//		authtest.WithInvalid(forgedRequest))
func WithInvalid(newRequest func(t *testing.T) *http.Request) Option {
	return func(config *settings) {
		config.newInvalid = newRequest
	}
}

// WithIdentity supplies a check run on the [auth.Identity] resolved from the
// valid request, for example comparing its subject. It only affects [Run].
//
// Example:
//
//	authtest.Run(t, authenticate, anonymousRequest, validRequest,
//		authtest.WithIdentity(func(t *testing.T, identity auth.Identity) {
//			if identity.Subject() != "42" {
//				t.Fatalf("subject = %q", identity.Subject())
//			}
//		}))
func WithIdentity(check func(t *testing.T, identity auth.Identity)) Option {
	return func(config *settings) {
		config.checkIdentity = check
	}
}

// authenticatorSuite holds the settings and request factories of one [Run].
type authenticatorSuite struct {
	settings

	// authenticate is the authenticator under test.
	authenticate auth.Authenticator

	// newAnonymous creates a request without credentials.
	newAnonymous func(t *testing.T) *http.Request

	// newValid creates a request with valid credentials.
	newValid func(t *testing.T) *http.Request
}

// Run checks that authenticate follows the [auth.Authenticator] contract:
// a request without credentials yields [auth.ErrNoCredentials] and passes
// [auth.Middleware] anonymously, a request with valid credentials yields an
// identity that the middleware stores in the context, and, with [WithInvalid],
// rejected credentials yield [auth.ErrUnauthenticated] and stop the request.
//
// newAnonymous and newValid are called once per subtest; each request must
// carry whatever context the authenticator reads, such as a session.
//
// Example:
//
//	func TestBearer(t *testing.T) {
//		authenticate := auth.Bearer(verify)
//		authtest.Run(t, authenticate, anonymousRequest, validRequest,
//			authtest.WithInvalid(forgedRequest))
//	}
func Run(t *testing.T, authenticate auth.Authenticator, newAnonymous, newValid func(t *testing.T) *http.Request, opts ...Option) {
	suite := &authenticatorSuite{
		settings:     settings{timeout: defaultTimeout},
		authenticate: authenticate,
		newAnonymous: newAnonymous,
		newValid:     newValid,
	}

	for _, opt := range opts {
		opt(&suite.settings)
	}

	t.Run("AnonymousHasNoCredentials", suite.testAnonymousHasNoCredentials)
	t.Run("AnonymousPassesMiddleware", suite.testAnonymousPassesMiddleware)
	t.Run("ValidResolvesIdentity", suite.testValidResolvesIdentity)
	t.Run("ValidStoresIdentity", suite.testValidStoresIdentity)
	t.Run("InvalidIsUnauthenticated", suite.testInvalidIsUnauthenticated)
	t.Run("InvalidStopsMiddleware", suite.testInvalidStopsMiddleware)
}

// serve runs request through [auth.Middleware] with the authenticator under
// test. It reports whether the next handler ran, whether that handler saw an
// identity, and the middleware's error.
func (suite *authenticatorSuite) serve(request *http.Request) (called, hasIdentity bool, err error) {
	handler := auth.Middleware(suite.authenticate)(func(_ http.ResponseWriter, r *http.Request) error {
		called = true
		_, hasIdentity = auth.FromContext(r.Context())

		return nil
	})

	err = handler(httptest.NewRecorder(), request)

	return called, hasIdentity, err
}

// testAnonymousHasNoCredentials verifies a request without credentials yields
// auth.ErrNoCredentials, so the next authenticator may try.
func (suite *authenticatorSuite) testAnonymousHasNoCredentials(t *testing.T) {
	if _, err := suite.authenticate(suite.newAnonymous(t)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("authenticate() error = %v, want ErrNoCredentials", err)
	}
}

// testAnonymousPassesMiddleware verifies the middleware lets a request
// without credentials through without an identity.
func (suite *authenticatorSuite) testAnonymousPassesMiddleware(t *testing.T) {
	called, hasIdentity, err := suite.serve(suite.newAnonymous(t))

	if err != nil || !called || hasIdentity {
		t.Fatalf("Middleware: error = %v, called = %v, identity = %v; want nil, true, false", err, called, hasIdentity)
	}
}

// testValidResolvesIdentity verifies a request with valid credentials yields an
// identity and no error.
func (suite *authenticatorSuite) testValidResolvesIdentity(t *testing.T) {
	identity, err := suite.authenticate(suite.newValid(t))

	if err != nil {
		t.Fatalf("authenticate() error = %v", err)
	}

	if suite.checkIdentity != nil {
		suite.checkIdentity(t, identity)
	}
}

// testValidStoresIdentity verifies the middleware stores the identity of a
// valid request in the context.
func (suite *authenticatorSuite) testValidStoresIdentity(t *testing.T) {
	called, hasIdentity, err := suite.serve(suite.newValid(t))

	if err != nil || !called || !hasIdentity {
		t.Fatalf("Middleware: error = %v, called = %v, identity = %v; want nil, true, true", err, called, hasIdentity)
	}
}

// testInvalidIsUnauthenticated verifies rejected credentials yield
// auth.ErrUnauthenticated, which renders as 401.
func (suite *authenticatorSuite) testInvalidIsUnauthenticated(t *testing.T) {
	if suite.newInvalid == nil {
		t.Skip("no invalid request; see WithInvalid")
	}

	if _, err := suite.authenticate(suite.newInvalid(t)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("authenticate() error = %v, want ErrUnauthenticated", err)
	}
}

// testInvalidStopsMiddleware verifies the middleware returns the error of
// rejected credentials and does not call the next handler.
func (suite *authenticatorSuite) testInvalidStopsMiddleware(t *testing.T) {
	if suite.newInvalid == nil {
		t.Skip("no invalid request; see WithInvalid")
	}

	called, _, err := suite.serve(suite.newInvalid(t))

	if err == nil || called {
		t.Fatalf("Middleware: error = %v, called = %v; want an error and false", err, called)
	}
}
