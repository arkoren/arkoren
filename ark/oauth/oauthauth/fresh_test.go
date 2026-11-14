package oauthauth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"arkoren.dev/core/auth"
)

// freshScenario describes a login whose result is checked against RequireFresh.
type freshScenario struct {
	// claims are extra ID token claims, such as auth_time.
	claims map[string]any

	// fresh starts the login with fresh=1.
	fresh bool

	// signedIn logs the session in as the same subject, with only an old
	// password factor, before the callback runs.
	signedIn bool
}

// requireFreshAfterCallback runs a full OIDC login for scenario and reports
// whether the logged-in identity satisfies auth.RequireFresh(10 minutes).
func requireFreshAfterCallback(t *testing.T, scenario freshScenario) bool {
	t.Helper()

	provider := newTestProvider(t)
	provider.claims = scenario.claims
	client := newClient(t, OIDC(provider.server.URL))
	guard := newGuard(t)
	jar := cookieJar{}
	target := "/login"

	if scenario.fresh {
		target += "?fresh=1"
	}

	response := client.Login().Record(jar.attach(httptest.NewRequest(http.MethodGet, target, nil)))
	location, err := url.Parse(response.Header.Get("Location"))

	if err != nil || response.StatusCode != http.StatusFound {
		t.Fatalf("login = %d, %v", response.StatusCode, err)
	}

	jar.apply(response)

	query := location.Query()
	httpRequest := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce"))

	if scenario.signedIn {
		old := auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge, At: time.Now().Add(-3 * time.Hour)}

		if _, err := guard.Begin(httpRequest, "user-1", old); err != nil {
			t.Fatal(err)
		}
	}

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

	if requireFreshAfterCallback(t, freshScenario{claims: map[string]any{"auth_time": old}}) {
		t.Fatal("a silent re-login with an old auth_time passed RequireFresh")
	}
}

// TestRecentAuthTimeIsFresh verifies a real, recent authentication passes, for
// a first login and for a session already logged in as the subject.
func TestRecentAuthTimeIsFresh(t *testing.T) {
	t.Parallel()

	claims := map[string]any{"auth_time": float64(time.Now().Add(-time.Minute).Unix())}

	if !requireFreshAfterCallback(t, freshScenario{claims: claims}) {
		t.Fatal("a recent auth_time did not pass RequireFresh")
	}

	if !requireFreshAfterCallback(t, freshScenario{claims: claims, signedIn: true}) {
		t.Fatal("a recent auth_time on a signed-in session did not pass RequireFresh")
	}
}

// TestFreshLoginWithRecentAuthTimeIsFresh verifies fresh=1 accepts an
// auth_time from after the login started.
func TestFreshLoginWithRecentAuthTimeIsFresh(t *testing.T) {
	t.Parallel()

	claims := map[string]any{"auth_time": float64(time.Now().Unix())}

	if !requireFreshAfterCallback(t, freshScenario{claims: claims, fresh: true, signedIn: true}) {
		t.Fatal("fresh login with a new auth_time did not pass RequireFresh")
	}
}

// TestMissingAuthTimeIsNeverFresh verifies an unknown authentication time is
// not treated as now: a silent re-login of a signed-in session must not pass
// RequireFresh, with or without fresh=1.
func TestMissingAuthTimeIsNeverFresh(t *testing.T) {
	t.Parallel()

	for _, scenario := range []freshScenario{
		{signedIn: true},
		{signedIn: true, fresh: true},
		{},
		{fresh: true},
	} {
		if requireFreshAfterCallback(t, scenario) {
			t.Errorf("login without auth_time passed RequireFresh: %+v", scenario)
		}
	}
}

// TestFreshLoginWithStaleAuthTimeIsNotFresh verifies fresh=1 rejects an
// auth_time that predates the login even when it is within the RequireFresh
// window, because the provider ignored the re-authentication request.
func TestFreshLoginWithStaleAuthTimeIsNotFresh(t *testing.T) {
	t.Parallel()

	claims := map[string]any{"auth_time": float64(time.Now().Add(-5 * time.Minute).Unix())}

	if requireFreshAfterCallback(t, freshScenario{claims: claims, fresh: true, signedIn: true}) {
		t.Fatal("fresh login with a pre-login auth_time passed RequireFresh")
	}
}

// TestFirstLoginWithoutAuthTimeSignsIn verifies providers without auth_time
// still log a new session in.
func TestFirstLoginWithoutAuthTimeSignsIn(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL))
	guard := newGuard(t)
	jar := cookieJar{}
	query := login(t, client, jar)
	httpRequest := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), query.Get("nonce"))

	if status := client.Callback(guard, idLogin).Record(httpRequest).StatusCode; status != http.StatusFound {
		t.Fatalf("callback status = %d", status)
	}

	if _, err := guard.Authenticator()(httpRequest); err != nil {
		t.Fatalf("Authenticator() = %v, want a logged-in identity", err)
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
