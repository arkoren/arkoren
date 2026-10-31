//go:build paypal_sandbox

package sandbox

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"arkoren.dev/core/service/payment"
)

// TestSandboxCreateAndReadCheckout is an opt-in real-protocol smoke test, not
// approval or payment conformance. It creates an unapproved sandbox order only;
// it never captures money, completes approval, or selects the production host.
// Run with -tags paypal_sandbox and PAYPAL_SANDBOX_TEST=1, PAYPAL_CLIENT_ID and
// PAYPAL_CLIENT_SECRET. Short mode always skips, even when credentials exist.
func TestSandboxCreateAndReadCheckout(t *testing.T) {
	backend := sandboxBackend(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)

	checkout, err := backend.CreateCheckout(ctx, payment.NewCheckoutRequest(
		payment.Money{Amount: 1200, Currency: "USD"}, "https://shop.example/paid", "sandbox-"+rand.Text()))

	if err != nil {
		t.Fatal(err)
	}

	if checkout.ID == "" || checkout.Status != payment.CheckoutStatusOpen {
		t.Fatal("sandbox creation did not return an unapproved checkout")
	}

	observed, err := backend.GetCheckout(ctx, checkout.ID)

	if err != nil {
		t.Fatal(err)
	}

	if observed.ID != checkout.ID || observed.Status != payment.CheckoutStatusOpen {
		t.Fatal("sandbox read changed the unapproved checkout")
	}
}
