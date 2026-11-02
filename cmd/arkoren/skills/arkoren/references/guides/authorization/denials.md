---
title: Handling Denials
nav: Denials
description: Return accurate authorization responses and observe rule denials without collecting private request data.
summary:
  - Choose responses that distinguish missing identity, denied access, private absence, and state conflict.
  - Derive safe forbidden messages while preserving the stable problem identity.
  - Observe rule denials with configured gate instances and privacy-conscious listeners.
order: 40
---

# Handling Denials

A caller who lacks permission needs a different response from a caller whose
permission could not be checked. Preserve that distinction when handling gate
errors: a denied rule means the application reached a decision; a failed lookup
means it did not. Both stop the protected action, but they require different
client behavior and operational investigation.

## Response Statuses

Choose the response from the authentication state, the permission decision, and
what the caller may learn about the resource:

| Situation | Response | Responsibility |
| --- | --- | --- |
| No identity of the rule's exact type is present | `401 Unauthorized` | The gate returns `auth.ErrUnauthenticated`. |
| A known actor lacks permission and may know the resource exists | `403 Forbidden` | A rule returning `false, nil` produces `gate.ErrForbidden`. |
| The record is absent or outside the caller's visibility boundary | `404 Not Found` | Your handler maps scoped `database.ErrNoRows` to `problem.NotFound()`. |
| An authorized action violates current state, such as removing the last team owner | `409 Conflict` | Your application returns a conflict problem. |
| A database or service fails while checking permission | Normal error handling | The gate returns the rule's non-nil error unchanged. |

A public report can return `403` when a reader cannot edit it. A private invoice
outside the caller's [query scope](row-access.md#private-absence) should return
the same `404` as a missing invoice. The gate does not automatically turn
`ErrNoRows` into `404`; that translation belongs at your resource boundary.

Do not convert every authorization error to `403`, or treat every failed
database query as missing membership. An ordinary error uses the router's normal
error rendering, typically `500`; a rule returning a problem retains that
problem's status. See [HTTP errors](../http-fundamentals/error-handling.md) for
the rendering boundary. Invalid credentials belong to authentication, while an
unexpected `401` with valid credentials can indicate a `User`/`*User` mismatch
between authentication and the rule.

## Forbidden Details

The shared `gate.ErrForbidden` problem uses this stable identity and default
message:

```json
{
  "type": "tag:arkoren.dev,2026-09:core:http/auth/gate:forbidden",
  "title": "Forbidden",
  "status": 403,
  "detail": "You do not have permission to perform this action."
}
```

Clients can use the `type` rather than parsing prose. The default response does
not disclose which rule denied access. HTTP rendering can add `instance` from
the request path; this response is not a sanitized diagnostic record.

If the caller may know why an action is unavailable, derive a more useful detail
without changing the problem identity. Assume `team` was loaded through your
authorized store and `policy.CanInvite(team)` is your application rule. Import
standard-library `errors`, `gate` from `arkoren.dev/core/http/auth/gate`, and
`problem` from `arkoren.dev/foundation/problem`:

```go
err := gate.Authorize(r.Context(), policy.CanInvite(team))
if errors.Is(err, gate.ErrForbidden) {
	return gate.ErrForbidden.Derive(func(details *problem.Builder) {
		details.Detail("Only team owners can invite members.")
	})
}
if err != nil {
	return err
}
```

`Derive` creates a new value; it does not mutate the shared problem. The derived
value still matches `gate.ErrForbidden` with `errors.Is`. Missing identity and
infrastructure errors pass through unchanged instead of acquiring a misleading
owner-only message.

Keep details about permitted actions, not private membership records, hidden
resource IDs, token contents, or internal service errors. For a resource the
caller may not discover, use the scoped `404` path instead of explaining a
resource-specific denial.

## Denial Events

The free functions `gate.Require` and `gate.Authorize` publish no events. If
you need denial observation, construct a `gate.Gate` with your existing
application event bus and use its methods consistently:

```go
access := gate.New(func(access *gate.Builder) {
	access.Events(app.Events())
})
```

With an authenticated router `r`, an application rule `canExport`, and handler
`exportReport`, protect the route using that instance:

```go
r.With(access.Require(canExport)).Post("/reports/{id}/export", exportReport)
```

The gate publishes `gate.Denied` (`http.gate.denied`) only when a rule returns
`false` without an error. It publishes nothing for allowed calls, missing
identities, or rule failures. Direct checks that need observation must call
`access.Authorize`, not the free `gate.Authorize`. Without an event bus, a gate
instance publishes nothing too.

The `gate.DeniedEvent` payload contains `Rule`, the zero-based position of the
first denying rule, and `Method`, the HTTP method for `Require`. `Authorize`
has no request, so its `Method` is empty. Later rules do not run. Rule positions
depend on the order at each call site; they are not globally unique policy IDs.

There is no resource path, identity, query string, or failure cause in the
payload. `DeniedEvent.Path` is not available; remove old path reads rather than
replacing them with raw request URLs. The event context still retains request
values, including the authenticated identity, and custom HTTP methods are
client-controlled. Select attributes deliberately; neither event contexts nor
problem `instance` values are safe to dump into diagnostics.

## Safe Observation

Register a listener on the same bus. This example uses standard-library
`context` and `log/slog`; it intentionally logs neither context values nor the
raw method:

```go
unsubscribe, err := app.Events().Listen(gate.Denied, func(_ context.Context, denied gate.DeniedEvent) error {
	slog.Warn("authorization denied", "rule", denied.Rule)

	return nil
})
```

Handle the registration error before relying on observation. The subscription's
owner retains `unsubscribe` and calls it when the subscription is no longer
needed; deferring it in a short-lived setup function would remove the listener
too early. If you record methods, map them to a fixed allowlist of standard
methods plus an `other` value rather than logging arbitrary client strings.

Every denial emits: there is no deduplication, and a client controls the rate.
Keep listeners inexpensive, bound log volume, and place
[rate limiting](../abuse-prevention/rate-limiting.md) before the gate. A denial
listener is not an audit of successful actions or failed permission lookups;
observe those separately under your application's privacy policy.
[Event auditing](../events/auditing.md) explains durable audit design.

For all functions, options, and event fields, see the
[`gate` API reference](https://pkg.go.dev/arkoren.dev/core/http/auth/gate).
