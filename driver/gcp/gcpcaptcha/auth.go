package gcpcaptcha

import (
	"context"
	"log/slog"
	"net/http"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
)

// defaultClient discovers native credentials without capturing the construction
// context in an oauth2.TokenSource. DetectDefault has no context parameter, so
// cancellation is checked around discovery. Property lookup uses ctx; later
// token refresh uses the request context through native authorization middleware.
func defaultClient(ctx context.Context) (*http.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	identity, err := credentials.DetectDefault(&credentials.DetectOptions{
		Scopes:              []string{"https://www.googleapis.com/auth/cloud-platform"},
		DisableAsyncRefresh: true,
		// Credential diagnostics can contain secrets; do not inherit SDK logging
		// enabled through the process environment.
		Logger: slog.New(slog.DiscardHandler),
	})

	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return credentialClient(ctx, identity, http.DefaultTransport)
}

// credentialClient wires Google's context-aware authorization middleware without
// adding another token cache. NewClient would introduce a default asynchronous
// cache that refreshes using context.Background; AddAuthorizationMiddleware uses
// identity.Token(request.Context()) directly instead. The native credential
// provider already owns synchronous caching. The base transport remains borrowed.
// Quota-project discovery uses the construction context and is not repeated per
// assessment. This boundary also permits deterministic native-provider tests.
func credentialClient(ctx context.Context, identity *auth.Credentials, base http.RoundTripper) (*http.Client, error) {
	quota, err := identity.QuotaProjectID(ctx)

	if err != nil {
		return nil, err
	}

	client := &http.Client{Transport: quotaTransport{base: base, project: quota}}

	if err := httptransport.AddAuthorizationMiddleware(client, identity); err != nil {
		return nil, err
	}

	return client, nil
}

// quotaTransport carries the native credential's optional billing project into
// the assessment request without changing the caller's request headers.
type quotaTransport struct {
	// base is the borrowed assessment transport, never a credential transport.
	base http.RoundTripper
	// project is resolved once from ADC using the construction context.
	project string
}

// RoundTrip sets only the optional Google quota header on a detached request.
// Authentication and universe validation have already run in native middleware.
func (quotaTransport quotaTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if quotaTransport.project == "" {
		return quotaTransport.base.RoundTrip(request)
	}

	request = request.Clone(request.Context())
	request.Header.Set("X-Goog-User-Project", quotaTransport.project)

	return quotaTransport.base.RoundTrip(request)
}
