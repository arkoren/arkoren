package stripepayment

import (
	"context"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/stripe/stripepayment/internal/protocol"
)

// GetPayment reads a native pi_ PaymentIntent. Requiring a payment method is
// RequiresAction, not a terminal failed payment; the native status is preserved.
//
// Example:
//
//	result, err := backend.GetPayment(ctx, payment.ID("pi_example"))
//	if err != nil { return err }
//	_ = result.Status
func (driver *Driver) GetPayment(ctx context.Context, id payment.ID) (payment.Payment, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Payment{}, err
	}

	if err := protocol.CheckFamily(string(id), "pi_"); err != nil {
		return payment.Payment{}, err
	}

	intent, err := driver.client.V1PaymentIntents.Retrieve(ctx, string(id), nil)

	if err != nil {
		return payment.Payment{}, readError(err)
	}

	result, err := protocol.Payment(intent)

	return result, readError(err)
}

// RefundPayment first reads the PaymentIntent to verify currency: Stripe's
// refund API takes no currency parameter. Mismatches return ErrInvalidRequest
// without submitting a refund. The explicit amount and key are sent once, and
// only the mutation response supplies the returned snapshot.
//
// Example:
//
//	amount, err := payment.NewMoney("USD", 500)
//	if err != nil { return err }
//	refund, err := backend.RefundPayment(ctx, payment.NewRefundRequest("pi_example", amount, "refund-123"))
//	if err != nil { return err }
//	_ = refund.ID
func (driver *Driver) RefundPayment(ctx context.Context, request payment.RefundRequest) (payment.Refund, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Refund{}, err
	}

	original, err := driver.GetPayment(ctx, request.PaymentID())

	if err != nil {
		return payment.Refund{}, err
	}

	if original.Amount.Currency != request.Amount().Currency {
		return payment.Refund{}, payment.ErrInvalidRequest
	}

	if err := payment.CheckContext(ctx); err != nil {
		return payment.Refund{}, err
	}

	params := &stripe.RefundCreateParams{
		PaymentIntent: stripe.String(string(request.PaymentID())),
		Amount:        stripe.Int64(request.Amount().Amount),
	}
	params.SetIdempotencyKey(request.IdempotencyKey())

	refund, err := driver.client.V1Refunds.Create(ctx, params)

	if err != nil {
		return payment.Refund{}, mutationError(err)
	}

	result, err := protocol.Refund(refund)

	return result, mutationError(err)
}

// GetRefund reads a native re_ refund independently from the original payment.
// It never resubmits a refund and retains its native progress status.
//
// Example:
//
//	refund, err := backend.GetRefund(ctx, payment.RefundID("re_example"))
//	if err != nil { return err }
//	_ = refund.Status
func (driver *Driver) GetRefund(ctx context.Context, id payment.RefundID) (payment.Refund, error) {
	if err := id.Validate(ctx); err != nil {
		return payment.Refund{}, err
	}

	if err := protocol.CheckFamily(string(id), "re_"); err != nil {
		return payment.Refund{}, err
	}

	refund, err := driver.client.V1Refunds.Retrieve(ctx, string(id), nil)

	if err != nil {
		return payment.Refund{}, readError(err)
	}

	result, err := protocol.Refund(refund)

	return result, readError(err)
}
