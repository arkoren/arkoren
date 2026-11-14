package oauthauth

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/crypto"
)

// testProvider is an in-process OIDC provider. Its token endpoint treats the
// authorization code as the nonce to embed in the ID token.
type testProvider struct {
	// server owns the local discovery, key-set, and token endpoints. An
	// offline provider has only its URL.
	server *httptest.Server

	// mux serves the endpoints; offline providers are reached through it.
	mux *http.ServeMux
	// signer signs fixture tokens with the key advertised by discovery.
	signer jose.Signer
	// key is the public verification key exposed by the JWKS endpoint.
	key jose.JSONWebKey

	// exchanges counts token requests across the test server's goroutines.
	// Configuration-failure tests assert that this remains zero.
	exchanges atomic.Int64

	// claims are extra ID token claims the token endpoint adds, such as
	// auth_time. Set them before the first callback.
	claims map[string]any
}

// newTestProvider starts a provider serving discovery, JWKS and token endpoints.
func newTestProvider(t *testing.T) *testProvider {
	t.Helper()

	return buildTestProvider(t, true)
}

// newOfflineProvider returns a provider that opens no socket. Reach it through
// [testProvider.client], which routes requests to its handlers in process.
// Tests that run inside testing/synctest use it, because connections of the
// shared default transport cannot cross a synctest bubble.
func newOfflineProvider(t *testing.T) *testProvider {
	t.Helper()

	return buildTestProvider(t, false)
}

// client returns an HTTP client that serves requests to the provider's
// handlers without a network. Pass it to Start and to callback requests with
// oidc.ClientContext.
func (provider *testProvider) client() *http.Client {
	return &http.Client{Transport: protocolTransport(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		provider.mux.ServeHTTP(recorder, request)

		return recorder.Result(), nil
	})}
}

// buildTestProvider creates the provider; online starts a real test server.
func buildTestProvider(t *testing.T, online bool) *testProvider {
	t.Helper()

	private, err := rsa.GenerateKey(rand.Reader, 2048)

	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	key := jose.JSONWebKey{
		Key:       private.Public(),
		KeyID:     "k1",
		Algorithm: "RS256",
		Use:       "sig",
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{
			Algorithm: jose.RS256,
			Key: jose.JSONWebKey{
				Key:   private,
				KeyID: "k1",
			},
		},
		(&jose.SignerOptions{}).WithType("JWT"),
	)

	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	provider := &testProvider{
		signer: signer,
		key:    key,
	}

	mux := http.NewServeMux()
	provider.mux = mux

	if online {
		provider.server = httptest.NewServer(mux)
		t.Cleanup(provider.server.Close)
	} else {
		provider.server = &httptest.Server{URL: "https://idp.test"}
	}

	mux.HandleFunc("/.well-known/openid-configuration", provider.discovery)
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.MarshalWrite(w, jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{key},
		})
	})
	mux.HandleFunc("/token", provider.token)

	return provider
}

// discovery serves the OIDC discovery document.
func (provider *testProvider) discovery(w http.ResponseWriter, _ *http.Request) {
	base := provider.server.URL
	_ = json.MarshalWrite(w, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/auth",
		"token_endpoint":                        base + "/token",
		"jwks_uri":                              base + "/jwks",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

// token serves the token endpoint.
func (provider *testProvider) token(w http.ResponseWriter, r *http.Request) {
	provider.exchanges.Add(1)
	claims := map[string]any{"nonce": r.FormValue("code"), "name": "Ada"}
	maps.Copy(claims, provider.claims)
	idToken := provider.sign(claims)
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, map[string]any{
		"access_token": "opaque",
		"token_type":   "Bearer",
		"id_token":     idToken,
		"scope":        "openid read",
	})
}

// sign returns a JWT for client "cid" and subject "user-1" with extra claims.
func (provider *testProvider) sign(extra map[string]any) string {
	return provider.signFor("cid", extra)
}

// signFor returns a JWT for subject "user-1" issued to audience, with extra
// claims. Native login tests use it to mint tokens for a mobile app's client.
func (provider *testProvider) signFor(audience string, extra map[string]any) string {
	claims := jwt.Claims{
		Issuer:   provider.server.URL,
		Subject:  "user-1",
		Audience: jwt.Audience{audience},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}

	raw, err := jwt.Signed(provider.signer).Claims(claims).Claims(extra).Serialize()

	if err != nil {
		panic(err)
	}

	return raw
}

// userInfoStub is a [UserInfoFunc] returning a fixed identity.
func userInfoStub(_ context.Context, client *http.Client) (auth.External, error) {
	if client == nil {
		return auth.External{}, errors.New("no client")
	}

	return auth.External{
		Issuer:  "stub",
		Subject: "stub-1",
		Email:   "ada@example.com",
	}, nil
}

// gcmDriver is a test fake of crypto.Driver backed by AES-GCM.
type gcmDriver struct {
	// aead authenticates the cookie fixture using the test's fixed key.
	aead cipher.AEAD
}

// Encrypt seals value and prefixes the random nonce.
func (driver gcmDriver) Encrypt(value []byte) ([]byte, error) {
	nonce := make([]byte, driver.aead.NonceSize())

	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	return driver.aead.Seal(nonce, nonce, value, nil), nil
}

// Decrypt opens a value produced by Encrypt.
func (driver gcmDriver) Decrypt(value []byte) ([]byte, error) {
	n := driver.aead.NonceSize()

	if len(value) < n {
		return nil, errors.New("ciphertext too short")
	}

	return driver.aead.Open(nil, value[:n], value[n:], nil)
}

// testCipher returns an Encrypter over a fixed-key AES-GCM fake.
func testCipher(t *testing.T) *crypto.Encrypter {
	t.Helper()

	block, err := aes.NewCipher(bytes.Repeat([]byte{7}, 32))

	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}

	aead, err := cipher.NewGCM(block)

	if err != nil {
		t.Fatalf("cipher.NewGCM: %v", err)
	}

	enc, err := crypto.New(gcmDriver{
		aead: aead,
	})

	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}

	return enc
}

// newClient returns a started client with baseline login settings followed by
// caller policies. Every policy is passed through once to New on a fresh draft.
func newClient(t *testing.T, provider Provider, configure ...func(*ClientBuilder)) *Client {
	t.Helper()

	return newClientAt(t, t.Context(), provider, configure...)
}

// newClientAt is like newClient but starts the client with ctx, which can carry
// the HTTP client of an offline provider.
func newClientAt(t *testing.T, ctx context.Context, provider Provider, configure ...func(*ClientBuilder)) *Client {
	t.Helper()

	policies := append([]func(*ClientBuilder){func(client *ClientBuilder) {
		client.ClientSecret("secret")
		client.RedirectURL("http://app.test/callback")
		client.Scopes("openid", "read")
		client.Audiences("cid")
	}}, configure...)

	client, err := New(provider, testCipher(t), "cid", policies...)

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := client.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	return client
}

// cookieJar is a minimal browser cookie store.
type cookieJar map[string]*http.Cookie

// apply stores or deletes the cookies set by response.
func (jar cookieJar) apply(response *http.Response) {
	for _, cookie := range response.Cookies() {
		if cookie.MaxAge < 0 {
			delete(jar, cookie.Name)
		} else {
			jar[cookie.Name] = cookie
		}
	}
}

// attach adds the stored cookies to request.
func (jar cookieJar) attach(request *http.Request) *http.Request {
	for _, cookie := range jar {
		request.AddCookie(&http.Cookie{
			Name:  cookie.Name,
			Value: cookie.Value,
		})
	}

	return request
}

// login runs the Login handler with jar and returns the authorization URL query.
func login(t *testing.T, client *Client, jar cookieJar) url.Values {
	t.Helper()

	request := jar.attach(httptest.NewRequest(http.MethodGet, "/login?return_to=/dash", nil))
	response := client.LoginHandler().Record(request)

	location, err := url.Parse(response.Header.Get("Location"))

	if err != nil || response.StatusCode != http.StatusFound {
		t.Fatalf("login = %d %q, %v", response.StatusCode, response.Header.Get("Location"), err)
	}

	jar.apply(response)

	return location.Query()
}

// callbackRequest builds a callback carrying the cookies of jar. The code
// doubles as the nonce the test provider embeds in the ID token.
func callbackRequest(method string, jar cookieJar, sess *session.Session, state, code string) *http.Request {
	values := url.Values{"state": {state}, "code": {code}}

	var request *http.Request

	if method == http.MethodPost {
		request = httptest.NewRequest(method, "/callback", strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		request = httptest.NewRequest(method, "/callback?"+values.Encode(), nil)
	}

	request = jar.attach(request)

	return request.WithContext(session.NewContext(request.Context(), sess))
}

// newSession returns an empty session.
func newSession() *session.Session {
	return session.New(time.Now().Add(time.Hour), map[string]any{})
}

// idLogin maps the identity subject to the user identifier.
func idLogin(_ context.Context, identity auth.External) (string, error) {
	return identity.Subject, nil
}

// testSubjects are the users newGuard seeds: the subjects the fixture
// providers produce plus subjects tests map to explicitly.
var testSubjects = []string{"user-1", "default", "selected", "object-1", "stub-1", "42", "7", "583231"}

// newGuard returns a guard over an in-memory user directory holding
// testSubjects, with a fresh in-memory cache. Callers pass second-factor and
// session-handle policies through configure.
func newGuard(t *testing.T, configure ...auth.GuardBuilderFunc) *auth.Guard {
	t.Helper()

	driver := memoryauth.New()

	for _, subject := range testSubjects {
		if err := driver.Put(auth.UserRecord{Subject: subject}); err != nil {
			t.Fatalf("Put(%q): %v", subject, err)
		}
	}

	users, err := auth.NewUserProvider(driver)

	if err != nil {
		t.Fatalf("NewUserProvider: %v", err)
	}

	guard, err := auth.NewGuard(users, cache.New(memorycache.New()), configure...)

	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}

	return guard
}

// testCredential returns a credential of method that accepts any input. Every
// user has it enrolled when enrolled is true and none has otherwise.
func testCredential(method auth.Method, kind auth.Kind, enrolled bool) auth.Credential {
	return auth.Credential{
		Method: method,
		Kind:   kind,
		Enrolled: func(context.Context, string) (bool, error) {
			return enrolled, nil
		},
		Verify: func(*http.Request, string, jsontext.Value) (auth.Factor, error) {
			return auth.Factor{Method: method, Kind: kind}, nil
		},
	}
}

// withSecondFactor requires a second factor for every login.
func withSecondFactor(policy *auth.GuardBuilder) {
	policy.SecondFactor(testCredential(auth.MethodOTP, auth.KindPossession, true))
}

// withStepUp registers a password and a TOTP step-up method that every user
// has, so freshness follows those methods and not the federated login.
func withStepUp(policy *auth.GuardBuilder) {
	policy.StepUp(
		testCredential(auth.MethodPassword, auth.KindKnowledge, true),
		testCredential(auth.MethodOTP, auth.KindPossession, true),
	)
}

// userOf returns the subject logged in on the session of request.
func userOf(guard *auth.Guard, request *http.Request) (string, error) {
	identity, err := guard.Authenticator()(request)

	return identity.Subject(), err
}
