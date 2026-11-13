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
