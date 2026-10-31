---
title: Authorization Overview
nav: Overview
description: Decide what a signed-in user may do with plain Go rules, protect routes with the gate, and check resources in handlers.
order: 10
---

# Authorization Overview

## Introduction

[Authentication](../authentication/overview.md) tells you who sent a request.
**Authorization** decides whether they may do what they asked: an editor may
publish posts, a customer may see their own invoices but not anyone else's, and
only an administrator may delete accounts.

Arkoren's `gate` package expresses these decisions as **rules**, plain Go
functions that receive the signed-in user and answer yes or no. There is no
policy language, no registry of abilities, and no roles built in. A rule can
check a field, query the database, or call another service, and rules combine
with ordinary `if` and `||`. The gate runs them, and turns a "no" into the right
HTTP response. Use route checks for admission to an area of your service, then
resource rules for the particular record being read or changed. Passing a route
check does not grant access to every resource behind that route.

The gate is independent of how the user signed in. It reads the user from the
request's context and nothing else, so the same rules work behind
[session login](../authentication/session-login.md),
[API tokens](../authentication/api-tokens.md),
[OAuth](../authentication/oauth.md), an authenticator of your own, or a
background job that has no request at all. Authentication can change without
touching a single rule.

For tools, keep resource checks in the shared application operation rather
than only on an HTTP route. [Securing MCP](../mcp/securing-mcp.md) distinguishes
endpoint admission and tool availability from authorization of each resource.

This page covers rules and where to check them. [Writing Policies](policies.md)
organizes rules by feature and covers roles, permissions, teams, and token
scopes. [Row-Level Access](row-access.md) keeps "which records may this user
see?" in queries, and [Handling Denials](denials.md) explains the responses and
how to observe them.

## Writing a rule

Examples import `gate` from `arkoren.dev/core/http/auth/gate`, `auth` from
`arkoren.dev/core/http/auth`, `router` from `arkoren.dev/core/http/router`, and
`context` from the standard library. `r` is your authenticated router;
`User`, `Post`, and their fields are application-owned types.

A rule receives the request's context and the user, and returns whether the
user is allowed:

```go
func isAdmin(ctx context.Context, u User) (bool, error) {
	return u.Role == "admin", nil
}
```

The two return values keep two different situations apart. `false` means the
user is not allowed, and the request is denied with `403 Forbidden`. An error
means the rule could not decide, for example because the database is down, and
the error is returned as is, usually as a `500`. A rule that fails is therefore
never mistaken for a denial, and never for permission.

## Protecting routes

`gate.Require` checks rules before a route's handler runs:

```go
r.With(gate.Require(isAdmin)).Delete("/users/{id}", deleteUser)
```

The gate reads the user that the [auth middleware](../authentication/overview.md#the-auth-middleware)
stored on the request. An anonymous request is rejected with `401`, so you do
not need `auth.Require` in front of the gate. A signed-in user whom a rule
denies receives a `403` Problem Details response, described in
[Handling Denials](denials.md).

To protect a whole area of your service, apply the gate to a group:

```go
r.Group("/admin", func(admin *router.Router) {
	admin.Use(gate.Require(isAdmin))
	admin.Get("/", dashboard)
	admin.Get("/users", listUsers)
})
```

`gate.Require` panics when you give it no rule, because a gate that checks
nothing is almost always a mistake. To require only that someone is signed in,
use `auth.Require`.

## Checking in a handler

A route-level rule only knows the user. Many decisions also depend on the
thing being accessed: an author may edit their own post, not every post. Load
the resource in the handler first, then check with `gate.Authorize`, which runs
the same rules and returns the same errors. With `post` already loaded by your
application's store, stop the update if authorization fails:

```go
if err := gate.Authorize(r.Context(), canEdit(post)); err != nil {
	return err
}
```

Return the error unchanged: the gate's missing-user and denial errors are
Problem Details values that render as `401` and `403`. A rule's own error is
returned as is and follows your application's normal error rendering; it is not
necessarily a Problem Details value.

### Rules about a resource

A rule about a resource is a function that takes the resource and returns a
rule. The returned rule closes over the resource, so it still has the shape the
gate expects:

```go
func canEdit(post Post) func(context.Context, User) (bool, error) {
	return func(ctx context.Context, u User) (bool, error) {
		return u.Role == "admin" || post.AuthorID == u.ID, nil
	}
}
```

The `||` is how rules combine: there are no special combinators, because Go
already has them.

### Several rules

When you pass several rules, all of them must allow. They run in order, and the
first denial or error stops the check, so put cheap rules before rules that
query the database:

```go
gate.Require(isActive, hasPaidPlan(db))
```

## Where the user comes from

The gate looks the user up with `auth.FromContext`, by its type. The type
parameter of a rule must therefore be exactly the type your authenticator
returns. If the auth middleware stores a `User` and a rule takes a `*User`, the
gate finds no user of that type and answers `401` for every request, even
signed-in ones. Use one user type for authentication and authorization, and let
the compiler infer it from the rules.

Because the gate only reads the context, it also works where there is no HTTP
request. A [queue job](../queues/consuming-jobs.md) or a scheduled task that acts for a
user can put the user in its context with `auth.NewContext` and check the same
rules a handler would. First load the current user from your application's
store; with `user` and the queued `job` available, authorize before exporting:

```go
ctx = auth.NewContext(ctx, user)
if err := gate.Authorize(ctx, canExport(job.ReportID)); err != nil {
	return err // the user lost access after the job was queued
}
```

Here `canExport` is your application's resource rule for the report, following
the same pattern as `canEdit` above.

Checking again in the job matters when time passes between the request and the
work: a user whose access was revoked after queueing an export should not
receive it.

## Testing

Rules are functions, so the simplest test calls them directly:

```go
ok, err := canEdit(Post{AuthorID: 7})(t.Context(), User{ID: 8})
if err != nil || ok {
	t.Fatalf("a stranger may edit the post: %v, %v", ok, err)
}
```

To test a protected route, put a user in the request's context and send it
through the router, as shown in [Testing Authentication](../testing/auth-tests.md).
Test each route both with a user who is allowed and with one who is not: the
second test is the one that catches a missing check.

For every function, see the [`gate` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth/gate).
