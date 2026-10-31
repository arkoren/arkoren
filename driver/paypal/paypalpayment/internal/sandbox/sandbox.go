//go:build paypal_sandbox

// Package sandbox provides explicitly gated PayPal sandbox fixtures.
package sandbox

import (
	"os"
	"testing"

	"arkoren.dev/driver/paypal/paypalpayment"
)

// sandboxBackend requires both a build tag and explicit runtime opt-in before
// reading credentials. Its default origin is sandbox; no live selection is
// exposed. Construction itself performs no I/O and reports sanitized failures.
func sandboxBackend(t *testing.T) *paypalpayment.Driver {
	t.Helper()

	if testing.Short() || os.Getenv("PAYPAL_SANDBOX_TEST") != "1" {
		t.Skip("requires explicit PayPal sandbox opt-in outside short mode")
	}

	clientID, secret := os.Getenv("PAYPAL_CLIENT_ID"), os.Getenv("PAYPAL_CLIENT_SECRET")

	if clientID == "" || secret == "" {
		t.Skip("requires PayPal sandbox app credentials")
	}

	backend, err := paypalpayment.New(clientID, secret)

	if err != nil {
		t.Fatal(err)
	}

	return backend
}
