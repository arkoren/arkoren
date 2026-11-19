---
title: Permissions
nav: Permissions
description: Assign core gate roles and permissions to users, check them on sessions and tokens, and manage them through optional routes.
order: 85
summary:
  - Declare permissions and roles with core gate and give them to IAM's permission store.
  - Grant and revoke them in Go, protect roles that must keep a holder, and transfer them safely.
  - Check them with gate rules on sessions and tokens, or for any user from jobs and admin screens.
  - Register the self-service and management routes with their session, freshness, and escalation rules.
---

# Permissions

Your application decides that only editors may publish posts and only staff
may export billing data. Core's [gate package](../authorization/overview.md#permissions-and-roles)
names those abilities and checks them, but it stores nothing. IAM's
**permission store** records which users hold which roles and permissions and
loads them into every session, so the same gate rules work without a query.

The store handles global grants: a user is an editor everywhere or nowhere.
Per-team roles stay [application policies](../authorization/policies.md#team-roles),
and relationship-based access belongs behind your own gate rule.

## Define Access

Declare permissions and roles once, as package variables, with `gate` from
`arkoren.dev/core/auth/gate`:

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

`Editor` includes `Viewer`, so an editor holds both permissions. Give the
top-level roles to IAM with `permission` from `arkoren.dev/ark/iam/permission`:

```go
identity, err := iam.New(db, store, func(identity *iam.Builder) {
	identity.Password(true, hasher)
	identity.Permissions(true, func(policy *permission.Builder) {
		policy.Roles(Admin).Define(BillingExport)
	})
})
```

Every role `Admin` includes, and every permission they hold, is defined too.
`Define` adds permissions that no role grants, such as `BillingExport`, which
you grant to individual users. Construction
fails with `iam.ErrInvalidConfig` for an invalid definition or for two
different roles that share a name. The store adds one table,
`permission_grants`, to `identity.Migrations()`; run them before serving.

## Grant Access

Assign a role or a single permission from your invitation or admin code:

```go
err = identity.GrantRole(ctx, subject, Editor)
err = identity.Grant(ctx, subject, BillingExport)
```

Both need a user that exists and a value the store defines; otherwise they
return `auth.ErrUserNotFound` or `permission.ErrUnknownGrant`. Granting what a
user already holds changes nothing. `Revoke` and `RevokeRole` remove a grant,
and revoking what was never granted is not an error. Every call joins the
[transaction](../database/transactions.md) in its context, and a change
publishes `permission.GrantCreated` or `permission.GrantRevoked` after commit
when you configure `identity.Events`.

Revoking `Editor` does not remove `PostsRead` if the user was also granted it
directly or holds `Viewer` on its own. `identity.Permissions().Assignments(ctx,
subject)` shows what was assigned; `Holders(ctx, role)` lists who holds a role
directly.

## Keep a Holder

Some roles must never become empty: without an administrator nobody can
grant access again. Protect them with `KeepOne`:

```go
identity.Permissions(true, func(policy *permission.Builder) {
	policy.Roles(Admin).KeepOne(Admin)
})
```

Revoking `Admin` from its only holder then returns `permission.ErrLastHolder`,
which renders as `409`. A user assigned a role that includes `Admin` also counts
as a holder, and revoking such a role is refused when it would leave `Admin`
without one. The check locks the holders' rows on PostgreSQL and MySQL, so two
administrators revoking each other at once leave one. SQLite has no row locks:
a concurrent revoke may fail with a database-locked error instead, which never
removes the last holder.

A permission granted directly with `Grant` has no such protection. Give
authority that must not disappear, such as the management permission, through
a `KeepOne` role. To hand the role
over, grant it first and revoke it second, in one transaction:

```go
err = db.WithTransaction(ctx, func(ctx context.Context) error {
	if err := identity.GrantRole(ctx, next, Admin); err != nil {
		return err
	}

	return identity.RevokeRole(ctx, previous, Admin)
})
```

`KeepOne` protects revokes only. [Deleting the account](accounts.md#delete-an-account)
of the last holder still succeeds and publishes `permission.HolderOrphaned`;
listen for it to alert someone who can appoint a new holder.

## Check Access

With the store enabled, IAM's guard loads each logged-in user's roles and
permissions on every request. The session's identity carries the expanded
role names (an editor also carries `viewer`) and the permission names as
scopes, so gate rules need no database call:

```go
private := web.With(auth.Require())
private.With(gate.Require(gate.HasPermission(PostsWrite))).Post("/posts", createPost)
```

A grant or revoke applies to existing sessions on their next request. The
loader runs once per authenticated session request; if the store cannot be
read, the request fails rather than continuing without permissions. To reduce
that cost, set `policy.CacheTTL(time.Minute)`: the store caches each user's
grants in the facade's cache and clears the entry after every write commits.
A reader racing that clear can store the old grants again, so a change usually
applies at once and at worst within the cache lifetime. Reads inside a
transaction skip the cache.

> [!WARNING]
> Sessions now carry scopes, so a cookie session passes `gate.HasPermission`
> and `gate.HasScopes` routes that previously only API tokens could pass. If
> such a route runs `identity.Middleware()` without CSRF protection, another
> site can submit requests with the user's cookie. Keep
> [CSRF middleware](../browser-security/csrf-protection.md) on every route
> under `identity.Middleware()`.

To answer for a user who is not making the request, such as in a job or an
admin screen, ask the facade:

```go
allowed, err := identity.HasPermission(ctx, subject, BillingExport)
```

`HasAnyPermission`, `HasRole`, and `HasAnyRole` work the same way. An empty or
undefined argument is `permission.ErrUnknownGrant`.

Prefer permission checks over role checks. A role is only a bundle, and
checking the permission keeps working when you move it to another role and when
the caller is a token.

## Tokens

With [API tokens](tokens.md) enabled too, IAM caps every token at its owner's
permissions: the catalog is every permission the store defines, and a user can
only request scopes they hold. Revoking a role removes its permissions from
existing tokens on their next request. A token's identity carries only its
scopes and never roles, so `gate.HasRole` always denies a token, while
`gate.HasPermission(PostsRead)` passes for a token granted `posts:read`.

Keep the token `GrantTTL` at zero, its default, when the store has a `CacheTTL`:
a positive `GrantTTL` caches the ceiling again, so a revoke can take both
lifetimes to reach a token.

Calling `tokens.Grants` in your `Tokens` callback replaces this ceiling. Do so
only on purpose: your own grant function then decides what tokens may carry.

## Routes

The store can register two route groups, both off by default. Self-service lets a
signed-in user see what they hold:

```go
identity.Permissions(true, func(policy *permission.Builder) {
	policy.Roles(Admin).SelfService(true)
})
```

`GET /me/permissions` answers `{"roles": [...], "permissions": [...]}` from the
request's identity, without a query. A token sees its own scopes and no roles.

Management lets administrators change other users' grants. Name the
permission a caller must hold:

```go
identity.Permissions(true, func(policy *permission.Builder) {
	policy.Roles(Admin).KeepOne(Admin).Management(true, UsersManage)
})
```

This registers `GET /users/{subject}/permissions` and `PUT` and `DELETE` on
`/users/{subject}/permissions/{name}` and `/users/{subject}/roles/{name}`.
Every call needs a browser session holding `users:manage`; a token is refused
with `401`. Writes also need a [fresh login](freshness.md) and
`Content-Type: application/json` on `PUT`. A caller can only grant or revoke a
permission they hold, or a role when they hold all of its permissions and every
role it includes (and the role itself); anything else
is `403 not-grantable`. An undefined name is `422`, an unknown user `404`, and
the last holder of a `KeepOne` role `409`. See the [route reference](routes.md)
for custom paths.

For all functions and options, see the
[`permission` API reference](https://pkg.go.dev/arkoren.dev/ark/iam/permission).
