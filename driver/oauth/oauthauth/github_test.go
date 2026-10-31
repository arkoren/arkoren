package oauthauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

// requireGitHubIdentity serves the given /user and /user/emails bodies and
// verifies userInfo builds the want identity, sending the GitHub API headers.
func requireGitHubIdentity(t *testing.T, user, emails string, want Identity) {
	t.Helper()

	mux := http.NewServeMux()
	serve := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
				t.Errorf("headers = %v", r.Header)
			}

			_, _ = w.Write([]byte(body))
		}
	}

	mux.HandleFunc("/user", serve(user))
	mux.HandleFunc("/user/emails", serve(emails))
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)

	got, err := github(api.URL).userInfo(t.Context(), api.Client())

	if err != nil {
		t.Fatalf("userInfo: %v", err)
	}

	if got.Issuer != want.Issuer || got.Subject != want.Subject || got.Name != want.Name || got.Email != want.Email {
		t.Errorf("identity = %+v, want %+v", got, want)
	}
}

// TestGitHubIdentityPrimaryVerified verifies the numeric subject, the name and
// the primary verified email choice.
func TestGitHubIdentityPrimaryVerified(t *testing.T) {
	t.Parallel()

	requireGitHubIdentity(t,
		`{"id": 583231, "login": "octocat", "name": "The Octocat"}`,
		`[{"email":"a@x.com","primary":false,"verified":true},{"email":"b@x.com","primary":true,"verified":true}]`,
		Identity{
			Issuer:  "https://github.com",
			Subject: "583231",
			Name:    "The Octocat",
			Email:   "b@x.com",
		})
}

// TestGitHubIdentityUnverifiedIgnoredLoginFallback verifies an unverified
// email is ignored and the login stands in for a missing name.
func TestGitHubIdentityUnverifiedIgnoredLoginFallback(t *testing.T) {
	t.Parallel()

	requireGitHubIdentity(t,
		`{"id": 7, "login": "octocat", "name": null}`,
		`[{"email":"b@x.com","primary":true,"verified":false}]`,
		Identity{
			Issuer:  "https://github.com",
			Subject: "7",
			Name:    "octocat",
		})
}

// TestGitHubFlow verifies the login against stubbed GitHub endpoints,
// including the form-encoded token response GitHub sends by default.
func TestGitHubFlow(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.PostFormValue("code_verifier") == "" {
			t.Error("token request lacks code_verifier")
		}

		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		_, _ = w.Write([]byte("access_token=gho_x&token_type=bearer&scope=read%3Auser%2Cuser%3Aemail"))
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id": 42, "login": "octocat"}`))
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"email":"o@x.com","primary":true,"verified":true}]`))
	})
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)

	provider := github(api.URL)
	provider.endpoint.TokenURL = api.URL + "/token"
	client := newClient(t, provider, func(client *ClientBuilder) {
		client.Scopes()
	})

	jar := cookieJar{}

	query := login(t, client, jar)

	if query.Get("code_challenge_method") != "S256" || query.Get("scope") != "read:user user:email" {
		t.Errorf("auth query = %v", query)
	}

	sess := newSession()
	request := callbackRequest(http.MethodGet, jar, sess, query.Get("state"), "code")

	if status := client.Callback(externalIdentity, func(_ context.Context, identity Identity) (Identity, error) {
		return identity, nil
	}).Record(request).StatusCode; status != http.StatusFound {
		t.Fatalf("status = %d, want 302", status)
	}

	identity, err := userOfIdentity(request)

	if err != nil || identity.Subject != "42" || identity.Email != "o@x.com" || len(identity.Scopes) != 1 {
		t.Fatalf("identity = %+v, %v", identity, err)
	}
}

// requireGitHubAPIError verifies userInfo fails when the failing endpoint answers 403.
func requireGitHubAPIError(t *testing.T, failing string) {
	t.Helper()

	mux := http.NewServeMux()
	bodies := map[string]string{"/user": `{"id": 1}`, "/user/emails": `[]`}

	for path, body := range bodies {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			if path == failing {
				http.Error(w, `{"message":"x"}`, http.StatusForbidden)

				return
			}

			_, _ = w.Write([]byte(body))
		})
	}

	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)

	if _, err := github(api.URL).userInfo(t.Context(), api.Client()); err == nil {
		t.Errorf("%s 403: err = nil", failing)
	}
}

// TestGitHubAPIErrorUser verifies a non-200 response from /user fails.
func TestGitHubAPIErrorUser(t *testing.T) {
	t.Parallel()

	requireGitHubAPIError(t, "/user")
}

// TestGitHubAPIErrorEmails verifies a non-200 response from /user/emails fails.
func TestGitHubAPIErrorEmails(t *testing.T) {
	t.Parallel()

	requireGitHubAPIError(t, "/user/emails")
}

// TestGitHubCleanupIsBestEffort confirms all native response bodies are closed
// and cleanup errors do not undo an otherwise verified public callback.
func TestGitHubCleanupIsBestEffort(t *testing.T) {
	t.Parallel()

	client := newClient(t, GitHub())
	jar := cookieJar{}

	query := login(t, client, jar)

	closed := 0
	transport := protocolTransport(func(request *http.Request) (*http.Response, error) {
		bodies := map[string]string{
			"/login/oauth/access_token": `{"access_token":"synthetic","token_type":"Bearer"}`,
			"/user":                     `{"id":42}`,
			"/user/emails":              `[]`,
		}

		reply := protocolResponse(http.StatusOK, "")
		reply.Body = &closingBody{Reader: strings.NewReader(bodies[request.URL.Path]), closed: &closed}

		return reply, nil
	})
	request := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), "code")
	request = request.WithContext(context.WithValue(request.Context(), oauth2.HTTPClient, &http.Client{Transport: transport}))

	response := client.Callback(localIdentity, idLogin).Record(request)

	if response.StatusCode != http.StatusFound || closed != 3 {
		t.Fatalf("cleanup changed callback: status %d, closes %d", response.StatusCode, closed)
	}
}

// closingBody exposes native HTTP response ownership independently of the
// adapter's private decoder; its Close deliberately reports a cleanup failure.
type closingBody struct {
	// Reader supplies the synthetic provider response.
	io.Reader
	// closed counts completed releases during this synchronous callback.
	closed *int
}

// Close records release and fails without changing previously read evidence.
func (body *closingBody) Close() error {
	(*body.closed)++

	return errors.New("synthetic cleanup failure")
}

// TestGitHubCallbackRejectsMissingID exercises the public preset and browser
// callback using only synthetic GitHub responses, never its private decoder.
func TestGitHubCallbackRejectsMissingID(t *testing.T) {
	t.Parallel()

	client := newClient(t, GitHub())
	jar := cookieJar{}

	query := login(t, client, jar)

	transport := protocolTransport(func(request *http.Request) (*http.Response, error) {
		bodies := map[string]string{
			"/login/oauth/access_token": `{"access_token":"synthetic","token_type":"Bearer"}`,
			"/user":                     `{"login":"display-only"}`,
			"/user/emails":              `[]`,
		}

		return protocolResponse(http.StatusOK, bodies[request.URL.Path]), nil
	})
	request := callbackRequest(http.MethodGet, jar, newSession(), query.Get("state"), "code")
	request = request.WithContext(context.WithValue(request.Context(), oauth2.HTTPClient, &http.Client{Transport: transport}))
	called := false

	response := client.Callback(localIdentity, func(context.Context, Identity) (string, error) {
		called = true

		return "mapped", nil
	}).Record(request)

	if called || response.StatusCode == http.StatusFound {
		t.Fatalf("missing GitHub ID reached mapper: %t, status %d", called, response.StatusCode)
	}
}
