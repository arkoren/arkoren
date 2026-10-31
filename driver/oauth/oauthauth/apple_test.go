package oauthauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"arkoren.dev/core/http/session"
	"arkoren.dev/foundation/key"
)

// appleKey returns a P-256 key and its PKCS#8 PEM encoding.
func appleKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(key)

	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	return key, pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	})
}

// TestAppleClientSecret verifies the header and claims of the client secret.
func TestAppleClientSecret(t *testing.T) {
	t.Parallel()

	private, pemKey := appleKey(t)

	identity, err := key.ParseSigning(pemKey)

	if err != nil {
		t.Fatal(err)
	}

	secret, err := Apple("TEAM", "KEYID", identity).clientSecret("com.example.web", "")

	if err != nil {
		t.Fatalf("clientSecret: %v", err)
	}

	token, err := jwt.ParseSigned(secret, []jose.SignatureAlgorithm{jose.ES256})

	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if header := token.Headers[0]; header.KeyID != "KEYID" || header.Algorithm != "ES256" {
		t.Errorf("header = %+v", header)
	}

	var claims jwt.Claims

	if err := token.Claims(&private.PublicKey, &claims); err != nil {
		t.Fatalf("verify: %v", err)
	}

	ttl := claims.Expiry.Time().Sub(claims.IssuedAt.Time())

	if claims.Issuer != "TEAM" || claims.Subject != "com.example.web" || !claims.Audience.Contains("https://appleid.apple.com") || ttl <= 0 || ttl > time.Hour {
		t.Errorf("claims = %+v", claims)
	}
}

// TestAppleClientSecretRejectsNilKey verifies that a missing key yields an error.
func TestAppleClientSecretRejectsNilKey(t *testing.T) {
	t.Parallel()

	if _, err := Apple("T", "K", key.Signing{}).clientSecret("c", ""); err == nil {
		t.Error("nil key accepted")
	}
}

// TestAppleClientSecretRejectsMalformedKey verifies that a PEM block with junk key bytes yields an error.
func TestAppleClientSecretRejectsMalformedKey(t *testing.T) {
	t.Parallel()

	bad := []byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----")

	if _, err := key.ParseSigning(bad); err == nil {
		t.Errorf("key %q accepted", bad)
	}
}

// TestAppleNewRejectsBadKey verifies that an invalid key fails at New.
func TestAppleNewRejectsBadKey(t *testing.T) {
	t.Parallel()

	_, err := New(Apple("T", "K", key.Signing{}), testCipher(t), "c")

	if err == nil {
		t.Fatal("New accepted an invalid Apple key")
	}
}

// requireAppleFormPostName runs the form_post flow with the given "user" form
// field: no PKCE, SameSite=None cookies, and the field filling the name only
// when present. It verifies the resulting identity carries the want name.
func requireAppleFormPostName(t *testing.T, user, want string) {
	t.Helper()

	_, pemKey := appleKey(t)
	identity, err := key.ParseSigning(pemKey)

	if err != nil {
		t.Fatal(err)
	}

	provider := Apple("TEAM", "KEYID", identity)
	provider.issuer = newTestProvider(t).server.URL
	provider.clientSecret = nil
	client := newClient(t, provider, func(client *ClientBuilder) {
		client.Scopes()
	})

	jar := cookieJar{}

	query := login(t, client, jar)

	if query.Get("response_mode") != "form_post" || query.Get("scope") != "openid name email" || query.Has("code_challenge") {
		t.Errorf("auth query = %v", query)
	}

	for _, cookie := range jar {
		if cookie.SameSite != http.SameSiteNoneMode {
			t.Errorf("SameSite = %v, want None", cookie.SameSite)
		}
	}

	form := url.Values{"state": {query.Get("state")}, "code": {query.Get("nonce")}, "user": {user}}
	request := httptest.NewRequest(http.MethodPost, "/callback", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request = jar.attach(request)
	request = request.WithContext(session.NewContext(request.Context(), newSession()))

	if status := client.Callback(externalIdentity, func(_ context.Context, identity Identity) (Identity, error) {
		return identity, nil
	}).Record(request).StatusCode; status != http.StatusFound {
		t.Fatalf("status = %d, want 302", status)
	}

	if identity, err := userOfIdentity(request); err != nil || identity.Name != want {
		t.Errorf("identity = %+v, %v; want name %q", identity, err, want)
	}
}

// TestAppleFormPostLoginUserField verifies the "user" form field fills the name.
func TestAppleFormPostLoginUserField(t *testing.T) {
	t.Parallel()

	requireAppleFormPostName(t, `{"name":{"firstName":"Grace","lastName":"Hopper"},"email":"x@y.z"}`, "Grace Hopper")
}

// TestAppleFormPostLoginWithoutUserField verifies the name falls back to the token claim when "user" is absent.
func TestAppleFormPostLoginWithoutUserField(t *testing.T) {
	t.Parallel()

	requireAppleFormPostName(t, "", "Ada")
}
