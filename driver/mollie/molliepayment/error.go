package molliepayment

import (
	"errors"
	"fmt"
	"net/http"

	"arkoren.dev/core/service/payment"
)

// Error exposes only an HTTP status, never Mollie's free-form response body,
// request URLs, keys or transport diagnostics. Unwrap preserves transport and
// decoding causes for cancellation checks; those causes can contain secrets
// and must not be logged without application redaction.
//
// Example:
//
//	var failure *molliepayment.Error
//	if errors.As(err, &failure) {
//		_ = failure.Status
//	}
type Error struct {
	// Status is the non-2xx HTTP status retained from a provider reply. Zero means no
	// status was retained, including transport failures and successful-response
	// body read or JSON decoding failures; it does not prove no reply arrived.
	// Example: _ = molliepayment.Error{Status: http.StatusUnauthorized}
	Status int
	// cause retains external failures without including their text in Error.
	cause error
}

// Error returns a safe status-only summary. Non-2xx provider response prose is
// never read or retained, even if it echoes a credential. Successful-response
// decoding causes stay private diagnostics accessible through Unwrap.
//
// Example: _ = (&molliepayment.Error{Status: 401}).Error()
func (failure *Error) Error() string {
	return fmt.Sprintf("mollie request failed (HTTP %d)", failure.Status)
}

// Unwrap preserves the original transport or decoder cause. Inspect error
// identities instead of logging the returned cause, which may contain secrets.
//
// Example: _ = errors.Is(err, context.Canceled)
func (failure *Error) Unwrap() error {
	return failure.cause
}

// readError classifies local validation of a successful GET response as provider
// unavailability while preserving its safe diagnostic cause. Pass only snapshot
// validation failures: transport and non-2xx errors are already classified by
// call and must bypass this boundary. Mutations retain mutationError instead.
func readError(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%w: %w", payment.ErrUnavailable, err)
}

// responseError classifies status codes without reading untrusted provider
// error prose. Conflicts can describe an in-flight idempotent request, so the
// mutation boundary conservatively marks their outcome unknown.
func responseError(status int) error {
	categories := map[int]error{
		http.StatusBadRequest:           payment.ErrInvalidRequest,
		http.StatusUnauthorized:         payment.ErrUnauthorized,
		http.StatusForbidden:            payment.ErrForbidden,
		http.StatusNotFound:             payment.ErrNotFound,
		http.StatusMethodNotAllowed:     payment.ErrInvalidRequest,
		http.StatusConflict:             payment.ErrInvalidState,
		http.StatusGone:                 payment.ErrNotFound,
		http.StatusUnsupportedMediaType: payment.ErrInvalidRequest,
		http.StatusUnprocessableEntity:  payment.ErrInvalidRequest,
		http.StatusTooManyRequests:      payment.ErrRateLimited,
	}

	category, found := categories[status]

	if !found {
		category = payment.ErrUnavailable
	}

	return fmt.Errorf("%w: %w", category, &Error{Status: status})
}

// mutationError distinguishes an authoritative rejection from ambiguity after
// dispatch. Timeouts, conflicts, redirects, 5xx and unreadable success responses
// require reconciliation. It never replays a request or manufactures a result.
func mutationError(err error) error {
	if err == nil {
		return nil
	}

	var failure *Error

	if errors.As(err, &failure) && confirmedRejection(failure.Status) {
		return err
	}

	return fmt.Errorf("%w: %w", payment.ErrOutcomeUnknown, err)
}

// confirmedRejection accepts only 4xx other than timeouts and in-flight
// conflicts. The latter cannot prove the original attempt had no effect.
func confirmedRejection(status int) bool {
	clientFailure := status >= http.StatusBadRequest && status < http.StatusInternalServerError
	ambiguous := status == http.StatusRequestTimeout || status == http.StatusConflict

	return clientFailure && !ambiguous
}
