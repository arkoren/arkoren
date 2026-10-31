package oauthauth

import (
	"context"
	"encoding/json/v2"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
)

// microsoftIssuer returns the v2.0 issuer URL of tenant id tid on the test authority.
func microsoftIssuer(idp *testProvider, tid string) string {
	return idp.server.URL + "/" + tid + "/v2.0"
}

// requireMicrosoftVerify verifies a token with the given claims against a
// Microsoft provider for tenant: Verify must fail exactly when wantErr is set,
// and on success must report the oid as subject and the token issuer.
func requireMicrosoftVerify(t *testing.T, idp *testProvider, tenant string, claims map[string]any, wantErr bool) {
	t.Helper()

	client := newClient(t, microsoft(idp.server.URL, idp.server.URL, tenant))

	identity, err := client.Verify(t.Context(), idp.sign(claims))

	if (err != nil) != wantErr {
		t.Fatalf("Verify() error = %v, wantErr %v", err, wantErr)
	}

	if err == nil && (identity.Subject != "o1" || identity.Issuer != claims["iss"]) {
		t.Errorf("identity = %q %q", identity.Issuer, identity.Subject)
	}
}

// TestMicrosoftCommon verifies the Microsoft common case: tenant issuer validation and the oid subject.
func TestMicrosoftCommon(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireMicrosoftVerify(t, idp, "common", map[string]any{"iss": microsoftIssuer(idp, "t1"), "tid": "t1", "oid": "o1"}, false)
}

// TestMicrosoftOrganizations verifies the Microsoft organizations case: tenant issuer validation and the oid subject.
func TestMicrosoftOrganizations(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireMicrosoftVerify(t, idp, "organizations", map[string]any{"iss": microsoftIssuer(idp, "t2"), "tid": "t2", "oid": "o1"}, false)
}

// TestMicrosoftTenantIDMismatch verifies the Microsoft tid mismatch case: tenant issuer validation and the oid subject.
func TestMicrosoftTenantIDMismatch(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireMicrosoftVerify(t, idp, "common", map[string]any{"iss": microsoftIssuer(idp, "t1"), "tid": "t2", "oid": "o1"}, true)
}

// TestMicrosoftMissingOID verifies the Microsoft missing oid case: tenant issuer validation and the oid subject.
func TestMicrosoftMissingOID(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireMicrosoftVerify(t, idp, "common", map[string]any{"iss": microsoftIssuer(idp, "t1"), "tid": "t1"}, true)
}

// TestMicrosoftMissingTenantID verifies the Microsoft missing tid case: tenant issuer validation and the oid subject.
func TestMicrosoftMissingTenantID(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireMicrosoftVerify(t, idp, "common", map[string]any{"iss": microsoftIssuer(idp, "t1"), "oid": "o1"}, true)
}

// TestMicrosoftSpecificTenant verifies the Microsoft specific case: tenant issuer validation and the oid subject.
func TestMicrosoftSpecificTenant(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireMicrosoftVerify(t, idp, "t1", map[string]any{"iss": microsoftIssuer(idp, "t1"), "tid": "t1", "oid": "o1"}, false)
}

// TestMicrosoftOtherTenant verifies the Microsoft other tenant case: tenant issuer validation and the oid subject.
func TestMicrosoftOtherTenant(t *testing.T) {
	t.Parallel()

	idp := newTestProvider(t)

	requireMicrosoftVerify(t, idp, "t1", map[string]any{"iss": microsoftIssuer(idp, "t2"), "tid": "t2", "oid": "o1"}, true)
}

// requireMicrosoftEmail verifies a token carrying claims and returns the
// resulting Identity.Email.
func requireMicrosoftEmail(t *testing.T, extra map[string]any) string {
	t.Helper()

	idp := newTestProvider(t)
	claims := map[string]any{"iss": microsoftIssuer(idp, "t1"), "tid": "t1", "oid": "o1", "email": "a@b.c"}
	maps.Copy(claims, extra)

	client := newClient(t, microsoft(idp.server.URL, idp.server.URL, "common"))

	identity, err := client.Verify(t.Context(), idp.sign(claims))

	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	return identity.Email
}

// TestMicrosoftEmailRequiresVerifiedClaim verifies an email without xms_edov is dropped.
func TestMicrosoftEmailRequiresVerifiedClaim(t *testing.T) {
	t.Parallel()

	if email := requireMicrosoftEmail(t, nil); email != "" {
		t.Errorf("Email = %q, want empty", email)
	}
}

// TestMicrosoftEmailDroppedWhenNotVerified verifies xms_edov false drops the email.
func TestMicrosoftEmailDroppedWhenNotVerified(t *testing.T) {
	t.Parallel()

	if email := requireMicrosoftEmail(t, map[string]any{"xms_edov": false}); email != "" {
		t.Errorf("Email = %q, want empty", email)
	}
}

// TestMicrosoftEmailDroppedForNonBooleanClaim verifies a string "true" is not accepted.
func TestMicrosoftEmailDroppedForNonBooleanClaim(t *testing.T) {
	t.Parallel()

	if email := requireMicrosoftEmail(t, map[string]any{"xms_edov": "true"}); email != "" {
		t.Errorf("Email = %q, want empty", email)
	}
}

// TestMicrosoftEmailKeptWhenVerified verifies xms_edov true keeps the email.
func TestMicrosoftEmailKeptWhenVerified(t *testing.T) {
	t.Parallel()

	if email := requireMicrosoftEmail(t, map[string]any{"xms_edov": true}); email != "a@b.c" {
		t.Errorf("Email = %q, want a@b.c", email)
	}
}

// microsoftPolicyPersonalTenant is Microsoft's documented tenant for personal
// account sign-ins. Tests keep the literal independent of the policy under test.
const microsoftPolicyPersonalTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"

// microsoftPolicyOrganizationTenant represents an ordinary organizational
// tenant, distinct from Microsoft's reserved personal-account tenant.
const microsoftPolicyOrganizationTenant = "11111111-2222-3333-4444-555555555555"

// microsoftPolicyTransport routes the public Microsoft preset's discovery to
// a local OIDC server without replacing the preset or its identity policy.
// All other requests must already target that server, so tests stay offline.
type microsoftPolicyTransport struct {
	// discovery is the local discovery endpoint; it is not mutated after setup.
	discovery *url.URL
}

// RoundTrip preserves normal HTTP exchange and JWKS verification, changing only
// Microsoft's discovery destination. Unexpected external requests fail closed.
func (transport microsoftPolicyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())

	if request.URL.Host == "login.microsoftonline.com" {
		*request.URL = *transport.discovery
		request.Host = transport.discovery.Host
	}

	if request.URL.Host != transport.discovery.Host {
		return nil, errors.New("Microsoft policy fixture received an external request")
	}

	return http.DefaultTransport.RoundTrip(request)
}

// microsoftPolicyClaims describes one tenant-scoped API identity, with matching
// issuer and tid and an oid distinct from the fixture's ordinary sub claim.
// The caller owns the returned map and may add protocol-specific claims.
func microsoftPolicyClaims(tid string) map[string]any {
	return map[string]any{
		"aud": "api",
		"iss": "https://login.microsoftonline.com/" + tid + "/v2.0",
		"tid": tid,
		"oid": "object-1",
	}
}

// newMicrosoftPolicyClient starts the public Microsoft preset against real
// local discovery, JWKS and token endpoints. The token endpoint copies claims,
// selects the browser client-ID audience and binds its nonce to the authorization
// code, leaving caller data unchanged.
// Assertions use Verify or the complete Login/Callback flow, never private checks.
func newMicrosoftPolicyClient(t *testing.T, tenant string, claims map[string]any) (*Client, *testProvider) {
	t.Helper()

	provider := newTestProvider(t)
	base := provider.server.URL
	mux := provider.server.Config.Handler.(*http.ServeMux)
	mux.HandleFunc("/microsoft/discovery", func(writer http.ResponseWriter, _ *http.Request) {
		if err := json.MarshalWrite(writer, map[string]any{
			"issuer":                                "https://login.microsoftonline.com/{tenantid}/v2.0",
			"authorization_endpoint":                base + "/auth",
			"token_endpoint":                        base + "/microsoft/token",
			"jwks_uri":                              base + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}); err != nil {
			t.Errorf("write Microsoft discovery: %v", err)
		}
	})
	mux.HandleFunc("/microsoft/token", func(writer http.ResponseWriter, request *http.Request) {
		tokenClaims := maps.Clone(claims)
		tokenClaims["aud"] = "cid"
		tokenClaims["nonce"] = request.FormValue("code")
		writer.Header().Set("Content-Type", "application/json")

		if err := json.MarshalWrite(writer, map[string]any{
			"access_token": "opaque",
			"token_type":   "Bearer",
			"id_token":     provider.sign(tokenClaims),
		}); err != nil {
			t.Errorf("write Microsoft token response: %v", err)
		}
	})

	discovery, err := url.Parse(base + "/microsoft/discovery")

	if err != nil {
		t.Fatalf("parse discovery URL: %v", err)
	}

	ctx := oidc.ClientContext(t.Context(), &http.Client{
		Transport: microsoftPolicyTransport{discovery: discovery},
	})
	client, err := New(Microsoft(tenant), testCipher(t), "cid", func(client *ClientBuilder) {
		client.ClientSecret("secret")
		client.RedirectURL("http://app.test/callback")
		client.Audiences("api")
	})

	if err != nil {
		t.Fatalf("New Microsoft client: %v", err)
	}

	if err := client.Start(ctx); err != nil {
		t.Fatalf("Start Microsoft client: %v", err)
	}

	return client, provider
}

// TestMicrosoftPolicyOrganizationsRejectsPersonalBearer checks the reserved
// personal tenant case-insensitively even with a matching issuer and signature.
func TestMicrosoftPolicyOrganizationsRejectsPersonalBearer(t *testing.T) {
	t.Parallel()

	claims := microsoftPolicyClaims(strings.ToUpper(microsoftPolicyPersonalTenant))
	client, provider := newMicrosoftPolicyClient(t, "organizations", claims)

	identity, err := client.Verify(t.Context(), provider.sign(claims))

	if err == nil {
		t.Fatalf("Verify accepted personal account: %+v", identity)
	}

	if identity.Subject != "" || identity.Issuer != "" {
		t.Fatalf("Verify returned an identity on rejection: %+v", identity)
	}
}

// TestMicrosoftPolicyConsumersRejectsOrganizationBearer ensures a correctly
// signed organizational token cannot bypass the personal-only authority choice.
func TestMicrosoftPolicyConsumersRejectsOrganizationBearer(t *testing.T) {
	t.Parallel()

	claims := microsoftPolicyClaims(microsoftPolicyOrganizationTenant)
	client, provider := newMicrosoftPolicyClient(t, "consumers", claims)

	identity, err := client.Verify(t.Context(), provider.sign(claims))

	if err == nil {
		t.Fatalf("Verify accepted organizational account: %+v", identity)
	}

	if identity.Subject != "" || identity.Issuer != "" {
		t.Fatalf("Verify returned an identity on rejection: %+v", identity)
	}
}

// TestMicrosoftPolicyConsumersAcceptsPersonalBearer keeps personal sign-ins
// available and preserves Microsoft's immutable oid identity mapping.
func TestMicrosoftPolicyConsumersAcceptsPersonalBearer(t *testing.T) {
	t.Parallel()

	claims := microsoftPolicyClaims(microsoftPolicyPersonalTenant)
	client, provider := newMicrosoftPolicyClient(t, "consumers", claims)

	identity, err := client.Verify(t.Context(), provider.sign(claims))

	if err != nil {
		t.Fatalf("Verify rejected personal account: %v", err)
	}

	if identity.Subject != "object-1" || identity.Issuer != claims["iss"] {
		t.Fatalf("Verify identity = %+v", identity)
	}
}

// TestMicrosoftPolicyOrganizationsAcceptsPersonalGuestBearer checks the token's
// tenant context, not the home identity provider: a personal-account guest of an
// organization remains an organizational sign-in with its own tenant identity.
func TestMicrosoftPolicyOrganizationsAcceptsPersonalGuestBearer(t *testing.T) {
	t.Parallel()

	claims := microsoftPolicyClaims(microsoftPolicyOrganizationTenant)
	claims["idp"] = "live.com"
	client, provider := newMicrosoftPolicyClient(t, "organizations", claims)

	identity, err := client.Verify(t.Context(), provider.sign(claims))

	if err != nil {
		t.Fatalf("Verify rejected organizational guest: %v", err)
	}

	if identity.Subject != "object-1" || identity.Issuer != claims["iss"] {
		t.Fatalf("Verify identity = %+v", identity)
	}
}

// TestMicrosoftPolicyCommonAcceptsPersonalBearer prevents the class restriction
// from narrowing common, which must continue to accept personal tenant tokens.
func TestMicrosoftPolicyCommonAcceptsPersonalBearer(t *testing.T) {
	t.Parallel()

	claims := microsoftPolicyClaims(microsoftPolicyPersonalTenant)
	client, provider := newMicrosoftPolicyClient(t, "common", claims)

	identity, err := client.Verify(t.Context(), provider.sign(claims))

	if err != nil {
		t.Fatalf("Verify rejected personal account through common: %v", err)
	}

	if identity.Subject != "object-1" || identity.Issuer != claims["iss"] {
		t.Fatalf("Verify identity = %+v", identity)
	}
}

// TestMicrosoftPolicyCommonAcceptsOrganizationBearer keeps common open to
// organizational sign-ins as well as personal ones, without rewriting issuers.
func TestMicrosoftPolicyCommonAcceptsOrganizationBearer(t *testing.T) {
	t.Parallel()

	claims := microsoftPolicyClaims(microsoftPolicyOrganizationTenant)
	client, provider := newMicrosoftPolicyClient(t, "common", claims)

	identity, err := client.Verify(t.Context(), provider.sign(claims))

	if err != nil {
		t.Fatalf("Verify rejected organization through common: %v", err)
	}

	if identity.Subject != "object-1" || identity.Issuer != claims["iss"] {
		t.Fatalf("Verify identity = %+v", identity)
	}
}

// TestMicrosoftPolicyExplicitPersonalTenantAcceptsMatchingBearer preserves
// explicit tenant matching even when the chosen ID is the personal-account one.
func TestMicrosoftPolicyExplicitPersonalTenantAcceptsMatchingBearer(t *testing.T) {
	t.Parallel()

	claims := microsoftPolicyClaims(strings.ToUpper(microsoftPolicyPersonalTenant))
	client, provider := newMicrosoftPolicyClient(t, microsoftPolicyPersonalTenant, claims)

	identity, err := client.Verify(t.Context(), provider.sign(claims))

	if err != nil {
		t.Fatalf("Verify rejected matching explicit tenant: %v", err)
	}

	if identity.Subject != "object-1" || identity.Issuer != claims["iss"] {
		t.Fatalf("Verify identity = %+v", identity)
	}
}

// TestMicrosoftPolicyExplicitTenantRejectsOtherBearer keeps explicit tenant
// isolation when the presented token otherwise satisfies issuer and signature.
func TestMicrosoftPolicyExplicitTenantRejectsOtherBearer(t *testing.T) {
	t.Parallel()

	claims := microsoftPolicyClaims(microsoftPolicyPersonalTenant)
	client, provider := newMicrosoftPolicyClient(t, microsoftPolicyOrganizationTenant, claims)

	if identity, err := client.Verify(t.Context(), provider.sign(claims)); err == nil {
		t.Fatalf("Verify accepted a different explicit tenant: %+v", identity)
	}
}

// TestMicrosoftPolicyCommonRejectsUntrustedSignature retains the pinned OIDC
// signature check before any account-class policy can yield an identity.
func TestMicrosoftPolicyCommonRejectsUntrustedSignature(t *testing.T) {
	t.Parallel()

	claims := microsoftPolicyClaims(microsoftPolicyPersonalTenant)
	client, _ := newMicrosoftPolicyClient(t, "common", claims)
	untrusted := newTestProvider(t)

	if identity, err := client.Verify(t.Context(), untrusted.sign(claims)); err == nil {
		t.Fatalf("Verify accepted an untrusted signature: %+v", identity)
	}
}

// TestMicrosoftPolicyOrganizationsRejectsPersonalCallback exercises the full
// browser protocol: a valid nonce and signature must not reach local mapping or
// mutate the session when the account class disagrees with organizations.
func TestMicrosoftPolicyOrganizationsRejectsPersonalCallback(t *testing.T) {
	t.Parallel()

	claims := microsoftPolicyClaims(microsoftPolicyPersonalTenant)
	client, _ := newMicrosoftPolicyClient(t, "organizations", claims)
	jar := cookieJar{}

	query := login(t, client, jar)

	current := newSession()
	before := current.ID()
	request := callbackRequest(http.MethodGet, jar, current, query.Get("state"), query.Get("nonce"))
	response := httptest.NewRecorder()

	err := client.Callback(localIdentity, func(_ context.Context, _ Identity) (string, error) {
		t.Fatal("Callback invoked the mapper for a rejected personal account")

		return "", nil
	})(response, request)

	if !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("Callback error = %v, want ErrInvalidCallback", err)
	}

	if current.ID() != before {
		t.Fatal("Callback rotated the session for a rejected account")
	}

	if id, err := userOf(request); err == nil {
		t.Fatalf("Callback authenticated rejected account %q", id)
	}
}

// TestMicrosoftPolicyConsumersRejectsOrganizationCallback proves personal-only
// policy also protects browser login, not only exported bearer verification.
func TestMicrosoftPolicyConsumersRejectsOrganizationCallback(t *testing.T) {
	t.Parallel()

	claims := microsoftPolicyClaims(microsoftPolicyOrganizationTenant)
	client, _ := newMicrosoftPolicyClient(t, "consumers", claims)
	jar := cookieJar{}

	query := login(t, client, jar)

	current := newSession()
	before := current.ID()
	request := callbackRequest(http.MethodGet, jar, current, query.Get("state"), query.Get("nonce"))
	response := httptest.NewRecorder()

	err := client.Callback(localIdentity, func(_ context.Context, _ Identity) (string, error) {
		t.Fatal("Callback invoked the mapper for a rejected organizational account")

		return "", nil
	})(response, request)

	if !errors.Is(err, ErrInvalidCallback) {
		t.Fatalf("Callback error = %v, want ErrInvalidCallback", err)
	}

	if current.ID() != before {
		t.Fatal("Callback rotated the session for a rejected account")
	}

	if id, err := userOf(request); err == nil {
		t.Fatalf("Callback authenticated rejected account %q", id)
	}
}

// TestMicrosoftPolicyConsumersAcceptsPersonalCallback is the positive browser
// control: the same real token endpoint, nonce binding and session bridge must
// complete successfully for an account allowed by the chosen authority.
func TestMicrosoftPolicyConsumersAcceptsPersonalCallback(t *testing.T) {
	t.Parallel()

	claims := microsoftPolicyClaims(microsoftPolicyPersonalTenant)
	client, _ := newMicrosoftPolicyClient(t, "consumers", claims)
	jar := cookieJar{}

	query := login(t, client, jar)

	current := newSession()
	before := current.ID()
	request := callbackRequest(http.MethodGet, jar, current, query.Get("state"), query.Get("nonce"))
	response := httptest.NewRecorder()

	if err := client.Callback(localIdentity, idLogin)(response, request); err != nil {
		t.Fatalf("Callback rejected personal account: %v", err)
	}

	if response.Code != http.StatusFound || response.Header().Get("Location") != "/dash" {
		t.Fatalf("Callback response = %d %q", response.Code, response.Header().Get("Location"))
	}

	if current.ID() == before {
		t.Fatal("Callback did not rotate the authenticated session")
	}

	if id, err := userOf(request); err != nil || id != "object-1" {
		t.Fatalf("Callback user = %q, error = %v", id, err)
	}
}
