package gate

import "arkoren.dev/core/event"

// DeniedEvent identifies the denying rule by position, without a resource path,
// user, query, or cause. Paths can contain identifiers or credentials. The event
// context retains request context values; listeners must select safe attributes.
//
// Example:
//
//	unsubscribe, err := events.Listen(gate.Denied, func(ctx context.Context, denied gate.DeniedEvent) error {
//		logger.InfoContext(ctx, "access denied", "method", denied.Method, "rule", denied.Rule)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type DeniedEvent struct {
	// Method is the request method. It is empty when the denial came from
	// [Gate.Authorize], which has no request.
	// Custom methods are client-controlled and must not contain secrets.
	//
	// Example:
	//	logger.InfoContext(ctx, "access denied", "method", denied.Method)
	Method string

	// Rule is the zero-based index of the rule that returned false, in the
	// order the rules were given.
	//
	// Example:
	//	logger.InfoContext(ctx, "access denied", "rule", denied.Rule)
	Rule int
}

// Denied is published when a rule of a [Gate] returns false and the call
// therefore fails with [ErrForbidden]. A rule that returns an error, a
// missing user, and allowed calls publish nothing. The free functions
// [Authorize] and [Require] publish nothing: only a [Gate] built with
// [Builder.Events] does.
//
// Every denial publishes: there is no deduplication, so a client controls
// the rate. Listeners must be cheap, and a rate limiter such as the ratelimit
// middleware should sit in front of the gate.
//
// Example:
//
//	unsubscribe, err := events.Listen(gate.Denied, onDenied)
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var Denied = event.NewTopic[DeniedEvent]("http.gate.denied")
