//go:build integration

package sandbox

import (
	"io"
	"net/http"
	"os"
	"testing"

	"arkoren.dev/core/service/payment"
)

// TestSandboxReadOnlyAuthentication verifies profile authentication with the
// read-only Methods API. It creates no payment and cannot charge or refund.
func TestSandboxReadOnlyAuthentication(t *testing.T) {
	t.Parallel()

	driver, key := sandboxDriver(t)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.mollie.com/v2/methods", nil)

	if err != nil {
		t.Fatal("failed to construct read-only auth request")
	}

	request.Header.Set("Authorization", "Bearer "+key)
	result, err := driver.HTTPClient().Do(request)

	if err != nil {
		t.Fatal("read-only test authentication request failed")
	}

	t.Cleanup(func() {
		// Closing the observed read response has no effect on provider state.
		_ = result.Body.Close()
	})

	if result.StatusCode != http.StatusOK {
		t.Fatalf("read-only authentication HTTP status = %d", result.StatusCode)
	}

	if _, err := io.Copy(io.Discard, io.LimitReader(result.Body, 64<<10)); err != nil {
		t.Fatal("failed to read bounded authentication response")
	}
}

// TestSandboxExistingPaymentSnapshot optionally verifies a manually created
// test payment through the driver. It does not claim hosted approval or refund
// conformance; those require a buyer or an independently prepared test fixture.
func TestSandboxExistingPaymentSnapshot(t *testing.T) {
	t.Parallel()

	driver, _ := sandboxDriver(t)
	id := os.Getenv("MOLLIE_TEST_PAYMENT_ID")

	if id == "" {
		t.Skip("set MOLLIE_TEST_PAYMENT_ID to an existing test-mode tr_ payment")
	}

	observed, err := driver.GetPayment(t.Context(), payment.ID(id))

	if err != nil {
		t.Fatal("existing test-mode payment lookup failed")
	}

	if observed.ID != payment.ID(id) {
		t.Fatal("existing payment identity mismatch")
	}
}
