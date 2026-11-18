---
title: Writing Policies
nav: Policies
description: Group feature rules in policy structs and distinguish global permissions, team roles, and token limits.
summary:
  - Organize feature decisions in policy structs with explicit dependencies.
  - Check global abilities with declared gate permissions that your permission store loads into sessions.
  - Check tenant-specific membership roles without hiding database failures as denials.
  - Keep token authority within the user's permissions and check permissions rather than roles.
order: 20
---

# Writing Policies

When several handlers need the same billing decision, keep that decision in a
**policy**: an application-owned struct with rule methods and explicit
dependencies. A policy is not an Arkoren registry or base type. Its methods
follow the [gate rule shape](overview.md), so callers use the same checks from
HTTP handlers, tools, or jobs.

## Feature Policies

For a team billing feature, keep the authenticated actor separate from the team
being managed. These application types identify the user, the team, and the
role held in one team:

```go
type User struct {
	ID int
}

type Team struct {
	ID int `db:"id"`
}

type Membership struct {
	Role string `db:"role"`
}
```

Authentication must attach exactly `User`, not `*User`, to the identity's record.
Global permissions and verified token scopes travel on the `auth.Identity`, not
on `User`; the [permission checks](#global-permissions) below read them there.

Give the policy its configured database rather than reaching for a global.
Import `database` from `arkoren.dev/core/service/database`:

```go
type BillingPolicy struct {
	db *database.Database
}

policy := &BillingPolicy{db: db}
```

Construct it during application setup and share it with the operations that
need billing checks. Keep shared fields and captured inputs immutable during
requests; the gate does not synchronize your policy's state.

## Global Permissions

Some abilities do not depend on a team. Selected staff might prepare billing
exports for any team they can already see, without becoming owners. Declare that
ability once as a `gate.Permission`, and bundle permissions into a `gate.Role`
when several users share them. Import `gate` from `arkoren.dev/core/auth/gate`:

```go
var (
	BillingExport = gate.NewPermission("billing:export")

	Accountant = gate.NewRole("accountant", func(role *gate.RoleBuilder) {
		role.Grants(BillingExport)
	})
)
```

A **permission store** is your application code that records which users hold
`Accountant` or `BillingExport`. Arkoren core stores no assignments. Give the
guard a loader that returns a user's role names, including every role those
include, and permission names. With `users` and `store` from your
[session login](../authentication/session-login.md) setup and your store's
`permissions.Load(context.Context, string) (roles, scopes []string, err error)`:

```go
guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
	policy.Grants(permissions.Load)
})
```

The guard calls `permissions.Load` on every authenticated session request and
puts the permission names in the identity's scopes and the role names in its
roles. Its cost and availability therefore join every logged-in request: if the
loader fails, the request fails with `auth: load grants: ...` rather than
continuing without permissions. Cache inside the loader, and clear a user's
entry when their grants change, so a store outage or slow query does not stop
every session.

`gate.HasPermission` checks the identity, so the rule needs no database call:

```go
if err := gate.Authorize(ctx, gate.HasPermission(BillingExport)); err != nil {
	return err
}
```

This is an action-level permission, not access to all billing records. Combine
it with the target's membership rule and scoped query when exporting one team's
data. Check the permission rather than `gate.HasRole(Accountant)`: the same
check then also works for API tokens, which carry scopes but never roles, and
moving the permission to another role changes no caller.

The identity holds the permissions the loader returned for this request, and the
guard calls the loader again on the next one, so a revocation takes effect no
later than the loader's cache allows. A permission revoked mid-request still
applies until that request ends. Long-running or queued operations should
[reload the actor before work](overview.md#deferred-work), and sensitive writes
might need a transactional check rather than a request-start snapshot.

## Team Roles

A `gate.Role` is global: a user either holds `accountant` or does not. If a user
can be an owner in one team and a viewer in another, look up the role for both
the actor and the target team instead; neither a global role nor a field such as
`User.Role` can express that boundary.

Assume `memberships` has a unique `(team_id, user_id)` pair and a `role` column.
Import standard-library `context` and `errors`, and `query` from
`arkoren.dev/foundation/query`. A billing rule allows owners and billing members:

```go
func (policy *BillingPolicy) CanManage(team Team) func(context.Context, User) (bool, error) {
	return func(ctx context.Context, user User) (bool, error) {
		member, err := policy.db.Find[Membership](ctx, query.Table("memberships", func(statement *query.Builder) {
			statement.Select("role")
			statement.Where("team_id", team.ID)
			statement.Where("user_id", user.ID)
		}))
		if errors.Is(err, database.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}

		return member.Role == "owner" || member.Role == "billing", nil
	}
}
```

No membership or an unrecognized role denies access. A lookup failure returns
an error instead of disguising an unavailable database as a missing membership.
Name the rule after the action, so changing the qualifying roles does not change
what callers ask.

With `team` returned by your [authorized store lookup](row-access.md), apply the
rule before changing billing details:

```go
if err := gate.Authorize(ctx, policy.CanManage(team)); err != nil {
	return err
}
```

The store lookup owns whether the caller may discover the team; this rule owns
whether the caller may manage its billing. Neither decision authorizes a
different team ID supplied later in the request. Keep writes tied to the same
target, and use [atomic write boundaries](row-access.md#shared-invariants) when
membership changes and writes must be serialized.

## Token Limits

A **scope** limits a client's delegated authority. It must restrict, not replace,
the user's application permissions and resource access. A token's identity
carries only the scopes the token was granted, and no roles, so
`gate.HasPermission(BillingExport)` passes only for a token granted
`billing:export`. Because one `gate.Authorize` call takes rules of a single
type, check the identity rule first and the user rules after it:

```go
if err := gate.Authorize(ctx, gate.HasPermission(BillingExport)); err != nil {
	return err
}
if err := gate.Authorize(ctx, policy.CanManage(team)); err != nil {
	return err
}
```

The same two calls serve a session, whose scopes are the user's permissions, and
a token, whose scopes are its grants. That only holds if a token can never be
granted more than its owner holds. Your token issuer and verifier own that
limit: [API keys](../authentication/api-tokens.md#issue-keys) must intersect
their scopes with the owner's current permissions. [IAM tokens](../iam/tokens.md)
narrow their stored scopes to the result of the grant function you pass to
`apitoken.Builder.Grants` when the token authenticates: on every request with the
default `GrantTTL` of zero, or at most once per TTL when it is positive. With a
negative `GrantTTL`, or with `apitoken.AnyScope` as the grant function, the
stored scopes are used as issued and not narrowed to the owner's current
permissions, so a revoked permission stays on existing tokens. Do not fill the identity's scopes from a header or
unverified claim, or copy the user's full permissions into a token's scopes:
either action expands what a restricted credential may do.

Once the guard has `Grants`, sessions pass these permission and scope checks
too. A route group you wrote for tokens alone is then reachable with the
session cookie wherever session middleware and the guard authenticator run, so
keep [CSRF protection](../browser-security/csrf-protection.md) on it or mount it on a
router that authenticates with `auth.Bearer` only.

Use `gate.HasScopes` for raw scope strings that are not application
permissions, such as provider scopes. It requires every named scope, and an
empty requirement allows any authenticated identity; `gate.HasPermission`
instead panics without arguments.

The OAuth adapter has no scope rule of its own. Wrap its verifier so the
granted scopes ride on the `auth.Identity`, as shown in
[OIDC Tokens](../oauth/bearer-tokens.md#require-identity), and check them as
above. It neither authenticates nor checks local team membership. Map verified
issuer-and-subject identity to your local actor before local resource
decisions, following [OIDC bearer verification](../oauth/bearer-tokens.md).

## Policy Tests

Call rules directly to test decisions independently of HTTP. With `policy`
backed by a [test database](../testing/integration-tests.md), seed a team and a
membership whose role is `viewer`. For that authenticated `viewer`, denial is
the expected result:

```go
allowed, err := policy.CanManage(team)(t.Context(), viewer)
if err != nil || allowed {
	t.Fatalf("viewer billing access = %v, %v; want false, nil", allowed, err)
}
```

Also cover owner and billing roles, absent membership, a member of a different
team, unknown roles, and database failure. Test permission rules with
identities built by `auth.NewIdentity` and `principal.Scopes(...)`, including a
token-like identity without roles. Then test protected routes for allowed,
forbidden, and anonymous callers. Direct rule tests cannot prove that a handler
actually calls the rule.

For all functions and options, see the
[`gate` API reference](https://pkg.go.dev/arkoren.dev/core/auth/gate).
