package apitoken

import "arkoren.dev/core/event"

// UserEvent is the payload of the events of this package. It carries only the
// subject, never a token or its id.
//
// Example:
//
//	events.Listen(apitoken.Issued, func(ctx context.Context, issued apitoken.UserEvent) error {
//		return notify(ctx, issued.Subject, "a new API token was created")
//	})
type UserEvent struct {
	// Subject is the user the event is about.
	Subject string
}

// Issued is published after a personal or device token was issued.
//
// Example:
//
//	unsubscribe, err := events.Listen(apitoken.Issued, func(ctx context.Context, issued apitoken.UserEvent) error {
//		return nil
//	})
var Issued = event.NewTopic[UserEvent]("apitoken.token.issued")

// Revoked is published after tokens were revoked, one event per call.
//
// Example:
//
//	unsubscribe, err := events.Listen(apitoken.Revoked, func(ctx context.Context, revoked apitoken.UserEvent) error {
//		return nil
//	})
var Revoked = event.NewTopic[UserEvent]("apitoken.token.revoked")
