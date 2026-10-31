//go:build integration

// Package sandbox provides explicitly gated, read-only Mollie sandbox fixtures.
package sandbox

import (
	"os"
	"strings"
	"testing"

	"arkoren.dev/driver/mollie/molliepayment"
)

// sandboxDriver requires both explicit opt-in and a test-mode profile key.
// It never accepts live credentials, mutates provider state or automates buyers.
func sandboxDriver(t *testing.T) (*molliepayment.Driver, string) {
	t.Helper()

	if testing.Short() || os.Getenv("MOLLIE_INTEGRATION") != "1" {
		t.Skip("set MOLLIE_INTEGRATION=1 and a test-mode MOLLIE_API_KEY to opt in")
	}

	key := os.Getenv("MOLLIE_API_KEY")

	if !strings.HasPrefix(key, "test_") {
		t.Fatal("integration tests require a test-mode API key; live requests are forbidden")
	}

	driver, err := molliepayment.New(key)

	if err != nil {
		t.Fatal("invalid test-mode API key")
	}

	return driver, key
}
