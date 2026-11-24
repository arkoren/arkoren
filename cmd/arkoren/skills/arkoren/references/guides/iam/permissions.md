---
title: Roles & Permissions
nav: Roles & Permissions
description: Define core gate roles and permissions, assign them to users in IAM, protect critical roles, check access on sessions and tokens, and mount management routes.
order: 80
summary:
  - Define roles and permissions once with core gate and wire them into IAM's permission store.
  - Grant, revoke, and transfer roles safely while protecting roles that must always keep a holder.
  - Check permissions on HTTP requests, in background jobs, and automatically as API token ceilings.
  - Enable optional self-service and administrator routes with built-in escalation guards.
---

# Roles & Permissions

Core's [`gate` package](../authorization/overview.md#permissions-and-roles) gives you a vocabulary for permissions, roles, and route checks, but it does not store who holds what. IAM's **permission store** fills that gap: it persists user assignments in your database and loads them onto every authenticated session so your `gate` checks run without extra queries.

IAM's permission store models **global** grants: a user is an `editor` across the application or not at all. Keep per-team roles in [application policies](../authorization/policies.md#team-roles) and object-ownership checks in your own handlers or gate rules.

## Defining Roles and Permissions

Declare your permissions and roles once as package variables using `gate` from `arkoren.dev/core/auth/gate`:

```go
var (
	PostsRead     = gate.NewPermission("posts:read")
	PostsWrite    = gate.NewPermission("posts:write")
	UsersManage   = gate.NewPermission("users:manage")
	BillingExport = gate.NewPermission("billing:export")

	Viewer = gate.NewRole("viewer", func(role *gate.RoleBuilder) {
		role.Grants(PostsRead)
	})
	Editor = gate.NewRole("editor", func(role *gate.RoleBuilder) {
		role.Includes(Viewer).Grants(PostsWrite)
	})
	Admin = gate.NewRole("admin", func(role *gate.RoleBuilder) {
		role.Includes(Editor).Grants(UsersManage)
	})
)
```

Because `Admin` includes `Editor` and `Editor` includes `Viewer`, passing the top-level role to IAM registers the entire hierarchy automatically. Use `Define` for standalone permissions that no role grants directly. Import `permission` from `arkoren.dev/ark/iam/permission`:

```go
identity.Permissions(func(policy *permission.Builder) {
	policy.Roles(Admin).Define(BillingExport)
})
```

Invalid definitions or duplicate role names cause `iam.New` to fail with `iam.ErrInvalidConfig`. Any grant stored in the database that is later removed from your code is ignored everywhere (failing closed).

## Granting and Revoking Access

Assign roles or individual permissions from your invitation or admin code:

```go
err = identity.GrantRole(ctx, subject, Editor)
err = identity.Grant(ctx, subject, BillingExport)
```

Both methods require an existing user (`auth.ErrUserNotFound` otherwise) and a defined role or permission (`permission.ErrUnknownGrant` otherwise). Granting something a user already holds is a no-op; `RevokeRole` and `Revoke` are likewise idempotent. Every write joins the active [database transaction](../database/transactions.md) in `ctx` and publishes `permission.GrantCreated` or `permission.GrantRevoked` after commit when `identity.Events` is configured.

Revoking `Editor` leaves `PostsRead` intact if the user also holds `Viewer` or a direct grant to `PostsRead`. To inspect stored assignments, use `identity.Permissions().Assignments(ctx, subject)` or `identity.Permissions().Holders(ctx, role)`.

### Protecting Critical Roles

Some roles, such as `Admin`, should never become empty, or no one will be left to administer the app. Protect them with `KeepOne`:

```go
identity.Permissions(func(policy *permission.Builder) {
	policy.Roles(Admin).KeepOne(Admin)
})
```

Revoking `Admin` (or any role that includes `Admin`) from its final holder returns `permission.ErrLastHolder` (`409`). On PostgreSQL and MySQL, row locks serialize concurrent revokes so two admins cannot accidentally remove each other at the same time; on SQLite, a concurrent revoke may fail with a database-locked error without removing the last holder.

To hand off a `KeepOne` role, grant the new holder first and revoke the previous one inside one transaction:

```go
err = db.WithTransaction(ctx, func(ctx context.Context) error {
	if err := identity.GrantRole(ctx, next, Admin); err != nil {
		return err
	}

	return identity.RevokeRole(ctx, previous, Admin)
})
```

`KeepOne` guards revocations only; [deleting the account](accounts.md#deleting-accounts) of the last holder still succeeds and publishes `permission.HolderOrphaned` so you can alert an operator. Direct permissions granted with `Grant` have no `KeepOne` protection; assign critical authority through a protected role instead.

## Checking Access

With the permission store enabled, IAM loads the signed-in user's expanded roles and permissions onto the session identity on every request. Protect your routes with standard `gate` middleware:

```go
private := web.With(auth.Require())
private.With(gate.Require(gate.HasPermission(PostsWrite))).Post("/posts", createPost)
```

Prefer checking **permissions** (`gate.HasPermission`) over roles (`gate.HasRole`): permissions stay stable when you reorganize roles, and they work identically for both browser sessions and API tokens.

Changes apply on the user's very next request. If the database cannot be read, the request fails closed rather than continuing without permissions. To avoid a database lookup on every request, cache grants in your facade's cache:

```go
identity.Permissions(func(policy *permission.Builder) {
	policy.Roles(Admin).CacheTTL(time.Minute)
})
```

IAM clears a user's cache entry whenever a write commits (reads inside a transaction always bypass the cache). Because a concurrent reader can occasionally repopulate a stale entry during invalidation, changes usually apply immediately and at worst within `CacheTTL`.

> [!WARNING]
> Enabling the permission store puts scopes onto cookie-authenticated sessions, which means browser cookies now pass `gate.HasPermission` and `gate.HasScopes` routes. Keep [CSRF middleware](../browser-security/csrf-protection.md) on every route behind `identity.Middleware()` so other sites cannot trigger state-changing requests with a user's cookie.

To check permissions outside an HTTP request, such as in a background job or admin screen, query the facade directly:

```go
allowed, err := identity.HasPermission(ctx, subject, BillingExport)
```

`HasAnyPermission`, `HasRole`, and `HasAnyRole` work the same way and return `permission.ErrUnknownGrant` for unknown names.

## How Permissions Govern API Tokens

When you enable both `identity.Permissions(...)` and `identity.Tokens()`, IAM wires them together automatically:

- The API token scope catalog becomes every permission defined in your permission store.
- Users can only request scopes they personally hold.
- Revoking a user's role removes those permissions from their existing tokens on the next request.
- Tokens carry scopes only, never roles, so `gate.HasPermission` works on tokens while `gate.HasRole` always denies them.

Keep the token `GrantTTL` at its default (`0`) when using `policy.CacheTTL` so revocations do not wait through two stacked cache windows. Calling `tokens.Grants(...)` explicitly overrides this automatic ceiling.

## Self-Service and Management Routes

Both route groups are off by default. Enable `SelfService(true)` to mount `GET /me/permissions`, which returns `{"roles": [...], "permissions": [...]}` directly from the request identity (tokens see their own scopes and no roles):

```go
identity.Permissions(func(policy *permission.Builder) {
	policy.Roles(Admin).SelfService(true)
})
```

Enable `Management(UsersManage)` to mount administrator endpoints for viewing and changing other users' grants:

```go
identity.Permissions(func(policy *permission.Builder) {
	policy.Roles(Admin).KeepOne(Admin).Management(UsersManage)
})
```

This mounts `GET /users/{subject}/permissions`, plus `PUT` and `DELETE` on `/users/{subject}/permissions/{name}` and `/users/{subject}/roles/{name}`. Every management call requires a browser session holding `users:manage` (tokens receive `401`), and writes also require a [fresh login](freshness.md) and `Content-Type: application/json` on `PUT`.

To prevent privilege escalation, a caller can only grant or revoke permissions they personally hold, or roles for which they hold the role itself, every included role, and every granted permission (`403` `permission:not-grantable` otherwise). Unknown names return `422`, unknown users `404`, and removing the last holder of a `KeepOne` role returns `409`.

Next, add [API & Device Tokens](tokens.md) for scripts, integrations, and first-party mobile apps.

For all permission store methods and events, see the [`permission` API reference](https://pkg.go.dev/arkoren.dev/ark/iam/permission).
