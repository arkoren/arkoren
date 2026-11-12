package auth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// secured is a test user whose sessions end when Password changes.
type secured struct {
	// ID is the login identifier reported by AuthID.
	ID int

	// Password stands in for the credential that AuthVersion tracks.
	Password string
}

// AuthID reports the login identifier accepted by loadSecured.
func (user secured) AuthID() int {
	return user.ID
}

// AuthVersion reports the current credential, so a change ends other sessions.
func (user secured) AuthVersion() string {
	return user.Password
}

// loadSecured resolves any positive id to a user with the given password.
func loadSecured(password *string) func(context.Context, int) (secured, error) {
	return func(_ context.Context, id int) (secured, error) {
		return secured{ID: id, Password: *password}, nil
	}
}

// staff is a second user type so two realms can share one physical session.
type staff struct {
	// ID is the login identifier reported by AuthID.
	ID int
}

// AuthID reports the login identifier accepted by loadStaff.
func (user staff) AuthID() int {
	return user.ID
}

// loadStaff resolves any id to a staff user.
func loadStaff(_ context.Context, id int) (staff, error) {
	return staff{ID: id}, nil
}

// newSession returns an empty session valid for an hour.
func newSession() *session.Session {
	return session.New(time.Now().Add(time.Hour), map[string]any{})
}

// authenticated reports whether the authenticator resolves a user for current.
func authenticated(t *testing.T, identity auth.SessionIdentity[int, user], current *session.Session) bool {
	t.Helper()

	_, err := identity.Authenticator()(sessionRequest(current))

	if err != nil && !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatal(err)
	}

	return err == nil
}

// stepUp runs middleware for a request carrying ctxUser and current and returns
// the handler error. A nil current sends no session.
func stepUp(middleware router.Middleware, withUser bool, current *session.Session) error {
	reached := func(http.ResponseWriter, *http.Request) error { return nil }
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	if current != nil {
		r = sessionRequest(current)
	}

	if withUser {
		r = r.WithContext(auth.NewContext(r.Context(), user{ID: 7}))
	}

	return middleware(reached)(httptest.NewRecorder(), r)
}

// TestChallengeIsAnonymousUntilComplete verifies a pending login grants nothing
// through the authenticator or middleware, and Complete logs the user in with
// the earlier and new methods.
func TestChallengeIsAnonymousUntilComplete(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := newSession()

	if err := identity.Challenge(current, user{ID: 7}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	if authenticated(t, identity, current) {
		t.Fatal("pending login authenticated")
	}

	var seen []string

	handler := auth.Middleware(identity.Authenticator())(ok(&seen))

	if err := handler(httptest.NewRecorder(), sessionRequest(current)); err != nil || seen[0] != "anonymous" {
		t.Fatalf("seen = %v, err = %v", seen, err)
	}

	waiting, err := identity.Pending(t.Context(), current)

	if err != nil || waiting.ID != 7 {
		t.Fatalf("pending = %v, %v", waiting, err)
	}

	if err := identity.Complete(t.Context(), current, auth.MethodOTP); err != nil {
		t.Fatal(err)
	}

	if !authenticated(t, identity, current) {
		t.Fatal("completed login did not authenticate")
	}

	state, err := identity.State(current)

	if err != nil || len(state.Factors) != 2 || state.Factors[0].Method != auth.MethodPassword || state.Factors[1].Method != auth.MethodOTP {
		t.Fatalf("state = %+v, %v", state, err)
	}
}

// TestChallengeEndsLoggedInUser verifies challenging a logged-in session logs
// the user out until the challenge completes.
func TestChallengeEndsLoggedInUser(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := newSession()

	if err := identity.Login(current, user{ID: 7}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	if err := identity.Challenge(current, user{ID: 8}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	if authenticated(t, identity, current) {
		t.Fatal("challenge kept the previous login")
	}

	if state, err := identity.State(current); err != nil || len(state.Factors) != 0 {
		t.Fatalf("state = %+v, %v", state, err)
	}
}

// TestPendingExpires verifies the challenge deadline, its configurable
// lifetime and that expiry clears the pending login.
func TestPendingExpires(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		identity := auth.NewSessionIdentity(loadByID, func(authenticator *auth.SessionBuilder) {
			authenticator.ChallengeTTL(time.Minute)
		})
		current := newSession()

		if err := identity.Challenge(current, user{ID: 7}, auth.MethodPassword); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Minute - time.Nanosecond)

		if _, err := identity.Pending(t.Context(), current); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Nanosecond)

		if _, err := identity.Pending(t.Context(), current); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("err = %v", err)
		}

		if _, ok := current.All()["arkoren:auth:pending"]; ok {
			t.Fatal("expired challenge was retained")
		}
	})
}

// TestDefaultChallengeTTLIsFiveMinutes verifies the documented default.
func TestDefaultChallengeTTLIsFiveMinutes(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		identity := auth.NewSessionIdentity(loadByID)
		current := newSession()

		if err := identity.Challenge(current, user{ID: 7}); err != nil {
			t.Fatal(err)
		}

		time.Sleep(5*time.Minute - time.Nanosecond)

		if _, err := identity.Pending(t.Context(), current); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Nanosecond)

		if err := identity.Complete(t.Context(), current); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestCompleteIsSingleUse verifies a second Complete finds no pending login.
func TestCompleteIsSingleUse(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := newSession()

	if err := identity.Challenge(current, user{ID: 7}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	if err := identity.Complete(t.Context(), current, auth.MethodOTP); err != nil {
		t.Fatal(err)
	}

	if err := identity.Complete(t.Context(), current, auth.MethodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}
}

// TestCompleteWithoutChallengeFails verifies a session that was never
// challenged cannot be completed, and nothing is written.
func TestCompleteWithoutChallengeFails(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := newSession()
	id := current.ID()

	if err := identity.Complete(t.Context(), current, auth.MethodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}

	if current.ID() != id || authenticated(t, identity, current) {
		t.Fatal("failed completion changed the session")
	}
}

// TestCompletePassesLoaderErrors verifies a user deleted during the challenge
// is not logged in and the loader's error is returned.
func TestCompletePassesLoaderErrors(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := newSession()

	if err := identity.Challenge(current, user{ID: -1}); err != nil {
		t.Fatal(err)
	}

	if err := identity.Complete(t.Context(), current); err == nil || errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}

	if authenticated(t, identity, current) {
		t.Fatal("failed completion logged in")
	}
}

// TestTransitionsRotateSessionID verifies every state transition changes the ID.
func TestTransitionsRotateSessionID(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := newSession()
	seen := map[string]bool{current.ID(): true}

	steps := []func() error{
		func() error { return identity.Challenge(current, user{ID: 7}, auth.MethodPassword) },
		func() error { return identity.Complete(t.Context(), current, auth.MethodOTP) },
		func() error { return identity.Confirm(current, auth.MethodPassword) },
		func() error { return identity.Login(current, user{ID: 7}, auth.MethodPassword) },
		func() error { return identity.Logout(current) },
	}

	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}

		if seen[current.ID()] {
			t.Fatalf("step %d reused a session ID", i)
		}

		seen[current.ID()] = true
	}
}

// TestLogoutClearsAllFields verifies Logout removes the identifier, factors and
// a pending login, leaving unrelated data.
func TestLogoutClearsAllFields(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := newSession()

	if err := identity.Login(current, user{ID: 7}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	if err := current.Put(session.NewField[string]("arkoren:auth:pending"), "stale"); err != nil {
		t.Fatal(err)
	}

	if err := current.Put(session.NewField[string]("theme"), "dark"); err != nil {
		t.Fatal(err)
	}

	if err := identity.Logout(current); err != nil {
		t.Fatal(err)
	}

	if len(current.All()) != 1 || current.All()["theme"] != "dark" {
		t.Fatalf("remaining = %v", current.All())
	}
}

// TestLoginRefreshesAndDeduplicatesMethods verifies a repeated method appears
// once.
func TestLoginRefreshesAndDeduplicatesMethods(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := newSession()

	if err := identity.Login(current, user{ID: 7}, auth.MethodPassword, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	if state, err := identity.State(current); err != nil || len(state.Factors) != 1 {
		t.Fatalf("state = %+v, %v", state, err)
	}
}

// TestLoginDropsPendingLogin verifies logging in directly discards a challenge.
func TestLoginDropsPendingLogin(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := newSession()

	if err := identity.Challenge(current, user{ID: 7}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	if err := identity.Login(current, user{ID: 8}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	if _, err := identity.Pending(t.Context(), current); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}
}

// TestConfirmRefreshesFactorTime verifies Confirm moves a recorded method's
// time forward, appends a new method and requires a logged-in user.
func TestConfirmRefreshesFactorTime(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		identity := auth.NewSessionIdentity(loadByID)
		current := newSession()

		if err := identity.Confirm(current, auth.MethodPassword); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("anonymous confirm = %v", err)
		}

		if err := identity.Login(current, user{ID: 7}, auth.MethodPassword); err != nil {
			t.Fatal(err)
		}

		first, err := identity.State(current)

		if err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Minute)

		if err := identity.Confirm(current, auth.MethodPassword, auth.MethodOTP); err != nil {
			t.Fatal(err)
		}

		second, err := identity.State(current)

		if err != nil || len(second.Factors) != 2 || !second.Factors[0].At.Equal(first.Factors[0].At.Add(time.Minute)) {
			t.Fatalf("state = %+v, %v", second, err)
		}
	})
}

// TestStateIsACopy verifies editing a returned State leaves the session alone.
func TestStateIsACopy(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := newSession()

	if err := identity.Login(current, user{ID: 7}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	state, err := identity.State(current)

	if err != nil {
		t.Fatal(err)
	}

	state.Factors[0].Method = auth.MethodRecovery

	if again, _ := identity.State(current); again.Factors[0].Method != auth.MethodPassword {
		t.Fatal("State shared its slice with the session")
	}
}

// TestLegacySessionStaysLoggedInButFailsStepUp verifies a session holding only
// the identifier authenticates but satisfies no step-up check.
func TestLegacySessionStaysLoggedInButFailsStepUp(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := session.New(time.Now().Add(time.Hour), map[string]any{"arkoren:auth:user": 7})

	if !authenticated(t, identity, current) {
		t.Fatal("legacy session was logged out")
	}

	if state, err := identity.State(current); err != nil || len(state.Factors) != 0 {
		t.Fatalf("state = %+v, %v", state, err)
	}

	for _, middleware := range []router.Middleware{
		identity.RequireMethods(auth.MethodPassword),
		identity.RequireFresh(time.Hour),
	} {
		if err := stepUp(middleware, true, current); !errors.Is(err, auth.ErrStepUpRequired) {
			t.Fatalf("err = %v", err)
		}
	}
}

// TestVersionMismatchEndsSession verifies a changed credential ends the
// session, and a session stored before versioning is ended too.
func TestVersionMismatchEndsSession(t *testing.T) {
	t.Parallel()

	password := "one"
	identity := auth.NewSessionIdentity(loadSecured(&password))
	current := newSession()

	if err := identity.Login(current, secured{ID: 7, Password: password}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	authenticate := func(current *session.Session) error {
		_, err := identity.Authenticator()(sessionRequest(current))

		return err
	}

	if err := authenticate(current); err != nil {
		t.Fatal(err)
	}

	password = "two"

	if err := authenticate(current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("after change = %v", err)
	}

	legacy := session.New(time.Now().Add(time.Hour), map[string]any{"arkoren:auth:user": 7})

	if err := authenticate(legacy); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("legacy = %v", err)
	}
}

// TestVersionIsNotStoredInClear verifies only a digest of AuthVersion persists.
func TestVersionIsNotStoredInClear(t *testing.T) {
	t.Parallel()

	password := "hunter2-secret"
	identity := auth.NewSessionIdentity(loadSecured(&password))
	current := newSession()

	if err := identity.Login(current, secured{ID: 7, Password: password}); err != nil {
		t.Fatal(err)
	}

	if text := fmt.Sprint(current.All()); strings.Contains(text, password) {
		t.Fatalf("stored state leaks the version: %s", text)
	}
}

// TestRequireMethods verifies all listed methods must be present.
func TestRequireMethods(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	current := newSession()

	if err := identity.Login(current, user{ID: 7}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	if err := stepUp(identity.RequireMethods(auth.MethodPassword), true, current); err != nil {
		t.Fatal(err)
	}

	if err := stepUp(identity.RequireMethods(auth.MethodPassword, auth.MethodOTP), true, current); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("err = %v", err)
	}

	if err := identity.Confirm(current, auth.MethodOTP); err != nil {
		t.Fatal(err)
	}

	if err := stepUp(identity.RequireMethods(auth.MethodPassword, auth.MethodOTP), true, current); err != nil {
		t.Fatal(err)
	}
}

// TestRequireFreshUsesMostRecentFactor verifies the age window and that Confirm
// reopens it.
func TestRequireFreshUsesMostRecentFactor(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		identity := auth.NewSessionIdentity(loadByID)
		current := newSession()
		fresh := identity.RequireFresh(10 * time.Minute)

		if err := identity.Login(current, user{ID: 7}, auth.MethodPassword); err != nil {
			t.Fatal(err)
		}

		time.Sleep(10 * time.Minute)

		if err := stepUp(fresh, true, current); err != nil {
			t.Fatalf("at the limit: %v", err)
		}

		time.Sleep(time.Nanosecond)

		if err := stepUp(fresh, true, current); !errors.Is(err, auth.ErrStepUpRequired) {
			t.Fatalf("stale: %v", err)
		}

		if err := identity.Confirm(current, auth.MethodPassword); err != nil {
			t.Fatal(err)
		}

		if err := stepUp(fresh, true, current); err != nil {
			t.Fatalf("after confirm: %v", err)
		}

		if err := stepUp(identity.RequireFresh(0), true, current); !errors.Is(err, auth.ErrStepUpRequired) {
			t.Fatalf("zero age: %v", err)
		}
	})
}

// TestStepUpFailsClosedWithoutSessionUser verifies anonymous requests are 401,
// while bearer-style requests (a user, no session) and a user that is not the
// session's user are step-up failures.
func TestStepUpFailsClosedWithoutSessionUser(t *testing.T) {
	t.Parallel()

	identity := auth.NewSessionIdentity(loadByID)
	middleware := identity.RequireMethods(auth.MethodPassword)
	current := newSession()

	if err := identity.Login(current, user{ID: 8}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	if err := stepUp(middleware, false, current); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("anonymous = %v", err)
	}

	if err := stepUp(middleware, true, nil); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("bearer without session = %v", err)
	}

	if err := stepUp(middleware, true, current); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("bearer user with another session = %v", err)
	}

	if err := stepUp(middleware, true, newSession()); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("session without user = %v", err)
	}
}

// TestStepUpRequiredIsForbidden verifies the problem's status and stable type.
func TestStepUpRequiredIsForbidden(t *testing.T) {
	t.Parallel()

	if auth.ErrStepUpRequired.Status() != http.StatusForbidden {
		t.Fatalf("status = %d", auth.ErrStepUpRequired.Status())
	}

	if auth.ErrStepUpRequired.Type() != "tag:arkoren.dev,2026-11:core:http/auth:step-up-required" {
		t.Fatalf("type = %s", auth.ErrStepUpRequired.Type())
	}
}

// TestRealmsAreIsolatedInOnePhysicalSession verifies two realms keep separate
// users and factors, and that logging one out leaves the other.
func TestRealmsAreIsolatedInOnePhysicalSession(t *testing.T) {
	t.Parallel()

	customers := auth.NewSessionIdentity(loadByID)
	admins := auth.NewSessionIdentity(loadStaff, func(authenticator *auth.SessionBuilder) {
		authenticator.Realm("admin")
	})
	current := newSession()

	if err := customers.Login(current, user{ID: 7}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	if err := admins.Challenge(current, staff{ID: 1}, auth.MethodPassword); err != nil {
		t.Fatal(err)
	}

	if !authenticated(t, customers, current) {
		t.Fatal("admin challenge ended the customer login")
	}

	if err := admins.Complete(t.Context(), current, auth.MethodOTP); err != nil {
		t.Fatal(err)
	}

	if state, err := customers.State(current); err != nil || len(state.Factors) != 1 {
		t.Fatalf("customer state = %+v, %v", state, err)
	}

	if state, err := admins.State(current); err != nil || len(state.Factors) != 2 {
		t.Fatalf("admin state = %+v, %v", state, err)
	}

	if err := admins.Logout(current); err != nil {
		t.Fatal(err)
	}

	if !authenticated(t, customers, current) {
		t.Fatal("admin logout ended the customer login")
	}

	if _, err := admins.Authenticator()(sessionRequest(current)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("admin = %v", err)
	}

	if _, ok := current.All()["arkoren:auth:user:admin"]; ok {
		t.Fatal("admin logout retained its identifier")
	}
}

// TestStateSurvivesReload verifies factors and pending logins round trip
// through cache persistence and still satisfy step-up checks.
func TestStateSurvivesReload(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		identity := auth.NewSessionIdentity(loadByID)
		sessions := session.Middleware(cache.New(memorycache.New()))
		guarded := identity.RequireMethods(auth.MethodPassword, auth.MethodOTP)

		run := func(cookie *http.Cookie, handler router.Handler) *http.Response {
			r := httptest.NewRequest(http.MethodGet, "/", nil)

			if cookie != nil {
				r.AddCookie(cookie)
			}

			return sessions(handler).Record(r)
		}

		challenge := func(_ http.ResponseWriter, r *http.Request) error {
			current, err := identity.Session(r.Context())

			if err != nil {
				return err
			}

			return identity.Challenge(current, user{ID: 7}, auth.MethodPassword)
		}

		first := run(nil, challenge)
		cookie := first.Cookies()[0]

		complete := func(_ http.ResponseWriter, r *http.Request) error {
			current, err := identity.Session(r.Context())

			if err != nil {
				return err
			}

			return identity.Complete(r.Context(), current, auth.MethodOTP)
		}

		second := run(cookie, complete)

		if second.StatusCode != http.StatusNoContent {
			t.Fatal(second.StatusCode)
		}

		cookie = second.Cookies()[0]

		protected := func(w http.ResponseWriter, r *http.Request) error {
			user, err := identity.Authenticator()(r)

			if err != nil {
				return err
			}

			r = r.WithContext(auth.NewContext(r.Context(), user))

			return guarded(func(http.ResponseWriter, *http.Request) error { return nil })(w, r)
		}

		if third := run(cookie, protected); third.StatusCode != http.StatusNoContent {
			t.Fatal(third.StatusCode)
		}
	})
}
