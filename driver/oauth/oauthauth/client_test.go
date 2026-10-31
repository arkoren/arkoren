package oauthauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
)

// TestNewRejectsMissingCipher verifies New fails without a cipher.
func TestNewRejectsMissingCipher(t *testing.T) {
	t.Parallel()

	if _, err := New(OIDC("https://i"), nil, "id"); err == nil {
		t.Error("New succeeded")
	}
}

// TestNewRejectsMissingClientID verifies New fails without a client id.
func TestNewRejectsMissingClientID(t *testing.T) {
	t.Parallel()

	if _, err := New(OIDC("https://i"), testCipher(t), ""); err == nil {
		t.Error("New succeeded")
	}
}

// TestNewRejectsZeroProvider verifies New fails for the zero Provider.
func TestNewRejectsZeroProvider(t *testing.T) {
	t.Parallel()

	if _, err := New(Provider{}, testCipher(t), "id"); err == nil {
		t.Error("New succeeded")
	}
}

// TestNewRejectsProviderWithoutUserInfo verifies New fails for an OAuth2 provider without user info.
func TestNewRejectsProviderWithoutUserInfo(t *testing.T) {
	t.Parallel()

	endpoint := Endpoint{
		AuthURL:  "a",
		TokenURL: "t",
	}

	if _, err := New(OAuth2(endpoint, nil), testCipher(t), "id"); err == nil {
		t.Error("New succeeded")
	}
}

// TestClientRejectsUseBeforeStart verifies handlers fail with ErrNotStarted.
func TestClientRejectsUseBeforeStart(t *testing.T) {
	t.Parallel()

	client, err := New(OIDC("https://issuer.example"), testCipher(t), "id", func(client *ClientBuilder) {
		client.RedirectURL("https://app/cb")
		client.Audiences("api")
	})

	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := client.Verify(t.Context(), "token"); !errors.Is(err, ErrNotStarted) {
		t.Errorf("Verify error = %v, want ErrNotStarted", err)
	}

	if err := client.Login()(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/login", nil)); !errors.Is(err, ErrNotStarted) {
		t.Errorf("Login error = %v, want ErrNotStarted", err)
	}

	response := client.Callback(externalIdentity, func(_ context.Context, identity Identity) (Identity, error) {
		return identity, nil
	}).
		Record(httptest.NewRequest(http.MethodGet, "/callback", nil))

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", response.StatusCode)
	}
}

// protocolTransport supplies local synthetic token/user responses without
// contacting a provider. The request remains available to each fixture policy.
type protocolTransport func(*http.Request) (*http.Response, error)

// RoundTrip implements the native transport seam; fixture policies own response
// contents and must not perform real provider I/O.
func (transport protocolTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// protocolResponse builds a JSON response with a closeable in-memory body. It
// models protocol responses, not the client's private endpoint state.
func protocolResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// TestNewRejectsInvalidCookieName prevents a successful construction followed
// by net/http silently omitting the login cookie during a redirect.
func TestNewRejectsInvalidCookieName(t *testing.T) {
	t.Parallel()

	client, err := New(GitHub(), testCipher(t), "cid", func(client *ClientBuilder) {
		client.Name("invalid name")
	})

	if err == nil || client != nil {
		t.Fatal("New accepted an invalid login cookie name")
	}
}

// TestStartPreservesDiscoveryReadCancellation covers cancellation after HTTP
// headers, where native discovery formats the body-read error without wrapping.
func TestStartPreservesDiscoveryReadCancellation(t *testing.T) {
	t.Parallel()

	client, err := New(OIDC("https://issuer.example"), testCipher(t), "cid")

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	transport := protocolTransport(func(*http.Request) (*http.Response, error) {
		response := protocolResponse(http.StatusOK, "")
		response.Body = cancelingBody(cancel)

		return response, nil
	})

	err = client.Start(oidc.ClientContext(ctx, &http.Client{Transport: transport}))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start canceled body read = %v", err)
	}
}

// cancelingBody ends the caller's context during response parsing. It isolates
// the native discovery read path without waiting on wall-clock deadlines.
type cancelingBody context.CancelFunc

// Read reports the same cancellation it triggers, modeling a canceled network
// response rather than a preflight failure before HTTP headers arrive.
func (body cancelingBody) Read([]byte) (int, error) {
	body()

	return 0, context.Canceled
}

// Close owns no resource; the body exists only to inject a read failure.
func (body cancelingBody) Close() error {
	return nil
}
