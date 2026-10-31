package suite

import (
	"errors"
	"testing"

	"arkoren.dev/core/service/payment"
)

// Run executes the original validation, preparation, unsupported-operation, and
// journey sequence. Each scenario receives its own fixture, with no background
// work, automatic approval, provider polling, or additional factory calls.
func Run(t *testing.T, factory func(*testing.T) Fixture, config Policy) {
	t.Helper()

	t.Run("validation", func(t *testing.T) {
		runValidation(t, factory(t).Driver)
	})
	t.Run("client preparation", func(t *testing.T) {
		checkPreparation(t, factory(t), config)
	})

	if config.WithoutSubscriptions {
		t.Run("unsupported subscriptions", func(t *testing.T) {
			checkUnsupportedSubscriptions(t, factory(t).Driver)
		})
	}

	if config.WithoutRefundLookup {
		t.Run("unsupported refund lookup", func(t *testing.T) {
			_, err := factory(t).Driver.GetRefund(t.Context(), "refund-test")

			requireError(t, err, payment.ErrUnsupported)
		})
	}

	if config.WithoutApproval {
		return
	}

	t.Run("payment journey", func(t *testing.T) {
		runPayment(t, factory(t), config)
	})

	if !config.WithoutSubscriptions {
		t.Run("subscription journey", func(t *testing.T) {
			runSubscription(t, factory(t))
		})
	}
}

// checkoutRequest supplies the default amount and valid redirects for validation
// and journeys. Fixture.Amount can replace the journey amount. Fresh fixtures
// keep its deterministic key from colliding with another journey.
func checkoutRequest() payment.CheckoutRequest {
	return payment.NewCheckoutRequest(payment.Money{Amount: 1200, Currency: "USD"}, "https://shop.example/paid", "conformance-order",
		func(details *payment.CheckoutRequestBuilder) {
			details.CancelURL("https://shop.example/cart")
		})
}

// requireError checks error identity without relying on vendor error strings.
// It reports only the class, avoiding potentially sensitive provider records.
func requireError(t *testing.T, err, target error) {
	t.Helper()

	if !errors.Is(err, target) {
		t.Fatalf("error class = %v, want %v", err, target)
	}
}

// requireSuccess stops a dependent journey at its first failed operation.
// A helper keeps scenario bodies focused on the state transitions being tested.
func requireSuccess(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("operation failed: %v", err)
	}
}
