package suite

import (
	"cmp"
	"testing"

	"arkoren.dev/core/service/payment"
)

// checkPreparation exercises initialization independently of hosted journeys.
// Valid session-only or order-only responses need no financial lookup; the suite
// checks only observed reference syntax and a required well-formed action.
func checkPreparation(t *testing.T, harness Fixture, config Policy) {
	t.Helper()

	amount := cmp.Or(harness.Amount, payment.Money{Amount: 1200, Currency: "USD"})
	request := payment.NewPreparePaymentRequest(amount, "conformance-prepare")

	prepared, err := harness.Driver.PreparePayment(t.Context(), request)

	if config.WithoutPreparation {
		requireError(t, err, payment.ErrUnsupported)

		if prepared != (payment.Preparation{}) {
			t.Fatal("unsupported preparation returned a handoff")
		}

		return
	}

	requireSuccess(t, err)
	requireSuccess(t, prepared.Err())
}
