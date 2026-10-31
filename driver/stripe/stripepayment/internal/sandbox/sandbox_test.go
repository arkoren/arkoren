//go:build sandbox

package sandbox

import (
	"crypto/rand"
	"testing"

	"arkoren.dev/core/service/payment"
)

// TestSandboxPrepareAndRead exercises preparation through the public read
// contract without collecting payment details or confirming an intent.
func TestSandboxPrepareAndRead(t *testing.T) {
	t.Parallel()

	backend := sandboxBackend(t)
	client := payment.New(backend)

	result, err := client.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
		payment.Money{Amount: 100, Currency: "USD"}, "prepare-"+rand.Text(),
		func(details *payment.PreparePaymentRequestBuilder) {
			details.Reference("arkoren-sandbox")
		},
	))

	if err != nil {
		t.Fatal(err)
	}

	current, err := client.GetPayment(t.Context(), result.PaymentID)

	if err != nil {
		t.Fatal(err)
	}

	if current.Status != payment.StatusRequiresAction || current.Amount.Amount != 100 {
		t.Fatal("prepared PaymentIntent should require a payment method, not be paid")
	}
}

// TestSandboxPreparedTestCardPaymentAndRefund verifies that a prepared PaymentIntent
// remains usable through existing provider-neutral payment and refund operations.
// This cannot verify device wallets, PaymentSheet, or 3DS handling.
func TestSandboxPreparedTestCardPaymentAndRefund(t *testing.T) {
	t.Parallel()

	backend := sandboxBackend(t)
	client := payment.New(backend)

	result, err := client.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
		payment.Money{Amount: 100, Currency: "USD"}, "prepare-"+rand.Text(),
		func(details *payment.PreparePaymentRequestBuilder) {
			details.Reference("arkoren-sandbox-refund")
		},
	))

	if err != nil {
		t.Fatal(err)
	}

	confirmSandboxPayment(t, backend, result.PaymentID)

	current, err := client.GetPayment(t.Context(), result.PaymentID)

	if err != nil {
		t.Fatal(err)
	}

	if current.Status != payment.StatusSucceeded {
		t.Fatal("provider-neutral read did not observe test payment success")
	}

	refund, err := client.RefundPayment(t.Context(), payment.NewRefundRequest(
		result.PaymentID, payment.Money{Amount: 100, Currency: "USD"}, "refund-"+rand.Text(),
	))

	if err != nil {
		t.Fatal(err)
	}

	if refund.PaymentID != result.PaymentID || refund.Status != payment.RefundStatusSucceeded {
		t.Fatal("test payment refund did not succeed")
	}

	observed, err := client.GetRefund(t.Context(), refund.ID)

	if err != nil {
		t.Fatal(err)
	}

	if observed.PaymentID != result.PaymentID || observed.Status != payment.RefundStatusSucceeded {
		t.Fatal("provider-neutral read did not observe test refund success")
	}
}
