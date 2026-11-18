package permission

import "arkoren.dev/core/event"

// Kind says whether a stored grant is a role or a permission.
//
// Example:
//
//	if created.Kind == permission.KindRole {
//		// a role assignment
//	}
type Kind string

const (
	// KindPermission marks a permission granted directly ([Store.Grant]).
	//
	// Example:
	//
	//	fmt.Println(permission.KindPermission) // permission
	KindPermission Kind = "permission"

	// KindRole marks a role assignment ([Store.GrantRole]).
	//
	// Example:
	//
	//	fmt.Println(permission.KindRole) // role
	KindRole Kind = "role"
)

// GrantEvent is the payload of the events of this package: the subject, and the
// kind and name of the grant. It carries no other user data.
//
// Example:
//
//	events.Listen(permission.GrantCreated, func(ctx context.Context, created permission.GrantEvent) error {
//		return audit(ctx, created.Subject, created.Kind, created.Name)
//	})
type GrantEvent struct {
	// Subject is the user the grant belongs to.
	Subject string

	// Kind is whether the grant is a role or a permission.
	Kind Kind

	// Name is the role or permission name.
	Name string
}

// GrantCreated is published after a write stored a new grant, once its
// transaction commits. A repeated grant stores nothing and publishes nothing.
//
// Example:
//
//	unsubscribe, err := events.Listen(permission.GrantCreated, func(ctx context.Context, created permission.GrantEvent) error {
//		return nil
//	})
var GrantCreated = event.NewTopic[GrantEvent]("permission.grant.created")

// GrantRevoked is published after a revoke deleted a grant, once its
// transaction commits. Revoking a grant the subject does not have publishes
// nothing; neither does [auth.UserProvider.Forget].
//
// Example:
//
//	unsubscribe, err := events.Listen(permission.GrantRevoked, func(ctx context.Context, revoked permission.GrantEvent) error {
//		return nil
//	})
var GrantRevoked = event.NewTopic[GrantEvent]("permission.grant.revoked")

// HolderOrphaned is published when forgetting a user removed the last holder of
// a role named in [Builder.KeepOne]. Subject is the forgotten user, Kind is
// [KindRole] and Name the role. Forgetting is never blocked, so this event is
// how the application learns that it must appoint a new holder.
//
// Example:
//
//	unsubscribe, err := events.Listen(permission.HolderOrphaned, func(ctx context.Context, orphaned permission.GrantEvent) error {
//		return alertOperators(ctx, orphaned.Name)
//	})
var HolderOrphaned = event.NewTopic[GrantEvent]("permission.holder.orphaned")
