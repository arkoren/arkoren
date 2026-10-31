//go:build sandbox

// Package sandbox provides explicitly gated Stripe test-mode fixtures.
package sandbox

import (
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/stripe/stripepayment"
)

// sandboxBackend opts into real Stripe test-mode I/O only when both the sandbox
// build tag and ARKOREN_STRIPE_TEST_KEY are present. Live and restricted keys are
// refused before constructing a client. Never display the supplied credential.
func sandboxBackend(t *testing.T) *stripepayment.Driver {
	t.Helper()

	if testing.Short() {
		t.Skip("Stripe sandbox requires non-short tests")
	}

	key := os.Getenv("ARKOREN_STRIPE_TEST_KEY")

	if key == "" {
		t.Skip("set ARKOREN_STRIPE_TEST_KEY to opt into Stripe test-mode I/O")
	}

	if !strings.HasPrefix(key, "sk_test_") {
		t.Fatal("sandbox requires a Stripe sk_test_ key; other modes are refused")
	}

	backend, err := stripepayment.New(key)

	if err != nil {
		t.Fatal(err)
	}

	return backend
}

// confirmSandboxPayment uses Stripe's documented synthetic Visa method instead
// of a native device. It isolates the test-only SDK mutation from the production
// helper, which never confirms. Native errors are not printed because they can
// contain sensitive PaymentIntent fields.
func confirmSandboxPayment(t *testing.T, backend *stripepayment.Driver, id payment.ID) {
	t.Helper()

	params := &stripe.PaymentIntentConfirmParams{PaymentMethod: stripe.String("pm_card_visa")}
	params.SetIdempotencyKey("confirm-" + rand.Text())

	confirmed, err := backend.Client().V1PaymentIntents.Confirm(t.Context(), string(id), params)

	if err != nil {
		t.Fatal("Stripe test-card confirmation failed")
	}

	if confirmed.Livemode || confirmed.Status != stripe.PaymentIntentStatusSucceeded {
		t.Fatal("expected a successful test-mode card payment")
	}
}
