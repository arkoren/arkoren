# Receiving webhooks

Receive authenticated provider notifications, bind their decoded payloads to an
application action, and acknowledge acceptance without assuming provider retry
guarantees. The [package documentation](https://pkg.go.dev/arkoren.dev/core/http/webhook)
contains the API contracts and provider-adapter examples.

## Bind an application action

A provider adapter validates its credentials and supplies a `Source` that
authenticates each request. It publishes credential-free `Event` definitions
separately, so the same definition works with several accounts. An `Event` key
is the provider's opaque identifier, not an Arkoren topic or job name.

Given a configured `source`, a provider's `InvoicePaid` event, an application
`acceptInvoice` function, and router `r`, bind the action at startup. Here the
provider's envelope type is `Envelope`; the imported package is
`arkoren.dev/core/http/webhook`:

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[Envelope]) {
	receiver.Handle(InvoicePaid, acceptInvoice)
	receiver.MaxBodySize(256 << 10)
})
r.Post("/webhooks/billing", handler)
```

The receiver reads the limited raw body before authentication, selects the exact
provider key, decodes the envelope, and runs acceptance. Construction rejects
invalid sources, bindings and settings; at least one event must be bound.
Callbacks run once in order on one draft, even for invalid input. Nil callbacks
panic; an expanded nil or empty callback slice supplies none.

The omitted or zero body limit is 1 MiB; negative limits are invalid. An outer
body-limit middleware remains a ceiling. Bindings and rejection containers are
detached from retained drafts, but services, functions, error values and captures
remain shared. Make those dependencies safe for concurrent requests and manage
their lifecycle separately. There is no event bus or deduplication by default.

## Acknowledge only the intended outcome

- Successful acceptance answers exactly 200 with an empty body. Returning nil
  for an acceptance no-op also records the ID when deduplication is enabled.
- `ErrSkip` from the source or decoder acknowledges an **authenticated** delivery
  before acceptance without recording its ID. Never use it before authentication.
- Every acceptance error, including `ErrSkip` and permanent queue errors, answers
  503. Work may already have committed; there is no automatic rollback.
- Authenticated unbound types and previously handled IDs answer 200 without
  acceptance. Only unbound types publish `Ignored`.
- Oversized bodies retain the native 413 body-limit problem. Other malformed
  body reads answer 400; interrupted or timed-out reads answer 503.
- Verifier errors matching `Reject` mappings select the first matching 4xx.
  Otherwise `ErrRejected` selects 400 and unclassified errors select 503.
  Decoder errors other than `ErrSkip` also answer 503.

The receiver commits failure responses itself, so an outer error mapper cannot
replace the provider's response. Returned receipt errors still match the class
and original cause with `errors.Is`/`errors.As`. Redact causes before logging;
event-payload privacy does not sanitize error chains, request context values or
normal HTTP problem instances. A 503 permits a retry; the provider decides
whether to send one.

## Tolerate repeated delivery

Handlers must be idempotent. Optional `Deduplicate` needs a stable explicit
`cache.Namespace[bool]` and a stable provider event ID, not an attempt ID. Empty
IDs disable suppression. The namespace TTL controls retention; zero means no
expiry. Cache read errors fail open, concurrent copies can both pass, and marker
write failures are ignored. Markers are written after successful acceptance,
before the 200, without a transaction with application work.

Namespace names and separators are literal; only IDs are base64url encoded.
Changing either forgets existing receipts and permits replays. Preserve old
dot-joined receipts with explicit `Separator(".")`; colon is the current default.
Choose final keys that cannot collide with unrelated receivers sharing storage.
Neither routes nor diagnostic labels are part of the receipt identity.

For slow work, bind a queue job with `Dispatch`. Acceptance is only as durable as
the chosen queue driver. Use `Event.Map` to convert a decoded provider payload
into an application-owned queue data transfer object: it preserves the provider
key, delivery ID and type, runs the original decoder first, and retains the first
definition error. Conversion must be pure, concurrency-safe and perform no I/O.
Conversion errors follow decoder policy, not acceptance policy. The stable
`Delivery` JSON members remain `id`, `type` and `payload`; avoid vendor SDK types
whose shape could change while messages remain queued.

## Observe categories without collecting request data

Configure `Events(bus)` and an optional short, application-chosen `Label` to
observe receipts. Labels are not validated or redacted; keep secrets, personal
identifiers and provider-controlled values out of them. Payloads omit bodies,
headers, provider IDs, provider types and causes. Listener contexts still retain
request context values; select safe attributes explicitly.

`RejectedEvent.Reason`, `FailedEvent.Stage` and `IgnoredEvent.Reason` now use
`RejectionReason`, `FailureStage` and `IgnoreReason`. Their explicit values retain
the previous bytes: `body_too_large`, `unreadable_body`, `verify`; `read`, `verify`,
`decode`, `accept`; and `unbound`, respectively. Empty zero values stay `""`
(unspecified), not a new default. No JSON member names change.

Prefer typed constants for comparisons. Convert explicitly when an existing API
requires a plain string; string variables also need conversion when constructing
these fields. Untyped literals and JSON string representations remain usable.
With an existing event `bus` and `logger`, register a listener and retain its
unsubscribe function until its owner stops:

```go
unsubscribe, err := bus.Listen(webhook.Failed, func(ctx context.Context, failed webhook.FailedEvent) error {
	if failed.Stage == webhook.FailureStageAccept {
		logger.ErrorContext(ctx, "webhook acceptance failed", "label", failed.Label, "stage", string(failed.Stage))
	}
	return nil
})
defer unsubscribe()
```

This listener observes an already selected receipt; its return value cannot
change the HTTP result. These categories do not make `Delivery.Type`, `Event.Key`
or `Label` enums: those identifiers stay strings under their existing owners.
