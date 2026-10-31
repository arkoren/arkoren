package push

import "errors"

var (
	// ErrInvalidMessage identifies invalid portable input or send configuration.
	// Details are secret-safe; drivers may wrap this class for payload limits.
	//
	// Example:
	//
	//	if errors.Is(err, push.ErrInvalidMessage) { return err }
	ErrInvalidMessage = errors.New("invalid push message")

	// ErrUnsupported means the driver cannot submit this recipient or feature.
	// It never means a registration is expired and must not trigger deletion.
	//
	// Example:
	//
	//	if errors.Is(err, push.ErrUnsupported) { return err }
	ErrUnsupported = errors.New("unsupported push operation")

	// ErrNotFound means the provider confirmed this registration is invalid.
	// Authentication, project mismatch, malformed payloads, and generic HTTP 404
	// responses are not sufficient evidence. False classification can cause an
	// application to delete a live registration permanently.
	//
	// Example:
	//
	//	if errors.Is(err, push.ErrNotFound) { removeConfirmedRegistration() }
	ErrNotFound = errors.New("push registration not found")

	// ErrRateLimited means the provider rejected submission due to throttling.
	// Applications choose any retry policy; Sender never retries automatically.
	//
	// Example:
	//
	//	if errors.Is(err, push.ErrRateLimited) { return err }
	ErrRateLimited = errors.New("push provider rate limited")

	// ErrUnavailable means submission failed due to a provider availability issue.
	// It does not imply that a registration should be deleted.
	//
	// Example:
	//
	//	if errors.Is(err, push.ErrUnavailable) { return err }
	ErrUnavailable = errors.New("push provider unavailable")

	// ErrOutcomeUnknown means provider acceptance cannot be determined after a
	// request may have left the process. Retrying can deliver duplicate messages.
	// Drivers preserve any context or native cause alongside this classification.
	//
	// Example:
	//
	//	if errors.Is(err, push.ErrOutcomeUnknown) { return err /* reconcile */ }
	ErrOutcomeUnknown = errors.New("push outcome unknown")
)
