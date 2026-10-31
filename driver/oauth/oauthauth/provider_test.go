package oauthauth

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/crypto"
)

// testProvider is an in-process OIDC provider. Its token endpoint treats the
// authorization code as the nonce to embed in the ID token.
type testProvider struct {
	// server owns the local discovery, key-set, and token endpoints.
	server *httptest.Server
	// signer signs fixture tokens with the key advertised by discovery.
	signer jose.Signer
	// key is the public verification key exposed by the JWKS endpoint.
	key jose.JSONWebKey

	// exchanges counts token requests across the test server's goroutines.
	// Configuration-failure tests assert that this remains zero.
	exchanges atomic.Int64
}

// newTestProvider starts a provider serving discovery, JWKS and token endpoints.
func newTestProvider(t *testing.T) *testProvider {
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
	provider.server = httptest.NewServer(mux)
	t.Cleanup(provider.server.Close)

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
	idToken := provider.sign(map[string]any{"nonce": r.FormValue("code"), "name": "Ada"})
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
	claims := jwt.Claims{
		Issuer:   provider.server.URL,
		Subject:  "user-1",
		Audience: jwt.Audience{"cid"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}

	raw, err := jwt.Signed(provider.signer).Claims(claims).Claims(extra).Serialize()

	if err != nil {
		panic(err)
	}

	return raw
}

// userInfoStub is a [UserInfoFunc] returning a fixed identity.
func userInfoStub(_ context.Context, client *http.Client) (Identity, error) {
	if client == nil {
		return Identity{}, errors.New("no client")
	}

	return Identity{
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

	if err := client.Start(t.Context()); err != nil {
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
	response := client.Login().Record(request)

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
func idLogin(_ context.Context, identity Identity) (string, error) {
	return identity.Subject, nil
}

// localIdentity is the application identity shared by string-ID login handlers
// and authenticators. Its loader has no I/O and preserves the subject exactly.
var localIdentity = auth.NewSessionIdentity(func(_ context.Context, id string) (string, error) {
	return id, nil
})

// externalIdentity keeps provider identities directly in the existing session
// member for applications without a separate local user record.
var externalIdentity = auth.NewSessionIdentity(func(_ context.Context, id Identity) (Identity, error) {
	return id, nil
})

// userOf returns the user id stored in the session of request.
func userOf(request *http.Request) (string, error) {
	return localIdentity.Authenticator()(request)
}

// userOfIdentity returns the identity stored in the session of request.
func userOfIdentity(request *http.Request) (Identity, error) {
	return externalIdentity.Authenticator()(request)
}
