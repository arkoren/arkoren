package oauthauth

import (
	"context"
	"crypto"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/auth/authtest"
	"arkoren.dev/core/http/router"
)

// TestVerifyUsesAPIAudiencesAlongsideBrowserLogin ensures the browser audience
// policy does not replace configured API audiences in public bearer verification.
func TestVerifyUsesAPIAudiencesAlongsideBrowserLogin(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL), func(client *ClientBuilder) {
		client.Audiences("api")
	})

	identity, err := client.Verify(t.Context(), provider.sign(map[string]any{"aud": "api"}))

	if err != nil {
		t.Fatalf("Verify API token: %v", err)
	}

	if identity.Subject != "user-1" {
		t.Fatalf("API token subject = %q, want user-1", identity.Subject)
	}

	if _, err := client.Verify(t.Context(), provider.sign(nil)); err == nil {
		t.Fatal("Verify accepted a token for the browser client ID")
	}
}

// requireBearerStatus sends the token made by token as a bearer credential
// through Verify, auth.Middleware and auth.Require and verifies the status.
func requireBearerStatus(t *testing.T, token func(*testProvider) string, want int) {
	t.Helper()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL))
	handler := auth.Middleware(auth.Bearer(client.Verify))(auth.Require[Identity]()(
		func(w http.ResponseWriter, _ *http.Request) error {
			w.WriteHeader(http.StatusNoContent)

			return nil
		}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token(provider))

	if got := router.Handler(handler).Record(request).StatusCode; got != want {
		t.Errorf("status = %d, want %d", got, want)
	}
}

// TestBearerAuthenticatesAccessToken verifies Verify accepts an access token with auth.Bearer and Require.
func TestBearerAuthenticatesAccessToken(t *testing.T) {
	t.Parallel()

	requireBearerStatus(t, func(p *testProvider) string {
		return p.sign(map[string]any{"scope": "read"})
	}, http.StatusNoContent)
}

// TestBearerRejectsIDToken verifies an ID token is not accepted as an access token.
func TestBearerRejectsIDToken(t *testing.T) {
	t.Parallel()

	requireBearerStatus(t, func(p *testProvider) string {
		return p.sign(map[string]any{"nonce": "n"})
	}, http.StatusUnauthorized)
}

// TestBearerRejectsGarbage verifies an unparsable token yields 401.
func TestBearerRejectsGarbage(t *testing.T) {
	t.Parallel()

	requireBearerStatus(t, func(*testProvider) string {
		return "nope"
	}, http.StatusUnauthorized)
}

// TestIssuerHook verifies the provider issuer check replaces the issuer comparison.
func TestIssuerHook(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	oidcProvider := OIDC(provider.server.URL)
	oidcProvider.identity = func(identity Identity) (Identity, error) {
		if identity.Subject != "user-1" {
			return Identity{}, errors.New("bad subject")
		}

		return identity, nil
	}

	client := newClient(t, oidcProvider)

	if _, err := client.Verify(t.Context(), provider.sign(nil)); err != nil {
		t.Errorf("Verify() error = %v", err)
	}

	denied := newClient(t, func() Provider {
		p := oidcProvider
		p.identity = func(Identity) (Identity, error) {
			return Identity{}, errors.New("no")
		}

		return p
	}())

	if _, err := denied.Verify(t.Context(), provider.sign(nil)); err == nil {
		t.Error("Verify() accepted a token rejected by the hook")
	}
}

// TestVerifyRequiresAudiences verifies Verify refuses to default the
// audience to the client ID, which would accept nonce-less ID tokens.
func TestVerifyRequiresAudiences(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL), func(client *ClientBuilder) {
		client.Audiences()
	})

	if _, err := client.Verify(t.Context(), provider.sign(nil)); !errors.Is(err, ErrNoAudiences) {
		t.Errorf("Verify error = %v, want ErrNoAudiences", err)
	}
}

// TestVerifyRejectsClientIDAudienceWhenAudiencesSet verifies an ID-token
// audience is not accepted once API audiences are configured.
func TestVerifyRejectsClientIDAudienceWhenAudiencesSet(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL), func(client *ClientBuilder) {
		client.Audiences("api")
	})

	if _, err := client.Verify(t.Context(), provider.sign(nil)); err == nil {
		t.Error("Verify() accepted a token for the client ID audience")
	}
}

// TestVerifyConformance runs the authenticator suite against Verify used as
// an auth.Bearer verifier.
func TestVerifyConformance(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL))
	request := func(authorization string) func(*testing.T) *http.Request {
		return func(*testing.T) *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/", nil)

			if authorization != "" {
				r.Header.Set("Authorization", authorization)
			}

			return r
		}
	}

	authtest.Run(t, auth.Bearer(client.Verify), request(""), request("Bearer "+provider.sign(nil)),
		authtest.WithInvalid[Identity](request("Bearer forged")),
		authtest.WithUser(func(t *testing.T, identity Identity) {
			if identity.Subject != "user-1" {
				t.Fatalf("Subject = %q, want user-1", identity.Subject)
			}
		}))
}

// Keycloak test constants configure the shared integration-test provider.
const (
	keycloakIssuerURL    = "http://127.0.0.1:8080/realms/arkoren-integration"
	keycloakClientID     = "arkoren-integration"
	keycloakClientSecret = "arkoren-integration-secret"
	keycloakUsername     = "arkoren-test"
	keycloakPassword     = "arkoren-test-password"
)

// keycloakToken starts a client against Keycloak and returns a direct-grant access token.
func keycloakToken(t *testing.T, ctx context.Context, audiences ...string) (*Client, string) {
	t.Helper()

	client, err := New(OIDC(keycloakIssuerURL), testCipher(t), keycloakClientID,
		func(client *ClientBuilder) {
			client.ClientSecret(keycloakClientSecret)
			client.Scopes("openid", "profile")
			client.Audiences(audiences...)
		},
	)

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := client.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	token, err := client.ready.Load().oauth.PasswordCredentialsToken(ctx, keycloakUsername, keycloakPassword)

	if err != nil {
		t.Fatalf("direct-grant token: %v", err)
	}

	return client, token.AccessToken
}

// requireKeycloakBearer verifies a Keycloak access token for a client accepting
// audiences passes through auth.Bearer with the want status, and on success
// carries the Keycloak issuer, a subject and the profile scope.
func requireKeycloakBearer(t *testing.T, audiences []string, want int) {
	t.Helper()

	if testing.Short() {
		t.Skip("integration test")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	client, token := keycloakToken(t, ctx, audiences...)

	var identity Identity
	handler := auth.Middleware(auth.Bearer(client.Verify))(func(w http.ResponseWriter, r *http.Request) error {
		identity, _ = auth.FromContext[Identity](r.Context())
		w.WriteHeader(http.StatusNoContent)

		return nil
	})

	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)

	if got := handler.Record(request).StatusCode; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}

	if want == http.StatusNoContent && (identity.Issuer != keycloakIssuerURL || identity.Subject == "" || !slices.Contains(identity.Scopes, "profile")) {
		t.Errorf("identity = %+v", identity)
	}
}

// TestKeycloakBearerAcceptsToken verifies Keycloak access tokens through auth.Bearer.
func TestKeycloakBearerAcceptsToken(t *testing.T) {
	requireKeycloakBearer(t, []string{keycloakClientID}, http.StatusNoContent)
}

// TestKeycloakBearerRejectsUnacceptedAudience verifies an unaccepted audience is rejected.
func TestKeycloakBearerRejectsUnacceptedAudience(t *testing.T) {
	requireKeycloakBearer(t, []string{"other"}, http.StatusUnauthorized)
}

// TestVerifyAcceptsUnmarkedJWTForExplicitAudience records the generic adapter's
// limit: an unmarked JWT's purpose cannot be inferred from its signed claims.
func TestVerifyAcceptsUnmarkedJWTForExplicitAudience(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL), func(client *ClientBuilder) {
		client.Audiences("api")
	})

	identity, err := client.Verify(t.Context(), provider.sign(map[string]any{"aud": "api"}))

	if err != nil || identity.Subject != "user-1" {
		t.Fatalf("generic unmarked JWT = %+v, %v", identity, err)
	}
}

// TestVerifyRejectsMissingSubject requires a stable identity key after verified
// claims are mapped, rather than publishing an authenticated empty subject.
func TestVerifyRejectsMissingSubject(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL))

	identity, err := client.Verify(t.Context(), provider.sign(map[string]any{"sub": ""}))

	if err == nil || identity.Subject != "" {
		t.Fatalf("Verify(missing subject) = %+v, %v", identity, err)
	}
}

// TestVerifyPreservesCanceledContext exercises an uncached native JWKS lookup;
// dependency formatting must not erase the caller's cancellation identity.
func TestVerifyPreservesCanceledContext(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := client.Verify(ctx, provider.sign(nil))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify canceled context = %v", err)
	}
}

// TestVerifyPreservesExpiredDeadline checks the same native key-fetch boundary
// with an already-expired deterministic deadline rather than sleeping.
func TestVerifyPreservesExpiredDeadline(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := newClient(t, OIDC(provider.server.URL))
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(1, 0))
	t.Cleanup(cancel)

	_, err := client.Verify(ctx, provider.sign(nil))

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Verify expired deadline = %v", err)
	}
}

// staticVerifierClient uses a real signed token and a local public key. Neither
// discovery nor verification needs HTTP, so cancellation cannot race a key fetch.
func staticVerifierClient(t *testing.T, provider Provider, key crypto.PublicKey) *Client {
	t.Helper()

	client, err := New(provider, testCipher(t), "cid", func(client *ClientBuilder) {
		client.Audiences("cid")
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	client.ready.Store(&endpoints{
		verifier: oidc.NewVerifier(provider.issuer, &oidc.StaticKeySet{
			PublicKeys: []crypto.PublicKey{key},
		}, &oidc.Config{ClientID: "cid"}),
	})

	return client
}

// TestVerifyRejectsCanceledContextWithLocalKey covers native verification that
// succeeds without I/O even though the caller was already canceled.
func TestVerifyRejectsCanceledContextWithLocalKey(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	checked := false
	configuration := OIDC(provider.server.URL)
	configuration.identity = func(identity Identity) (Identity, error) {
		checked = true

		return identity, nil
	}
	client := staticVerifierClient(t, configuration, provider.key.Key)
	token := provider.sign(nil)
	provider.server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	identity, err := client.Verify(ctx, token)

	if !errors.Is(err, context.Canceled) || identity.Subject != "" || identity.Claims != nil {
		t.Fatalf("Verify canceled local-key context = %+v, %v", identity, err)
	}

	if checked {
		t.Fatal("Verify invoked the identity check after entry cancellation")
	}
}

// TestVerifyRejectsExpiredContextWithLocalKey covers the same no-I/O path with
// an already-expired deadline rather than a timing-dependent sleep.
func TestVerifyRejectsExpiredContextWithLocalKey(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	client := staticVerifierClient(t, OIDC(provider.server.URL), provider.key.Key)
	token := provider.sign(nil)
	provider.server.Close()
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(1, 0))
	t.Cleanup(cancel)

	identity, err := client.Verify(ctx, token)

	if !errors.Is(err, context.DeadlineExceeded) || identity.Subject != "" || identity.Claims != nil {
		t.Fatalf("Verify expired local-key context = %+v, %v", identity, err)
	}
}

// TestVerifyRejectsCancellationDuringIdentityCheck ensures a successful native
// verification cannot publish an identity after a provider hook cancels the call.
func TestVerifyRejectsCancellationDuringIdentityCheck(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	configuration := OIDC(provider.server.URL)
	configuration.identity = func(identity Identity) (Identity, error) {
		cancel()

		return identity, nil
	}
	client := staticVerifierClient(t, configuration, provider.key.Key)
	token := provider.sign(nil)
	provider.server.Close()

	identity, err := client.Verify(ctx, token)

	if !errors.Is(err, context.Canceled) || identity.Subject != "" || identity.Claims != nil {
		t.Fatalf("Verify canceled during identity check = %+v, %v", identity, err)
	}
}

// TestVerifyCanceledContextPreservesUnsupported keeps the OAuth2 capability
// error ahead of context errors without starting any provider or connection.
func TestVerifyCanceledContextPreservesUnsupported(t *testing.T) {
	t.Parallel()

	client, err := New(GitHub(), testCipher(t), "cid")

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.Verify(ctx, "token")

	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Verify unsupported canceled context = %v", err)
	}
}

// TestVerifyCanceledContextPreservesMissingAudiences keeps the API-audience
// guard ahead of context errors without native discovery or signature work.
func TestVerifyCanceledContextPreservesMissingAudiences(t *testing.T) {
	t.Parallel()

	client, err := New(OIDC("https://issuer.example.com"), testCipher(t), "cid")

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.Verify(ctx, "token")

	if !errors.Is(err, ErrNoAudiences) {
		t.Fatalf("Verify missing audiences canceled context = %v", err)
	}
}
