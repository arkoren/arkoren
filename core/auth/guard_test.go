package auth_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
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

// federatedMethod is a module-defined method name: core no longer declares one.
const federatedMethod auth.Method = "federated"

// Request bodies of the challenge and confirm routes: the code "ok" verifies.
const (
	goodOTP = `{"method":"otp","code":"ok"}`
	badOTP  = `{"method":"otp","code":"bad"}`
)

// codeVerify is the Verify of the test credentials: it accepts the code "ok" and
// returns the factor of its method and kind, and rejects any other code with
// [auth.ErrInvalidCredential].
func codeVerify(method auth.Method, kind auth.Kind) func(*http.Request, string, jsontext.Value) (auth.Factor, error) {
	return func(_ *http.Request, _ string, input jsontext.Value) (auth.Factor, error) {
		var body struct {
			Code string `json:"code"`
		}

		if err := json.Unmarshal(input, &body); err != nil || body.Code != "ok" {
			return auth.Factor{}, auth.ErrInvalidCredential
		}

		return auth.Factor{Method: method, Kind: kind}, nil
	}
}

// credential returns a credential of method and kind whose Enrolled answers
// enrolled and err and whose Verify is [codeVerify].
func credential(method auth.Method, kind auth.Kind, enrolled func(subject string) (bool, error)) auth.Credential {
	return auth.Credential{
		Method: method,
		Kind:   kind,
		Enrolled: func(_ context.Context, subject string) (bool, error) {
			return enrolled(subject)
		},
		Verify: codeVerify(method, kind),
	}
}

// secondFactor returns the "otp" credential with a fixed enrolment answer and
// error.
func secondFactor(needed bool, err error) auth.Credential {
	return credential(auth.MethodOTP, auth.KindPossession, func(string) (bool, error) { return needed, err })
}

// withChecks registers second-factor credentials on a guard.
func withChecks(credentials ...auth.Credential) auth.GuardBuilderFunc {
	return func(policy *auth.GuardBuilder) {
		policy.SecondFactor(credentials...)
	}
}

// withStepUp registers step-up credentials on a guard.
func withStepUp(credentials ...auth.Credential) auth.GuardBuilderFunc {
	return func(policy *auth.GuardBuilder) {
		policy.StepUp(credentials...)
	}
}

// post returns a copy of r, which carries the session in its context, as a POST
// with body.
func post(r *http.Request, body string) *http.Request {
	clone := r.Clone(r.Context())
	clone.Method = http.MethodPost
	clone.Body = io.NopCloser(strings.NewReader(body))

	return clone
}

// challenge posts body to the guard's challenge route with r's session.
func challenge(guard *auth.Guard, r *http.Request, body string) error {
	return guard.ChallengeHandler()(httptest.NewRecorder(), post(r, body))
}

// confirm posts body to the guard's confirm route behind the guard's
// authenticator, as the application wiring does.
func confirm(guard *auth.Guard, r *http.Request, body string) error {
	return auth.Middleware(guard.Authenticator())(guard.ConfirmHandler())(httptest.NewRecorder(), post(r, body))
}

// wrongCode spends one attempt the way a client does: a challenge with a wrong
// code, which must be [auth.ErrInvalidCredential].
func wrongCode(t *testing.T, guard *auth.Guard, r *http.Request) {
	t.Helper()

	if err := challenge(guard, r, badOTP); !errors.Is(err, auth.ErrInvalidCredential) {
		t.Fatalf("wrong code = %v", err)
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

	outcome, err := guard.Begin(r, subject, password)

	if err != nil {
		t.Fatal(err)
	}

	return outcome.Complete()
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

	guard, _ := newGuard(t, withChecks(secondFactor(false, nil), credential(hardwareMethod, auth.KindPossession, no)))

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

	guard, _ := newGuard(t, withChecks(credential(hardwareMethod, auth.KindPossession, no), secondFactor(true, nil)))

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

	guard, _ := newGuard(t, withChecks(credential(auth.MethodOTP, auth.KindPossession, func(string) (bool, error) {
		t.Error("second-factor check ran for a rejected user")

		return false, nil
	})))

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

	if err := challenge(guard, r, goodOTP); err != nil {
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
	guard, _ := newGuard(t, withChecks(credential(auth.MethodOTP, auth.KindPossession, func(string) (bool, error) {
		return needed, nil
	})))
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

	if err := challenge(guard, r, goodOTP); err != nil {
		t.Fatal(err)
	}

	before := current.ID()
	outcome, err := guard.Begin(r, "7", federated)

	if err != nil || !outcome.Complete() {
		t.Fatalf("begin = %v, %v", outcome, err)
	}

	identity, err := authenticate(guard, current)
	factors := identity.Factors()

	if err != nil || len(factors) != 3 || factors[0].Method != auth.MethodPassword || factors[1].Method != auth.MethodOTP || factors[2].Method != federatedMethod {
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

	guard, _ := newGuard(t, withChecks(credential(auth.MethodOTP, auth.KindPossession, func(subject string) (bool, error) {
		return subject == "7", nil
	})))
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

	if outcome, err := guard.Begin(r, "7", federated); err != nil || outcome.Complete() {
		t.Fatalf("begin = %v, %v", outcome, err)
	}

	if _, err := guard.Pending(r); err != nil {
		t.Fatal(err)
	}
}

// TestBeginDropsOlderPendingLogin verifies a direct login discards a pending one.
func TestBeginDropsOlderPendingLogin(t *testing.T) {
	t.Parallel()

	needed := true
	guard, _ := newGuard(t, withChecks(credential(auth.MethodOTP, auth.KindPossession, func(string) (bool, error) {
		return needed, nil
	})))
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

	if err := challenge(guard, r, goodOTP); err != nil {
		t.Fatal(err)
	}

	if err := challenge(guard, r, goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
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

	if err := challenge(guard, sessionRequest(current), goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
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

	if err := challenge(guard, r, goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
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

	if err := challenge(guard, r, goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
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

		if err := challenge(guard, r, goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
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

	for attempt := 1; attempt <= 3; attempt++ {
		before := current.ID()

		wrongCode(t, guard, r)

		if current.ID() == before {
			t.Fatalf("attempt %d did not rotate the session", attempt)
		}
	}

	if err := challenge(guard, r, goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("challenge after the limit = %v", err)
	}
}

// TestConfirmRequiresSession verifies the confirm route rejects an anonymous
// request, a pending login and a bearer identity, which is authenticated but not
// a browser session, and records nothing for them.
func TestConfirmRequiresSession(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), withStepUp(secondFactor(true, nil)))
	r := sessionRequest(newSession())

	if err := confirm(guard, r, goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("anonymous confirm = %v", err)
	}

	begin(t, guard, r, "7")

	if err := confirm(guard, r, goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending confirm = %v", err)
	}

	// A token identity of a logged-in user is not a browser session, so it cannot
	// confirm, and nothing is recorded on the user's real session.
	open, _ := newGuard(t, withStepUp(secondFactor(true, nil)))
	current := newSession()
	own := sessionRequest(current)
	begin(t, open, own, "7")

	bearer := post(own, goodOTP)
	bearer = bearer.WithContext(auth.NewContext(bearer.Context(), auth.NewIdentity("7")))

	if err := open.ConfirmHandler()(httptest.NewRecorder(), bearer); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("bearer confirm = %v", err)
	}

	if identity, err := authenticate(open, current); err != nil || len(identity.Factors()) != 1 {
		t.Fatalf("factors after a rejected confirm = %v, %v", identity.Factors(), err)
	}
}

// TestConfirmRejectsForeignSession verifies a session identity of one user
// cannot confirm on a session logged in as another, as can happen when two
// guards share a request.
func TestConfirmRejectsForeignSession(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withStepUp(secondFactor(true, nil)))
	mine, theirs := newSession(), newSession()
	begin(t, guard, sessionRequest(mine), "7")
	begin(t, guard, sessionRequest(theirs), "9")

	identity, err := authenticate(guard, mine)

	if err != nil {
		t.Fatal(err)
	}

	// The request carries user 7's session identity but user 9's session.
	r := post(sessionRequest(theirs), goodOTP)
	r = r.WithContext(auth.NewContext(r.Context(), identity))

	if err := guard.ConfirmHandler()(httptest.NewRecorder(), r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("confirm = %v", err)
	}

	if other, err := authenticate(guard, theirs); err != nil || len(other.Factors()) != 1 {
		t.Fatalf("factors after a rejected confirm = %v, %v", other.Factors(), err)
	}
}

// TestConfirmDoesNotResetAttempts verifies a successful confirmation gives back
// its own attempt but leaves the count of wrong codes, so confirming one method
// cannot reset the limit on guessing another.
func TestConfirmDoesNotResetAttempts(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withStepUp(secondFactor(true, nil), credential(auth.MethodPassword, auth.KindKnowledge, func(string) (bool, error) { return true, nil })), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(3)
	})
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	wrong := func() {
		if err := confirm(guard, r, badOTP); !errors.Is(err, auth.ErrInvalidCredential) {
			t.Fatalf("wrong code = %v", err)
		}
	}

	wrong()
	wrong()

	if err := confirm(guard, r, `{"method":"pwd","code":"ok"}`); err != nil {
		t.Fatalf("confirm = %v", err)
	}

	wrong()

	if err := confirm(guard, r, `{"method":"pwd","code":"ok"}`); !errors.Is(err, auth.ErrInvalidCredential) {
		t.Fatalf("correct code after the limit = %v", err)
	}
}

// TestConfirmAddsAndRefreshesFactors verifies ConfirmHandler lifts a session to
// multi level and replaces a factor of the same method and kind with the newer
// time.
func TestConfirmAddsAndRefreshesFactors(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, _ := newGuard(t, withStepUp(secondFactor(true, nil), credential(auth.MethodPassword, auth.KindKnowledge, func(string) (bool, error) { return true, nil })))
		current := newSession()
		r := sessionRequest(current)
		begin(t, guard, r, "7")

		time.Sleep(time.Hour)

		before := current.ID()

		if err := confirm(guard, r, goodOTP); err != nil {
			t.Fatal(err)
		}

		if err := confirm(guard, r, `{"method":"pwd","code":"ok"}`); err != nil {
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

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), withStepUp(secondFactor(true, nil)))
	current := newSession()
	r := sessionRequest(current)
	seen := map[string]bool{current.ID(): true}

	steps := []func() error{
		func() error { _, err := guard.Begin(r, "7", password); return err },
		func() error {
			if err := challenge(guard, r, badOTP); !errors.Is(err, auth.ErrInvalidCredential) {
				return err
			}

			return nil
		},
		func() error { return challenge(guard, r, goodOTP) },
		func() error { return confirm(guard, r, goodOTP) },
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
		"begin":     beginErr,
		"pending":   pendingErr,
		"challenge": challenge(guard, r, goodOTP),
		"logout":    guard.Logout(r),
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
		func(policy *auth.GuardBuilder) { policy.SecondFactor(auth.Credential{Method: auth.MethodOTP}) },
		func(policy *auth.GuardBuilder) { policy.SecondFactor(secondFactor(true, nil), secondFactor(true, nil)) },
		func(policy *auth.GuardBuilder) {
			policy.StepUp(auth.Credential{Enrolled: secondFactor(true, nil).Enrolled})
		},
		func(policy *auth.GuardBuilder) { policy.FreshFor(0) },
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

	if err := challenge(guard, r, goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
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

	if err := challenge(guard, r, goodOTP); err != nil {
		t.Fatal(err)
	}

	if _, err := authenticate(guard, current); err != nil {
		t.Fatal(err)
	}
}

// grantsOf returns a Grants function answering roles and scopes, which counts
// its calls in calls.
func grantsOf(calls *atomic.Int64, roles, scopes []string) auth.GuardBuilderFunc {
	return func(policy *auth.GuardBuilder) {
		policy.Grants(func(_ context.Context, _ string) ([]string, []string, error) {
			calls.Add(1)

			return roles, scopes, nil
		})
	}
}

// TestAuthenticatorSetsGrants verifies the roles and scopes from Grants reach
// the session identity.
func TestAuthenticatorSetsGrants(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	guard, _ := newGuard(t, grantsOf(&calls, []string{"editor", "viewer"}, []string{"posts:write"}))
	current := newSession()
	begin(t, guard, sessionRequest(current), "7")

	identity, err := authenticate(guard, current)

	if err != nil {
		t.Fatal(err)
	}

	if roles, scopes := identity.Roles(), identity.Scopes(); !slices.Equal(roles, []string{"editor", "viewer"}) || !slices.Equal(scopes, []string{"posts:write"}) {
		t.Fatalf("roles = %v, scopes = %v", roles, scopes)
	}
}

// TestAuthenticatorGrantsErrorFailsAuthentication verifies a failing Grants
// stops the request with its error instead of yielding an identity.
func TestAuthenticatorGrantsErrorFailsAuthentication(t *testing.T) {
	t.Parallel()

	failure := errors.New("store down")
	guard, _ := newGuard(t, func(policy *auth.GuardBuilder) {
		policy.Grants(func(context.Context, string) ([]string, []string, error) {
			return []string{"admin"}, []string{"posts:write"}, failure
		})
	})
	current := newSession()
	begin(t, guard, sessionRequest(current), "7")

	identity, err := authenticate(guard, current)

	if !errors.Is(err, failure) || !strings.HasPrefix(err.Error(), "auth: load grants: ") || identity.Subject() != "" || identity.Roles() != nil {
		t.Fatalf("identity = %q %v, err = %v", identity.Subject(), identity.Roles(), err)
	}
}

// TestAuthenticatorGrantsNoCredentialsStaysMatchable verifies the wrapped
// loader error still matches ErrNoCredentials, which makes Middleware treat the
// request as anonymous.
func TestAuthenticatorGrantsNoCredentialsStaysMatchable(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, func(policy *auth.GuardBuilder) {
		policy.Grants(func(context.Context, string) ([]string, []string, error) {
			return nil, nil, auth.ErrNoCredentials
		})
	})
	current := newSession()
	begin(t, guard, sessionRequest(current), "7")

	if _, err := authenticate(guard, current); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("err = %v", err)
	}
}

// beginWithFailingGrants logs subject "7" in through the second factor on a
// guard whose loader fails with failure while fail is set, then runs Begin with
// another factor with the loader failing. It returns the guard, the session and
// the outcome and error of that Begin; fail is cleared before returning.
func beginWithFailingGrants(t *testing.T, failure error) (*auth.Guard, *session.Session, auth.Outcome, error) {
	t.Helper()

	var fail atomic.Bool
	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.Grants(func(context.Context, string) ([]string, []string, error) {
			if fail.Load() {
				return nil, nil, failure
			}

			return nil, nil, nil
		})
	})
	current := newSession()
	r := sessionRequest(current)
	begin(t, guard, r, "7")

	if err := challenge(guard, r, goodOTP); err != nil {
		t.Fatal(err)
	}

	fail.Store(true)
	outcome, err := guard.Begin(r, "7", federated)
	fail.Store(false)

	return guard, current, outcome, err
}

// TestBeginSameSubjectIgnoresFailingGrants verifies a store outage cannot break
// a step-up login: Begin never loads grants, so the factor is still added.
func TestBeginSameSubjectIgnoresFailingGrants(t *testing.T) {
	t.Parallel()

	guard, current, outcome, err := beginWithFailingGrants(t, errors.New("store down"))

	if err != nil || !outcome.Complete() {
		t.Fatalf("begin = %v, %v", outcome, err)
	}

	identity, err := authenticate(guard, current)

	if err != nil || len(identity.Factors()) != 3 {
		t.Fatalf("factors = %+v, %v", identity.Factors(), err)
	}
}

// TestBeginSameSubjectIgnoresAnonymousGrants verifies a loader answering
// ErrNoCredentials cannot make Begin park a logged-in user as a new pending
// login.
func TestBeginSameSubjectIgnoresAnonymousGrants(t *testing.T) {
	t.Parallel()

	guard, current, outcome, err := beginWithFailingGrants(t, auth.ErrNoCredentials)

	if err != nil || !outcome.Complete() {
		t.Fatalf("begin = %v, %v", outcome, err)
	}

	if _, err := guard.Pending(sessionRequest(current)); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending login exists: %v", err)
	}
}

// TestAuthenticatorWithoutGrantsHasNone verifies a guard without Grants builds
// session identities with no roles and no scopes.
func TestAuthenticatorWithoutGrantsHasNone(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	current := newSession()
	begin(t, guard, sessionRequest(current), "7")

	identity, err := authenticate(guard, current)

	if err != nil || identity.Roles() != nil || identity.Scopes() != nil {
		t.Fatalf("roles = %v, scopes = %v, err = %v", identity.Roles(), identity.Scopes(), err)
	}
}

// TestAuthenticatorCallsGrantsOncePerAuthentication verifies Grants runs once
// for each authenticated request and never for an anonymous one.
func TestAuthenticatorCallsGrantsOncePerAuthentication(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	guard, _ := newGuard(t, grantsOf(&calls, nil, nil))

	if _, err := authenticate(guard, newSession()); !errors.Is(err, auth.ErrNoCredentials) {
		t.Fatalf("anonymous err = %v", err)
	}

	current := newSession()
	begin(t, guard, sessionRequest(current), "7")

	for range 2 {
		if _, err := authenticate(guard, current); err != nil {
			t.Fatal(err)
		}
	}

	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}
