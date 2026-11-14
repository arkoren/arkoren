package federation

import "arkoren.dev/core/event"

// UserEvent is the payload of the events of this package. It carries only the
// local subject, never the issuer or the external identifier.
//
// Example:
//
//	events.Listen(federation.LinkCreated, func(ctx context.Context, linked federation.UserEvent) error {
//		return notify(ctx, linked.Subject, "a sign-in method was connected")
//	})
type UserEvent struct {
	// Subject is the user the event is about.
	Subject string
}

// LinkCreated is published after an external identity was connected to a user,
// by a connect flow or by the first login that created the user.
//
// Example:
//
//	unsubscribe, err := events.Listen(federation.LinkCreated, func(ctx context.Context, linked federation.UserEvent) error {
//		return nil
//	})
var LinkCreated = event.NewTopic[UserEvent]("federation.link.created")

// LinkDeleted is published after a connection was removed.
//
// Example:
//
//	unsubscribe, err := events.Listen(federation.LinkDeleted, func(ctx context.Context, unlinked federation.UserEvent) error {
//		return nil
//	})
var LinkDeleted = event.NewTopic[UserEvent]("federation.link.deleted")
