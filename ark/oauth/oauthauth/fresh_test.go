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

// TestFederatedFactorUsesCallbackTime verifies the federated factor carries
// this server's clock at the callback and ignores the provider's auth_time,
// which is three hours old here.
func TestFederatedFactorUsesCallbackTime(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard := newGuard(t)
		old := float64(time.Now().Add(-3 * time.Hour).Unix())
		httpRequest := federatedCallback(t, guard, newSession(), map[string]any{"auth_time": old})

		identity, err := guard.Authenticator()(httpRequest)

		if err != nil {
			t.Fatal(err)
		}

		factors := identity.Factors()

		if len(factors) != 1 || factors[0].Method != MethodFederated || !factors[0].At.Equal(time.Now()) {
			t.Fatalf("factors = %+v, want one federated factor stamped %v", factors, time.Now())
		}
	})
}

// TestFederatedOnlyUserIsFreshAfterCallback verifies a user without a step-up
// method is fresh right after a federated login, whatever auth_time says.
func TestFederatedOnlyUserIsFreshAfterCallback(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard := newGuard(t, func(policy *auth.GuardBuilder) {
			policy.StepUp(testCredential(auth.MethodPassword, auth.KindKnowledge, false))
		})
		old := float64(time.Now().Add(-3 * time.Hour).Unix())
		httpRequest := federatedCallback(t, guard, newSession(), map[string]any{"auth_time": old})

		if err := freshness(guard, httpRequest); err != nil {
			t.Fatalf("Fresh after a federated login = %v", err)
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
