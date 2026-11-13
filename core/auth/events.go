package auth

import "arkoren.dev/core/event"

// UserForgottenEvent reports that every forget hook of a user completed. It
// carries only the subject, never personal data.
//
// Example:
//
//	unsubscribe, err := events.Listen(auth.UserForgotten, func(ctx context.Context, forgotten auth.UserForgottenEvent) error {
//		logger.InfoContext(ctx, "user forgotten", "subject", forgotten.Subject)
//		return nil
//	})
type UserForgottenEvent struct {
	// Subject is the subject passed to [UserProvider.Forget].
	Subject string
}

// UserForgotten is emitted by [UserProvider.Forget] with [event.Bus.Emit] after
// every hook registered with [UserProvider.OnForget] succeeded, and never when
// a hook failed. It means "forget hooks completed": the delete may still roll
// back with the caller's transaction. A listener whose effect needs the delete
// to be committed wraps that effect in Database.AfterCommit with the event
// context, which carries the transaction. Emit logs listener errors instead of
// returning them, so listeners cannot change the outcome of Forget.
//
// Example:
//
//	unsubscribe, err := events.Listen(auth.UserForgotten, func(ctx context.Context, forgotten auth.UserForgottenEvent) error {
//		return search.Remove(ctx, forgotten.Subject)
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
var UserForgotten = event.NewTopic[UserForgottenEvent]("auth.user.forgotten")

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

// Rejected is published when [Require], built with [MiddlewareBuilder.Events], rejects an
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
