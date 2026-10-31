package gcpcaptcha

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Builder is a construction-only authentication draft. Later calls replace
// earlier settings. Publication detaches this draft, not borrowed transports.
//
// Example:
//
//	configure := func(access *gcpcaptcha.Builder) {
//		access.APIKey("restricted-key")
//	}
//	_, err := gcpcaptcha.New(ctx, "shop-prod", "site-key", []string{"shop.example"}, configure)
//	if err != nil {
//		return err
//	}
type Builder struct {
	// key is an optional API credential, never included in diagnostics.
	key string
	// client bypasses ADC when supplied; its transport is borrowed.
	client *http.Client
}

// BuilderFunc edits one fresh draft once, synchronously in argument order.
// Nil callbacks panic; an expanded nil slice makes no edits.
//
// Example:
//
//	var configure gcpcaptcha.BuilderFunc = func(access *gcpcaptcha.Builder) {
//		access.APIKey("restricted-key")
//	}
type BuilderFunc = func(*Builder)

// APIKey selects API-key authentication. Empty selects ADC unless HTTPClient
// supplies authentication. The key is sent as X-Goog-Api-Key, not in the URL.
//
// Example:
//
//	configure := func(access *gcpcaptcha.Builder) {
//		access.APIKey("restricted-key")
//	}
func (access *Builder) APIKey(key string) *Builder {
	access.key = key

	return access
}

// HTTPClient borrows a client's transport and snapshots its other fields at New.
// A nonnil client bypasses ADC: without APIKey it must authenticate requests
// itself. With APIKey, the key is added and existing transport auth is retained.
// Redirects are always disabled and timeouts capped at ten seconds. The jar is
// discarded. The package never closes the client or its transport. Custom
// transports must respect context, be concurrency-safe and never retry tokens.
// Nil restores the default transport/ADC selection.
//
// Example:
//
//	configure := func(access *gcpcaptcha.Builder) {
//		access.HTTPClient(authenticatedClient)
//	}
func (access *Builder) HTTPClient(client *http.Client) *Builder {
	access.client = client

	return access
}

// requestTimeout bounds calls even when an injected client has no timeout.
const requestTimeout = 10 * time.Second

// projectPattern prevents path/query injection while allowing project numbers.
var projectPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,127}$`)

// hostnameLabel permits internal hyphens, not empty labels or edge hyphens.
var hostnameLabel = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// New creates an immutable, concurrently usable verifier. Hostnames must be
// nonempty DNS names (no scheme, port or wildcard); the copied allowlist matches
// case-insensitively and exactly, never including subdomains automatically.
// Callbacks run once in order before validation, including invalid arguments.
// Nil callbacks panic. No assessment or readiness I/O occurs during construction.
// Without APIKey or HTTPClient, Google's native ADC discovery uses cloud-platform
// scope and may perform credential-loading I/O. Its synchronous discovery API
// has no context parameter; ctx is checked before and after discovery and passed
// to quota-project lookup. Discovery itself cannot be interrupted through ctx.
// The constructor context is not retained. Token refresh receives each Verify
// request's bounded context; asynchronous background refresh is disabled.
// Credential loading failures wrap ErrConfiguration without SDK diagnostic text.
//
// Example:
//
//	v, err := gcpcaptcha.New(ctx, "shop-prod", "site-key", []string{"shop.example"},
//		func(access *gcpcaptcha.Builder) {
//			access.APIKey(apiKey)
//		})
//	if err != nil {
//		return err
//	}
//	_, err = v.Verify(ctx, gcpcaptcha.Input{Token: token, ExpectedAction: "login"})
//	if err != nil {
//		return err
//	}
func New(ctx context.Context, projectID, siteKey string, hostnames []string, configure ...BuilderFunc) (*Verifier, error) {
	hostnames = slices.Clone(hostnames)
	access := &Builder{}

	for _, configure := range configure {
		configure(access)
	}

	if err := validateConfig(projectID, siteKey, hostnames); err != nil {
		return nil, err
	}

	client, err := access.httpClient(ctx)

	if err != nil {
		return nil, err
	}

	return &Verifier{client: client, key: access.key, siteKey: siteKey,
		endpoint:  "https://recaptchaenterprise.googleapis.com/v1/projects/" + projectID + "/assessments",
		hostnames: hostnames}, nil
}

// validateConfig rejects ambiguous trust boundaries without echoing input.
func validateConfig(projectID, siteKey string, hostnames []string) error {
	if !projectPattern.MatchString(projectID) {
		return fmt.Errorf("gcpcaptcha project ID: %w", ErrConfiguration)
	}

	if strings.TrimSpace(siteKey) == "" || len(hostnames) == 0 {
		return fmt.Errorf("gcpcaptcha site key and hostnames required: %w", ErrConfiguration)
	}

	for _, hostname := range hostnames {
		if !validHostname(hostname) {
			return fmt.Errorf("gcpcaptcha hostname: %w", ErrConfiguration)
		}
	}

	return nil
}

// validHostname restricts allowlist entries to unambiguous ASCII DNS names.
func validHostname(hostname string) bool {
	if len(hostname) == 0 || len(hostname) > 253 {
		return false
	}

	for label := range strings.SplitSeq(hostname, ".") {
		if !hostnameLabel.MatchString(label) {
			return false
		}
	}

	return true
}

// httpClient snapshots client policy without taking ownership of its transport.
func (access *Builder) httpClient(ctx context.Context) (*http.Client, error) {
	client := access.client

	if client == nil && access.key == "" {
		var err error
		client, err = defaultClient(ctx)

		if err != nil {
			return nil, safeError(ErrConfiguration, err)
		}
	}

	if client == nil {
		client = &http.Client{}
	}

	owned := *client
	owned.Jar = nil
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	if owned.Timeout <= 0 || owned.Timeout > requestTimeout {
		owned.Timeout = requestTimeout
	}

	return &owned, nil
}
