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

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/authtest"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// newDirectory returns a provider driver with enabled user "7" (version v1,
// application value user{"u"}), disabled user "8" and enabled user "9".
func newDirectory(t *testing.T) (*auth.UserProvider, *memoryauth.Driver) {
	t.Helper()

	driver := memoryauth.New()

	for _, record := range []auth.UserRecord{
		{Subject: "7", Version: "v1", Value: user{"u"}},
		{Subject: "8"},
		{Subject: "9"},
	} {
		if err := driver.Put(record); err != nil {
			t.Fatal(err)
		}
	}

	if err := driver.Disable("8"); err != nil {
		t.Fatal(err)
	}

	provider, err := auth.NewUserProvider(driver)

	if err != nil {
		t.Fatal(err)
	}

	return provider, driver
}

// newGuard returns a login over newDirectory with a memory store.
func newGuard(t *testing.T, configure ...auth.GuardBuilderFunc) (*auth.Guard, *memoryauth.Driver) {
	t.Helper()

	provider, driver := newDirectory(t)
	guard, err := auth.NewGuard(provider, cache.New(memorycache.New()), configure...)

	if err != nil {
		t.Fatal(err)
	}

	return guard, driver
}

// withChecks registers second-factor checks on a login.
func withChecks(checks ...func(context.Context, string) (bool, error)) auth.GuardBuilderFunc {
	return func(policy *auth.GuardBuilder) {
		for _, check := range checks {
			policy.SecondFactor(check)
		}
	}
}

// secondFactor returns a check with a fixed answer and error.
func secondFactor(needed bool, err error) func(context.Context, string) (bool, error) {
	return func(context.Context, string) (bool, error) {
		return needed, err
	}
}

// newSession returns an empty session valid for an hour.
func newSession() *session.Session {
	return session.New(time.Now().Add(time.Hour), map[string]any{})
}

// authenticate runs the login's authenticator for the session.
func authenticate(guard *auth.Guard, current *session.Session) (auth.Identity, error) {
	return guard.Authenticator()(sessionRequest(current))
}

// begin logs subject in with the password factor, fails the test on error and
// reports whether the login completed.
func begin(t *testing.T, guard *auth.Guard, r *http.Request, subject string) bool {
	t.Helper()

	complete, err := guard.Begin(r, subject, password)

	if err != nil {
		t.Fatal(err)
	}

	return complete
}

// TestBeginLogsInWithoutChecks verifies a login without second-factor checks
// completes, rotates the session ID and authenticates.
func TestBeginLogsInWithoutChecks(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	current := newSession()
	before := current.ID()

	if !begin(t, guard, sessionRequest(current), "7") {
		t.Fatal("login did not complete")
	}

	if current.ID() == before {
		t.Fatal("begin did not rotate the session")
	}

	if _, err := authenticate(guard, current); err != nil {
		t.Fatal(err)
	}
}

// TestBeginLogsInWhenAllChecksSayNo verifies several checks that all answer no
// still log the user in.
func TestBeginLogsInWhenAllChecksSayNo(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(false, nil), secondFactor(false, nil)))

	if !begin(t, guard, sessionRequest(newSession()), "7") {
		t.Fatal("login did not complete")
	}
}

// TestBeginParksLoginWhenOneCheckSaysYes verifies one yes parks the login, the
// request stays anonymous and the session ID rotates.
func TestBeginParksLoginWhenOneCheckSaysYes(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)))
	current := newSession()
	before := current.ID()

	if begin(t, guard, sessionRequest(current), "7") {
		t.Fatal("login completed without its second factor")
	}

	if current.ID() == before {
		t.Fatal("begin did not rotate the session")
	}

	if _, err := authenticate(guard, current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("pending login authenticated: %v", err)
	}
}

// TestBeginParksLoginWhenLaterCheckSaysYes verifies every registered check is
// asked, not only the first.
func TestBeginParksLoginWhenLaterCheckSaysYes(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(false, nil), secondFactor(true, nil)))

	if begin(t, guard, sessionRequest(newSession()), "7") {
		t.Fatal("login completed without its second factor")
	}
}

// TestBeginFailsOnCheckError verifies a failing check aborts the login with its
// error and changes nothing.
func TestBeginFailsOnCheckError(t *testing.T) {
	t.Parallel()

	failure := errors.New("lookup failed")
	guard, _ := newGuard(t, withChecks(secondFactor(false, failure)))
	current := newSession()
	before := current.ID()

	if _, err := guard.Begin(sessionRequest(current), "7", password); !errors.Is(err, failure) {
		t.Fatalf("err = %v", err)
	}

	if current.ID() != before || len(current.All()) != 0 {
		t.Fatal("failed begin changed the session")
	}
}

// TestBeginBuildsIdentity verifies the logged-in identity carries the factor
// (stamped now when its time is zero), the provider's record and a level.
func TestBeginBuildsIdentity(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, _ := newGuard(t)
		current := newSession()
		begin(t, guard, sessionRequest(current), "7")

		identity, err := authenticate(guard, current)

		if err != nil {
			t.Fatal(err)
		}

		factors := identity.Factors()

		if identity.Subject() != "7" || identity.Level() != auth.LevelSingle || len(factors) != 1 || factors[0].Method != auth.MethodPassword || !factors[0].At.Equal(time.Now()) {
			t.Fatalf("identity = %q, %v, %+v", identity.Subject(), identity.Level(), factors)
		}

		if value, ok := auth.UserFromContext[user](auth.NewContext(t.Context(), identity)); !ok || value.Name != "u" || identity.Record().Version != "v1" {
			t.Fatalf("record = %+v", identity.Record())
		}
	})
}

// TestBeginRejectsMissingUsers verifies an unknown or disabled user is
// ErrUnauthenticated, touches nothing and never asks a second-factor check.
func TestBeginRejectsMissingUsers(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(func(context.Context, string) (bool, error) {
		t.Error("second-factor check ran for a rejected user")

		return false, nil
	}))

	for _, subject := range []string{"404", "8"} {
		current := newSession()
		before := current.ID()

		if _, err := guard.Begin(sessionRequest(current), subject, password); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("Begin(%q) = %v", subject, err)
		}

		if current.ID() != before || len(current.All()) != 0 {
			t.Fatalf("Begin(%q) changed the session", subject)
		}
	}
}

// TestBeginRejectsInvalidSubject verifies a malformed subject is a provider
// error, not an anonymous rejection.
func TestBeginRejectsInvalidSubject(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)

	if _, err := guard.Begin(sessionRequest(newSession()), "", password); !errors.Is(err, auth.ErrInvalidSubject) {
		t.Fatalf("err = %v", err)
	}
}

// TestPendingIsAnonymousUntilComplete verifies a pending login grants nothing
// and Complete logs the user in with both factors, at level multi.
func TestPendingIsAnonymousUntilComplete(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)))
	current := newSession()
	r := sessionRequest(current)
	begin(t, guard, r, "7")

	var seen []string

	if err := auth.Middleware(guard.Authenticator())(ok(&seen))(httptest.NewRecorder(), r); err != nil || seen[0] != "anonymous" {
		t.Fatalf("seen = %v, err = %v", seen, err)
	}

	waiting, err := guard.Pending(r)

	if err != nil || waiting.Subject != "7" {
		t.Fatalf("pending = %+v, %v", waiting, err)
	}

	before := current.ID()

	if err := guard.Complete(r, otp); err != nil {
		t.Fatal(err)
	}

	if current.ID() == before {
		t.Fatal("complete did not rotate the session")
	}

	identity, err := authenticate(guard, current)

	if err != nil || identity.Level() != auth.LevelMulti || len(identity.Factors()) != 2 {
		t.Fatalf("identity = %v, %v, %v", identity.Level(), identity.Factors(), err)
	}
}

// TestBeginReplacesLoggedInUserWithPending verifies a second login that needs a
// second factor ends the previous login until completed.
func TestBeginReplacesLoggedInUserWithPending(t *testing.T) {
	t.Parallel()

	needed := false
	guard, _ := newGuard(t, withChecks(func(context.Context, string) (bool, error) {
		return needed, nil
	}))
	current := newSession()
	r := sessionRequest(current)
	begin(t, guard, r, "9")

	needed = true

	if begin(t, guard, r, "7") {
		t.Fatal("login completed without its second factor")
	}

	if _, err := authenticate(guard, current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("previous login survived: %v", err)
	}
}

// TestBeginSameSubjectAddsFactorAndSkipsSecondFactor verifies the v0.6.0
// behavior change: a session already logged in as the subject keeps its factors
// (pwd, otp), gains the new one, is not parked as pending and keeps one record.
// Shipped v0.5.0 replaced the factors and challenged the user again.
func TestBeginSameSubjectAddsFactorAndSkipsSecondFactor(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)))
	current := newSession()
	r := sessionRequest(current)
	begin(t, guard, r, "7")

	if err := guard.Complete(r, otp); err != nil {
		t.Fatal(err)
	}

	before := current.ID()
	complete, err := guard.Begin(r, "7", federated)

	if err != nil || !complete {
		t.Fatalf("begin = %v, %v", complete, err)
	}

	identity, err := authenticate(guard, current)
	factors := identity.Factors()

	if err != nil || len(factors) != 3 || factors[0].Method != auth.MethodPassword || factors[1].Method != auth.MethodOTP || factors[2].Method != auth.MethodFederated {
		t.Fatalf("identity = %+v, %v", factors, err)
	}

	if current.ID() == before {
		t.Fatal("begin did not rotate the session")
	}

	if _, err := guard.Pending(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending login exists: %v", err)
	}

	if len(loginsOf(t, guard, "7")) != 1 {
		t.Fatal("login record was not kept")
	}
}

// TestBeginDifferentSubjectKeepsReplacing verifies another subject still
// replaces the login and goes through the second-factor checks.
func TestBeginDifferentSubjectKeepsReplacing(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(func(_ context.Context, subject string) (bool, error) {
		return subject == "7", nil
	}))
	current := newSession()
	r := sessionRequest(current)
	begin(t, guard, r, "9")

	if begin(t, guard, r, "7") {
		t.Fatal("different subject skipped its second factor")
	}

	if _, err := authenticate(guard, current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("previous login survived: %v", err)
	}
}

// TestBeginAnonymousSessionUnchanged verifies an anonymous session logs in with
// the single factor and is parked when a second factor is enrolled.
func TestBeginAnonymousSessionUnchanged(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)))
	r := sessionRequest(newSession())

	if complete, err := guard.Begin(r, "7", federated); err != nil || complete {
		t.Fatalf("begin = %v, %v", complete, err)
	}

	if _, err := guard.Pending(r); err != nil {
		t.Fatal(err)
	}
}

// TestBeginDropsOlderPendingLogin verifies a direct login discards a pending one.
func TestBeginDropsOlderPendingLogin(t *testing.T) {
	t.Parallel()

	needed := true
	guard, _ := newGuard(t, withChecks(func(context.Context, string) (bool, error) {
		return needed, nil
	}))
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	needed = false
	begin(t, guard, r, "9")

	if _, err := guard.Pending(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending login survived: %v", err)
	}
}

// TestCompleteIsSingleUse verifies a second Complete finds no pending login.
func TestCompleteIsSingleUse(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)))
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	if err := guard.Complete(r, otp); err != nil {
		t.Fatal(err)
	}

	if err := guard.Complete(r, otp); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("second complete = %v", err)
	}

	if _, err := guard.Pending(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending after complete = %v", err)
	}
}

// TestCompleteWithoutPendingFails verifies nothing is written for a session
// that never began a login.
func TestCompleteWithoutPendingFails(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	current := newSession()
	before := current.ID()

	if err := guard.Complete(sessionRequest(current), otp); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}

	if current.ID() != before || len(current.All()) != 0 {
		t.Fatal("failed completion changed the session")
	}
}

// TestCompleteFailsForDeletedUser verifies a user deleted during the challenge
// cannot complete and the pending login is discarded.
func TestCompleteFailsForDeletedUser(t *testing.T) {
	t.Parallel()

	guard, driver := newGuard(t, withChecks(secondFactor(true, nil)))
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")
	driver.Delete("7")

	if err := guard.Complete(r, otp); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("complete = %v", err)
	}

	if _, err := guard.Pending(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending survived: %v", err)
	}
}

// TestPendingFailsForDisabledUser verifies a user disabled during the challenge
// is rejected by Pending and the pending login is discarded.
func TestPendingFailsForDisabledUser(t *testing.T) {
	t.Parallel()

	guard, driver := newGuard(t, withChecks(secondFactor(true, nil)))
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	if err := driver.Disable("7"); err != nil {
		t.Fatal(err)
	}

	if _, err := guard.Pending(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending = %v", err)
	}

	if err := guard.Complete(r, otp); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("complete = %v", err)
	}
}

// expire parks a login with the given settings, waits until just before ttl and
// verifies it still resolves, then waits past ttl and verifies Complete fails
// and the pending record is gone.
func expire(t *testing.T, ttl time.Duration, configure ...auth.GuardBuilderFunc) {
	t.Helper()

	synctest.Test(t, func(t *testing.T) {
		guard, _ := newGuard(t, append(configure, withChecks(secondFactor(true, nil)))...)
		current := newSession()
		r := sessionRequest(current)
		begin(t, guard, r, "7")

		time.Sleep(ttl - time.Nanosecond)

		if _, err := guard.Pending(r); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Nanosecond)

		if err := guard.Complete(r, otp); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("complete after expiry = %v", err)
		}

		if _, ok := current.All()["arkoren:auth:pending"]; ok {
			t.Fatal("expired pending login was retained")
		}
	})
}

// TestPendingExpiresAfterFiveMinutesByDefault verifies the default lifetime.
func TestPendingExpiresAfterFiveMinutesByDefault(t *testing.T) {
	t.Parallel()

	expire(t, 5*time.Minute)
}

// TestPendingExpiresAfterChallengeTTL verifies the configured lifetime.
func TestPendingExpiresAfterChallengeTTL(t *testing.T) {
	t.Parallel()

	expire(t, time.Minute, func(policy *auth.GuardBuilder) {
		policy.ChallengeTTL(time.Minute)
	})
}

// TestFailDiscardsPendingAtMaxAttempts verifies the attempt limit and that every
// counted attempt rotates the session ID.
func TestFailDiscardsPendingAtMaxAttempts(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(3)
	})
	current := newSession()
	r := sessionRequest(current)
	begin(t, guard, r, "7")

	for attempt := 1; attempt <= 2; attempt++ {
		before := current.ID()

		if err := guard.Fail(r); err != nil {
			t.Fatal(err)
		}

		if current.ID() == before {
			t.Fatalf("attempt %d did not rotate the session", attempt)
		}

		if _, err := guard.Pending(r); err != nil {
			t.Fatalf("pending after %d attempts = %v", attempt, err)
		}
	}

	if err := guard.Fail(r); err != nil {
		t.Fatal(err)
	}

	if err := guard.Complete(r, otp); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("complete after the limit = %v", err)
	}

	if err := guard.Fail(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("fail without pending = %v", err)
	}
}

// TestConfirmRequiresLogin verifies Confirm on an anonymous or pending session
// is ErrUnauthenticated.
func TestConfirmRequiresLogin(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)))
	r := sessionRequest(newSession())

	if err := guard.Confirm(r, otp); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("anonymous confirm = %v", err)
	}

	begin(t, guard, r, "7")

	if err := guard.Confirm(r, otp); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending confirm = %v", err)
	}
}

// TestConfirmAddsAndRefreshesFactors verifies Confirm lifts a session to multi
// level and replaces a factor of the same method with the newer time.
func TestConfirmAddsAndRefreshesFactors(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, _ := newGuard(t)
		current := newSession()
		r := sessionRequest(current)
		begin(t, guard, r, "7")

		time.Sleep(time.Hour)

		before := current.ID()

		if err := guard.Confirm(r, otp); err != nil {
			t.Fatal(err)
		}

		if err := guard.Confirm(r, password); err != nil {
			t.Fatal(err)
		}

		if current.ID() == before {
			t.Fatal("confirm did not rotate the session")
		}

		identity, err := authenticate(guard, current)
		factors := identity.Factors()

		if err != nil || identity.Level() != auth.LevelMulti || len(factors) != 2 || !factors[0].At.Equal(time.Now()) {
			t.Fatalf("identity = %v, %+v, %v", identity.Level(), factors, err)
		}
	})
}

// TestLogoutEndsLoginAndPending verifies Logout removes both records, even a
// wrongly typed stale one, keeps other data and rotates the ID.
func TestLogoutEndsLoginAndPending(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	current := session.New(time.Now().Add(time.Hour), map[string]any{
		"arkoren:auth:session": struct{ N int }{N: 1},
		"arkoren:auth:pending": 7,
		"theme":                "dark",
	})
	before := current.ID()

	if err := guard.Logout(sessionRequest(current)); err != nil {
		t.Fatal(err)
	}

	all := current.All()

	if current.ID() == before || len(all) != 1 || all["theme"] != "dark" {
		t.Fatalf("id rotated = %v, data = %v", current.ID() != before, all)
	}
}

// TestTransitionsRotateSessionID verifies every transition changes the ID and
// no ID is ever reused.
func TestTransitionsRotateSessionID(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)))
	current := newSession()
	r := sessionRequest(current)
	seen := map[string]bool{current.ID(): true}

	steps := []func() error{
		func() error { _, err := guard.Begin(r, "7", password); return err },
		func() error { return guard.Fail(r) },
		func() error { return guard.Complete(r, otp) },
		func() error { return guard.Confirm(r, password) },
		func() error { return guard.Logout(r) },
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

// TestVersionIsNotStoredInClear verifies only a digest of the credential
// version reaches the session.
func TestVersionIsNotStoredInClear(t *testing.T) {
	t.Parallel()

	provider, driver := newDirectory(t)

	if err := driver.Put(auth.UserRecord{Subject: "7", Version: "hunter2-secret"}); err != nil {
		t.Fatal(err)
	}

	guard, err := auth.NewGuard(provider, cache.New(memorycache.New()))

	if err != nil {
		t.Fatal(err)
	}

	current := newSession()
	begin(t, guard, sessionRequest(current), "7")

	if text := fmt.Sprint(current.All()); strings.Contains(text, "hunter2-secret") {
		t.Fatalf("stored session leaks the version: %s", text)
	}
}

// TestMethodsWithoutSession verifies every method fails with ErrNoSession and
// the authenticator reports no credentials.
func TestMethodsWithoutSession(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	_, beginErr := guard.Begin(r, "7", password)
	_, pendingErr := guard.Pending(r)

	for name, err := range map[string]error{
		"begin":    beginErr,
		"pending":  pendingErr,
		"complete": guard.Complete(r, otp),
		"fail":     guard.Fail(r),
		"confirm":  guard.Confirm(r, otp),
		"logout":   guard.Logout(r),
	} {
		if !errors.Is(err, session.ErrNoSession) {
			t.Errorf("%s = %v", name, err)
		}
	}

	if _, err := guard.Authenticator()(r); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("authenticator = %v", err)
	}
}

// TestAuthenticatorRejectsDeletedUser verifies a deleted user is anonymous.
func TestAuthenticatorRejectsDeletedUser(t *testing.T) {
	t.Parallel()

	guard, driver := newGuard(t)
	current := newSession()
	begin(t, guard, sessionRequest(current), "7")
	driver.Delete("7")

	if _, err := authenticate(guard, current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("err = %v", err)
	}
}

// TestAuthenticatorRejectsDisabledUser verifies a disabled user is anonymous.
func TestAuthenticatorRejectsDisabledUser(t *testing.T) {
	t.Parallel()

	guard, driver := newGuard(t)
	current := newSession()
	begin(t, guard, sessionRequest(current), "7")

	if err := driver.Disable("7"); err != nil {
		t.Fatal(err)
	}

	if _, err := authenticate(guard, current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("err = %v", err)
	}
}

// TestAuthenticatorRejectsChangedVersion verifies a changed credential version
// ends the session, while an unchanged one keeps it.
func TestAuthenticatorRejectsChangedVersion(t *testing.T) {
	t.Parallel()

	guard, driver := newGuard(t)
	current := newSession()
	begin(t, guard, sessionRequest(current), "7")

	if _, err := authenticate(guard, current); err != nil {
		t.Fatal(err)
	}

	if err := driver.Put(auth.UserRecord{Subject: "7", Version: "v2"}); err != nil {
		t.Fatal(err)
	}

	if _, err := authenticate(guard, current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("err = %v", err)
	}
}

// failingDriver is a user driver whose lookups fail.
type failingDriver struct{}

// Find always fails with a non-credential error.
func (failingDriver) Find(context.Context, string) (auth.UserRecord, error) {
	return auth.UserRecord{}, errors.New("db down")
}

// TestAuthenticatorPassesProviderErrors verifies an unavailable provider is an
// error that stops the chain, never an anonymous request.
func TestAuthenticatorPassesProviderErrors(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	current := newSession()
	begin(t, guard, sessionRequest(current), "7")

	broken, err := auth.NewUserProvider(failingDriver{})

	if err != nil {
		t.Fatal(err)
	}

	other, err := auth.NewGuard(broken, cache.New(memorycache.New()))

	if err != nil {
		t.Fatal(err)
	}

	if _, err := authenticate(other, current); err == nil || errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("err = %v", err)
	}
}

// TestAuthenticatorIgnoresLegacyAndForeignRecords verifies sessions written
// under the former key, or with a record of the wrong shape, are anonymous.
func TestAuthenticatorIgnoresLegacyAndForeignRecords(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)

	for name, storage := range map[string]map[string]any{
		"legacy key":  {"arkoren:auth:user": 7},
		"wrong shape": {"arkoren:auth:session": "7"},
	} {
		current := session.New(time.Now().Add(time.Hour), storage)

		if _, err := authenticate(guard, current); !errors.Is(err, auth.ErrNoCredentials) {
			t.Errorf("%s = %v", name, err)
		}
	}
}

// TestHandleSelectsSessionBridge verifies the login reads only the session of
// its handle.
func TestHandleSelectsSessionBridge(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	guard, _ := newGuard(t, func(policy *auth.GuardBuilder) {
		policy.Handle(handle)
	})
	current := newSession()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	custom := r.WithContext(session.NewContextFor(r.Context(), handle, current))
	begin(t, guard, custom, "7")

	if _, err := guard.Authenticator()(custom); err != nil {
		t.Fatal(err)
	}

	if _, err := authenticate(guard, current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("default session was read: %v", err)
	}
}

// TestZeroHandleRestoresDefaultSession verifies a later zero handle replaces a
// custom selection.
func TestZeroHandleRestoresDefaultSession(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, func(policy *auth.GuardBuilder) {
		policy.Handle(session.NewHandle()).Handle(session.Handle{})
	})
	current := newSession()
	begin(t, guard, sessionRequest(current), "7")

	if _, err := authenticate(guard, current); err != nil {
		t.Fatal(err)
	}
}

// TestNewGuardRejectsNilCollaborators verifies a nil provider or store is an
// error.
func TestNewGuardRejectsNilCollaborators(t *testing.T) {
	t.Parallel()

	provider, _ := newDirectory(t)

	if guard, err := auth.NewGuard(nil, cache.New(memorycache.New())); err == nil || guard != nil {
		t.Fatalf("nil provider: guard = %v, err = %v", guard, err)
	}

	if guard, err := auth.NewGuard(provider, nil); err == nil || guard != nil {
		t.Fatalf("nil store: guard = %v, err = %v", guard, err)
	}
}

// TestNewGuardRejectsInvalidSettings verifies settings that cannot work are
// construction errors.
func TestNewGuardRejectsInvalidSettings(t *testing.T) {
	t.Parallel()

	provider, _ := newDirectory(t)
	store := cache.New(memorycache.New())

	for _, configure := range []auth.GuardBuilderFunc{
		func(policy *auth.GuardBuilder) { policy.ChallengeTTL(0) },
		func(policy *auth.GuardBuilder) { policy.MaxAttempts(0) },
		func(policy *auth.GuardBuilder) { policy.SecondFactor(nil) },
	} {
		if guard, err := auth.NewGuard(provider, store, configure); err == nil || guard != nil {
			t.Errorf("guard = %v, err = %v", guard, err)
		}
	}
}

// TestNewGuardRunsCallbacksOnceInOrder verifies callbacks share one draft and
// run once, in order.
func TestNewGuardRunsCallbacksOnceInOrder(t *testing.T) {
	t.Parallel()

	provider, _ := newDirectory(t)
	var order []int
	var retained *auth.GuardBuilder

	_, err := auth.NewGuard(provider, cache.New(memorycache.New()), func(policy *auth.GuardBuilder) {
		retained = policy
		order = append(order, 1)
	}, func(policy *auth.GuardBuilder) {
		if retained != policy {
			t.Error("callbacks used different drafts")
		}

		order = append(order, 2)
	})

	if err != nil || len(order) != 2 || order[0] != 1 {
		t.Fatalf("order = %v, err = %v", order, err)
	}
}

// TestLoginConformance runs the authenticator suite against Login.
func TestLoginConformance(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)

	authtest.Run(t, guard.Authenticator(), func(*testing.T) *http.Request {
		return sessionRequest(newSession())
	}, func(t *testing.T) *http.Request {
		r := sessionRequest(newSession())
		begin(t, guard, r, "7")

		return r
	})
}

// TestCompleteFailsWhenVersionChangedDuringChallenge verifies a credential
// change between Begin and Complete rejects the login and clears the pending
// login.
func TestCompleteFailsWhenVersionChangedDuringChallenge(t *testing.T) {
	t.Parallel()

	guard, driver := newGuard(t, withChecks(secondFactor(true, nil)))
	current := newSession()
	r := sessionRequest(current)
	begin(t, guard, r, "7")

	if err := driver.Put(auth.UserRecord{Subject: "7", Version: "v2"}); err != nil {
		t.Fatal(err)
	}

	if err := guard.Complete(r, otp); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("complete = %v", err)
	}

	if _, ok := current.All()["arkoren:auth:pending"]; ok {
		t.Fatal("pending login survived a version change")
	}

	if _, err := authenticate(guard, current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("logged in despite version change: %v", err)
	}
}

// TestPendingFailsWhenVersionChanged verifies Pending rejects and clears a
// pending login whose user version changed.
func TestPendingFailsWhenVersionChanged(t *testing.T) {
	t.Parallel()

	guard, driver := newGuard(t, withChecks(secondFactor(true, nil)))
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	if err := driver.Put(auth.UserRecord{Subject: "7", Version: "v2"}); err != nil {
		t.Fatal(err)
	}

	if _, err := guard.Pending(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending = %v", err)
	}

	if _, err := guard.Pending(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending survived = %v", err)
	}
}

// TestCompleteSucceedsWhenVersionUnchanged verifies a user whose version is
// unchanged completes even if other fields of the record changed.
func TestCompleteSucceedsWhenVersionUnchanged(t *testing.T) {
	t.Parallel()

	guard, driver := newGuard(t, withChecks(secondFactor(true, nil)))
	current := newSession()
	r := sessionRequest(current)
	begin(t, guard, r, "7")

	if err := driver.Put(auth.UserRecord{Subject: "7", Version: "v1", Value: user{"renamed"}}); err != nil {
		t.Fatal(err)
	}

	if err := guard.Complete(r, otp); err != nil {
		t.Fatal(err)
	}

	if _, err := authenticate(guard, current); err != nil {
		t.Fatal(err)
	}
}
