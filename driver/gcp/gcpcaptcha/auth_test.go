package gcpcaptcha

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"cloud.google.com/go/auth"
)

// tokenProvider adapts a native context-aware credential source, not a CAPTCHA
// verifier substitute. Tests observe the same Token(ctx) boundary as ADC refresh.
type tokenProvider func(context.Context) (*auth.Token, error)

// Token delegates native credential lookup using its actual request context.
func (provider tokenProvider) Token(ctx context.Context) (*auth.Token, error) {
	return provider(ctx)
}

// nativeRoundTrip supplies assessment responses after native auth middleware.
type nativeRoundTrip func(*http.Request) (*http.Response, error)

// RoundTrip delegates the native middleware's authenticated assessment request.
func (transport nativeRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// TestNativeRefreshUsesVerifyDeadline proves credential refresh is part of the
// bounded request, not work using a retained constructor context.
func TestNativeRefreshUsesVerifyDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		identity := auth.NewCredentials(&auth.CredentialsOptions{
			TokenProvider: tokenProvider(func(ctx context.Context) (*auth.Token, error) {
				<-ctx.Done()

				return nil, errors.Join(ctx.Err(), errors.New("secret auth diagnostic"))
			}),
		})

		client, err := credentialClient(t.Context(), identity, nativeRoundTrip(func(*http.Request) (*http.Response, error) {
			t.Error("assessment sent without completed authentication")

			return nil, errors.New("unexpected assessment")
		}))

		if err != nil {
			t.Fatal(err)
		}

		verifier, err := New(t.Context(), "shop-prod", "site-key", []string{"shop.example"}, func(access *Builder) {
			access.HTTPClient(client)
		})

		if err != nil {
			t.Fatal(err)
		}

		startedAt := time.Now()
		_, err = verifier.Verify(t.Context(), Input{Token: "browser-token"})

		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("lost bounded-refresh cause: %v", err)
		}

		if time.Since(startedAt) != 10*time.Second || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unbounded or unsafe auth failure: %v", err)
		}
	})
}

// TestNativeRefreshUsesEarlierRequestDeadline verifies auth sees the caller's
// earlier bound rather than only the package's ten-second maximum.
func TestNativeRefreshUsesEarlierRequestDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		identity := auth.NewCredentials(&auth.CredentialsOptions{
			TokenProvider: tokenProvider(func(ctx context.Context) (*auth.Token, error) {
				<-ctx.Done()

				return nil, ctx.Err()
			}),
		})

		client, err := credentialClient(t.Context(), identity, nativeRoundTrip(func(*http.Request) (*http.Response, error) {
			t.Error("unexpected assessment")

			return nil, errors.New("unexpected")
		}))

		if err != nil {
			t.Fatal(err)
		}

		verifier := &Verifier{client: client, endpoint: "https://recaptchaenterprise.googleapis.com/v1/projects/shop-prod/assessments"}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		t.Cleanup(cancel)

		startedAt := time.Now()
		_, err = verifier.Verify(ctx, Input{Token: "token"})

		if !errors.Is(err, context.DeadlineExceeded) || time.Since(startedAt) != time.Second {
			t.Fatalf("earlier auth deadline lost: %v", err)
		}
	})
}

// TestNativeAuthDoesNotRetainConstructionContext checks native authentication and
// quota lookup separately: properties use construction ctx, refresh uses Verify.
func TestNativeAuthDoesNotRetainConstructionContext(t *testing.T) {
	t.Parallel()

	construction, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	var calls int
	identity := auth.NewCredentials(&auth.CredentialsOptions{
		TokenProvider: tokenProvider(func(ctx context.Context) (*auth.Token, error) {
			calls++

			if ctx.Err() != nil {
				t.Fatal("construction cancellation leaked into refresh")
			}

			return &auth.Token{Value: "access-secret"}, nil
		}),
		QuotaProjectIDProvider: auth.CredentialsPropertyFunc(func(ctx context.Context) (string, error) {
			if ctx != construction {
				t.Error("quota lookup lost construction context")
			}

			return "billing-project", nil
		}),
	})

	client, err := credentialClient(construction, identity, nativeRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer access-secret" {
			t.Error("native authentication missing")
		}

		if request.Header.Get("X-Goog-User-Project") != "billing-project" {
			t.Error("quota project missing")
		}

		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
			`{"tokenProperties":{"valid":true,"hostname":"shop.example"},"riskAnalysis":{"score":0.9}}`))}, nil
	}))

	if err != nil {
		t.Fatal(err)
	}

	if calls != 0 {
		t.Fatal("constructor refreshed credentials")
	}

	cancel()
	verifier := &Verifier{client: client, endpoint: "https://recaptchaenterprise.googleapis.com/v1/projects/shop-prod/assessments", hostnames: []string{"shop.example"}}

	if _, err := verifier.Verify(t.Context(), Input{Token: "token"}); err != nil {
		t.Fatal(err)
	}
}

// TestNativeRefreshCancellation reaches cancellation during authentication,
// rather than merely testing Verify's pre-canceled request guard.
func TestNativeRefreshCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	identity := auth.NewCredentials(&auth.CredentialsOptions{
		TokenProvider: tokenProvider(func(request context.Context) (*auth.Token, error) {
			cancel()
			<-request.Done()

			return nil, request.Err()
		}),
	})

	client, err := credentialClient(t.Context(), identity, nativeRoundTrip(func(*http.Request) (*http.Response, error) {
		t.Error("assessment sent after authentication canceled")

		return nil, errors.New("unexpected")
	}))

	if err != nil {
		t.Fatal(err)
	}

	verifier := &Verifier{client: client, endpoint: "https://recaptchaenterprise.googleapis.com/v1/projects/shop-prod/assessments"}

	_, err = verifier.Verify(ctx, Input{Token: "token"})

	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("refresh cancellation lost: %v", err)
	}
}

// TestADCDiscoveryCanceledBeforeLookup proves ADC construction checks ctx before
// touching the environment, filesystem or metadata discovery path.
func TestADCDiscoveryCanceledBeforeLookup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := New(ctx, "shop-prod", "site-key", []string{"shop.example"})

	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrConfiguration) {
		t.Fatalf("discovery cancellation lost: %v", err)
	}
}

// TestADCDiscoveryDoesNotRefreshTokens exercises real native discovery using a
// local authorized-user fixture, with no live credentials or network requests.
// It is not parallel because ADC discovery reads process-wide environment.
func TestADCDiscoveryDoesNotRefreshTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adc.json")

	if err := os.WriteFile(path, []byte(`{"type":"authorized_user","client_id":"test-client","client_secret":"test-secret","refresh_token":"test-refresh","quota_project_id":"test-quota"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)

	verifier, err := New(t.Context(), "shop-prod", "site-key", []string{"shop.example"})

	if err != nil {
		t.Fatal(err)
	}

	if verifier.client == nil {
		t.Fatal("native credential discovery did not publish a client")
	}
}

// TestNativeQuotaLookupPreservesConstructionCause keeps context-aware credential
// property failures inspectable before any authentication or assessment occurs.
func TestNativeQuotaLookupPreservesConstructionCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret quota discovery failure")
	identity := auth.NewCredentials(&auth.CredentialsOptions{
		TokenProvider: tokenProvider(func(context.Context) (*auth.Token, error) {
			t.Error("construction attempted token refresh")

			return nil, errors.New("unexpected")
		}),
		QuotaProjectIDProvider: auth.CredentialsPropertyFunc(func(context.Context) (string, error) {
			return "", cause
		}),
	})

	_, err := credentialClient(t.Context(), identity, http.DefaultTransport)

	if !errors.Is(err, cause) {
		t.Fatalf("quota lookup cause lost: %v", err)
	}
}
