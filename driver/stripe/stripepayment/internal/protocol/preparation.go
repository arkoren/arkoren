package protocol

import (
	"strings"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
)

// Preparation validates initialization data from the mutation response.
// A secret must belong to this pi_ resource; failures return no partial result,
// and no secret text enters an error. Settlement is deliberately not inferred.
func Preparation(intent *stripe.PaymentIntent) (payment.Preparation, error) {
	if intent == nil {
		return payment.Preparation{}, errResponse
	}

	if err := checkResource(intent.ID, "pi_", intent.Object, "payment_intent"); err != nil {
		return payment.Preparation{}, err
	}

	suffix, ok := strings.CutPrefix(intent.ClientSecret, intent.ID+"_secret_")

	if !ok || suffix == "" {
		return payment.Preparation{}, errResponse
	}

	if strings.ContainsFunc(intent.ClientSecret, CredentialSpace) {
		return payment.Preparation{}, errResponse
	}

	return payment.Preparation{
		PaymentID: payment.ID(intent.ID),
		Action: payment.NewClientAuthorization(
			payment.ClientProtocolStripePaymentIntent, intent.ClientSecret,
		),
	}, nil
}
