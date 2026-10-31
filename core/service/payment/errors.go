package payment

import "errors"

var (
	// ErrInvalidRequest means portable input validation failed before dispatch.
	// Inspect with errors.Is; the accompanying text identifies a field, not its value.
	//
	// Example:
	//	if errors.Is(err, payment.ErrInvalidRequest) { return err }
	ErrInvalidRequest = errors.New("invalid payment request")

	// ErrInvalidState means the operation is not allowed in the resource's state.
	// A fresh read may help reconcile the state; do not blindly repeat mutations.
	//
	// Example:
	//	if errors.Is(err, payment.ErrInvalidState) { return err }
	ErrInvalidState = errors.New("invalid payment state")

	// ErrNotFound means the requested provider resource does not exist or is hidden.
	// It does not identify whether a previous ambiguous mutation succeeded.
	//
	// Example:
	//	if errors.Is(err, payment.ErrNotFound) { return err }
	ErrNotFound = errors.New("payment resource not found")

	// ErrUnsupported means a currency or operation is outside supported behavior.
	// Drivers may support fewer currencies or operations than the portable contract.
	//
	// Example:
	//	if errors.Is(err, payment.ErrUnsupported) { return err }
	ErrUnsupported = errors.New("unsupported payment operation")

	// ErrUnauthorized means provider authentication failed, not customer approval.
	// Correct credentials before retrying the operation.
	//
	// Example:
	//	if errors.Is(err, payment.ErrUnauthorized) { return err }
	ErrUnauthorized = errors.New("payment provider unauthorized")

	// ErrForbidden means authenticated credentials lack permission for the operation.
	// Changing customer checkout state will not fix provider authorization.
	//
	// Example:
	//	if errors.Is(err, payment.ErrForbidden) { return err }
	ErrForbidden = errors.New("payment provider forbidden")

	// ErrDeclined means the provider explicitly rejected the payment attempt.
	// This is a known rejection, unlike ErrOutcomeUnknown.
	//
	// Example:
	//	if errors.Is(err, payment.ErrDeclined) { return err }
	ErrDeclined = errors.New("payment declined")

	// ErrRateLimited means the provider rejected work because of a rate limit.
	// Honor provider retry policy and preserve mutation idempotency keys.
	//
	// Example:
	//	if errors.Is(err, payment.ErrRateLimited) { return err }
	ErrRateLimited = errors.New("payment provider rate limited")

	// ErrUnavailable means the provider could not serve the request with a known
	// failure. Ambiguous dispatched mutations use ErrOutcomeUnknown instead.
	//
	// Example:
	//	if errors.Is(err, payment.ErrUnavailable) { return err }
	ErrUnavailable = errors.New("payment provider unavailable")

	// ErrOutcomeUnknown means a dispatched mutation has an uncertain outcome.
	// Reconcile or retry within provider idempotency guarantees using the same key.
	//
	// Example:
	//	if errors.Is(err, payment.ErrOutcomeUnknown) { return err }
	ErrOutcomeUnknown = errors.New("payment outcome unknown")
)
