package wiring_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"arkoren.dev/ark/access"
	"arkoren.dev/ark/access/account"
	"arkoren.dev/ark/access/federation"
	"arkoren.dev/ark/access/password"
	"arkoren.dev/ark/oauth/oauthauth"
	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/link"
	"arkoren.dev/core/http/middleware/ratelimit"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/aes/aescrypto"
	"arkoren.dev/driver/bcrypt/bcrypthash"
	sqlite "arkoren.dev/driver/sqlite/sqlitedatabase"
	"arkoren.dev/foundation/key"
	"arkoren.dev/foundation/totp"
)

const (
	// issuer is Google's, because the Google preset is the one with native login
	// and its issuer is fixed; the fake provider answers for it in process.
	issuer = "https://accounts.google.com"

	// nativeAudience is the client ID a mobile app's ID tokens are issued to.
	nativeAudience = "mobile-app"
)

// fakeProvider is an in-process OpenID Connect provider. Its token endpoint
// treats the authorization code as the nonce to embed in the ID token, so a test
// completes a login without a browser.
type fakeProvider struct {
	signer jose.Signer
	key    jose.JSONWebKey
	mux    *http.ServeMux

	mu      sync.Mutex
	subject string
	email   string
}

// newFakeProvider returns a provider with a fresh signing key.
func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()

	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: private, KeyID: "k1"}}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	provider := &fakeProvider{
		signer: signer,
		key:    jose.JSONWebKey{Key: private.Public(), KeyID: "k1", Algorithm: "RS256", Use: "sig"},
		mux:    http.NewServeMux(),
	}
	provider.mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.MarshalWrite(w, map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/auth",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	provider.mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.MarshalWrite(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{provider.key}})
	})
	provider.mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		provider.mu.Lock()
		defer provider.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{
			"access_token": "opaque",
			"token_type":   "Bearer",
			"id_token":     provider.token("cid", provider.subject, provider.email, r.FormValue("code")),
		})
	})

	return provider
}

// as sets the identity the next token endpoint call returns.
func (provider *fakeProvider) as(subject, email string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()

	provider.subject, provider.email = subject, email
}

// token signs an ID token for audience carrying a verified email and nonce.
func (provider *fakeProvider) token(audience, subject, email, nonce string) string {
	raw, err := jwt.Signed(provider.signer).Claims(jwt.Claims{
		Issuer:   issuer,
		Subject:  subject,
		Audience: jwt.Audience{audience},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).Claims(map[string]any{"nonce": nonce, "email": email, "email_verified": true, "name": "Ada"}).Serialize()
	if err != nil {
		panic(err)
	}

	return raw
}

// httpClient serves requests to the provider's handlers without a network.
func (provider *fakeProvider) httpClient() *http.Client {
	return &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		provider.mux.ServeHTTP(recorder, r)

		return recorder.Result(), nil
	})}
}

// roundTripper adapts a function to http.RoundTripper.
type roundTripper func(*http.Request) (*http.Response, error)

// RoundTrip calls the function.
func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// browser is a cookie-keeping HTTP client of the system's server.
type browser struct {
	t      *testing.T
	server string
	http   *http.Client
}

// reply is one completed response.
type reply struct {
	status int
	body   string
	header http.Header
}

// newBrowser returns a client with an empty cookie jar.
func newBrowser(t *testing.T, server string) *browser {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}

	return &browser{t: t, server: server, http: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// device returns another browser of the same server.
func (b *browser) device() *browser {
	return newBrowser(b.t, b.server)
}

// do sends a request with an optional JSON body and "Name: value" headers.
func (b *browser) do(method, path, body string, headers ...string) reply {
	b.t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, b.server+path, reader)
	if err != nil {
		b.t.Fatalf("request: %v", err)
	}
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, header := range headers {
		name, value, _ := strings.Cut(header, ": ")
		request.Header.Set(name, value)
	}
	response, err := b.http.Do(request)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		b.t.Fatalf("read body: %v", err)
	}

	return reply{status: response.StatusCode, body: string(data), header: response.Header}
}

// post sends a POST with a JSON body.
func (b *browser) post(path, body string) reply {
	b.t.Helper()

	return b.do(http.MethodPost, path, body)
}

// get sends a GET.
func (b *browser) get(path string, headers ...string) reply {
	b.t.Helper()

	return b.do(http.MethodGet, path, "", headers...)
}

// system is a composed facade behind a running server.
type system struct {
	iam      *access.Access
	provider *fakeProvider
	browser  *browser
	verified []account.VerifyMessage
	mu       sync.Mutex
}

// deliverVerification records the verification emails the facade sends.
func (sys *system) deliverVerification(_ context.Context, message account.VerifyMessage) error {
	sys.mu.Lock()
	defer sys.mu.Unlock()

	sys.verified = append(sys.verified, message)

	return nil
}

// newCipher returns an AES encrypter over a fresh key.
func newCipher(t *testing.T) *crypto.Encrypter {
	t.Helper()

	secret, err := key.GenerateShared()
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	driver, err := aescrypto.New(secret)
	if err != nil {
		t.Fatalf("aescrypto.New: %v", err)
	}
	encrypter, err := crypto.New(driver)
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}

	return encrypter
}

// newSystem composes the facade with the Google provider of the fake provider
// over in-memory SQLite, runs the migrations and serves the routes.
func newSystem(t *testing.T, configure ...access.BuilderFunc) *system {
	t.Helper()

	return newSystemWith(t, true, configure...)
}

// newSystemWith is [newSystem] with the built-in users table and its email
// verification when builtIn is true, and without them for a test that brings its
// own users.
func newSystemWith(t *testing.T, builtIn bool, configure ...access.BuilderFunc) *system {
	t.Helper()

	driver, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := driver.Start(t.Context()); err != nil {
		t.Fatalf("start database: %v", err)
	}
	t.Cleanup(func() { _ = driver.Stop(context.Background()) })
	db := database.New(driver)
	sys := &system{provider: newFakeProvider(t)}
	ctx := oidc.ClientContext(t.Context(), sys.provider.httpClient())
	google, err := oauthauth.New(oauthauth.Google(), newCipher(t), "cid", func(client *oauthauth.ClientBuilder) {
		client.ClientSecret("secret")
		client.RedirectURL("http://app.test/auth/google/callback")
		client.Audiences("cid")
		client.NativeAudiences(nativeAudience)
		client.AllowInsecure(true)
	})
	if err != nil {
		t.Fatalf("oauthauth.New: %v", err)
	}
	if err := google.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	secret, err := key.GenerateShared()
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	signer, err := link.New(secret)
	if err != nil {
		t.Fatalf("link.New: %v", err)
	}
	sys.iam, err = access.New(db, cache.New(memorycache.New()), ratelimit.TrustedProxyKey(), append([]access.BuilderFunc{func(iam *access.Builder) {
		iam.Password(hash.New(bcrypthash.New(func(driver *bcrypthash.Builder) { driver.Cost(4) })), func(passwords *password.Builder) {
			passwords.Registration(true).Reset(signer, func(ctx context.Context, request password.ResetRequest) error {
				return sys.iam.SendReset(ctx, request)
			}, func(context.Context, password.ResetMessage) error { return nil })
		})
		if builtIn {
			iam.Directory(func(accounts *account.Builder) {
				accounts.Verification(signer, func(ctx context.Context, request account.VerifyRequest) error {
					return sys.iam.SendVerification(ctx, request)
				}, sys.deliverVerification)
			})
		}
		iam.TwoFactor(totp.New("Example"), newCipher(t))
		iam.Tokens()
		iam.Provider(google).Federation(func(links *federation.Builder) { links.SignUp(true) })
		iam.Session(func(cookies *session.Builder) { cookies.AllowInsecure(true) })
	}}, configure...)...)
	if err != nil {
		t.Fatalf("access.New: %v", err)
	}
	migrator := migrate.New(db)
	if err := migrator.Add(sys.iam.Migrations()...); err != nil {
		t.Fatalf("add migrations: %v", err)
	}
	if err := migrator.Up(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	routes := router.New()
	// The fake provider has no network: give every request its HTTP client, the
	// way an application would install a proxy-aware client.
	routes.Use(func(next router.Handler) router.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			return next(w, r.WithContext(oidc.ClientContext(r.Context(), sys.provider.httpClient())))
		}
	})
	web := routes.With(sys.iam.Middleware())
	sys.iam.Mount(web)
	web.Get("/me", func(w http.ResponseWriter, r *http.Request) error {
		identity, ok := auth.FromContext(r.Context())
		if !ok {
			_, err := io.WriteString(w, "anonymous")

			return err
		}
		_, err := io.WriteString(w, strings.Join([]string{identity.Subject(), levelName(identity.Level()), boolName(identity.Session())}, "|"))

		return err
	})
	server := httptest.NewServer(routes)
	t.Cleanup(server.Close)
	sys.browser = newBrowser(t, server.URL)

	return sys
}

// levelName renders an identity level.
func levelName(level auth.Level) string {
	switch level {
	case auth.LevelNone:
		return "none"
	case auth.LevelSingle:
		return "single"
	default:
		return "multi"
	}
}

// boolName renders a bool.
func boolName(value bool) string {
	if value {
		return "session"
	}

	return "token"
}

// code returns the TOTP code of a base32 secret at an offset from now.
func code(t *testing.T, encoded string, offset time.Duration) string {
	t.Helper()

	secret, err := totp.DecodeSecret(encoded)
	if err != nil {
		t.Fatalf("DecodeSecret: %v", err)
	}
	value, err := totp.New("Example").Generate(secret, time.Now().Add(offset))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	return value
}

// field returns the string member of a flat JSON object.
func field(t *testing.T, body, name string) string {
	t.Helper()

	var members map[string]any
	if err := json.Unmarshal([]byte(body), &members); err != nil {
		t.Fatalf("body %q is not JSON: %v", body, err)
	}
	value, _ := members[name].(string)

	return value
}

// authorize starts a browser login at path and returns the authorization
// request's state and nonce, which the fake provider's token endpoint echoes.
func authorize(t *testing.T, b *browser, path string) (state, nonce string) {
	t.Helper()

	login := b.get(path)
	if login.status != http.StatusFound {
		t.Fatalf("GET %s = %d %s", path, login.status, login.body)
	}
	location, err := url.Parse(login.header.Get("Location"))
	if err != nil {
		t.Fatalf("location: %v", err)
	}

	return location.Query().Get("state"), location.Query().Get("nonce")
}
