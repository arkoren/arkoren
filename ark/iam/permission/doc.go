// Package permission stores which users hold which roles and permissions, and
// answers what a user holds. The vocabulary is core's: [gate.Permission] names
// an action, [gate.Role] bundles permissions and may include other roles, and
// the gate rules (gate.HasPermission, gate.HasRole and their Any forms) check
// the request's [auth.Identity]. This package is only the store: assignments,
// grants and revokes, last-holder protection, routes, events and forgetting.
//
// # Defining
//
// Declare the definitions once, as package variables shared by the checks and
// the store, and give the store the roles (and any permission no role grants):
//
//	var (
//		PostsRead  = gate.NewPermission("posts:read")
//		PostsWrite = gate.NewPermission("posts:write")
//
//		Viewer = gate.NewRole("viewer", func(role *gate.RoleBuilder) {
//			role.Grants(PostsRead)
//		})
//		Editor = gate.NewRole("editor", func(role *gate.RoleBuilder) {
//			role.Includes(Viewer).Grants(PostsWrite)
//		})
//	)
//
//	permissions, err := permission.NewStore(provider, db, store, func(policy *permission.Builder) {
//		policy.Roles(Viewer, Editor)
//	})
//
// Every role a given role includes, and every permission they hold, is defined
// too. [NewStore] rejects invalid definitions and two different definitions
// with one name. The store does no I/O when built; the application runs
// [Store.Migrations] with its migrator and checks [Store.Ping].
//
// # Granting
//
// [Store.Grant] and [Store.GrantRole] store an assignment for a subject the
// user provider finds, and [Store.Revoke] and [Store.RevokeRole] remove one.
// Writes are idempotent, join the transaction in their context, and publish
// [GrantCreated] or [GrantRevoked] after commit only when they changed a row. A
// value the store does not define is [ErrUnknownGrant]. [Builder.KeepOne]
// protects roles that must keep a holder: revoking the last one is
// [ErrLastHolder], and a transfer is GrantRole then RevokeRole in one
// transaction.
//
// # Checking
//
// [Store.Load] returns a user's expanded role names and effective permission
// names. It has the shape of [auth.GuardBuilder.Grants], so the guard puts them
// on every session identity and the gate rules work without a query:
//
//	guard, err := auth.NewGuard(provider, store, func(policy *auth.GuardBuilder) {
//		policy.Grants(permissions.Load)
//	})
//	r.With(gate.Require(gate.HasPermission(PostsWrite))).Post("/posts", create)
//
// [Store.Permissions] is the same scopes as a grant function for API tokens,
// so a token never carries more than its owner holds:
//
//	tokens.Grants(permissions.Permissions, permissions.Catalog()...)
//
// [Store.HasPermission], [Store.HasRole] and their Any forms answer for any user,
// for jobs and admin screens. Names stored in the table that the store no longer
// defines are ignored everywhere, which fails closed.
//
// # Routes
//
// [Store.SelfHandler] serves GET /me/permissions from the request's identity.
// With [Builder.Management] the management handlers list, grant and revoke for
// other users; they need a fresh browser session holding the management
// permission and refuse to hand out more than the caller holds
// ([ErrNotGrantable]). The facade of arkoren.dev/ark/iam mounts them when
// [Builder.SelfService] or [Builder.Management] asks for them.
//
// # Storage
//
// One table, "permission_grants" ("<instance>_permission_grants" for a named
// [Builder.Instance]), keyed by subject, kind and name, in the migration group
// "arkoren.dev:ark/access:permission" (plus ":<instance>"). The provider's forget
// hook deletes a user's rows and publishes [HolderOrphaned] when that leaves a
// KeepOne role without holders. With [Builder.CacheTTL] the stored grants are
// cached per subject and cleared after every write commits.
package permission
