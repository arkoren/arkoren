package paypal

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strings"

	"arkoren.dev/core/service/payment"
)

// Error exposes safe provider diagnostics without response text, URLs, or
// credentials. Error text is fixed apart from the HTTP status. Unwrap preserves
// transport and decoding causes for errors.Is/errors.As without printing them.
// Example: var native *paypal.Error; if errors.As(err, &native) { _ = native.DebugID }
type Error struct {
	// Status is the HTTP status, or zero for transport/decoding failures.
	// Example: _ = paypal.Error{Status: http.StatusUnprocessableEntity}
	Status int
	// Code is the provider name token, blank if not safely representable.
	// Example: _ = paypal.Error{Code: "UNPROCESSABLE_ENTITY"}
	Code string
	// Issue is the first provider details issue token, not free-form description.
	// Example: _ = paypal.Error{Issue: "INSTRUMENT_DECLINED"}
	Issue string
	// DebugID is PayPal's correlation token for support, never the response body.
	// Example: _ = paypal.Error{DebugID: "abc123"}
	DebugID string
	// cause retains diagnostics without leaking them through Error.
	cause error
}

// Error returns a safe summary. Use structured fields for provider support;
// explicitly unwrapped causes may contain sensitive transport diagnostics.
// Example: _ = (&paypal.Error{Status: 422}).Error()
func (failure *Error) Error() string {
	return fmt.Sprintf("paypal request failed (HTTP %d)", failure.Status)
}

// Unwrap preserves the original transport, body-read, or decoder failure.
// Do not log unwrapped causes without applying application redaction policy.
// Example: _ = errors.Unwrap(&paypal.Error{Status: 422})
func (failure *Error) Unwrap() error {
	return failure.cause
}

// NewError hides potentially sensitive external diagnostics while retaining
// the original nonnil error for errors.As and cancellation checks. Classification
// is left to the call boundary: decoding is not itself a signature rejection,
// provider availability problem, or evidence of an uncertain mutation.
// Example: err := paypal.NewError(decoderError); _ = errors.Unwrap(err)
func NewError(err error) error {
	return &Error{cause: err}
}

// responseError decodes only bounded machine tokens. Failed error decoding is
// retained as a cause, while the HTTP status still supplies a reliable category.
func responseError(response *http.Response) error {
	failure := &Error{Status: response.StatusCode, DebugID: safeToken(response.Header.Get("Paypal-Debug-Id"))}
	data, err := readBounded(response.Body, smallLimit)

	if err != nil {
		failure.cause = err

		return fmt.Errorf("%w: %w", category(failure), failure)
	}

	// detail excludes descriptions so no provider body text enters error messages.
	var detail struct {
		// Name is a machine token, not the free-form provider message.
		Name string `json:"name"`
		// DebugID is the support correlation token, sanitized before exposure.
		DebugID string `json:"debug_id"`
		// Details contains issue tokens; descriptions and links are not retained.
		Details []struct {
			// Issue is the provider's structured failure code, not error prose.
			Issue string `json:"issue"`
		} `json:"details"`
	}

	if err := json.Unmarshal(data, &detail); err != nil {
		failure.cause = err
	}

	failure.Code = safeToken(detail.Name)

	if detail.DebugID != "" {
		failure.DebugID = safeToken(detail.DebugID)
	}

	if len(detail.Details) > 0 {
		failure.Issue = safeToken(detail.Details[0].Issue)
	}

	return fmt.Errorf("%w: %w", category(failure), failure)
}

// safeToken allows bounded ASCII machine identifiers, not arbitrary text.
func safeToken(value string) string {
	if len(value) > 128 {
		return ""
	}

	if strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return ""
	}

	return value
}

// category translates confirmed provider failures without suggesting retries
// for declines or state conflicts. Unmapped 4xx errors are invalid requests.
func category(failure *Error) error {
	if failure.Status >= http.StatusInternalServerError {
		return payment.ErrUnavailable
	}

	// categories keep the finite HTTP mapping separate from issue-specific state.
	categories := map[int]error{
		http.StatusUnauthorized:    payment.ErrUnauthorized,
		http.StatusForbidden:       payment.ErrForbidden,
		http.StatusNotFound:        payment.ErrNotFound,
		http.StatusConflict:        payment.ErrInvalidState,
		http.StatusTooManyRequests: payment.ErrRateLimited,
	}

	if category, ok := categories[failure.Status]; ok {
		return category
	}

	if failure.Issue == "INSTRUMENT_DECLINED" {
		return payment.ErrDeclined
	}

	if failure.Issue == "ORDER_ALREADY_CAPTURED" || failure.Issue == "SUBSCRIPTION_STATUS_INVALID" {
		return payment.ErrInvalidState
	}

	return payment.ErrInvalidRequest
}
