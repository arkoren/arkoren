package oauthauth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"arkoren.dev/core/auth"
)

// requireFreshAfterCallback runs a full OIDC login against a provider that adds
// claims to its ID token and reports whether the logged-in identity satisfies
// auth.RequireFresh(10 minutes).
func requireFreshAfterCallback(t *testing.T, claims map[string]any) bool {
	t.Helper()

	provider := newTestProvider(t)
	provider.claims = claims
	client := newClient(t, OIDC(provider.server.URL))
	guard := newGuard(t)
	jar := cookieJar{}
	query := login(t, client, jar)
	httpRequest := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce"))

	if status := client.Callback(guard, idLogin).Record(httpRequest).StatusCode; status != http.StatusFound {
		t.Fatalf("callback status = %d", status)
	}

	identity, err := guard.Authenticator()(httpRequest)

	if err != nil {
		t.Fatal(err)
	}

	reached := false
	next := func(http.ResponseWriter, *http.Request) error {
		reached = true

		return nil
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(auth.NewContext(t.Context(), identity))
	_ = auth.RequireFresh(10*time.Minute)(next)(httptest.NewRecorder(), request)

	return reached
}

// TestSilentLoginWithOldAuthTimeIsNotFresh verifies a provider that approves a
// login silently, reporting the old authentication, does not satisfy
// RequireFresh.
func TestSilentLoginWithOldAuthTimeIsNotFresh(t *testing.T) {
	t.Parallel()

	old := float64(time.Now().Add(-3 * time.Hour).Unix())

	if requireFreshAfterCallback(t, map[string]any{"auth_time": old}) {
		t.Fatal("a silent re-login with an old auth_time passed RequireFresh")
	}
}

// TestRecentAuthTimeIsFresh verifies a real, recent authentication passes.
func TestRecentAuthTimeIsFresh(t *testing.T) {
	t.Parallel()

	recent := float64(time.Now().Add(-time.Minute).Unix())

	if !requireFreshAfterCallback(t, map[string]any{"auth_time": recent}) {
		t.Fatal("a recent auth_time did not pass RequireFresh")
	}
}

// TestMissingAuthTimeFallsBackToNow verifies the documented fallback: without
// the claim the factor is stamped with the callback time.
func TestMissingAuthTimeFallsBackToNow(t *testing.T) {
	t.Parallel()

	if !requireFreshAfterCallback(t, nil) {
		t.Fatal("a login without auth_time was not stamped now")
	}
}

// TestLoginFreshRequestsReauthentication verifies fresh=1 adds prompt=login and
// max_age=0 to an OIDC login and that a plain login does not.
func TestLoginFreshRequestsReauthentication(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))

	for target, want := range map[string]bool{"/login?fresh=1": true, "/login": false} {
		response := client.Login().Record(httptest.NewRequest(http.MethodGet, target, nil))
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
