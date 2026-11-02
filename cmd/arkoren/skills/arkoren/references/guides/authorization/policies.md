---
title: Writing Policies
nav: Policies
description: Group feature rules in policy structs and distinguish tenant roles, application permissions, and delegated scopes.
summary:
  - Organize feature decisions in policy structs with explicit dependencies.
  - Check tenant-specific membership roles without hiding database failures as denials.
  - Require token scopes alongside application permissions and resource access.
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
being managed. These application types distinguish global permissions from the
role held in one team:

```go
type User struct {
	ID          int
	Permissions map[string]bool
	Scopes      []string
}

type Team struct {
	ID int `db:"id"`
}

type Membership struct {
	Role string `db:"role"`
}
```

Authentication must store exactly `User`, not `*User`. Load `Permissions` from
your trusted application data. Populate `Scopes` only when verified token
credentials supply them; the [token checks](#token-limits) below use that field
for a separate decision.

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

## Team Roles

A **role** groups responsibilities. If a user can be an owner in one team and a
viewer in another, look up the role for both the actor and the target team; a
global `User.Role` cannot express that boundary.

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
rule before changing billing details. Import `gate` from
`arkoren.dev/core/http/auth/gate`:

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

## Explicit Permissions

Use **permissions**, named abilities, when roles are too coarse. For example,
allow selected staff to prepare billing exports without making them owners of
every team. A missing map entry denies by default:

```go
func (policy *BillingPolicy) CanExport(_ context.Context, user User) (bool, error) {
	return user.Permissions["billing:export"], nil
}
```

This is an action-level permission, not access to all billing records. Combine
it with the target's membership rule and scoped query when exporting one team's
data. You can resolve role grants and individual grants into `Permissions`
during identity loading; Arkoren does not compute that union for you.

Permissions loaded once remain a snapshot until you reload them. Decide whether
that lifetime meets your revocation needs. Long-running or queued operations
should [reload the actor before work](overview.md#deferred-work), and sensitive
writes might need a transactional membership check rather than a request-start
snapshot.

## Token Limits

A **scope** limits a client's delegated authority. It must restrict, not replace,
the user's application permissions and resource access. For a bearer-token API
whose authenticator stores the `User` above, require the verified token scope
as another rule of that same type. Here `slices` is the standard-library package:

```go
func hasBillingScope(_ context.Context, user User) (bool, error) {
	return slices.Contains(user.Scopes, "billing:export"), nil
}

if err := gate.Authorize(ctx, hasBillingScope, policy.CanExport, policy.CanManage(team)); err != nil {
	return err
}
```

This token-specific path denies when the scope is absent. Do not fill `Scopes`
from a header or unverified claim, or copy the user's full permissions into it:
either action can expand what a restricted credential may do.

If authentication instead stores `oauth.Identity` directly, the OAuth adapter
provides a scope rule. Import
`oauth "arkoren.dev/driver/oauth/oauthauth"`; with the bearer-authenticated
router `api`, apply it as admission:

```go
api.With(gate.Require(oauth.HasScopes("billing:export"))).Post("/billing/exports", exportBilling)
```

`HasScopes` requires every named scope, and an empty requirement allows any
authenticated `oauth.Identity`. It neither authenticates nor checks local team
membership. Do not combine it with `User` rules in the same gate call: the types
differ. Map verified issuer-and-subject identity to your local actor before local
resource decisions, following [OIDC bearer verification](../authentication/oidc-bearer.md).
[API keys](../authentication/api-tokens.md#issue-keys) also need their credential
permissions intersected with the actor's authority.

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
team, unknown roles, and database failure. Test missing scopes separately from
missing application permissions, then test protected routes for allowed,
forbidden, and anonymous callers. Direct rule tests cannot prove that a handler
actually calls the rule.

For all functions and options, see the
[`gate` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth/gate).
