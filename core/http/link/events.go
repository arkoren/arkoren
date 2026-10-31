package link

import "arkoren.dev/core/event"

// RejectedEvent reports invalid link admission without the URL, path, binding,
// or cause. Paths and callback errors can contain credentials. The middleware's
// returned problem retains the cause; listeners do not receive it as payload.
//
// Example:
//
//	unsubscribe, err := events.Listen(link.Rejected, func(ctx context.Context, rejected link.RejectedEvent) error {
//		logger.InfoContext(ctx, "link rejected", "method", rejected.Method)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type RejectedEvent struct {
	// Method is the client-controlled HTTP method, not a route or identity.
	// Applications must not place secrets in custom method names.
	//
	// Example:
	//	logger.InfoContext(ctx, "link rejected", "method", rejected.Method)
	Method string
}

// Rejected is published when [Middleware] rejects a link whose signature or
// form is invalid. Expired links publish nothing: they are an ordinary
// outcome of a valid link.
// Event contexts retain request context values; listeners must select safe
// attributes rather than dumping the context or a returned error.
//
// Example:
//
//	unsubscribe, err := events.Listen(link.Rejected, onRejected)
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var Rejected = event.NewTopic[RejectedEvent]("link.rejected")

// ConsumedEvent reports a recorded single use without a URL, resource path,
// binding, cache key, or digest. Direct URL calls have no HTTP method to report.
// The event context retains the caller's context values, which are not redacted.
//
// Example:
//
//	unsubscribe, err := events.Listen(link.Consumed, func(ctx context.Context, consumed link.ConsumedEvent) error {
//		logger.InfoContext(ctx, "link consumed", "method", consumed.Method)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type ConsumedEvent struct {
	// Method is the request method. It is empty when the link was consumed
	// with [Signer.Consume] or [Signer.ConsumeURL], which have no request.
	// Custom methods are client-controlled and must not contain secrets.
	//
	// Example:
	//	logger.InfoContext(ctx, "link consumed", "method", consumed.Method)
	Method string
}

// Consumed is published when [Signer.Consume], [Signer.ConsumeURL], or
// [Signer.ConsumeRequest] spends a one-time link, after the use is recorded.
// Calls that fail, including [ErrConsumed] for a link spent earlier, publish
// nothing, and so does [Middleware], which never consumes a link.
//
// Example:
//
//	unsubscribe, err := events.Listen(link.Consumed, onConsumed)
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var Consumed = event.NewTopic[ConsumedEvent]("link.consumed")
