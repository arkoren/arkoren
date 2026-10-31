package cloudflarecaptcha

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Builder configures a new verifier. Retained drafts cannot change published
// verifiers; native transports remain shared and must be concurrency-safe.
//
// Example:
//
//	v, err := cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
//		connection.Timeout(3 * time.Second)
//	})
//	_, _ = v, err
type Builder struct {
	// client is borrowed and shallow-copied at publication.
	client *http.Client
	// timeout bounds the complete verification, including response reading.
	timeout time.Duration
}

// BuilderFunc edits one construction draft. New calls callbacks directly once,
// synchronously in order; nil callbacks panic and callback panics propagate.
//
// Example:
//
//	var policy cloudflarecaptcha.BuilderFunc = func(connection *cloudflarecaptcha.Builder) {
//		connection.Timeout(time.Second)
//	}
//	_, err := cloudflarecaptcha.New("secret", []string{"example.com"}, policy)
//	_ = err
type BuilderFunc = func(*Builder)

// HTTPClient borrows client without changing it. Nil selects a fresh standard
// client. New snapshots its fields, replaces CheckRedirect to reject redirects,
// and retains its transport and jar; configure those before construction.
// Neither client nor transport is closed. A shorter native timeout still wins.
//
// Example:
//
//	_, err := cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
//		connection.HTTPClient(&http.Client{Timeout: time.Second})
//	})
//	_ = err
func (connection *Builder) HTTPClient(client *http.Client) *Builder {
	connection.client = client

	return connection
}

// Timeout sets the total operation limit. Zero restores ten seconds; negative
// values fail construction with ErrConfiguration. Earlier context deadlines win.
//
// Example:
//
//	_, err := cloudflarecaptcha.New("secret", []string{"example.com"}, func(connection *cloudflarecaptcha.Builder) {
//		connection.Timeout(2 * time.Second)
//	})
//	_ = err
func (connection *Builder) Timeout(timeout time.Duration) *Builder {
	connection.timeout = timeout

	return connection
}

// New validates completed settings without network I/O. Secret must be nonblank;
// hostnames must contain at least one nonempty exact hostname, without schemes,
// ports, wildcards, paths or whitespace. Matching is case-sensitive and has no
// implicit subdomain support. The allowlist is copied before callbacks run.
// Callbacks run once in order even when required inputs are invalid; nil entries
// panic. Empty callback slices omit edits. The returned verifier owns its settings
// independently of retained drafts and has no lifecycle or readiness methods.
//
// Example:
//
//	v, err := cloudflarecaptcha.New("server-secret", []string{"example.com"})
//	if err != nil {
//		return err
//	}
//	_, err = v.Verify(ctx, cloudflarecaptcha.Input{Token: token})
//	return err
func New(secret string, hostnames []string, configure ...BuilderFunc) (*Verifier, error) {
	hostnames = slices.Clone(hostnames)
	connection := &Builder{}

	for _, configure := range configure {
		configure(connection)
	}

	if strings.TrimSpace(secret) == "" || len(hostnames) == 0 {
		return nil, fmt.Errorf("turnstile secret and hostnames required: %w", ErrConfiguration)
	}

	if err := validateHostnames(hostnames); err != nil {
		return nil, err
	}

	if connection.timeout < 0 {
		return nil, fmt.Errorf("turnstile timeout must not be negative: %w", ErrConfiguration)
	}

	if connection.timeout == 0 {
		connection.timeout = 10 * time.Second
	}

	client := http.Client{}

	if connection.client != nil {
		client = *connection.client
	}

	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &Verifier{secret: secret, hostnames: hostnames, client: client, timeout: connection.timeout}, nil
}

// validateHostnames rejects unsafe allowlist syntax without including configured
// values in diagnostics. Exact comparison remains the authorization boundary.
func validateHostnames(hostnames []string) error {
	for _, hostname := range hostnames {
		if hostname == "" || strings.ContainsAny(hostname, ":/*\\?#@") || strings.ContainsFunc(hostname, unicode.IsSpace) {
			return fmt.Errorf("turnstile hostnames must be nonempty bare hostnames: %w", ErrConfiguration)
		}
	}

	return nil
}
