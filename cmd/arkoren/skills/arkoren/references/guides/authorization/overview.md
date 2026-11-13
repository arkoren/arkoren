---
title: Authorization Overview
nav: Overview
description: Check actions with plain Go rules and keep permission decisions beside the resources they protect.
summary:
  - Match gate rules to the authenticated user type or identity and distinguish denials from failed checks.
  - Combine route admission with resource checks against authorized store results.
  - Reload identities and recheck permissions before deferred work runs.
order: 10
---

# Authorization Overview

A signed-in user is not necessarily allowed to export a report or change
another customer's records. Authentication establishes an identity;
**authorization** decides what that identity may do. Arkoren's `gate` package
runs your permission checks before the protected action proceeds.

A **rule** is a Go function that receives a context and your authenticated user
type, then returns `(bool, error)`. You supply the roles, ownership checks, and
other application decisions. The gate supplies ordered evaluation and the
missing-user and forbidden errors.

## Identity Boundaries

Configure [credential resolution](../authentication/overview.md#resolve-credentials)
before using the gate. A rule receives the application user that authentication
attached to the identity's record. Rules must accept the exact type that
authentication stored: `User` and `*User` are different types. A mismatch, an
identity without a user, or no identity produces `auth.ErrUnauthenticated`
(`401`), even if credentials were valid for the other type. A rule over
`auth.Identity` instead receives the identity itself and needs no application
user type. The gate does not resolve credentials itself.

The examples use standard-library `context`, `auth` from
`arkoren.dev/core/auth`, and `gate` from
`arkoren.dev/core/auth/gate`. Assume your authenticator attaches this
application-owned user to the identity:

```go
type User struct {
	ID         int
	CustomerID int
	CanExport  bool
}
```

These fields come from your trusted identity loader, not request form fields.
Focused snippets omit routine error propagation, but retain checks that prevent
unauthorized work.

## Route Admission

Use a route rule for an ability that does not depend on a particular record.
For example, an export entitlement can admit users to the report-export route:

```go
func canExport(_ context.Context, user User) (bool, error) {
	return user.CanExport, nil
}
```

With your authenticated router `r` and application handler `exportReport`, apply
the rule before the handler runs:

```go
r.With(gate.Require(canExport)).Post("/reports/{id}/export", exportReport)
```

No identity, or one without a `User`, produces `401`. A rule returning `false, nil` produces
`gate.ErrForbidden` (`403`); a non-nil error is returned unchanged, even if the
boolean is `true`. An infrastructure failure therefore does not become either
permission or an ordinary denial. [Handling Denials](denials.md) explains how
these errors render.

If you pass several rules, every rule must allow. They run in argument order,
stopping at the first denial or error; put inexpensive checks before external
lookups. Express alternatives inside a rule with ordinary Go conditions.
`gate.Require` panics with no rules. For sign-in alone, use
`auth.Require()` instead.

## Resource Checks

Passing `canExport` does not authorize every report. Restrict which records the
caller can load through a [scoped store lookup](row-access.md), then check any
additional action-specific permission against the stored record. Never build an
ownership decision from a request-supplied `CustomerID` or `OwnerID`.

For reports whose existence is already visible to the caller, an owner-only
export rule can close over the report returned by your store:

```go
type Report struct {
	ID         int
	CustomerID int
	OwnerID    int
}

func canExportReport(report Report) func(context.Context, User) (bool, error) {
	return func(_ context.Context, user User) (bool, error) {
		return report.CustomerID == user.CustomerID && report.OwnerID == user.ID, nil
	}
}
```

In the handler, with `report` already loaded through that scoped lookup, stop
before exporting if the resource check fails:

```go
if err := gate.Authorize(r.Context(), canExport, canExportReport(report)); err != nil {
	return err
}
```

Keep checks in the shared application operation when HTTP, tools, and jobs can
perform the same action. A transport-only gate cannot protect a direct call;
[MCP security](../mcp/securing-mcp.md) also separates tool availability from
resource permission. A check followed by a database write can race with another
request; use [conditional writes and locking](row-access.md#conditional-writes)
when permission or state can change between them.

## Deferred Work

A queued export might run after membership or permissions change. Load the
current user and the report through your authorized store again when the job
runs, rather than trusting the queued permission snapshot. With those trusted
`user` and `report` values, attach the actor to the job context and reuse the
rules:

```go
actor := auth.NewIdentity(strconv.Itoa(user.ID), func(principal *auth.IdentityBuilder) {
	principal.Record(auth.UserRecord{Subject: strconv.Itoa(user.ID), Value: user})
})
ctx = auth.NewContext(ctx, actor)
if err := gate.Authorize(ctx, canExport, canExportReport(report)); err != nil {
	return err
}
```

`auth.NewContext` attaches an identity; it does not authenticate it. The job
owns resolving the queued actor reference to a current identity. Do not attach
an arbitrary user decoded from the job payload and treat it as trusted.

As related checks grow, [Writing Policies](policies.md) keeps them together
without introducing a separate policy language. For all functions and options,
see the [`gate` API reference](https://pkg.go.dev/arkoren.dev/core/auth/gate).
