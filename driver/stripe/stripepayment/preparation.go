package stripepayment

import (
	"context"
	"strings"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/stripe/stripepayment/internal/protocol"
)

// PreparePayment creates an unconfirmed, card-only guest PaymentIntent for a
// Stripe client SDK. Automatic confirmation and capture are configured, but this
// call never confirms, hydrates, or reports settlement. Web, iOS, Android, and an
// omitted channel use the same server protocol; wallet availability and client
// configuration belong to the application. Customers, CustomerSessions, ephemeral
// keys, saved methods, and backend confirmation are outside this profile.
//
// Nonempty ReturnURL and DeviceReference return ErrUnsupported before dispatch:
// neither setting has a server-side role in this initialization protocol. Set
// the SDK return URL on the client, not on this request. Only the observed pi_
// payment identity is returned; no checkout or session identity is invented.
// The authorization token is sensitive and must reach only the authorized
// customer over HTTPS, never logs, URLs, or telemetry.
//
// New's one-attempt policy applies. Unusable success responses, transport failures,
// and idempotency_key_in_use conflicts wrap ErrOutcomeUnknown; reconcile rather
// than automatically creating another intent. NewFrom retains caller policies.
// Request validation precedes nil/zero-handle checks, which return
// ErrInvalidRequest. Use the payment facade for common instrumentation.
//
// Example:
//
//	amount, err := payment.NewMoney("USD", 2500)
//	if err != nil { return err }
//	client := payment.New(backend)
//	prepared, err := client.PreparePayment(ctx, payment.NewPreparePaymentRequest(amount, "order-123"))
//	if err != nil { return err }
//	authorization, ok := prepared.Action.Authorization()
//	if !ok { return payment.ErrInvalidState }
//	_ = authorization.Token // authorized SDK response only; do not log
func (driver *Driver) PreparePayment(
	ctx context.Context, request payment.PreparePaymentRequest,
) (payment.Preparation, error) {
	if err := request.Validate(ctx); err != nil {
		return payment.Preparation{}, err
	}

	if request.ReturnURL() != "" || request.DeviceReference() != "" {
		return payment.Preparation{}, payment.ErrUnsupported
	}

	if driver == nil || driver.client == nil {
		return payment.Preparation{}, payment.ErrInvalidRequest
	}

	if driver.client.V1PaymentIntents == nil {
		return payment.Preparation{}, payment.ErrInvalidRequest
	}

	params := preparationParams(request)

	intent, err := driver.client.V1PaymentIntents.Create(ctx, params)

	if err != nil {
		return payment.Preparation{}, mutationError(err)
	}

	result, err := protocol.Preparation(intent)

	return result, mutationError(err)
}

// preparationParams translates the validated common request into guest intent
// initialization. Optional empty text is omitted, preserving the native profile
// without sending customer credentials, expansions, or confirmation inputs.
func preparationParams(request payment.PreparePaymentRequest) *stripe.PaymentIntentCreateParams {
	params := &stripe.PaymentIntentCreateParams{
		Amount:                    stripe.Int64(request.Amount().Amount),
		Currency:                  stripe.String(strings.ToLower(request.Amount().Currency)),
		CaptureMethod:             stripe.String("automatic"),
		ConfirmationMethod:        stripe.String("automatic"),
		AllowedPaymentMethodTypes: []*string{stripe.String("card")},
	}

	if request.Reference() != "" {
		params.Metadata = map[string]string{"reference": request.Reference()}
	}

	if request.Description() != "" {
		params.Description = stripe.String(request.Description())
	}

	params.SetIdempotencyKey(request.IdempotencyKey())

	return params
}
