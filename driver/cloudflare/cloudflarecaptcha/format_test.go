package cloudflarecaptcha_test

import (
	"fmt"
	"testing"

	"arkoren.dev/driver/cloudflare/cloudflarecaptcha"
)

// TestOwnedVerifierFormattingRedactsSecret protects the constructor's owned
// server credential. It performs no verification or provider network access.
func TestOwnedVerifierFormattingRedactsSecret(t *testing.T) {
	t.Parallel()

	verifier, err := cloudflarecaptcha.New("synthetic-owned-secret", []string{"example.com"})

	if err != nil {
		t.Fatal(err)
	}

	for _, format := range []string{"%v", "%+v", "%#v"} {
		if got := fmt.Sprintf(format, verifier); got != "[REDACTED Turnstile verifier]" {
			t.Fatalf("owned verifier not redacted for %s", format)
		}
	}
}
