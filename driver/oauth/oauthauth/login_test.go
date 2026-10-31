package oauthauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLoginCapsCookies verifies a browser keeps at most five login cookies,
// dropping the oldest.
func TestLoginCapsCookies(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	jar := cookieJar{}

	first := login(t, client, jar)

	for range 5 {
		login(t, client, jar)
	}

	if len(jar) != maxLogins {
		t.Fatalf("cookies = %d, want %d", len(jar), maxLogins)
	}

	if _, ok := jar[client.cookieName(first.Get("state"))]; ok {
		t.Error("oldest login cookie was kept")
	}
}

// TestAllowInsecure verifies the cookie name and attributes over plain HTTP.
func TestAllowInsecure(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL), func(client *ClientBuilder) {
		client.AllowInsecure(true)
		client.Name("dev")
	})
	jar := cookieJar{}

	login(t, client, jar)

	for name, cookie := range jar {
		if !strings.HasPrefix(name, "dev.") || cookie.Secure || !cookie.HttpOnly {
			t.Errorf("cookie = %+v", cookie)
		}
	}
}

// TestLoginRejectsUnsafeReturnTo verifies open redirects are refused.
func TestLoginRejectsUnsafeReturnTo(t *testing.T) {
	t.Parallel()

	client := newClient(t, OIDC(newTestProvider(t).server.URL))
	request := httptest.NewRequest(http.MethodGet, "/login?return_to=//evil.example", nil)

	if got := client.Login().Record(request).StatusCode; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", got)
	}
}
