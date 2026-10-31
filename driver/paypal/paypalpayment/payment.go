package paypalpayment

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/paypal/internal/paypal"
)

// GetPayment reads a v2 capture. Native sale IDs remain useful webhook
// observations but are unsupported here; no deprecated v1 route is guessed.
// Refunded and partially refunded captures retain StatusSucceeded as historical
// payment success, with NativeStatus and gross Amount unchanged. Refund
// resources describe returned funds; this method does not calculate a balance.
// Malformed successful JSON wraps ErrUnavailable without adding ErrOutcomeUnknown;
// semantic snapshot validation retains its specific validation/capability errors.
//
// Example:
//
//	result, err := backend.GetPayment(ctx, "capture:ABC")
//	if err != nil {
//		return err
//	}
//	_ = result.Amount
func (driver *Driver) GetPayment(ctx context.Context, id payment.ID) (payment.Payment, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Payment{}, err
	}

	native, err := captureID(id)

	if err != nil {
		return payment.Payment{}, err
	}

	var resource paypal.Movement

	if err := driver.client.Call(ctx, paypal.Request{Method: http.MethodGet,
		Path: "/v2/payments/captures/" + native}, &resource); err != nil {
		return payment.Payment{}, err
	}

	return resource.Payment()
}

// captureID rejects unsupported sale operations without authenticating. Other
// incorrect resource families are invalid input, not provider not-found errors.
func captureID(id payment.ID) (string, error) {
	if strings.HasPrefix(string(id), "sale:") {
		return "", fmt.Errorf("%w: sale lookup and refund", payment.ErrUnsupported)
	}

	return paypal.Native(string(id), "capture:")
}
