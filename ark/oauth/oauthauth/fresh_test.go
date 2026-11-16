package oauthauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/session"
)

// freshness runs [auth.Guard.Fresh] for the identity the guard's authenticator
// builds from the session of r.
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

// federatedCallback runs a browser login against a fresh offline OIDC provider
// (usable inside synctest) whose ID tokens carry claims, on the session of current, and returns the callback
// request, which now holds the logged-in session.
func federatedCallback(t *testing.T, guard *auth.Guard, current *session.Session, claims map[string]any) *http.Request {
	t.Helper()

	provider := newOfflineProvider(t)
	provider.claims = claims
	ctx := oidc.ClientContext(t.Context(), provider.client())
	client := newClientAt(t, ctx, OIDC(provider.server.URL))
	jar := cookieJar{}
	query := login(t, client, jar)
	httpRequest := callbackRequest(http.MethodGet, jar, current, query.Get("state"), query.Get("nonce"))
	httpRequest = httpRequest.WithContext(oidc.ClientContext(httpRequest.Context(), provider.client()))

	if status := client.CallbackHandler(guard, idLogin).Record(httpRequest).StatusCode; status != http.StatusFound {
		t.Fatalf("callback status = %d", status)
	}

	return httpRequest
}

// TestFederatedFactorUsesEarlierAuthTime verifies an ID token's auth_time that
// is earlier than the callback becomes the factor time.
func TestFederatedFactorUsesEarlierAuthTime(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard := newGuard(t)
		old := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
		httpRequest := federatedCallback(t, guard, newSession(), map[string]any{"auth_time": float64(old.Unix())})

		identity, err := guard.Authenticator()(httpRequest)

		if err != nil {
			t.Fatal(err)
		}

		factors := identity.Factors()

		if len(factors) != 1 || factors[0].Method != MethodFederated || !factors[0].At.Equal(old) {
			t.Fatalf("factors = %+v, want one federated factor stamped %v", factors, old)
		}
	})
}

// TestFederatedFactorIgnoresLaterAuthTime verifies an auth_time in the future
// cannot make the factor fresher than the callback.
func TestFederatedFactorIgnoresLaterAuthTime(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard := newGuard(t)
		later := float64(time.Now().Add(time.Hour).Unix())
		httpRequest := federatedCallback(t, guard, newSession(), map[string]any{"auth_time": later})

		identity, err := guard.Authenticator()(httpRequest)

		if err != nil {
			t.Fatal(err)
		}

		if factors := identity.Factors(); len(factors) != 1 || !factors[0].At.Equal(time.Now()) {
			t.Fatalf("factors = %+v, want one stamped %v", factors, time.Now())
		}
	})
}

// TestFederatedLoginWithoutAuthTime verifies a token with no auth_time still
// logs in, with the factor stamped at the callback.
func TestFederatedLoginWithoutAuthTime(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard := newGuard(t)
		httpRequest := federatedCallback(t, guard, newSession(), nil)

		identity, err := guard.Authenticator()(httpRequest)

		if err != nil {
			t.Fatal(err)
		}

		if factors := identity.Factors(); len(factors) != 1 || !factors[0].At.Equal(time.Now()) {
			t.Fatalf("factors = %+v, want one stamped %v", factors, time.Now())
		}
	})
}

// TestFederatedOnlyUserIsFreshAfterCallback verifies a user without a step-up
// method is fresh right after a federated login whose auth_time is recent.
func TestFederatedOnlyUserIsFreshAfterCallback(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard := newGuard(t, func(policy *auth.GuardBuilder) {
			policy.StepUp(testCredential(auth.MethodPassword, auth.KindKnowledge, false))
		})
		recent := float64(time.Now().Add(-time.Minute).Unix())
		httpRequest := federatedCallback(t, guard, newSession(), map[string]any{"auth_time": recent})

		if err := freshness(guard, httpRequest); err != nil {
			t.Fatalf("Fresh after a federated login = %v", err)
		}
	})
}

// TestFederatedOnlyUserIsStaleAfterOldAuthTime verifies a silent single sign-on
// approval that reports an old auth_time does not count as a fresh login.
func TestFederatedOnlyUserIsStaleAfterOldAuthTime(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard := newGuard(t, func(policy *auth.GuardBuilder) {
			policy.StepUp(testCredential(auth.MethodPassword, auth.KindKnowledge, false))
		})
		old := float64(time.Now().Add(-3 * time.Hour).Unix())
		httpRequest := federatedCallback(t, guard, newSession(), map[string]any{"auth_time": old})

		if err := freshness(guard, httpRequest); !errors.Is(err, auth.ErrNoStepUpMethod) {
			t.Fatalf("Fresh after an old auth_time = %v, want ErrNoStepUpMethod", err)
		}
	})
}

// TestSilentReloginNotFreshForUserWithTOTP verifies a same-subject federated
// re-login does not refresh a user who has a step-up method, even when the
// provider reports a brand-new authentication.
func TestSilentReloginNotFreshForUserWithTOTP(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard := newGuard(t, withStepUp)
		current := newSession()
		httpRequest := callbackRequest(http.MethodGet, cookieJar{}, current, "", "")

		if _, err := guard.Begin(httpRequest, "user-1", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}); err != nil {
			t.Fatal(err)
		}

		if err := freshness(guard, httpRequest); err != nil {
			t.Fatalf("Fresh after the password login = %v", err)
		}

		time.Sleep(time.Hour)

		// The provider claims the user authenticated just now; the guard must not care.
		httpRequest = federatedCallback(t, guard, current, map[string]any{"auth_time": float64(time.Now().Unix())})

		if err := freshness(guard, httpRequest); !errors.Is(err, auth.ErrStepUpRequired) {
			t.Fatalf("Fresh after a silent federated re-login = %v, want ErrStepUpRequired", err)
		}
	})
}

// TestLoginFreshRequestsReauthentication verifies fresh=1 adds prompt=login and
// max_age=0 to an OIDC login and that a plain login does not.
func TestLoginFreshRequestsReauthentication(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))

	for target, want := range map[string]bool{"/login?fresh=1": true, "/login": false} {
		response := client.LoginHandler().Record(httptest.NewRequest(http.MethodGet, target, nil))
		location, err := url.Parse(response.Header.Get("Location"))

		if err != nil || response.StatusCode != http.StatusFound {
			t.Fatalf("%s = %d, %v", target, response.StatusCode, err)
		}

		query := location.Query()

		if got := query.Get("prompt") == "login" && query.Get("max_age") == "0"; got != want {
			t.Errorf("%s: prompt=%q max_age=%q", target, query.Get("prompt"), query.Get("max_age"))
		}
	}
}

// TestLoginFreshRecordedInState verifies fresh=1 is sealed into the login
// state of an OIDC login and a plain login records nothing.
func TestLoginFreshRecordedInState(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))

	for target, want := range map[string]bool{"/login?fresh=1": true, "/login": false} {
		response := client.LoginHandler().Record(httptest.NewRequest(http.MethodGet, target, nil))
		cookies := response.Cookies()

		if len(cookies) != 1 {
			t.Fatalf("%s set %d cookies, want 1", target, len(cookies))
		}

		state, err := client.open(cookies[0])

		if err != nil {
			t.Fatal(err)
		}

		if state.Fresh != want {
			t.Errorf("%s: Fresh = %v, want %v", target, state.Fresh, want)
		}
	}
}
