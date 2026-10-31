package molliepayment_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"arkoren.dev/core/service/payment"
)

// TestUnsupportedOperationsValidateBeforeRejecting covers malformed portable
// input without dispatch, rather than disguising input errors as capabilities.
func TestUnsupportedOperationsValidateBeforeRejecting(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		t.Fatal("unsupported operation dispatched HTTP")

		return nil, io.EOF
	})

	_, err := driver.CreateSubscriptionCheckout(t.Context(), payment.SubscriptionCheckoutRequest{})

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("subscription validation did not precede unsupported")
	}

	_, err = driver.GetSubscription(t.Context(), "")

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("subscription ID validation did not precede unsupported")
	}

	err = driver.CancelSubscription(t.Context(), payment.CancelSubscriptionRequest{})

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("cancellation validation did not precede unsupported")
	}
}

// TestPreparePaymentUnsupportedWithoutIO proves the facade cannot fabricate a
// native SDK flow, and valid requests never dispatch Mollie API calls.
func TestPreparePaymentUnsupportedWithoutIO(t *testing.T) {
	t.Parallel()

	backend := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		t.Fatal("unsupported preparation dispatched HTTP")

		return nil, io.EOF
	})

	client := payment.New(backend)

	_, err := client.PreparePayment(t.Context(), payment.NewPreparePaymentRequest(
		payment.Money{Amount: 1200, Currency: "EUR"}, "prepare-123"))

	if !errors.Is(err, payment.ErrUnsupported) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
}

// TestPreparePaymentValidationPrecedesUnsupported verifies direct-driver
// validation, including context precedence over zero or unsupported input.
func TestPreparePaymentValidationPrecedesUnsupported(t *testing.T) {
	t.Parallel()

	backend := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		t.Fatal("invalid preparation dispatched HTTP")

		return nil, io.EOF
	})

	_, err := backend.PreparePayment(t.Context(), payment.PreparePaymentRequest{})

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("invalid input was hidden by unsupported operation")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = backend.PreparePayment(ctx, payment.PreparePaymentRequest{})

	if !errors.Is(err, context.Canceled) {
		t.Fatal("context did not precede invalid request")
	}

	var missing context.Context

	_, err = backend.PreparePayment(missing, payment.PreparePaymentRequest{})

	if !errors.Is(err, payment.ErrInvalidRequest) {
		t.Fatal("nil context was accepted")
	}
}
