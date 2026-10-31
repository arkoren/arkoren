package molliepayment

import (
	"context"
	"errors"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/mollie/molliepayment/internal/resource"
)

// getPayment centralizes fixed-path observations for checkout and payment
// reads. An identity mismatch is a protocol failure, never a different result.
func (driver *Driver) getPayment(ctx context.Context, id string) (resource.Payment, error) {
	if err := resource.NativeID(id, "tr_"); err != nil {
		return resource.Payment{}, err
	}

	var observed resource.Payment

	if err := driver.call(ctx, operation{path: "/payments/" + id}, &observed); err != nil {
		return resource.Payment{}, err
	}

	if observed.ID != id {
		return resource.Payment{}, readError(errors.New("mollie response payment ID mismatch"))
	}

	return observed, nil
}

// GetPayment reads an authoritative payment snapshot using a tr_ payment ID.
// Open requires customer action, authorized and pending are not successful,
// and only paid maps to Succeeded. Unknown native states stay Unknown.
// Malformed successful JSON and unusable snapshots wrap ErrUnavailable without
// adding ErrOutcomeUnknown; transport and HTTP errors retain their categories.
//
// Example:
//
//	observed, err := backend.GetPayment(ctx, savedPaymentID)
//	if err != nil {
//		return err
//	}
//	paid := observed.Status == payment.StatusSucceeded
//	_ = paid
func (driver *Driver) GetPayment(ctx context.Context, id payment.ID) (payment.Payment, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Payment{}, err
	}

	observed, err := driver.getPayment(ctx, string(id))

	if err != nil {
		return payment.Payment{}, err
	}

	result, err := observed.Snapshot()

	return result, readError(err)
}
