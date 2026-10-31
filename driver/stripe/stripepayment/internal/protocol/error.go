package protocol

import (
	"errors"
	"net/http"

	"github.com/stripe/stripe-go/v87"

	"arkoren.dev/core/service/payment"
)

// Classify translates Stripe HTTP categories without reading sensitive messages.
func Classify(err error) error {
	var native *stripe.Error

	if !errors.As(err, &native) {
		return payment.ErrUnavailable
	}

	if native.Type == stripe.ErrorTypeCard {
		return payment.ErrDeclined
	}

	classes := map[int]error{
		http.StatusBadRequest:          payment.ErrInvalidRequest,
		http.StatusUnauthorized:        payment.ErrUnauthorized,
		http.StatusForbidden:           payment.ErrForbidden,
		http.StatusNotFound:            payment.ErrNotFound,
		http.StatusConflict:            payment.ErrInvalidState,
		http.StatusUnprocessableEntity: payment.ErrInvalidState,
		http.StatusTooManyRequests:     payment.ErrRateLimited,
	}

	if class, ok := classes[native.HTTPStatusCode]; ok {
		return class
	}

	return payment.ErrUnavailable
}
