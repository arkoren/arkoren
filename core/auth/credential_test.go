package auth_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/foundation/problem"
)

// hardwareMethod is the method of the second credential of the tests that need
// several methods.
const hardwareMethod = auth.MethodHardwareKey

// yes and no are the Enrolled answers of the credential fixtures.
func yes(string) (bool, error) { return true, nil }
func no(string) (bool, error)  { return false, nil }

// freshGuard returns a guard whose users are enrolled in the password and OTP
// step-up methods, so a stale session answers [auth.ErrStepUpRequired].
func freshGuard(t *testing.T) *auth.Guard {
	t.Helper()

	guard, _ := newGuard(t, withStepUp(
		credential(auth.MethodPassword, auth.KindKnowledge, yes),
		credential(auth.MethodOTP, auth.KindPossession, yes),
	))

	return guard
}

// freshness runs [auth.Guard.Fresh] for the identity the guard's authenticator
// builds from r's session.
func freshness(guard *auth.Guard, r *http.Request) error {
	var result error

	handler := auth.Middleware(guard.Authenticator())(func(_ http.ResponseWriter, r *http.Request) error {
		result = guard.Fresh(r)

		return nil
	})

	if err := handler(httptest.NewRecorder(), r); err != nil {
		return err
	}

	return result
}

// stepUpMethods returns the "methods" extension of err, which must be a problem.
func stepUpMethods(t *testing.T, err error) []auth.Method {
	t.Helper()

	details, ok := errors.AsType[problem.Details](err)

	if !ok {
		t.Fatalf("err = %v is not a problem", err)
	}

	value, _ := details.Extension("methods")
	methods, _ := value.([]auth.Method)

	return methods
}

// TestSecondFactorRegistrationOrder verifies the methods of an outcome, of
// Pending and of the pending route follow registration order, and that calls to
// SecondFactor append.
func TestSecondFactorRegistrationOrder(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, func(policy *auth.GuardBuilder) {
		policy.SecondFactor(credential(hardwareMethod, auth.KindPossession, yes))
		policy.SecondFactor(credential(auth.MethodOTP, auth.KindPossession, yes), credential(auth.MethodSoftwareKey, auth.KindPossession, no))
	})
	r := sessionRequest(newSession())

	outcome, err := guard.Begin(r, "7", password)

	if err != nil || outcome.Complete() || !slices.Equal(outcome.Methods(), []auth.Method{hardwareMethod, auth.MethodOTP}) {
		t.Fatalf("outcome = %v, %v, err = %v", outcome.Complete(), outcome.Methods(), err)
	}

	pending, err := guard.Pending(r)

	if err != nil || !slices.Equal(pending.Methods, []auth.Method{hardwareMethod, auth.MethodOTP}) {
		t.Fatalf("pending = %+v, %v", pending, err)
	}

	recorder := httptest.NewRecorder()

	if err := guard.PendingHandler()(recorder, r); err != nil || recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != `{"methods":["hwk","otp"]}` {
		t.Fatalf("pending route = %d %q, %v", recorder.Code, recorder.Body.String(), err)
	}
}

// TestOutcomeResponse verifies the 204 of a complete login and the 202 body of a
// parked one.
func TestOutcomeResponse(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)))
	parked, err := guard.Begin(sessionRequest(newSession()), "7", password)

	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()

	if err := parked.Response(recorder); err != nil || recorder.Code != http.StatusAccepted || strings.TrimSpace(recorder.Body.String()) != `{"second_factor":{"methods":["otp"]}}` {
		t.Fatalf("parked = %d %q, %v", recorder.Code, recorder.Body.String(), err)
	}

	open, _ := newGuard(t)
	done, err := open.Begin(sessionRequest(newSession()), "7", password)
	recorder = httptest.NewRecorder()

	if err != nil || done.Response(recorder) != nil || recorder.Code != http.StatusNoContent {
		t.Fatalf("complete = %d, %v", recorder.Code, err)
	}
}

// TestDuplicateMethodRejected verifies a registry holds each method once, and
// that the same method may be in both registries.
func TestDuplicateMethodRejected(t *testing.T) {
	t.Parallel()

	provider, _ := newDirectory(t)
	store := cache.New(memorycache.New())

	for name, configure := range map[string]auth.GuardBuilderFunc{
		"second factor": withChecks(secondFactor(true, nil), secondFactor(true, nil)),
		"step-up":       withStepUp(secondFactor(true, nil), secondFactor(true, nil)),
		"nil enrolled":  withChecks(auth.Credential{Method: auth.MethodOTP}),
		"empty method":  withChecks(auth.Credential{Enrolled: secondFactor(true, nil).Enrolled}),
	} {
		if guard, err := auth.NewGuard(provider, store, configure); err == nil || guard != nil {
			t.Errorf("%s: guard = %v, err = %v", name, guard, err)
		}
	}

	if _, err := auth.NewGuard(provider, store, withChecks(secondFactor(true, nil)), withStepUp(secondFactor(true, nil))); err != nil {
		t.Fatalf("one method in both registries: %v", err)
	}
}

// TestBeginMultiFactorSkipsChallenge verifies a login whose factors already reach
// LevelMulti is not parked and never asks whether the user is enrolled.
func TestBeginMultiFactorSkipsChallenge(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(credential(auth.MethodOTP, auth.KindPossession, func(string) (bool, error) {
		t.Error("enrolment was checked for a multi-factor login")

		return true, nil
	})))
	current := newSession()

	outcome, err := guard.Begin(sessionRequest(current), "7", password, hardware)

	if err != nil || !outcome.Complete() {
		t.Fatalf("outcome = %v, err = %v", outcome.Complete(), err)
	}

	identity, err := authenticate(guard, current)

	if err != nil || identity.Level() != auth.LevelMulti {
		t.Fatalf("identity = %v, %v", identity.Level(), err)
	}
}

// TestBeginRequiresFactor verifies Begin without a factor is an error and
// changes nothing.
func TestBeginRequiresFactor(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	current := newSession()

	if _, err := guard.Begin(sessionRequest(current), "7"); err == nil || len(current.All()) != 0 {
		t.Fatalf("begin without a factor = %v", err)
	}
}

// TestPendingIsReadOnly verifies reading the pending login, by Pending or its
// route, spends no attempt: with one attempt allowed, many reads leave it usable.
func TestPendingIsReadOnly(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(1)
	})
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	for range 5 {
		if _, err := guard.Pending(r); err != nil {
			t.Fatal(err)
		}

		if err := guard.PendingHandler()(httptest.NewRecorder(), r); err != nil {
			t.Fatal(err)
		}
	}

	if err := challenge(guard, r, goodOTP); err != nil {
		t.Fatalf("challenge after reads = %v", err)
	}
}

// TestChallengeUnknownMethod422 verifies a missing, unregistered or redirect-only
// method is 422 and costs no attempt.
func TestChallengeUnknownMethod422(t *testing.T) {
	t.Parallel()

	redirect := credential(auth.MethodSoftwareKey, auth.KindPossession, yes)
	redirect.Verify = nil

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil), redirect), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(1)
	})
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	for _, body := range []string{`{"code":"ok"}`, `{"method":"nope","code":"ok"}`, `{"method":"swk"}`, `{"method":7}`} {
		err := challenge(guard, r, body)

		if !errors.Is(err, auth.ErrUnknownMethod) || auth.ErrUnknownMethod.Status() != http.StatusUnprocessableEntity {
			t.Fatalf("challenge(%s) = %v", body, err)
		}
	}

	if err := challenge(guard, r, goodOTP); err != nil {
		t.Fatalf("challenge after unknown methods = %v", err)
	}
}

// TestChallengeRejectsBadBody verifies a body that is not a JSON object is a 400
// and costs no attempt.
func TestChallengeRejectsBadBody(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(1)
	})
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	for _, body := range []string{``, `[1]`, `{`} {
		details, ok := errors.AsType[problem.Details](challenge(guard, r, body))

		if !ok || details.Status() != http.StatusBadRequest {
			t.Fatalf("challenge(%q) = %v", body, details)
		}
	}

	if err := challenge(guard, r, goodOTP); err != nil {
		t.Fatalf("challenge after bad bodies = %v", err)
	}
}

// TestChallengeNotEnrolledLooksLikeWrongCode verifies a method the user is not
// enrolled in answers exactly like a wrong code and costs the attempt the same.
func TestChallengeNotEnrolledLooksLikeWrongCode(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil), credential(hardwareMethod, auth.KindPossession, no)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(2)
	})
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	wrong := challenge(guard, r, badOTP)
	absent := challenge(guard, r, `{"method":"hwk","code":"ok"}`)

	if !errors.Is(wrong, auth.ErrInvalidCredential) || !errors.Is(absent, auth.ErrInvalidCredential) {
		t.Fatalf("wrong = %v, not enrolled = %v", wrong, absent)
	}

	// Even the correct hardware code did not verify, and both attempts counted.
	if _, err := guard.Pending(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending after two attempts = %v", err)
	}
}

// TestChallengeInfraErrorRefundsAttempt verifies an error other than a wrong code,
// from Verify or Enrolled, passes through and gives the attempt back.
func TestChallengeInfraErrorRefundsAttempt(t *testing.T) {
	t.Parallel()

	outage := errors.New("backend down")
	failing := secondFactor(true, nil)
	failing.Verify = func(*http.Request, string, jsontext.Value) (auth.Factor, error) {
		return auth.Factor{}, outage
	}

	enrolledFailure := false
	flaky := credential(hardwareMethod, auth.KindPossession, func(string) (bool, error) {
		if enrolledFailure {
			return false, outage
		}

		return true, nil
	})

	guard, _ := newGuard(t, withChecks(failing, flaky), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(1)
	})
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	enrolledFailure = true

	for range 3 {
		if err := challenge(guard, r, `{"method":"hwk","code":"ok"}`); !errors.Is(err, outage) {
			t.Fatalf("enrolment outage = %v", err)
		}

		if err := challenge(guard, r, goodOTP); !errors.Is(err, outage) {
			t.Fatalf("verify outage = %v", err)
		}
	}

	enrolledFailure = false

	if err := challenge(guard, r, `{"method":"hwk","code":"ok"}`); err != nil {
		t.Fatalf("challenge after refunds = %v", err)
	}
}

// TestChallengeAttemptsSharedAcrossMethods verifies one counter covers every
// method: wrong codes of different methods lock the user out together.
func TestChallengeAttemptsSharedAcrossMethods(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil), credential(hardwareMethod, auth.KindPossession, yes)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(2)
	})
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	if err := challenge(guard, r, badOTP); !errors.Is(err, auth.ErrInvalidCredential) {
		t.Fatalf("first = %v", err)
	}

	if err := challenge(guard, r, `{"method":"hwk","code":"bad"}`); !errors.Is(err, auth.ErrInvalidCredential) {
		t.Fatalf("second = %v", err)
	}

	begin(t, guard, r, "7")

	if err := challenge(guard, r, goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("correct code after the shared limit = %v", err)
	}
}

// TestChallengeKeepsKindOfVerifiedFactor verifies the factor of the challenge
// keeps the kind Verify reports, defaulting to the credential's.
func TestChallengeKeepsKindOfVerifiedFactor(t *testing.T) {
	t.Parallel()

	passkey := credential(hardwareMethod, auth.KindPossession, yes)
	passkey.Verify = func(*http.Request, string, jsontext.Value) (auth.Factor, error) {
		return auth.Factor{Method: "ignored", Kind: auth.KindInherence}, nil
	}

	guard, _ := newGuard(t, withChecks(passkey))
	current := newSession()
	r := sessionRequest(current)
	begin(t, guard, r, "7")

	if err := challenge(guard, r, `{"method":"hwk"}`); err != nil {
		t.Fatal(err)
	}

	identity, err := authenticate(guard, current)
	factors := identity.Factors()

	if err != nil || len(factors) != 2 || factors[1].Method != hardwareMethod || factors[1].Kind != auth.KindInherence {
		t.Fatalf("factors = %+v, %v", factors, err)
	}
}

// TestFactorReplacedByMethodAndKind verifies a factor replaces an older one only
// when both method and kind match, so one method can prove two kinds.
func TestFactorReplacedByMethodAndKind(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, _ := newGuard(t)
		current := newSession()
		r := sessionRequest(current)
		begin(t, guard, r, "7")

		time.Sleep(time.Minute)

		inherence := auth.Factor{Method: auth.MethodPassword, Kind: auth.KindInherence}

		if _, err := guard.Begin(r, "7", inherence); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Minute)

		if _, err := guard.Begin(r, "7", password); err != nil {
			t.Fatal(err)
		}

		identity, err := authenticate(guard, current)
		factors := identity.Factors()

		if err != nil || len(factors) != 2 {
			t.Fatalf("factors = %+v, %v", factors, err)
		}

		if factors[0].Kind != auth.KindKnowledge || !factors[0].At.Equal(time.Now()) || factors[1].Kind != auth.KindInherence || !factors[1].At.Equal(time.Now().Add(-time.Minute)) {
			t.Fatalf("factors = %+v", factors)
		}
	})
}

// TestFactorTimeIsServerClock verifies a time claimed by the caller is ignored.
func TestFactorTimeIsServerClock(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, _ := newGuard(t)
		current := newSession()
		claimed := auth.Factor{Method: federatedMethod, Kind: auth.KindFederated, At: time.Now().Add(-48 * time.Hour)}

		if _, err := guard.Begin(sessionRequest(current), "7", claimed); err != nil {
			t.Fatal(err)
		}

		identity, err := authenticate(guard, current)

		if err != nil || !identity.Factors()[0].At.Equal(time.Now()) {
			t.Fatalf("factors = %+v, %v", identity.Factors(), err)
		}
	})
}

// TestSilentReloginNotFreshWithStepUpMethod verifies a same-subject federated
// re-login does not refresh a user who has a step-up method.
func TestSilentReloginNotFreshWithStepUpMethod(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard := freshGuard(t)
		current := newSession()
		r := sessionRequest(current)
		begin(t, guard, r, "7")

		if err := freshness(guard, r); err != nil {
			t.Fatalf("fresh after login = %v", err)
		}

		time.Sleep(time.Hour)

		if _, err := guard.Begin(r, "7", auth.Factor{Method: federatedMethod, Kind: auth.KindFederated}); err != nil {
			t.Fatal(err)
		}

		if err := freshness(guard, r); !errors.Is(err, auth.ErrStepUpRequired) {
			t.Fatalf("fresh after a silent re-login = %v", err)
		}
	})
}

// TestFederatedOnlyUserFreshAfterLogin verifies a user with no enrolled step-up
// method is fresh after any login and must log in again when it ages.
func TestFederatedOnlyUserFreshAfterLogin(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, _ := newGuard(t, withStepUp(credential(auth.MethodPassword, auth.KindKnowledge, no)))
		current := newSession()
		r := sessionRequest(current)

		if _, err := guard.Begin(r, "7", auth.Factor{Method: federatedMethod, Kind: auth.KindFederated}); err != nil {
			t.Fatal(err)
		}

		if err := freshness(guard, r); err != nil {
			t.Fatalf("fresh after federated login = %v", err)
		}

		time.Sleep(time.Hour)

		if err := freshness(guard, r); !errors.Is(err, auth.ErrNoStepUpMethod) {
			t.Fatalf("stale = %v", err)
		}

		if _, err := guard.Begin(r, "7", auth.Factor{Method: federatedMethod, Kind: auth.KindFederated}); err != nil {
			t.Fatal(err)
		}

		if err := freshness(guard, r); err != nil {
			t.Fatalf("fresh after a new federated login = %v", err)
		}
	})
}

// TestStepUpRequiredListsMethods verifies the problem names the user's enrolled
// step-up methods in registration order, skips methods they lack, and still
// matches ErrStepUpRequired.
func TestStepUpRequiredListsMethods(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, _ := newGuard(t, withStepUp(
			credential(auth.MethodOTP, auth.KindPossession, yes),
			credential(auth.MethodSoftwareKey, auth.KindPossession, no),
			credential(auth.MethodPassword, auth.KindKnowledge, yes),
		))
		r := sessionRequest(newSession())
		begin(t, guard, r, "7")

		time.Sleep(time.Hour)

		err := freshness(guard, r)

		if !errors.Is(err, auth.ErrStepUpRequired) || !slices.Equal(stepUpMethods(t, err), []auth.Method{auth.MethodOTP, auth.MethodPassword}) {
			t.Fatalf("err = %v, methods = %v", err, stepUpMethods(t, err))
		}

		details, _ := errors.AsType[problem.Details](err)

		if details.Status() != http.StatusForbidden {
			t.Fatalf("status = %d", details.Status())
		}
	})
}

// TestFreshFailsClosedOnEnrolledError verifies a failing enrolment lookup is an
// error, not "no step-up method", which would make any factor count.
func TestFreshFailsClosedOnEnrolledError(t *testing.T) {
	t.Parallel()

	outage := errors.New("backend down")
	broken := false
	guard, _ := newGuard(t, withStepUp(credential(auth.MethodPassword, auth.KindKnowledge, func(string) (bool, error) {
		if broken {
			return false, outage
		}

		return true, nil
	})))
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")
	broken = true

	if err := freshness(guard, r); !errors.Is(err, outage) {
		t.Fatalf("fresh = %v", err)
	}
}

// TestNoStepUpMethod verifies a stale session of a user who cannot step up is
// ErrNoStepUpMethod, a different problem from ErrStepUpRequired.
func TestNoStepUpMethod(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, _ := newGuard(t)
		r := sessionRequest(newSession())
		begin(t, guard, r, "7")

		time.Sleep(time.Hour)

		err := freshness(guard, r)

		if !errors.Is(err, auth.ErrNoStepUpMethod) || errors.Is(err, auth.ErrStepUpRequired) || auth.ErrNoStepUpMethod.Status() != http.StatusForbidden {
			t.Fatalf("err = %v", err)
		}

		if auth.ErrNoStepUpMethod.Type() == auth.ErrStepUpRequired.Type() {
			t.Fatal("the problems share a type")
		}
	})
}

// TestFreshForSetsDefaultWindow verifies FreshFor changes the window of Fresh and
// that the default is fifteen minutes.
func TestFreshForSetsDefaultWindow(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		defaultGuard := freshGuard(t)
		short, _ := newGuard(t, withStepUp(credential(auth.MethodPassword, auth.KindKnowledge, yes)), func(policy *auth.GuardBuilder) {
			policy.FreshFor(time.Minute)
		})
		a, b := sessionRequest(newSession()), sessionRequest(newSession())
		begin(t, defaultGuard, a, "7")
		begin(t, short, b, "7")

		time.Sleep(2 * time.Minute)

		if err := freshness(defaultGuard, a); err != nil {
			t.Fatalf("default window after 2m = %v", err)
		}

		if err := freshness(short, b); !errors.Is(err, auth.ErrStepUpRequired) {
			t.Fatalf("short window after 2m = %v", err)
		}

		time.Sleep(14 * time.Minute)

		if err := freshness(defaultGuard, a); !errors.Is(err, auth.ErrStepUpRequired) {
			t.Fatalf("default window after 16m = %v", err)
		}
	})
}

// TestConfirmHandlerRefreshesFreshness verifies a confirmation with a step-up
// method makes a stale session fresh and keeps the login record.
func TestConfirmHandlerRefreshesFreshness(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard := freshGuard(t)
		current := newSession()
		r := sessionRequest(current)
		begin(t, guard, r, "7")
		record := loginsOf(t, guard, "7")[0].ID

		time.Sleep(time.Hour)

		if err := freshness(guard, r); !errors.Is(err, auth.ErrStepUpRequired) {
			t.Fatalf("stale = %v", err)
		}

		if err := confirm(guard, r, badOTP); !errors.Is(err, auth.ErrInvalidCredential) {
			t.Fatalf("wrong confirmation = %v", err)
		}

		if err := confirm(guard, r, goodOTP); err != nil {
			t.Fatal(err)
		}

		if err := freshness(guard, r); err != nil {
			t.Fatalf("after confirm = %v", err)
		}

		if records := loginsOf(t, guard, "7"); len(records) != 1 || records[0].ID != record {
			t.Fatalf("records = %+v", records)
		}
	})
}

// TestConfirmHandlerLocksOut verifies the confirm route shares the attempt limit
// and answers a locked-out user like a wrong code.
func TestConfirmHandlerLocksOut(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withStepUp(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(2)
	})
	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	for range 2 {
		if err := confirm(guard, r, badOTP); !errors.Is(err, auth.ErrInvalidCredential) {
			t.Fatalf("wrong confirmation = %v", err)
		}
	}

	if err := confirm(guard, r, goodOTP); !errors.Is(err, auth.ErrInvalidCredential) {
		t.Fatalf("correct code after the limit = %v", err)
	}
}

// TestRecentLoginIgnoresSameSubjectBegin verifies a silent re-login by the same
// subject keeps the login record, so it does not make the login recent, while a
// new login of that user does.
func TestRecentLoginIgnoresSameSubjectBegin(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, _ := newGuard(t)
		r := sessionRequest(newSession())
		begin(t, guard, r, "7")

		if err := guard.RecentLogin(r, time.Minute); err != nil {
			t.Fatalf("new login = %v", err)
		}

		time.Sleep(10 * time.Minute)

		if _, err := guard.Begin(r, "7", auth.Factor{Method: federatedMethod, Kind: auth.KindFederated}); err != nil {
			t.Fatal(err)
		}

		if err := guard.RecentLogin(r, 5*time.Minute); !errors.Is(err, auth.ErrNoStepUpMethod) {
			t.Fatalf("after a silent re-login = %v", err)
		}

		if err := guard.RecentLogin(r, 15*time.Minute); err != nil {
			t.Fatalf("within a wide window = %v", err)
		}

		if err := guard.RecentLogin(r, 0); !errors.Is(err, auth.ErrNoStepUpMethod) {
			t.Fatalf("zero window = %v", err)
		}

		other := sessionRequest(newSession())
		begin(t, guard, other, "7")

		if err := guard.RecentLogin(other, 5*time.Minute); err != nil {
			t.Fatalf("new login on another device = %v", err)
		}
	})
}

// TestRecentLoginRequiresLogin verifies an anonymous session is
// ErrUnauthenticated.
func TestRecentLoginRequiresLogin(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)

	if err := guard.RecentLogin(sessionRequest(newSession()), time.Hour); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}
}

// TestBearerIdentityNotSession verifies only the guard's authenticator marks an
// identity as a session, and RequireSession rejects the others.
func TestBearerIdentityNotSession(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	current := newSession()
	begin(t, guard, sessionRequest(current), "7")

	sessionIdentity, err := authenticate(guard, current)

	if err != nil || !sessionIdentity.Session() {
		t.Fatalf("session identity = %v, %v", sessionIdentity.Session(), err)
	}

	bearer, err := auth.Bearer(func(_ context.Context, _ string) (auth.Identity, error) {
		return auth.NewIdentity("7", func(principal *auth.IdentityBuilder) {
			principal.Factors(password, otp)
		}), nil
	})(bearerRequest("Bearer token"))

	if err != nil || bearer.Session() {
		t.Fatalf("bearer identity session = %v, %v", bearer.Session(), err)
	}

	if auth.NewIdentity("7").Session() {
		t.Fatal("a built identity is a session")
	}

	if reached, err := guarded(auth.RequireSession(), &sessionIdentity); err != nil || !reached {
		t.Fatalf("session reached = %v, err = %v", reached, err)
	}

	if reached, err := guarded(auth.RequireSession(), &bearer); !errors.Is(err, auth.ErrUnauthenticated) || reached {
		t.Fatalf("bearer reached = %v, err = %v", reached, err)
	}

	if reached, err := guarded(auth.RequireSession(), nil); !errors.Is(err, auth.ErrUnauthenticated) || reached {
		t.Fatalf("anonymous reached = %v, err = %v", reached, err)
	}
}

// TestMustAcceptPanics verifies a method registered in either registry is
// accepted and any other panics at composition.
func TestMustAcceptPanics(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), withStepUp(credential(auth.MethodPassword, auth.KindKnowledge, yes)))

	guard.MustAccept(auth.MethodOTP)
	guard.MustAccept(auth.MethodPassword)

	defer func() {
		if recover() == nil {
			t.Fatal("MustAccept of an unregistered method did not panic")
		}
	}()

	guard.MustAccept(auth.MethodHardwareKey)
}
