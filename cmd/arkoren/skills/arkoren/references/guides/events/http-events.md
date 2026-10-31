---
title: HTTP Events
description: Observe HTTP server, client, middleware, session, signed-link, and webhook outcomes with their publishing conditions and payload limits.
order: 32
---

# HTTP Events

## Introduction

To observe a server starting, a failed outgoing request, or a rejected incoming
request, find its topic below and give the component an event bus through the
listed builder setting. Omitted or nil buses publish nothing. Authentication
and authorization topics remain in
[Built-in Events](built-in-events.md#authentication-and-authorization).
Their rejection and denial payloads have no `Path` or cause fields:
`auth.RejectedEvent` carries `Method`, and `gate.DeniedEvent` carries `Method`
and the zero-based `Rule` index.

Notifications are synchronous, process-local, and best effort. Slow listeners
delay the operation; listener errors or panics are logged without changing its
result and stop delivery to the remaining listeners. A crash can lose an event,
so required audit records need an application-owned durable boundary. Some
payloads carry raw errors, request paths, or a panic stack: review and sanitize
what you forward to logs or external services. See
[how services publish](built-in-events.md#how-services-publish) for shared
delivery and privacy rules, and [Writing Listeners](listeners.md) for setup.
Reduced payloads do not sanitize event contexts or returned error chains, which
can still retain caller-provided values and sensitive diagnostics.

## Topics

These events come from the request path. Each is enabled by the option shown.

| Topic | Name | Option | Fires |
| --- | --- | --- | --- |
| `server.Listening` | `http.server.listening` | `server.Builder.Events` | `Start` bound the listener |
| `server.ShutdownTimedOut` | `http.server.shutdown.timed-out` | `server.Builder.Events` | `Serve` force-closed tracked HTTP connections after its drain deadline |
| `router.PanicRecovered` | `http.panic.recovered` | `router.RecoverBuilder.Events` | `Recover` converted a handler panic into an error |
| `client.RequestFailed` | `http.client.request.failed` | `client.Builder.Events` | the HTTP client's transport gave up on a request |
| `csrf.Blocked` | `http.csrf.blocked` | `csrf.Builder.Events` | the CSRF middleware rejected a cross-origin request |
| `ratelimit.Exceeded` | `http.ratelimit.exceeded` | `ratelimit.Builder.Events` | the first denied request of a key in each window |
| `contenttype.Rejected` | `http.contenttype.rejected` | `contenttype.Builder.Events` | the content type middleware rejected a request, regardless of its configured response status |
| `session.Expired` | `session.expired` | `session.Builder.Events` | the session middleware replaced an expired session |
| `session.Destroyed` | `session.destroyed` | `session.Builder.Events` | the session middleware retired a session the handler destroyed |
| `session.PersistFailed` | `session.persist.failed` | `session.Builder.Events` | the session middleware could not save the handler's changes |
| `link.Rejected` | `link.rejected` | `link.Builder.Events` on the signer | the link middleware rejected an invalid link |
| `link.Consumed` | `link.consumed` | `link.Builder.Events` on the signer | the signer spent a one-time link |

`ListeningEvent` carries the bound `Addr`, including the chosen port when you
configured port zero, which makes this the event to read in tests that start a
server on a random port. `ShutdownTimedOutEvent` carries the same `Addr` and the
configured drain budget as `Timeout`. Only `Serve` publishes it, when native HTTP
connections have not drained before its deadline and the remaining tracked
connections are force-closed. This does not guarantee that arbitrary handler work
has finished. Hijacked connections are neither closed nor awaited by the server;
their owners must close them and wait for associated work. A shutdown that drains
in time publishes nothing, and neither does `Stop`, whose deadline belongs to its
caller. See [Server](../http-networking/server.md#shutting-down).

## Recovered panics

`PanicRecoveredEvent` carries the request `Method`, the matched route `Pattern`
(empty when no route matched), the `Err` the panic became, and the goroutine
`Stack`. `Err`, its causes, and `Stack` intentionally retain diagnostic data that
can be sensitive. Restrict and sanitize output before sending it to an approved
diagnostic service; do not forward the whole payload to clients.

The event's stack bytes are detached from the recovered error's storage, and
that error's `Stack()` accessor returns an owned copy. Listeners still share the
event's `Stack` slice, so copy it before mutation. These copies protect ownership,
not privacy. `http.ErrAbortHandler` is re-raised and publishes nothing. See
[Error Handling](../http-fundamentals/error-handling.md).

## Outgoing requests

`RequestFailedEvent` carries the `Method`, the destination `Host` without port,
path, or query, the number of `Attempts`, and either the transport `Err` or the
`StatusCode` of the final response when retries ran out or `Retry-After` asked
for too long. See [HTTP Client](../http-networking/http-client.md).

## Request rejections

`BlockedEvent` carries the `Method`, the `Path` without the query, the `Origin`
header (empty if the request had none), and the reason as `Err`. A steady
trickle usually means a misconfigured trusted origin. See
[CSRF Protection](../browser-security/csrf-protection.md).

`ExceededEvent` carries the `Key` (the client IP unless you set a key function),
the `Method`, the `Path`, and the configured `Limit` and `Window`. Only the first
denial of a key in each fixed window publishes, so a client hammering a limit
produces one event per window rather than one per rejected request. See
[Rate Limiting](../abuse-prevention/rate-limiting.md).

`contenttype.RejectedEvent` carries the `Method`, the `Path` without the query,
and the lower-case base `MediaType` without parameters. `MediaType` is empty when
the header is missing or its base type cannot be parsed. If only its parameters
are malformed, a parsed base type can still be reported even though the request
is rejected. The client chooses `MediaType`, so treat it as untrusted text.
Every content-type rejection publishes, including when
`contenttype.Builder.ErrorResponse` changes the default 415 response to another
status, as described in
[How services publish](built-in-events.md#how-services-publish). See
[Content Type](../http-fundamentals/content-type.md).

## Session changes

`ExpiredEvent` carries the `CreatedAt` and `ExpiresAt` times and nothing else,
never the session ID or data. It fires on the first request after the expiry,
not at the moment of expiry. A session the handler ends with `Session.Destroy`
publishes `session.destroyed` instead, with the request `Method` and registered
route `Path`, once the middleware has written the tombstone that retires the old
ID and before it expires the cookie. Regenerating a session publishes nothing.

For both `DestroyedEvent` and `PersistFailedEvent`, `Path` is the registered
route pattern with its method prefix removed, or empty if no pattern is available.
It never falls back to the expanded request path or query. A host-specific pattern
retains its registered host prefix. Keep secrets out of registered route literals.

`PersistFailedEvent` carries the `Method`, the `Path`, and the `Operation` that
failed: `session.PersistSave` (`"save"`, writing the record),
`session.PersistVerify` (`"verify"`, checking that the original record is still
live), or `session.PersistRetire` (`"retire"`, writing the tombstone). A failed
tombstone publishes this event instead of `session.destroyed`, because the old
ID may still be usable. The event fires in addition to
`session.Builder.ErrorHandler`, which intentionally receives the diagnostic error,
including its cause. The payload omits that error because its text can name the
backend or the session key; sanitize callback output before forwarding it.
`Destroyed` and `PersistFailed` are published with a context that is not canceled
with the request.

Best-effort session lease cleanup has its own ten-second deadline, separate from
persistence cancellation. Honoring that deadline requires backend context
cooperation; it does not bound arbitrary callback or listener execution. See
[Sessions](../http-capabilities/sessions.md).

## Signed links

`link.RejectedEvent` and `link.ConsumedEvent` carry only `Method`. Neither has a
`Path` field, and `RejectedEvent` has no `Err`: resource paths, full URLs, bindings,
and verification or binding causes are not notification payloads. A binding
callback failure remains in the middleware's returned problem for `errors.Is`
and `errors.As`; it is not copied into the event. Returned errors and event
contexts still need a privacy review before logging.

Expired links publish no rejection event: expiry is an ordinary outcome of a
valid link. `ConsumedEvent.Method` is empty for `Consume` and `ConsumeURL`, which
have no request. Consumption publishes only after `Consume`, `ConsumeURL`, or
`ConsumeRequest` records the use. A failed call, including one that finds the link
already spent, publishes nothing, and so do `Verify`, `Inspect`, and the middleware,
which never consume a link. See [Secure Links](../http-capabilities/secure-links.md).

## Webhook receipts

With `webhook.Builder.Events`, receivers publish `webhook.Rejected`
(`http.webhook.rejected`, `RejectedEvent{Label, Reason, Status}`) for 4xx,
`webhook.Failed` (`http.webhook.failed`, `FailedEvent{Label, Stage}`) for 503,
and `webhook.Ignored` (`http.webhook.ignored`, `IgnoredEvent{Label, Reason}`)
for authenticated, unbound deliveries acknowledged with 200. Skips and duplicates
publish none of these events.

The category fields use defined string types:

- `RejectedEvent.Reason` is `webhook.RejectionReason`:
  `RejectionReasonBodyTooLarge` (`"body_too_large"`),
  `RejectionReasonUnreadableBody` (`"unreadable_body"`), or
  `RejectionReasonVerify` (`"verify"`).
- `FailedEvent.Stage` is `webhook.FailureStage`: `FailureStageRead` (`"read"`),
  `FailureStageVerify` (`"verify"`), `FailureStageDecode` (`"decode"`), or
  `FailureStageAccept` (`"accept"`). Decoder and `Event.Map` conversion errors
  share the decode stage; every acceptance error, including `ErrSkip`, is an
  accept failure.
- `IgnoredEvent.Reason` is `webhook.IgnoreReason`, with `IgnoreReasonUnbound`
  (`"unbound"`) for a type with no binding.

The constants live in `webhook`. Their string/JSON values and JSON member names
are unchanged; empty zero values remain `""` (unspecified), with no default.
Compare with constants and convert for string-only APIs, as in
`string(failure.Stage)`. Provider event keys and application labels remain
strings; neither is one of these finite categories.

Payloads omit causes, bodies, headers, provider IDs, and event types. Labels are
unvalidated application values, and listener contexts retain request values;
neither is sanitized by those omissions. The receiver commits failure responses
locally, including the native 413 body-limit problem before authentication.
See [Receiving Webhooks](../http-capabilities/webhooks.md#reacting-to-rejections-and-failures)
for category migration and listener examples, and
[Provider Webhooks](../http-capabilities/provider-webhooks.md) for verification
and delivery-identity boundaries.

For every function and setting, see the API references for
[`event`](https://pkg.go.dev/arkoren.dev/core/event), [`server`](https://pkg.go.dev/arkoren.dev/core/http/server),
[`router`](https://pkg.go.dev/arkoren.dev/core/http/router), [`client`](https://pkg.go.dev/arkoren.dev/core/http/client),
[`csrf`](https://pkg.go.dev/arkoren.dev/core/http/middleware/csrf), [`ratelimit`](https://pkg.go.dev/arkoren.dev/core/http/middleware/ratelimit),
[`contenttype`](https://pkg.go.dev/arkoren.dev/core/http/middleware/contenttype), [`session`](https://pkg.go.dev/arkoren.dev/core/http/session),
[`link`](https://pkg.go.dev/arkoren.dev/core/http/link), and [`webhook`](https://pkg.go.dev/arkoren.dev/core/http/webhook).
