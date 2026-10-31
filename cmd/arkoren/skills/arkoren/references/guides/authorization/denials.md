---
title: Handling Denials
nav: Denials
description: Pick the right status for a denied or failed action, shape the 403 response, and log or count denials with the gate's events.
order: 40
---

# Handling Denials

## Introduction

A denial is part of an API's contract. The status code tells a client whether
to sign in, give up, or try again later, and the body tells a person what went
wrong. A denial is also a signal for operators: a burst of them can mean a
broken client, a misconfigured role, or someone probing for data.

This page covers the four responses that denied or failed actions use, the
response the gate sends, how to change it, and how to observe denials with
events.

## Choosing the status code

Four responses cover the cases of a denied or failed action, and choosing
correctly tells clients what to do next:

- `401 Unauthorized`: there is no signed-in user, or the credentials are
  invalid. The client should sign in. The gate returns this on its own.
- `403 Forbidden`: the user is known and may not do this, and it is fine for
  them to know the resource exists. A rule that returns `false` produces this.
- `404 Not Found`: the user must not learn that the resource exists. A scoped
  query that finds no row produces this, as described in
  [Row-Level Access](row-access.md#answering-with-404).
- `409 Conflict`: the action is allowed, but the current state forbids it, such
  as removing the last owner of a team.

The line between `403` and `404` is about what the user may know. Others'
invoices are private, so a stranger gets `404`. A post that everyone can read
but only its author can edit gets `403` on edit, because its existence is no
secret.

## The forbidden response

A rule that returns `false` makes the gate return `gate.ErrForbidden`, a
[Problem Details](../http-fundamentals/error-handling.md) value that renders as:

```json
{
  "type": "tag:arkoren.dev,2026-09:core:http/auth/gate:forbidden",
  "title": "Forbidden",
  "status": 403,
  "detail": "You do not have permission to perform this action."
}
```

The response deliberately does not say which rule denied, which keeps the
names of your rules and the shape of your policies private. Clients match it
by its `type`, which never changes. The example shows the shared problem members;
HTTP rendering can also add `instance` from the request path. That member is not
redacted by the event-payload privacy rules below.

### A more helpful message

Where a person benefits from knowing why, for example in a form that invites
team members, check for the forbidden error in the handler and return it with
your own detail:

Import `gate` from `arkoren.dev/core/http/auth/gate`, `problem` from
`arkoren.dev/foundation/problem`, and `errors` from the standard library.
Here `h.policy` is your application policy and `team` is already loaded.

```go
err := gate.Authorize(r.Context(), h.policy.CanInvite(team))
if errors.Is(err, gate.ErrForbidden) {
	return gate.ErrForbidden.Derive(func(details *problem.Builder) {
		details.Detail("Only team owners can invite members.")
	})
}
if err != nil {
	return err
}
```

`Derive` returns a copy, so the shared `gate.ErrForbidden` is not changed,
and the result still matches `gate.ErrForbidden` with `errors.Is`, so
middleware and tests that look for a forbidden error keep working. Keep the
detail about what the user can do, not about how the check works.

## Observing denials

The functions `gate.Require` and `gate.Authorize` have no event bus, so they
publish nothing. To log or count denials, build a `gate.Gate` once at startup
with `gate.New` and the bus, and call its `Require` and `Authorize` methods,
which take the same rules and return the same errors:

```go
access := gate.New(func(access *gate.Builder) {
	access.Events(app.Events())
})

r.With(access.Require(isAdmin)).Delete("/users/{id}", deleteUser)
```

When a rule returns `false`, the gate publishes `http.gate.denied` with `Method`
and `Rule`, the zero-based index of the first denying rule in the order you
passed them. Later rules do not run. A denial from `access.Authorize` has an empty
`Method`, because that call has no request; use `access.Require` when you need
HTTP method metadata. A rule error or missing user publishes nothing.

`gate.DeniedEvent.Path` has been removed: paths can contain resource identifiers
or credentials. Remove `Path` reads from existing listeners rather than replacing
them with raw URLs from the context. The payload identifies the denying rule,
not the user or resource.

Event contexts still retain request values, including the authenticated user.
If your audit needs personal identifiers, select them explicitly under your
application's privacy policy. `Method` is also client-controlled, especially for
custom HTTP methods; classify or filter such values before logging when needed.
Neither the context nor an HTTP problem's `instance` is a general-purpose
sanitized record.

A listener can record the method and denying rule without collecting paths:

Use standard-library `context` and `log/slog`; `app` is your existing
application with an event bus.

```go
unsubscribe, err := app.Events().Listen(gate.Denied, func(ctx context.Context, denied gate.DeniedEvent) error {
	slog.WarnContext(ctx, "access denied", "method", denied.Method, "rule", denied.Rule)

	return nil
})
```

Handle the registration error and retain `unsubscribe`; call it when the owner
of this subscription stops. Do not dump request context values into the log.

Every denial publishes, so a client decides how many events you receive. Keep
the listener cheap, such as a counter or a log line, and put the
[rate limiting middleware](../abuse-prevention/rate-limiting.md) in front of the
gate. For a durable record of who was turned away and who did what, see
[Auditing with Events](../events/auditing.md).

For every function, see the [`gate` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth/gate).
