package auth

import "arkoren.dev/core/event"

// RejectedEvent reports anonymous rejection without request paths, which can
// contain identifiers or credentials. It carries no user, credential, header,
// query, or cause. Its event context still contains the request's context values.
//
// Example:
//
//	unsubscribe, err := events.Listen(auth.Rejected, func(ctx context.Context, rejected auth.RejectedEvent) error {
//		logger.InfoContext(ctx, "authentication required", "method", rejected.Method)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
type RejectedEvent struct {
	// Method is the client-controlled HTTP method, not a route or user identity.
	// Applications must not place secrets in custom method names.
	//
	// Example:
	//	logger.InfoContext(ctx, "authentication required", "method", rejected.Method)
	Method string
}

// Rejected is published when [Require], built with [Builder.Events], rejects an
// anonymous request. The default response is 401; ErrorResponse can override it.
// Every rejection publishes without deduplication, so a client controls the
// rate. Listeners must be cheap, and
// a rate limiter such as the ratelimit middleware should sit in front of
// [Require]. Requests that pass publish nothing.
//
// Example:
//
//	unsubscribe, err := events.Listen(auth.Rejected, onRejected)
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var Rejected = event.NewTopic[RejectedEvent]("http.auth.rejected")
