package oauthauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/foundation/key"
)

// browser is a cookie store that applies the SameSite rules of RFC 6265bis
// section 5.2: a request that comes from another site carries only
// SameSite=None cookies, except that a top-level GET navigation also carries
// SameSite=Lax cookies.
type browser struct {
	// cookies holds the cookies by name.
	cookies map[string]*http.Cookie
}

// do sends request to handler as a browser would and stores the cookies of the
// response. crossSite says the navigation was started by another site, as the
// POST of Apple's form is; a POST is then not "safe", so Lax cookies stay home.
func (b *browser) do(handler router.Handler, request *http.Request, crossSite bool) *http.Response {
	for _, cookie := range b.cookies {
		lax := cookie.SameSite != http.SameSiteNoneMode && cookie.SameSite != http.SameSiteStrictMode

		if cookie.SameSite == http.SameSiteNoneMode || !crossSite || lax && request.Method == http.MethodGet {
			request.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
		}
	}

	response := handler.Record(request)

	for _, cookie := range response.Cookies() {
		if cookie.MaxAge < 0 {
			delete(b.cookies, cookie.Name)
		} else {
			b.cookies[cookie.Name] = cookie
		}
	}

	return response
}

// appleConnectFixture is an application that mounts login, callback and a
// password-less sign-in route behind session and authentication middleware, with
// a resolver that links the Apple identity to the logged-in user, or creates an
// account when the request is anonymous.
type appleConnectFixture struct {
	// client is the Apple-like form_post client.
	client *Client

	// guard owns the sessions.
	guard *auth.Guard

	// routes serve the three handlers behind the middleware.
	signIn, login, callback router.Handler

	// linked maps the Apple subject to the local subject the resolver chose.
	linked map[string]string

	// created counts accounts the resolver created for anonymous requests.
	created int
}

// newAppleConnectFixture builds the fixture around the fake Apple provider.
func newAppleConnectFixture(t *testing.T) *appleConnectFixture {
	t.Helper()

	_, pemKey := appleKey(t)
	identity, err := key.ParseSigning(pemKey)

	if err != nil {
		t.Fatal(err)
	}

	apple := Apple("TEAM", "KEYID", identity)
	apple.issuer = newTestProvider(t).server.URL
	apple.clientSecret = nil

	fixture := &appleConnectFixture{
		client: newClient(t, apple),
		guard:  newGuard(t),
		linked: map[string]string{},
	}

	store := cache.New(memorycache.New())
	wrap := func(handler router.Handler) router.Handler {
		return session.Middleware(store)(auth.Middleware(fixture.guard.Authenticator())(handler))
	}

	fixture.signIn = wrap(func(w http.ResponseWriter, r *http.Request) error {
		outcome, err := fixture.guard.Begin(r, "7", auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})

		if err != nil {
			return err
		}

		return outcome.Response(w)
	})
	fixture.login = wrap(fixture.client.LoginHandler())
	fixture.callback = wrap(fixture.client.CallbackHandler(fixture.guard, func(ctx context.Context, external auth.External) (string, error) {
		if current, ok := auth.FromContext(ctx); ok {
			fixture.linked[external.Subject] = current.Subject()

			return current.Subject(), nil
		}

		fixture.created++
		fixture.linked[external.Subject] = external.Subject

		return external.Subject, nil
	}))

	return fixture
}

// startLogin signs the browser in as user "7" when signedIn, then starts the
// Apple login and returns the state and code Apple would post back. The test
// provider takes the nonce as the authorization code.
func (f *appleConnectFixture) startLogin(t *testing.T, b *browser, signedIn bool) url.Values {
	t.Helper()

	if signedIn {
		if status := b.do(f.signIn, httptest.NewRequest(http.MethodPost, "/sign-in", nil), false).StatusCode; status != http.StatusNoContent {
			t.Fatalf("sign in status = %d", status)
		}

		found := false

		for name, cookie := range b.cookies {
			if !strings.HasPrefix(name, f.client.prefix) {
				found = true

				if cookie.SameSite != http.SameSiteLaxMode {
					t.Fatalf("session cookie %q SameSite = %v, want Lax", name, cookie.SameSite)
				}
			}
		}

		if !found {
			t.Fatal("sign in set no session cookie")
		}
	}

	response := b.do(f.login, httptest.NewRequest(http.MethodGet, "/login?return_to=/settings", nil), false)
	location, err := url.Parse(response.Header.Get("Location"))

	if err != nil || response.StatusCode != http.StatusFound {
		t.Fatalf("login = %d %q, %v", response.StatusCode, response.Header.Get("Location"), err)
	}

	query := location.Query()

	return url.Values{"state": {query.Get("state")}, "code": {query.Get("nonce")}}
}

// post sends Apple's form_post callback: a cross-site POST.
func (f *appleConnectFixture) post(b *browser, form url.Values) *http.Response {
	request := httptest.NewRequest(http.MethodPost, "/callback", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return b.do(f.callback, request, true)
}

// follow performs the navigation a 303 asks for. The chain still started on
// another site, so the browser treats it as cross-site and Lax cookies travel
// only because it is a top-level GET.
func (f *appleConnectFixture) follow(b *browser, response *http.Response) *http.Response {
	return b.do(f.callback, httptest.NewRequest(http.MethodGet, response.Header.Get("Location"), nil), true)
}

// TestAppleConnectLinksToLoggedInUser verifies the whole form_post chain for a
// logged-in user who connects Apple: the cross-site POST cannot see the session
// and must neither resolve nor log anyone in; the same-site GET that follows
// sees the session, so the identity links to that user and no second account is
// created. Without the hand-off the POST resolves anonymously and creates one.
func TestAppleConnectLinksToLoggedInUser(t *testing.T) {
	t.Parallel()

	f := newAppleConnectFixture(t)
	b := &browser{cookies: map[string]*http.Cookie{}}
	form := f.startLogin(t, b, true)

	posted := f.post(b, form)

	if posted.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST status = %d, want 303", posted.StatusCode)
	}

	if len(f.linked) != 0 || f.created != 0 {
		t.Fatalf("the POST resolved an identity: linked %v, created %d", f.linked, f.created)
	}

	finished := f.follow(b, posted)

	if finished.StatusCode != http.StatusFound || finished.Header.Get("Location") != "/settings" {
		t.Fatalf("GET = %d %q, want 302 /settings", finished.StatusCode, finished.Header.Get("Location"))
	}

	if f.created != 0 || len(f.linked) != 1 {
		t.Fatalf("created %d accounts, links %v", f.created, f.linked)
	}

	for _, local := range f.linked {
		if local != "7" {
			t.Fatalf("Apple linked to %q, want the logged-in user 7", local)
		}
	}
}

// TestAppleLoginWithoutSession verifies the same chain logs in an anonymous
// browser and creates its account once, at the GET.
func TestAppleLoginWithoutSession(t *testing.T) {
	t.Parallel()

	f := newAppleConnectFixture(t)
	b := &browser{cookies: map[string]*http.Cookie{}}
	form := f.startLogin(t, b, false)

	if finished := f.follow(b, f.post(b, form)); finished.StatusCode != http.StatusFound {
		t.Fatalf("GET status = %d, want 302", finished.StatusCode)
	}

	if f.created != 1 {
		t.Fatalf("created = %d, want 1", f.created)
	}
}

// TestAppleHandoffIsSingleUseAndBound verifies the hand-off finishes one login
// only: the GET spends it, a replay fails, a different state cannot open it and
// a cookie moved to another state's name is refused.
func TestAppleHandoffIsSingleUseAndBound(t *testing.T) {
	t.Parallel()

	f := newAppleConnectFixture(t)
	b := &browser{cookies: map[string]*http.Cookie{}}
	form := f.startLogin(t, b, true)
	posted := f.post(b, form)
	handoff := f.client.handoffName(form.Get("state"))

	if b.cookies[handoff] == nil || b.cookies[handoff].SameSite != http.SameSiteLaxMode || !b.cookies[handoff].HttpOnly {
		t.Fatalf("hand-off cookie = %+v, want HttpOnly SameSite=Lax", b.cookies[handoff])
	}

	copied := *b.cookies[handoff]

	// Another state cannot open this browser's hand-off.
	other := httptest.NewRequest(http.MethodGet, "/callback?state=other", nil)

	if status := b.do(f.callback, other, true).StatusCode; status != http.StatusBadRequest {
		t.Fatalf("other state status = %d, want 400", status)
	}

	// A cookie moved under another state's name fails the name binding.
	other = httptest.NewRequest(http.MethodGet, "/callback?state=other", nil)
	other.AddCookie(&http.Cookie{Name: f.client.handoffName("other"), Value: copied.Value})

	if status := f.callback.Record(other).StatusCode; status != http.StatusBadRequest {
		t.Fatalf("moved cookie status = %d, want 400", status)
	}

	if status := f.follow(b, posted).StatusCode; status != http.StatusFound {
		t.Fatalf("GET status = %d, want 302", status)
	}

	if status := f.follow(b, posted).StatusCode; status != http.StatusBadRequest {
		t.Fatalf("replayed GET status = %d, want 400", status)
	}

	if b.cookies[handoff] != nil {
		t.Fatal("hand-off cookie survived the GET")
	}
}

// TestFormPostRejectsDirectGetCallback verifies a form_post client no longer
// completes a login from a GET that carries a code: only the hand-off can.
func TestFormPostRejectsDirectGetCallback(t *testing.T) {
	t.Parallel()

	f := newAppleConnectFixture(t)
	b := &browser{cookies: map[string]*http.Cookie{}}
	form := f.startLogin(t, b, false)

	request := httptest.NewRequest(http.MethodGet, "/callback?"+form.Encode(), nil)

	if status := b.do(f.callback, request, true).StatusCode; status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}

	if f.created != 0 {
		t.Fatalf("created = %d, want 0", f.created)
	}
}
