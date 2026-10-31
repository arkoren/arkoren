package webpushpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"

	"arkoren.dev/core/service/push"
)

// TestEndpointProviderBoundary blocks lookalike and suffix-confusion hosts
// while preserving Apple's documented label-boundary subdomain family.
func TestEndpointProviderBoundary(t *testing.T) {
	t.Parallel()

	driver, _, _ := fixture(t)

	for _, endpoint := range []string{
		"https://fcm.googleapis.com.evil.test/path", "https://evilpush.apple.com/path",
		"https://push.apple.com/path", "https://updates.push.services.mozilla.com.evil/path",
		"https://unknown.example/path", "https://2130706433/path",
	} {
		if _, err := driver.checkEndpoint(endpoint); !errors.Is(err, push.ErrInvalidMessage) {
			t.Fatal("accepted unapproved provider")
		}
	}

	for _, endpoint := range []string{
		"https://fcm.googleapis.com/path", "https://updates.push.services.mozilla.com/path",
		"https://web.push.apple.com/path", "https://web.push.apple.com:443/path",
	} {
		if _, err := driver.checkEndpoint(endpoint); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAllowedOriginsReplaceAndSnapshot proves overrides replace rather than
// extend defaults, and neither source slices nor retained drafts remain live.
func TestAllowedOriginsReplaceAndSnapshot(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	origins := []string{"https://push.example.com"}
	var retained *Builder

	driver, err := newFixtureDriver(key, "https://example.com/contact", func(connection *Builder) {
		retained = connection
		connection.AllowedOrigins(origins...)
	})

	if err != nil {
		t.Fatal(err)
	}

	origins[0] = "https://evil.example"
	retained.origins[0] = "https://evil.example"

	if _, err := driver.checkEndpoint("https://push.example.com/opaque"); err != nil {
		t.Fatal(err)
	}

	if _, err := driver.checkEndpoint("https://fcm.googleapis.com/opaque"); err == nil {
		t.Fatal("defaults survived replacement")
	}
}

// TestAllowedOriginsEmptyDeniesAll distinguishes omission from an explicitly
// empty allowlist, which is a useful fail-closed deployment setting.
func TestAllowedOriginsEmptyDeniesAll(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if err != nil {
		t.Fatal(err)
	}

	driver, err := newFixtureDriver(key, "mailto:push@example.com", func(connection *Builder) {
		connection.AllowedOrigins()
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := driver.checkEndpoint("https://fcm.googleapis.com/path"); err == nil {
		t.Fatal("empty override restored defaults")
	}
}
