---
title: HTTP Events
description: Inspect HTTP publication timing, request metadata, and diagnostic privacy boundaries.
order: 32
summary:
  - Enable server, transport, middleware, session, link, and webhook notifications separately.
  - Interpret exact payload fields and the conditions that suppress publication.
  - Keep raw diagnostics and identifying request data out of unapproved outputs.
---

# HTTP Events

HTTP notifications help you distinguish a refused request, an exhausted outgoing
call, and a failure during response-time persistence. They observe local decisions;
they do not prove that a client received a response. Enable the relevant component
with the policy below and register on the same bus.

These notifications use synchronous, best-effort `Emit`. A slow listener delays
the operation; a listener error or panic is logged and skips later listeners
without changing the result. No bus means no publication. Review payloads and
context values before retaining them: selected schemas omit sensitive fields,
but raw errors and caller context values are not universally redacted.

## Topics

| Topic | Name | Enabling Policy |
| --- | --- | --- |
| `server.Listening` | `http.server.listening` | `server.Builder.Events` |
| `server.ShutdownTimedOut` | `http.server.shutdown.timed-out` | `server.Builder.Events` |
| `router.PanicRecovered` | `http.panic.recovered` | `router.RecoverBuilder.Events` |
| `client.RequestFailed` | `http.client.request.failed` | `client.Builder.Events` |
| `csrf.Blocked` | `http.csrf.blocked` | `csrf.Builder.Events` |
| `ratelimit.Exceeded` | `http.ratelimit.exceeded` | `ratelimit.Builder.Events` |
| `contenttype.Rejected` | `http.contenttype.rejected` | `contenttype.Builder.Events` |
| `session.Expired` | `session.expired` | `session.Builder.Events` |
| `session.Destroyed` | `session.destroyed` | `session.Builder.Events` |
| `session.PersistFailed` | `session.persist.failed` | `session.Builder.Events` |
| `link.Rejected` | `link.rejected` | `link.Builder.Events` on the signer |
| `link.Consumed` | `link.consumed` | `link.Builder.Events` on the signer |
| `webhook.Rejected` | `http.webhook.rejected` | `webhook.Builder.Events` |
| `webhook.Failed` | `http.webhook.failed` | `webhook.Builder.Events` |
| `webhook.Ignored` | `http.webhook.ignored` | `webhook.Builder.Events` |

Authentication, gate, and OAuth notifications have their own
[catalog entries](built-in-events.md#authentication-and-authorization).

## Server Lifecycle

`ListeningEvent{Addr}` fires when `Start` binds its listener. With port zero,
`Addr` includes the chosen port. It is a bound address, not a public URL or proof
that serving or TLS negotiation has begun.

`ShutdownTimedOutEvent{Addr, Timeout}` fires only when `Serve` exceeds its native
HTTP drain budget and force-closes tracked connections. `Timeout` is the
configured budget, not total elapsed time. A clean drain, another shutdown
failure, and `Stop` publish nothing. Handler work can still remain, and hijacked
connections are outside native draining: their owners must close them and await
associated work. See [server shutdown](../http-networking/server.md#shutting-down).

## Recovered Panics

`PanicRecoveredEvent{Method, Pattern, Err, Stack}` fires when `Recover` converts
a handler panic into an error. `Pattern` is the registered route pattern,
including its method when present, or empty if no route matched. It is not the
expanded path. `http.ErrAbortHandler` is re-raised and publishes nothing.

`Err`, its causes, and the goroutine `Stack` are sensitive diagnostics. A monitoring
listener can avoid them and select only registered route metadata. With `context`
and `arkoren.dev/core/http/router` imported, and your existing bus and logger:

```go
_, err := bus.Listen(router.PanicRecovered,
	func(ctx context.Context, notice router.PanicRecoveredEvent) error {
		logger.ErrorContext(ctx, "handler panic", "route", notice.Pattern)

		return nil
	})
```

Keep secrets out of route literals too. Send full diagnostics only to approved,
restricted sinks, never directly to clients. The event stack is detached from
the returned error's diagnostic storage, and that error's `Stack()` accessor
returns an owned copy. Listeners still share the event's slice: copy before
mutation. Ownership copies are not redaction.

The event does not promise a replacement response after bytes are committed.
See [panic handling](../http-fundamentals/error-handling.md#panics),
[response commitment](../http-fundamentals/responses.md#response-commitment), and
[streaming responses](../http-fundamentals/streaming-responses.md).

## Outgoing Requests

`RequestFailedEvent{Method, Host, Attempts, StatusCode, Err}` describes the final
outcome of one transport round trip. It fires for a returned transport error,
or a response `RetryIf` would retry after attempts run out or `Retry-After`
exceeds the allowed delay. Arbitrary 4xx/5xx responses do not automatically qualify.

`Attempts` includes the initial attempt; redirects have separate round trips.
`Host` is the request URL's hostname without port, path, or query. Exactly one
of nonnil `Err` and nonzero `StatusCode` describes the outcome. Native transport
errors retain causes and can contain sensitive text.

If the parent request context is done, publication is suppressed. A per-attempt
timeout can still publish while the parent context remains active. Later response
body-read failures are not transport outcome events. Retry and body ownership
belong in [HTTP Client](../http-networking/http-client.md).

## Request Rejections

`csrf.BlockedEvent{Method, Path, Origin, Err}` observes rejection by the configured
cross-origin policy. `Path` is the request path without query, not a route template.
`Origin` is the untrusted header, empty when absent; `Err` is the native diagnostic.
Paths can contain identifiers, and an origin is not an authenticated identity.
See [CSRF Protection](../browser-security/csrf-protection.md).

`ratelimit.ExceededEvent{Key, Method, Path, Limit, Window}` describes a denied
request. `Key` is the client part of the counter key—client IP by default—not
the internal namespace/window suffix. `Path` is the decoded request path without
query. `Limit` and `Window` contain normalized positive policy values; `Window`
is the sliding-window duration, not total counter retention.

A cache marker normally limits publication to the first denial per key and
fixed window. If marker insertion fails, the limiter still publishes and later
denials can duplicate it. This is best-effort deduplication, not exactly-once
notification. The marker failure does not allow the denied request. Treat keys
and paths as potentially identifying data; see
[Rate Limiting](../abuse-prevention/rate-limiting.md).

`contenttype.RejectedEvent{Method, Path, MediaType}` fires on every content-type
rejection, including with a custom response status. `MediaType` is the lower-case
base type without parameters; it is empty for missing or unparseable base types.
Malformed parameters can still leave a parsed base type. Paths and media types
are client-controlled. Keep these callbacks cheap and rate-limit hostile request
traffic before it reaches them. See [Content Type](../http-fundamentals/content-type.md).

## Session Changes

`ExpiredEvent{CreatedAt, ExpiresAt}` fires when middleware encounters an expired
session, including maximum-lifetime expiry, and replaces it. This happens on a
request after expiry, not on a timer. No session identifier or data is included.

`DestroyedEvent{Method, Path}` fires after middleware successfully writes the
tombstone for a session ended with `Session.Destroy`, before expiring its cookie.
If retirement fails, it publishes `PersistFailed` instead: the old identifier
can still be usable. Expiry is a different event; regeneration alone publishes
no destruction event. Persistence runs at response-header writing and is skipped
for server-error responses.

`PersistFailedEvent{Operation, Method, Path}` describes the failed persistence step:
`session.PersistSave` (`save`) writes the record, `session.PersistVerify` (`verify`)
checks the original record is live, and `session.PersistRetire` (`retire`) writes
a tombstone. The payload omits the storage error. `session.Builder.ErrorHandler`
still receives that diagnostic with its cause; sanitize it separately.

For destruction and persistence failure, `Path` is the registered route pattern
with its method prefix removed, or empty without a pattern. Expanded parameters
and queries never replace it; host-specific patterns retain their host prefix.
Avoid secrets in route literals.

Destruction uses a persistence context detached from request cancellation, with
the persistence timeout. Failure events use a context detached from cancellation
and deadlines, so an already-exhausted persistence budget does not cancel them.
This does not forcibly bound arbitrary listeners or callbacks. Best-effort
lease cleanup has a separate ten-second deadline and requires backend context
cooperation. Session persistence and cookie behavior belong in
[Sessions](../http-capabilities/sessions.md).

## Signed Links

`link.RejectedEvent{Method}` observes middleware rejection of invalid link form
or signature, including binding failure. Expiry of a valid link publishes no
rejection. The payload contains no path, URL, binding, or error; binding causes
remain in the middleware's returned problem for `errors.Is`/`errors.As`.

`link.ConsumedEvent{Method}` fires only after `Consume`, `ConsumeURL`, or
`ConsumeRequest` records single use. The direct URL operations leave `Method`
empty. Failed consumption, including an already-spent link, publishes nothing.
`Verify`, `Inspect`, and middleware do not consume and publish no consumption event.

Methods are client-controlled, and contexts and returned errors remain outside
payload redaction. See [Secure Links](../http-capabilities/secure-links.md).

## Webhook Receipts

Webhook notifications describe locally selected receipt outcomes:

| Topic | Payload | Outcome |
| --- | --- | --- |
| `webhook.Rejected` | `RejectedEvent{Label, Reason, Status}` | Selected 4xx rejection, including native 413 body-limit handling. |
| `webhook.Failed` | `FailedEvent{Label, Stage}` | Processing failure answered with 503. |
| `webhook.Ignored` | `IgnoredEvent{Label, Reason}` | Authenticated, unbound type acknowledged with 200. |

`RejectedEvent.Reason` is `webhook.RejectionReason`: compare
`RejectionReasonBodyTooLarge` (`body_too_large`), `RejectionReasonUnreadableBody`
(`unreadable_body`), or `RejectionReasonVerify` (`verify`). Interrupted body reads
from cancellation/timeouts are failures, not malformed-body rejections.

`FailedEvent.Stage` is `webhook.FailureStage`: `FailureStageRead` (`read`),
`FailureStageVerify` (`verify`), `FailureStageDecode` (`decode`), or
`FailureStageAccept` (`accept`). Decoder and `Event.Map` errors share decode;
every acceptance error, including `ErrSkip`, is an accept failure. Acceptance
can already have committed work, so this classification does not imply rollback
or safe replay.

`IgnoredEvent.Reason` is `webhook.IgnoreReason`, currently only
`IgnoreReasonUnbound` (`unbound`). Authenticated decoder/map skips and duplicates
publish none of these topics. Empty zero categories remain unspecified (`""`),
not defaults. Compare typed constants; convert explicitly for string-only APIs.

Payloads omit causes, bodies, headers, provider IDs, and event types. `Label`
remains application-controlled and unvalidated; keep it free of credentials and
personal data. Context values are not sanitized. Failure responses are committed
locally, including native 413 before authentication; provider retry behavior is
separate. See [Receiving Webhooks](../webhooks/receiving-webhooks.md),
[receiver diagnostics](../webhooks/delivery-policies.md#receiver-diagnostics), and
[acknowledgement outcomes](../webhooks/delivery-policies.md#acknowledgement-outcomes).

For declarations, see the API references for
[`server`](https://pkg.go.dev/arkoren.dev/core/http/server),
[`router`](https://pkg.go.dev/arkoren.dev/core/http/router),
[`client`](https://pkg.go.dev/arkoren.dev/core/http/client),
[`csrf`](https://pkg.go.dev/arkoren.dev/core/http/middleware/csrf),
[`ratelimit`](https://pkg.go.dev/arkoren.dev/core/http/middleware/ratelimit),
[`contenttype`](https://pkg.go.dev/arkoren.dev/core/http/middleware/contenttype),
[`session`](https://pkg.go.dev/arkoren.dev/core/http/session),
[`link`](https://pkg.go.dev/arkoren.dev/core/http/link), and
[`webhook`](https://pkg.go.dev/arkoren.dev/core/http/webhook).
