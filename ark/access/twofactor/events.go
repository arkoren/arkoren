package twofactor

import "arkoren.dev/core/event"

// UserEvent is the payload of the events of this package. It carries only the
// subject, never a secret or a code.
//
// Example:
//
//	events.Listen(twofactor.Disabled, func(ctx context.Context, off twofactor.UserEvent) error {
//		return notify(ctx, off.Subject, "two-factor was turned off")
//	})
type UserEvent struct {
	// Subject is the user the event is about.
	Subject string
}

// Enabled is published after a user confirmed an authenticator. The core
// [auth.CredentialEnrolled] event is published as well.
//
// Example:
//
//	unsubscribe, err := events.Listen(twofactor.Enabled, func(ctx context.Context, on twofactor.UserEvent) error {
//		return nil
//	})
var Enabled = event.NewTopic[UserEvent]("twofactor.totp.enabled")

// Disabled is published after a user's authenticator and recovery codes were
// removed. It is the hook for a "two-factor was turned off" notification.
//
// Example:
//
//	unsubscribe, err := events.Listen(twofactor.Disabled, func(ctx context.Context, off twofactor.UserEvent) error {
//		return nil
//	})
var Disabled = event.NewTopic[UserEvent]("twofactor.totp.disabled")

// Regenerated is published after a user's recovery codes were replaced.
//
// Example:
//
//	unsubscribe, err := events.Listen(twofactor.Regenerated, func(ctx context.Context, codes twofactor.UserEvent) error {
//		return nil
//	})
var Regenerated = event.NewTopic[UserEvent]("twofactor.codes.regenerated")

// RecoveryUsed is published after a recovery code was spent to log in or to
// confirm. It is the hook for a "a recovery code was used" notification.
//
// Example:
//
//	unsubscribe, err := events.Listen(twofactor.RecoveryUsed, func(ctx context.Context, used twofactor.UserEvent) error {
//		return nil
//	})
var RecoveryUsed = event.NewTopic[UserEvent]("twofactor.recovery.used")
