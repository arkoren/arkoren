---
title: Writing Policies
nav: Policies
description: Organize authorization rules by feature in policy structs, and express roles, permissions, teams, and token scopes as plain Go.
order: 20
---

# Writing Policies

## Introduction

A handful of rules can live as functions next to the routes that use them. As a
service grows, the same questions come back in many handlers: is this user an
owner of the team, may they manage billing, does their plan include exports?
A **policy** is the place where a feature keeps the answers. It is not a type
from Arkoren: it is an ordinary struct of your own, holding whatever the rules
need, with one method per rule.

This page shows how to organize policies, and how to express the common
authorization models with them: roles, permissions, membership of a team or
tenant, and the scopes of an access token. None of these models is built into
the gate, so you can mix them, or start with the simplest one and move on when
it no longer fits, without changing how routes are protected.

## A policy per feature

Examples use `gate` from `arkoren.dev/core/http/auth/gate`, `database` from
`arkoren.dev/core/service/database`, `query` from `arkoren.dev/foundation/query`,
and `oauth "arkoren.dev/driver/oauth/oauthauth"`. `context`, `errors`, and
`slices` are standard-library packages. `User`, `Team`, and `Member` are your
application's types; use the same exact user type as your authenticator.

Keep each feature's policy in the same package as its handlers, as described
in [Project Layout](../first-steps/project-layout.md). Rules that need the
database are methods on a struct that holds it, rather than functions that
reach for a global. Here `isTeamOwner` is an application lookup that checks the
membership record; it is not a gate helper:

```go
type Policy struct{ db *database.Database }

func (policy *Policy) CanInvite(team Team) func(context.Context, User) (bool, error) {
	return func(ctx context.Context, u User) (bool, error) {
		return policy.isTeamOwner(ctx, team.ID, u.ID)
	}
}
```

The struct is built once at startup and passed to the handlers like any other
dependency, which also makes it easy to replace in tests. A rule that needs no
resource is a method with the rule's own signature, so it can be passed to the
gate directly:

```go
func (policy *Policy) CanCreateTeams(_ context.Context, u User) (bool, error) {
	return u.EmailVerified, nil
}

r.With(gate.Require(policy.CanCreateTeams)).Post("/teams", teams.create)
```

Name rules after the action they allow (`CanInvite`, `CanRefund`), not after
the check inside them (`IsOwner`). Handlers then read as a description of the
feature, and the check can change, for example from "owner" to "owner or
billing manager", without renaming anything.

## Roles

The simplest model gives every user one **role**, stored with the user. A rule
that allows a set of roles is a small function:

```go
func hasRole(roles ...string) func(context.Context, User) (bool, error) {
	return func(_ context.Context, u User) (bool, error) {
		return slices.Contains(roles, u.Role), nil
	}
}

r.With(gate.Require(hasRole("admin", "editor"))).Post("/posts/{id}/publish", publish)
```

When roles form a ladder, where each one may do everything the one below may,
give them a rank and compare it, so adding a role does not mean editing every
rule:

```go
var rank = map[string]int{"viewer": 1, "editor": 2, "admin": 3}

func atLeast(role string) func(context.Context, User) (bool, error) {
	return func(_ context.Context, u User) (bool, error) {
		required, known := rank[role]
		return known && rank[u.Role] >= required, nil
	}
}
```

An unknown user role has rank zero and is allowed nothing. An unknown required
role also denies access, so a misspelled requirement cannot accidentally open
the route.

## Permissions

Roles become awkward when some users need one extra ability, such as an editor
who may also issue refunds. **Permissions** name abilities directly, and a
user holds a set of them. Load the set together with the user, in the function
your authenticator calls, so rules can check it without a query:

```go
type User struct {
	ID          int
	Permissions map[string]bool
}

func can(permission string) func(context.Context, User) (bool, error) {
	return func(_ context.Context, u User) (bool, error) {
		return u.Permissions[permission], nil
	}
}

r.With(gate.Require(can("refunds:issue"))).Post("/orders/{id}/refund", refund)
```

Roles and permissions combine well: store which permissions each role grants,
add the user's own extra permissions, and fill `Permissions` with the union
when the user is loaded. Rules then only ever ask about permissions.

> [!NOTE]
> Permissions loaded with the user are read once per request. A permission
> removed while a request runs still applies until that request ends, which is
> almost always acceptable. A long-running job that acts for a user should
> load the user again, as described in
> [Where the user comes from](overview.md#where-the-user-comes-from).

## Teams and tenants

In many services, what a user may do depends on where they are: a user is an
owner in one team and a plain member in another. The answer then comes from a
membership record, so the rule takes the team and queries it:

```go
func (policy *Policy) CanManageBilling(teamID int) func(context.Context, User) (bool, error) {
	return func(ctx context.Context, u User) (bool, error) {
		member, err := policy.db.Find[Member](ctx, query.Table("members", func(statement *query.Builder) {
			statement.Where("team_id", teamID)
			statement.Where("user_id", u.ID)
		}))
		if errors.Is(err, database.ErrNoRows) {
			return false, nil // not a member at all
		}
		if err != nil {
			return false, err
		}

		return member.Role == "owner" || member.Role == "billing", nil
	}
}
```

Notice the two outcomes: no membership row is a denial, while any other error
is returned, so a database failure is never reported as `403`.

When every route under a path belongs to one team, such as
`/teams/{team}/...`, check membership once for the whole group in a middleware
of your own and let the rules inside it ask only about the role. Which rows of
the team a user may then see is a query concern, covered in
[Row-Level Access](row-access.md).

## Token scopes

When requests come with an access token from an identity provider, the token
lists the permissions the user granted to the client, its **scopes**. A scope
limits what the client may do for the user; it never grants the user more than
their own permissions. Check both: the scope says the client may write posts,
and a rule of your own says this user may write this post.

The OAuth package provides a rule for scopes when authentication stores the
token's `oauth.Identity`, as shown in
[OIDC Bearer Verification](../authentication/oidc-bearer.md):

```go
api.With(gate.Require(oauth.HasScopes("invoices:read"))).Get("/invoices", list)
```

`oauth.HasScopes` takes an `oauth.Identity`, not an application `User`. If your
authenticator maps tokens to `User`, carry the verified scopes in a
request-local `User.Scopes []string` field and use a rule of that same type:

```go
func canReadInvoices(_ context.Context, user User) (bool, error) {
	return slices.Contains(user.Scopes, "invoices:read"), nil
}

api.With(gate.Require(canReadInvoices)).Get("/invoices", list)
```

Here `slices` is the standard-library package. The gate looks up the exact
type used by the rule: combining a `User` authenticator with
`oauth.HasScopes` yields `401`, even for a valid token. Do not populate token
scopes from unverified input or merge them with the user's full permissions.
Apply resource permissions and query scopes separately; a token's
`invoices:read` scope must not expose another customer's invoices.

[API Tokens](../authentication/api-tokens.md) covers keys that carry
permissions of their own. For MCP or LLM tools, availability controls discovery
and capability admission, not access to each record. Keep these resource rules
in the shared application operation, as described in
[Securing MCP](../mcp/securing-mcp.md), so HTTP, tools, and jobs use the same
authorization boundary.

## Testing policies

Construct the policy with a [test database](../testing/integration-tests.md),
seed a team and a plain member, then call the rule it returns. This assertion
checks denial; add a corresponding owner test that expects permission:

```go
policy := &Policy{db: db}

ok, err := policy.CanManageBilling(team.ID)(t.Context(), member)
if err != nil || ok {
	t.Fatalf("a plain member may manage billing: %v, %v", ok, err)
}
```

Rules that only read the user, such as `can` and `atLeast`, need no database
at all. Test the routes that use them as described in
[Authorization Overview](overview.md#testing).

For every function, see the [`gate` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth/gate).
