package feature

import "arkoren.dev/core/event"

// EvaluationFailedEvent is the payload of [EvaluationFailed]. It has no
// structured subject, attribute, fallback, or evaluated-value fields.
// Err remains an explicit raw diagnostic, not a redacted telemetry field;
// provider error text can contain sensitive details. Inspect error identities
// instead of forwarding the entire payload to untrusted logs or transports.
//
// Example: missing := errors.Is(payload.Err, feature.ErrNotFound)
type EvaluationFailedEvent struct {
	// Key is the flag key.
	// It is application-owned identity, not the subject's key.
	//
	// Example: name := payload.Key
	Key string

	// Err is the failure, such as [ErrNotFound] or [ErrNotReady], wrapped
	// with the key.
	// The original cause is retained and its text is not sanitized.
	//
	// Example: unavailable := errors.Is(payload.Err, feature.ErrNotReady)
	Err error
}

// EvaluationFailed is published by [Flags] built with [Builder.Events] when the
// driver's Evaluate fails and the caller therefore gets its default value
// together with the error. Successful evaluations, flags that are off, and
// failures the facade detects itself (an invalid subject, or a driver value
// that does not convert to the requested type) publish nothing. Every failing call publishes,
// so a missing flag read on a hot path publishes at the rate of that path.
//
// Example:
//
//	stop, err := bus.Listen(feature.EvaluationFailed, func(_ context.Context, payload feature.EvaluationFailedEvent) error {
//		_ = errors.Is(payload.Err, feature.ErrNotFound)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer stop()
var EvaluationFailed = event.NewTopic[EvaluationFailedEvent]("feature.evaluation.failed")
