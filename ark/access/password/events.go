package password

import "arkoren.dev/core/event"

// UserEvent is the payload of the events of this package. It carries only the
// subject, never a password, hash or token.
//
// Example:
//
//	events.Listen(password.Changed, func(ctx context.Context, changed password.UserEvent) error {
//		return notify(ctx, changed.Subject, "your password changed")
//	})
type UserEvent struct {
	// Subject is the user the event is about.
	Subject string
}

// Changed is published after a logged-in user set or changed their password.
// It is the hook for a "your password changed" notification.
//
// Example:
//
//	unsubscribe, err := events.Listen(password.Changed, func(ctx context.Context, changed password.UserEvent) error {
//		return nil
//	})
var Changed = event.NewTopic[UserEvent]("password.credential.changed")

// WasReset is published after a reset token replaced a user's password.
//
// Example:
//
//	unsubscribe, err := events.Listen(password.WasReset, func(ctx context.Context, reset password.UserEvent) error {
//		return nil
//	})
var WasReset = event.NewTopic[UserEvent]("password.credential.reset")
