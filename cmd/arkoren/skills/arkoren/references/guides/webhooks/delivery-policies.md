---
title: Delivery Policies
description: Choose receipt retention, queued acceptance, failure responses, and operating limits for a webhook receiver.
order: 30
summary:
  - Reduce duplicate handling with explicit receipt identities and retention without assuming exactly-once delivery.
  - Queue verified deliveries using application-owned payloads and understand the acknowledgement boundary.
  - Interpret HTTP outcomes, bound request bodies, and observe failures without exposing provider data.
---

# Delivery Policies

A provider can redeliver after your server accepts an event, including when the
response is lost. It can also send distinct events about the same resource.
Choose what counts as acceptance and make its effects safe to repeat before
enabling duplicate suppression or moving work to a queue.

These examples assume the Resend source and typed `accept` function from
[Receiving Webhooks](receiving-webhooks.md#binding-handlers). Import
`arkoren.dev/core/http/webhook` and `arkoren.dev/driver/resend/resendwebhook`.
Each receiver construction below is an alternative policy, not a sequence of
redefinitions to paste into one scope. Your application handles construction
errors before mounting the returned handler.

## Duplicate Receipts

`Deduplicate` remembers successfully accepted delivery IDs in a configured
[cache](../cache/overview.md). A recorded ID receives 200 without decoding or
acceptance. Deduplication is disabled by default and is best effort, not an
exactly-once guarantee: cache errors fail open and concurrent copies can both run.
Make the handler idempotent, for example by committing an event ID and its
business effect in the same application transaction.

With `cache` imported from `arkoren.dev/core/service/cache` and `time` from the
standard library, choose a namespace and retention for a new receiver:

```go
receipts := cache.NewNamespace[bool]("webhook.resend.primary", func(namespace *cache.NamespaceBuilder) {
	namespace.TTL(24 * time.Hour)
})
```

Twenty-four hours is an example policy, not a provider guarantee. Match retention
to your provider's retry and manual-redelivery window. Zero TTL means no expiry;
there is no automatic 72-hour fallback. Use separate, noncolliding physical names
for unrelated accounts or receivers sharing a cache.

With your existing `*cache.Cache` named `store`, attach the receipts:

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[resendwebhook.Envelope]) {
	receiver.Handle(resendwebhook.EmailDelivered, accept)
	receiver.Deduplicate(store, receipts)
})
```

The receiver authenticates first, checks the ID before decoding, and records it
after acceptance succeeds but before writing 200. A failed acceptance does not
write a receipt. An empty ID bypasses suppression; use the adapter's stable event ID,
not an email ID or retry-attempt ID.

A failed cache check still runs acceptance, and a failed receipt write still
answers 200. Cache eviction, expiry, or process-local storage can therefore admit
duplicates. The receiver neither starts nor stops the cache; its owner manages
that lifecycle. Nil caches and invalid namespaces fail receiver construction.

### Preserving Receipts

Preserve deployed namespace names, separators, and retention when upgrading.
Changing a receipt key forgets prior acceptance and can admit replays. A route
rename does not change keys: namespaces are not derived from routes, provider
names, secrets, or diagnostic labels.

Only delivery IDs are encoded with unpadded base64url. Namespace names and
separators are literal, so different definitions alone do not guarantee isolated
physical keys. See [cache identity safety](../cache/keys.md#identity-safety).
The default separator is `:`; explicitly set `namespace.Separator(".")` in the
namespace callback to retain receipts written with the former dot default.

If you are migrating from the former scope-based webhook API, reproduce its
prefix rather than choosing a new name. With `encoding/base64` imported,
`oldScope` and `oldRetention` are your deployed values:

```go
name := "webhook:v1:" + base64.RawURLEncoding.EncodeToString([]byte(oldScope))
receipts := cache.NewNamespace[bool](name, func(namespace *cache.NamespaceBuilder) {
	namespace.Separator(":")
	namespace.TTL(oldRetention)
})
```

Use this namespace in `Deduplicate` instead of the new-receiver namespace above.
Preserve the required replay window or explicitly migrate existing receipts;
there is no automatic lookup under old names. General namespace upgrades are
covered in [preserving cache names](../cache/keys.md#preserving-names).

## Queued Handling

If acceptance needs slow email, API, or database work, hand a verified delivery
to a [queue](../queues/overview.md). `Dispatch` acknowledges when the queue driver
accepts the message, not when a worker finishes. There is no built-in inbox or
outbox: that acknowledgement is only as durable as your chosen queue driver.
Workers must still tolerate duplicates, including work submitted before a lost
response or a failed receipt write.

Queued messages can outlive a deployment. Keep the payload in a small type you
control rather than depending on a provider SDK's future JSON encoding:

```go
type DeliveredEmail struct {
	EmailID string `json:"email_id"`
}
```

Use `Event.Map` to retain the provider decoder while projecting its result into
that application-owned type:

```go
delivered := resendwebhook.EmailDelivered.Map(func(email resendwebhook.Email) (DeliveredEmail, error) {
	return DeliveredEmail{EmailID: email.EmailID}, nil
})
```

The original event remains unchanged. Its decoder runs before conversion; a
decoder error prevents conversion. `Map` preserves the exact provider key and
does not replace the delivery ID or type. Keep conversion free of I/O and shared
payload mutation; functions and captured values must support concurrent calls.
An ordinary conversion error answers 503 at the decode stage. An authenticated
`ErrSkip` instead acknowledges without queueing or recording a receipt.

Import `arkoren.dev/core/service/queue`. With your configured `*queue.Dispatcher`
named `dispatcher`, declare a typed job and bind it instead of `Handle`:

```go
job := queue.NewJob[webhook.Delivery[DeliveredEmail]]("email-delivered")
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[resendwebhook.Envelope]) {
	receiver.Dispatch(delivered, dispatcher, job)
})
```

The queued payload includes the provider ID, type, and mapped payload under the
stable JSON members `id`, `type`, and `payload`. The queue creates its own message
ID. Evolve application payloads compatibly while old messages remain queued, and
remember that queue message-size limits can be smaller than the webhook body
limit. If the event already returns the payload type you want to store, dispatch
it directly without `Map`. A nil dispatcher, invalid job, or invalid mapped event
fails receiver construction.

With `context` imported, your existing `*queue.Consumer` named `consumer` can
perform the work later. Register before starting the consumer:

```go
err := consumer.Handle(job, func(ctx context.Context, delivery webhook.Delivery[DeliveredEmail]) error {
	return messages.RecordDelivered(ctx, delivery.Payload.EmailID)
})
```

Here `messages` is your idempotent application service, not an Arkoren helper.
Consumer failures follow [queue delivery and retry policy](../queues/delivery-and-retries.md),
not a second HTTP answer to the provider. Queue verified deliveries, not raw
bodies: signature headers are not included in the job.

## Acknowledgement Outcomes

The receiver processes one event per request and writes its own response.
Provider retry rules remain provider-owned: neither 503 nor any other answer
guarantees redelivery. A handler can commit part of its work and then fail;
an error is not evidence of rollback.

| Outcome | HTTP Response | New Receipt |
| --- | --- | --- |
| Acceptance returns nil, including an intentional no-op | 200, empty body | Write attempted if enabled and the ID is nonempty |
| Previously recorded ID | 200, empty body | None |
| Authenticated unbound event | 200, empty body | None |
| Source, decoder, or conversion returns `ErrSkip` | 200, empty body | None |
| Source error matches `Reject` | Mapped 4xx | None |
| Source error wraps `ErrRejected` without a matching override | 400 | None |
| Body exceeds the effective limit | 413 | None |
| Malformed body read | 400 | None |
| Body read interrupted by cancellation or timeout | 503 | None |
| Other source error, ordinary decode/conversion error, or any acceptance error | 503 | None |

Return `ErrSkip` from a source only after authentication succeeds; otherwise a
forged request could be acknowledged as valid. Decoders and conversions already
run after authentication. In acceptance, return nil for an intentional no-op.
Every acceptance error—including `ErrSkip` and permanent queue errors—answers
503 and leaves the ID unrecorded.

Failure responses use fixed Problem Details messages without the underlying
cause, and the receiver never redirects. Oversized bodies retain the native
`request.ErrBodyTooLarge` 413 problem. Because these responses are already
committed, application-wide [error mapping](../http-fundamentals/error-handling.md)
cannot replace them. Response-writer hooks still run.

Returned receipt errors still match their class (`webhook.ErrRejected` or
`webhook.ErrUnavailable`) and original cause through `errors.Is`. Oversized
receipts also match `request.ErrBodyTooLarge`. [Request logging](../observability/request-logging.md)
can retain the cause without exposing it to the sender; an error mapper can
still affect log classification, not the committed response.

### Rejection Overrides

Use `Reject` only for known-invalid verification errors. Do not map a temporary
lookup or verification outage to 4xx: the provider might stop redelivering a
genuine event. For Resend, an alternative receiver can answer 401 for a known bad
signature, with `net/http` imported:

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[resendwebhook.Envelope]) {
	receiver.Handle(resendwebhook.EmailDelivered, accept)
	receiver.Reject(http.StatusUnauthorized, resendwebhook.ErrInvalidSignature)
})
```

Mappings apply only to source errors and match through `errors.Is`. Calls
accumulate; the first matching mapping wins. Unmapped `ErrRejected` answers 400;
other source errors answer 503.

`Reject` requires at least one non-nil error and a 4xx status, but refuses 408
and 429 because they mean retry later, 406 because Mailgun stops retrying, and
410 because Standard Webhooks providers disable the endpoint. Invalid mappings
make `Handler` return `ErrInvalid` at construction.

## Body Limits

The receiver reads the body before authentication, so its size limit also bounds
unauthenticated requests. The default is 1 MiB. If provider notifications fit
within a smaller allowance, lower it while constructing the receiver:

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[resendwebhook.Envelope]) {
	receiver.Handle(resendwebhook.EmailDelivered, accept)
	receiver.MaxBodySize(256 << 10)
})
```

This permits at most 256 KiB; larger requests answer 413 without calling the
source. Zero selects the 1 MiB default; negative sizes fail construction. An
outer [body-limit middleware](../http-fundamentals/body-limit.md) remains a ceiling:
the smaller limit wins. Raising `MaxBodySize` cannot raise that outer ceiling.
Choose a larger allowance only for a provider payload that needs it, and keep
ingress limits and queue capacity consistent with that choice.

## Receiver Diagnostics

A reachable endpoint can still reject every request after a secret change.
Connect your application's [event bus](../events/overview.md) to distinguish
rejections, processing failures, and authenticated events without bindings.
With an existing bus named `bus`:

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[resendwebhook.Envelope]) {
	receiver.Handle(resendwebhook.EmailDelivered, accept)
	receiver.Events(bus)
	receiver.Label("resend-primary")
})
```

`Events(nil)`, the default, publishes nothing. Labels default to empty and are
not validated or redacted: choose low-cardinality application values without
credentials, personal identifiers, or provider-controlled data. A label is not a
receipt identity and does not alter deduplication.

| Topic | Payload | When Published |
| --- | --- | --- |
| `webhook.Rejected` (`http.webhook.rejected`) | `RejectedEvent`: label, reason, status | Any 4xx outcome |
| `webhook.Failed` (`http.webhook.failed`) | `FailedEvent`: label, stage | Any 503 outcome |
| `webhook.Ignored` (`http.webhook.ignored`) | `IgnoredEvent`: label, reason | Authenticated unbound event |

Skips and duplicate acknowledgements publish none of these topics. Categories
describe local processing, not provider business outcomes:

| Field Type | Constants and String Values |
| --- | --- |
| `RejectionReason` | `RejectionReasonBodyTooLarge` = `"body_too_large"`; `RejectionReasonUnreadableBody` = `"unreadable_body"`; `RejectionReasonVerify` = `"verify"` |
| `FailureStage` | `FailureStageRead` = `"read"`; `FailureStageVerify` = `"verify"`; `FailureStageDecode` = `"decode"`; `FailureStageAccept` = `"accept"` |
| `IgnoreReason` | `IgnoreReasonUnbound` = `"unbound"` |

These constants belong to `webhook`. Unclassified source errors, including
remote verification outages and envelope-processing failures, report
`FailureStageVerify`; classified source rejections report `RejectionReasonVerify`.
`Map` errors belong to the decode stage; every acceptance error belongs to the
accept stage. Empty zero values remain unspecified, not default categories.

With `context` imported and your existing logger, subscribe for the receiver's
lifetime:

```go
unsubscribe, err := bus.Listen(webhook.Failed, func(ctx context.Context, failure webhook.FailedEvent) error {
	logger.WarnContext(ctx, "webhook processing unavailable",
		"receiver", failure.Label, "stage", string(failure.Stage))

	return nil
})
```

Handle the registration error and call `unsubscribe` when retiring the receiver,
not when a short-lived setup function returns. Listeners run synchronously after
the outcome is known and cannot change it; send slow alert work to a worker.

Diagnostic payloads omit bodies, headers, provider IDs, event types, and causes.
That omission does not sanitize request context values or returned receipt
errors, and normal HTTP problem instances can still contain a request path.
Select safe logging attributes instead of forwarding whole contexts or errors.

If upgrading from plain-string category fields, compare typed constants or use
explicit conversions such as `string(failure.Stage)` for string-only APIs.
String and JSON category bytes and member names remain unchanged. This change
does not require renaming provider keys, labels, or receipt namespaces.

## Operating Receivers

Meet the provider's response deadline by keeping acceptance short and bounding
remote verification I/O. Core handles one event per request; for provider batches,
decode a batch payload and split it in the consumer. Challenge responses,
interactive decisions, and decoding that fetches remote resources need
application-owned handling rather than this receiver pipeline.

Before deploying, check the boundary with signed provider fixtures:

- A valid delivery returns 200 and reaches the intended handler or queue job.
- An invalid signature never reaches acceptance; a verification outage answers 503.
- Repeating an ID suppresses the second sequential acceptance when receipts are enabled.
- Failed acceptance can run again; cache failures and concurrent deliveries remain safe.
- Body-limit and decoder failures produce the intended status and diagnostic stage.

Use `handler.Record(signedRequest)` to exercise a fixture without a network;
close the returned response body when the test finishes reading it.
For queue handoff, `queuetest.New()` from
`arkoren.dev/core/service/queue/queuetest` records dispatches without a broker;
`FailWith(err)` simulates acceptance failure. That fake proves submission, not
external queue durability. Test worker effects independently and use
`testing/synctest` for timestamp-sensitive fixtures. Monitor
[cache failures](../cache/monitoring.md) as well as receiver outcomes, since
receipt failures intentionally do not fail the delivery.

For every function and option, see the
[`webhook` API reference](https://pkg.go.dev/arkoren.dev/core/http/webhook).
