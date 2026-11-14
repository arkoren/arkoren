package account

import "arkoren.dev/core/event"

// UserEvent is the payload of the events of this package. It carries only the
// subject, never an email or a token.
//
// Example:
//
//	events.Listen(account.UserCreated, func(ctx context.Context, created account.UserEvent) error {
//		return welcome(ctx, created.Subject)
//	})
type UserEvent struct {
	// Subject is the user the event is about.
	Subject string
}

// UserCreated is published after a user was created.
//
// Example:
//
//	unsubscribe, err := events.Listen(account.UserCreated, func(ctx context.Context, created account.UserEvent) error {
//		return nil
//	})
var UserCreated = event.NewTopic[UserEvent]("account.user.created")

// EmailVerified is published after a user verified their email.
//
// Example:
//
//	unsubscribe, err := events.Listen(account.EmailVerified, func(ctx context.Context, verified account.UserEvent) error {
//		return nil
//	})
var EmailVerified = event.NewTopic[UserEvent]("account.email.verified")

// UserDeleted is published after [Directory.Delete] committed.
//
// Example:
//
//	unsubscribe, err := events.Listen(account.UserDeleted, func(ctx context.Context, deleted account.UserEvent) error {
//		return nil
//	})
var UserDeleted = event.NewTopic[UserEvent]("account.user.deleted")
