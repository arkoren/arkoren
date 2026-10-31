---
title: Receiving Webhooks
nav: Webhooks
description: Authenticate provider notifications, bind typed event handlers, and acknowledge acceptance without assuming provider retry guarantees.
order: 40
---

# Receiving Webhooks

## Introduction

A **webhook** is an HTTP notification from a provider, such as a payment or mail service. With no logged-in user, the request must prove its authenticity. A successful acknowledgment confirms receipt, not completion of downstream business work; the provider decides whether to retry other answers.

The `webhook` package splits the work among three owners:

- A **source** authenticates the requests of one provider account and parses them into an envelope. It holds the credentials.
- An **event** names one provider event type and decodes the envelope into a payload type. It holds no credentials, so one event definition works with any number of sources.
- **Your application** binds a handler to each event it cares about and sets its own policy: size limit, rejection statuses, deduplication, and the event bus.

`webhook.Handler` combines them into a `router.Handler`, with no endpoint name or receiver lifecycle. A vendor package, library, or your code supplies authentication through the source, not core. This page covers shared receiving policy; `acmewebhook` is a hypothetical custom provider, not an importable package.

Delivery and retry guarantees belong to the provider. Assume duplicates: a provider may send an event again even after a successful answer, or send two events for one change. Some providers retry non-2xx answers; others do not automatically redeliver failed deliveries. A 503 means "may be retried", never "will be retried". Acceptance is not transactional: a handler that returned an error may already have committed part of its work.

Import `arkoren.dev/core/http/webhook`. The snippets also use `context`,
`net/http`, `fmt`, and `time`; registration assumes an existing router `r`.
Source-author snippets belong in your provider package. Later application
snippets refer to that package as `acmewebhook`; replace it with your own module's
import path rather than looking for a published package of that name.

Focused construction snippets keep error results visible and assume success;
handle those errors before registering routes. Authentication checks and test
assertions remain explicit.

## Provider adapters

Use the setup beside your service's driver:

- Email notifications: [Resend](../mail/drivers.md#resend),
  [Mailgun](../mail/drivers.md#mailgun), and [Postmark](../mail/drivers.md#postmark).
- Background responses: [OpenAI](../llm/drivers.md#receiving-background-notifications).
- Payments and subscriptions: [Stripe](../payments/drivers.md#receiving-stripe-notifications)
  and [PayPal](../payments/drivers.md#receiving-paypal-notifications).

Adapters work independently of outbound drivers. Constructing an outbound driver
does not register a webhook endpoint or require you to use its webhook adapter.
Your application owns handlers, queueing, and deduplication. Follow each provider's
authentication requirements; not every provider signs the body.
Compare the [provider verification and identity boundaries](provider-webhooks.md)
before choosing a source or a deduplication policy.

## Creating a source

A source owns authentication for one provider account. It verifies the request
and returns a `Delivery` containing the provider's event ID, event type, and
authenticated envelope. An envelope carries provider metadata and the
still-encoded event payload; it is not yet the order or invoice your handler uses.

If your provider library supplies a source constructor, pass the verification
credentials that provider requires. This hypothetical provider uses an account
signing secret:

```go
source, err := acmewebhook.New(secret)
```

The hypothetical constructor validates the secret; its verifier authenticates
before parsing the envelope. Known invalid
signatures and envelopes wrap both `webhook.ErrRejected` and the library's native
sentinel, such as `acmewebhook.ErrBadSignature`. They answer 400 by default;
unexpected verification failures answer 503.

If a library already supplies your source and events, continue to
[Receiving an event](#receiving-an-event). Otherwise, define them separately as
shown next.

## Defining a source and events

In your provider package, define the envelope independently of event payloads.
For the hypothetical `acmewebhook` protocol, the envelope is:

```go
type Envelope struct {
	ID   string         `json:"id"`
	Kind string         `json:"kind"`
	Data jsontext.Value `json:"data"`
}
```

Here `jsontext` is `encoding/json/jsontext`. `Data` remains encoded until a bound
event decodes it.

Create a source around your provider-specific verifier:

```go
source := webhook.NewSource(func(r *http.Request, body []byte) (webhook.Delivery[Envelope], error) {
	envelope, err := verifyAndParse(r, body, secret)
	if err != nil {
		return webhook.Delivery[Envelope]{}, err
	}

	return webhook.Delivery[Envelope]{ID: envelope.ID, Type: envelope.Kind, Payload: envelope}, nil
})
```

`verifyAndParse` is your own authentication and envelope-parsing function, not an
Arkoren helper. Implement the provider's actual signature protocol, including
required timestamp and replay checks. It must authenticate before returning an
envelope and wrap `webhook.ErrRejected` plus a native sentinel for known invalid
requests. For example, a bad signature can return
`fmt.Errorf("%w: %w", webhook.ErrRejected, ErrBadSignature)`. Leave transient
verification failures unclassified so they answer 503.

The source sees the exact bounded provider bytes; re-encoding JSON breaks
signatures. `r.Body` is spent. Use `r.Context()` for cancellation. Nothing is
selected before authentication. `NewSource` retains the verifier; `Source.Err`
and `Handler` detect a nil or missing function. Your provider constructor must
validate credentials; `NewSource` does not call the verifier during construction.

Next, define the payload for an order payment:

```go
type Order struct {
	OrderID string `json:"order_id"`
	Amount  int    `json:"amount"`
}
```

Publish a credential-free event that decodes that payload from the envelope:

```go
var OrderPaid = webhook.NewEvent("order.paid", func(envelope Envelope) (Order, error) {
	var order Order
	err := json.Unmarshal(envelope.Data, &order)

	return order, err
})
```

Here `json` is `encoding/json/v2`. Decoding runs only after authentication.
A payload-decoding error answers 503, because a later deploy may fix its schema
mismatch. Define `OrderCanceled` the same way with its own key and payload.

An event key is a nonempty provider string without control characters, not a
queue or topic name. Types tie handlers to payloads, not accounts or secrets.

## Receiving an event

To mark an order paid on notification, bind your application function to
`acmewebhook.OrderPaid`. The source authenticates and the event decodes before
your function runs:

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[acmewebhook.Envelope]) {
	receiver.Handle(acmewebhook.OrderPaid, onOrderPaid)
})
```

`onOrderPaid` has type
`func(context.Context, webhook.Delivery[acmewebhook.Order]) error`. It receives the
delivery ID, provider event type, and decoded payload. Return nil once your
application has accepted the event; return an error if acceptance failed.

Register the returned `router.Handler` on your router:

```go
r.Post("/webhooks/orders", handler)
```

`Handler` checks `Source.Err` during construction. Register its result directly
with `Post`, not through an adapter that hides returned errors.

For a bound event, the receiver answers 200 with an empty body after acceptance
returns nil. Authenticated unbound events, duplicates and preaccept skips also
acknowledge without acceptance.

To receive several events, add each binding to the receiver callback:

```go
receiver.Handle(acmewebhook.OrderPaid, onOrderPaid)
receiver.Handle(acmewebhook.OrderCanceled, onOrderCanceled)
```

Each event key has one owner; duplicate bindings are construction errors. Go
infers payload types from the event and acceptance callback.

## Queuing events

Sending a receipt or updating another service during a webhook request makes
the provider wait and risks a timeout. Put the verified delivery on a
[queue](../queues/overview.md) so a worker can do that work after the request.

`Dispatch` makes this handoff instead of calling your handler directly. The
receiver answers once the queue driver accepts the message, not when the worker
finishes. That answer tells the provider you have accepted responsibility for
the delivery; it does not mean the receipt has been sent.

A message may outlive a deployment. Keep its payload in a small type you control,
not a provider SDK type that might change on upgrade. For the payment worker,
keep the order ID and amount:

```go
type OrderPaidDTO struct {
	OrderID string `json:"order_id"`
	Amount  int    `json:"amount"`
}
```

Use `Event.Map` to copy those fields from the authenticated provider payload into
your own type:

```go
payment := acmewebhook.OrderPaid.Map(func(order acmewebhook.Order) (OrderPaidDTO, error) {
	return OrderPaidDTO{OrderID: order.OrderID, Amount: order.Amount}, nil
})
```

Import `arkoren.dev/core/service/queue`; `dispatcher` is your configured
`*queue.Dispatcher`. Declare a job for the delivery, then bind it inside the
receiver callback instead of calling `Handle`:

```go
job := queue.NewJob[webhook.Delivery[OrderPaidDTO]]("order-paid")
receiver.Dispatch(payment, dispatcher, job)
```

The whole `Delivery[OrderPaidDTO]` is queued, preserving the provider event ID and type; the queue creates its own message ID. Its JSON tags (`id`, `type`, `payload`) are stable. Add fields rather than renaming or retyping queued fields during [rolling deploys](../queues/consuming-jobs.md#unknown-jobs-and-rolling-deploys). A vendor SDK type's JSON encoding is not covered by this guarantee.

`Map` changes the payload type, not the provider event key. The original event
stays unchanged and can still be used with any compatible source. The provider's
decoder runs first; your conversion runs only if decoding succeeds.

Keep the conversion to data shaping: no network calls, database writes, or
changes to shared payload data. Its function and captured values are shared
between requests, so concurrent calls must be safe. A conversion error answers
503 unless it wraps `ErrSkip`, which acknowledges without running the worker or
recording the delivery ID. A nil converter or invalid base event retains its
first `ErrInvalid` configuration error.

If the provider event already produces the payload type you want to keep in the
queue, pass it directly to `Dispatch`; you do not need `Map`. Queue limits on
message size may be below the webhook body limit. `Dispatch` checks the job and
dispatcher at construction. There is no inbox or outbox: acknowledgment is only
as durable as the queue driver.

A [queue consumer](../queues/consuming-jobs.md#registering-handlers) does the work later.
Here `consumer` is your `*queue.Consumer` and `orders.MarkPaid` is your
application's idempotent order update. Register before starting the consumer:

```go
err := consumer.Handle(job, func(ctx context.Context, delivery webhook.Delivery[OrderPaidDTO]) error {
	return orders.MarkPaid(ctx, delivery.Payload.OrderID, delivery.Payload.Amount)
})
```

## Rejecting invalid deliveries

A bad signature stays invalid on retry; an unavailable verification lookup
service might recover. The receiver distinguishes these cases: known invalid
requests wrapping `webhook.ErrRejected` answer 400, while unclassified source
errors answer 503.

If your provider expects a different rejection status, use `Reject` for a known
error. For example, make a bad signature answer 401 instead of 400:

```go
receiver.Reject(http.StatusUnauthorized, acmewebhook.ErrBadSignature)
```

`Reject` matches with `errors.Is`, so a wrapped error maps too. Calls accumulate, and the first matching mapping in call order wins. A source error that wraps `webhook.ErrRejected` and matches no mapping answers 400; a vendor source usually wraps it for signature failures.

Map only errors that retrying cannot fix. A 4xx is a statement that this delivery is invalid, and many providers stop sending after it. Leave transient failures, such as a lookup that timed out while verifying, unmapped so they answer 503.

`Reject` refuses four statuses because providers give them special meaning:

| Status | Why it is refused |
| --- | --- |
| 406 | Mailgun stops retrying the delivery. |
| 410 | Standard Webhooks providers disable the endpoint. |
| 408 and 429 | Both mean "retry later", the opposite of a rejection. |

It also refuses a status outside 400 to 499 and a call without errors, or with a nil error; `Handler` returns the problem as described in [Configuration mistakes](#configuration-mistakes).

## Skipping events

A provider may send event types you do not use. Authenticated events with no bound handler are acknowledged with 200 automatically. To acknowledge a delivery the source or an event decoder recognizes but does not want, return an error that wraps `webhook.ErrSkip`:

```go
payments := acmewebhook.OrderPaid.Map(func(order acmewebhook.Order) (acmewebhook.Order, error) {
	if order.Amount == 0 {
		return acmewebhook.Order{}, fmt.Errorf("free order: %w", webhook.ErrSkip)
	}

	return order, nil
})
```

Bind `payments` instead of the base event. A source or decoder skip acknowledges with 200 and an empty body without calling acceptance or recording the ID for deduplication. Return `ErrSkip` only after authentication: skipping before checking the signature would acknowledge forged requests. A decode or conversion error without `ErrSkip` answers 503, because a deploy may fix the mismatch.

In acceptance, return nil for an intentional no-op instead:

```go
receiver.Handle(acmewebhook.OrderPaid, func(ctx context.Context, delivery webhook.Delivery[acmewebhook.Order]) error {
	if delivery.Payload.Amount == 0 {
		return nil
	}

	return orders.MarkPaid(ctx, delivery.Payload.OrderID, delivery.Payload.Amount)
})
```

Returning nil acknowledges and records the ID when deduplication is enabled. Returning `ErrSkip` from acceptance does **not** skip: every accept error, including a permanent queue error, answers 503 and leaves the ID unrecorded.

## Skipping duplicates

Providers may redeliver after a timeout even if you handled the event. Optional
`Deduplicate` remembers handled event IDs in a [cache](../cache/overview.md) and
answers 200 when it finds a recorded ID, without handling the event again. It is
best effort, not an exactly-once guarantee. With your cache facade
`store`, choose a retention policy. Preserve deployed receipt names when upgrading
to avoid admitting replays. For a new receiver:

```go
receipts := cache.NewNamespace[bool]("webhook.orders", func(namespace *cache.NamespaceBuilder) {
	namespace.TTL(24 * time.Hour)
})
receiver.Deduplicate(store, receipts)
```

Import `cache` from `arkoren.dev/core/service/cache`. The typed
[namespace](../cache/overview.md#sharing-a-prefix-and-ttl-with-namespaces) TTL
controls retention: zero means no expiry, not 72 hours. Set an explicit TTL for
the provider's redelivery policy. Nil cache or invalid namespace fails construction.

Only delivery IDs are encoded (unpadded base64url); prefixes and separators stay
literal. Choose noncolliding physical names: definitions alone do not isolate
receipts. Renaming loses recorded IDs; route renaming does not, since names are
not derived from routes, providers, or secrets. To preserve entries from the
former scope API, use `"webhook:v1:"` plus the unpadded base64url encoding of the
old scope as the namespace name, with `":"` as its separator. Choose an explicit
TTL matching the old retention; there is no 72-hour fallback. Import
`encoding/base64` when computing the name:

```go
name := "webhook:v1:" + base64.RawURLEncoding.EncodeToString([]byte(oldScope))
receipts := cache.NewNamespace[bool](name, func(namespace *cache.NamespaceBuilder) {
	namespace.Separator(":")
	namespace.TTL(oldRetention)
})
```

Here `oldScope` and `oldRetention` are the deployed scope string and retention
duration, not new defaults. Pass this namespace to `receiver.Deduplicate` in
place of the new receiver's namespace above.

For deployed dot-default namespaces, add `namespace.Separator(".")` to the
callback above to keep `webhook.orders.<encoded ID>`, not the new colon join.
Preserve names and retention for the required replay window or migrate receipts
explicitly. Dots inside prefixes are unchanged; see
[cache migration](../cache/overview.md#choosing-names-and-separators).

The source must return a stable event ID across retries, such as a provider event
ID or Standard Webhooks `webhook-id`, never an attempt ID. Empty IDs skip
deduplication. Checks precede decoding; recording follows successful acceptance,
before 200. Failed handlers therefore do not suppress later attempts.

Deduplication fails open: cache check failures still run acceptance to favor
duplicates over loss; recording failures still answer 200. Concurrent deliveries
of one ID can both pass the check.

> [!WARNING]
> Deduplication reduces duplicates but does not remove them. Write the handler so that handling an event twice has the same result as handling it once, for example by recording the event ID in the same transaction as the change it causes.

## Limiting the body size

Your endpoint should not have to read an arbitrarily large request before it
can check the signature. The receiver caps the body at 1 MiB by default and
answers 413 for a larger request without calling the source. If your provider's
notifications are smaller, lower the limit with `MaxBodySize`:

```go
receiver.MaxBodySize(256 << 10)
```

A negative size is invalid, and zero means the default. A [`bodylimit`](../http-fundamentals/body-limit.md) middleware on the router stays a ceiling: if both apply, the smaller limit wins, so `MaxBodySize` can lower a router-wide limit but never raise it.

A body that is malformed and cannot be read answers 400. A read cut short by cancellation or a timeout answers 503, because that is not a statement about the request.

## Reacting to rejections and failures

An endpoint can be reachable while every notification fails after a signing
secret changes. To alert on rejections or failures, connect your application's
[event bus](../events/overview.md). `Events` enables those notifications; `Label`
helps you identify which receiver is having trouble:

```go
receiver.Events(bus)
receiver.Label("orders")
```

The receiver then publishes:

- `webhook.Rejected` (`http.webhook.rejected`) for every delivery answered with a 4xx: `RejectedEvent` carries `Label`, `Reason` of type `webhook.RejectionReason`, and `Status`.
- `webhook.Failed` (`http.webhook.failed`) for every 503: `FailedEvent` carries `Label` and `Stage` of type `webhook.FailureStage`.
- `webhook.Ignored` (`http.webhook.ignored`) for an authenticated delivery nobody bound: `IgnoredEvent` carries `Label` and `Reason` of type `webhook.IgnoreReason`. Authenticated skips and duplicates publish none of these topics.

Use the category constants to select outcomes in listeners. These values describe
receiver processing, not the provider's event type or retry decision:

| Field | Constant | String value |
| --- | --- | --- |
| `RejectedEvent.Reason` | `webhook.RejectionReasonBodyTooLarge` | `"body_too_large"` |
| `RejectedEvent.Reason` | `webhook.RejectionReasonUnreadableBody` | `"unreadable_body"` |
| `RejectedEvent.Reason` | `webhook.RejectionReasonVerify` | `"verify"` |
| `FailedEvent.Stage` | `webhook.FailureStageRead` | `"read"` |
| `FailedEvent.Stage` | `webhook.FailureStageVerify` | `"verify"` |
| `FailedEvent.Stage` | `webhook.FailureStageDecode` | `"decode"` |
| `FailedEvent.Stage` | `webhook.FailureStageAccept` | `"accept"` |
| `IgnoredEvent.Reason` | `webhook.IgnoreReasonUnbound` | `"unbound"` |

`FailureStageRead` means an interrupted or timed-out body read, before the source
runs. A rejected source error reports `RejectionReasonVerify`; an unclassified
source error reports `FailureStageVerify`. An event decoder or `Map` conversion
failure reports `FailureStageDecode`, while every acceptance error, including
`ErrSkip`, reports `FailureStageAccept`.

Events are published after the outcome is known, so a listener cannot change the
response. The label is only for telling receivers apart; it is never part of a
deduplication key. Labels default to empty and are neither validated nor redacted:
use application-chosen, low-cardinality values without credentials or personal
identifiers. The payload does not include a route pattern.

With your existing event bus `bus` and application alert function `alerts.Warn`,
which accepts plain strings for the label and stage, keep the subscription for
the receiver's lifetime:

```go
unsubscribe, err := bus.Listen(webhook.Failed, func(ctx context.Context, failure webhook.FailedEvent) error {
	alerts.Warn(ctx, "webhook failing", failure.Label, string(failure.Stage))

	return nil
})
```

The payloads never contain the body, headers, the provider's event ID or event
type, or the error that caused the outcome. Listener contexts still retain
request context values, and returned receipt errors still retain their causes.
Neither is generally sanitized; select safe attributes rather than forwarding
whole contexts or errors. Normal HTTP problem instances can still contain the
request path.

Handle the registration error and call `unsubscribe` when retiring the receiver,
not when a short-lived setup function returns. Listeners run synchronously;
hand slow alert delivery to a worker rather than blocking webhook requests.

### Migrating category fields

These three fields now use defined string types instead of plain `string`.
Compare with typed constants, such as
`failure.Stage == webhook.FailureStageAccept`. For a string-only API, use
`string(failure.Stage)` as above; when constructing a payload from a string
variable, convert it to the field's type, such as `webhook.FailureStage(stage)`.
Untyped literals remain assignable.

The string and JSON category bytes in the table are unchanged, as are JSON
member names. The empty zero value stays `""` (unspecified); it is not replaced
with a default or with `"unbound"`. The receiver emits the listed nonempty
constants. Provider keys (`Delivery.Type` and `Event.Key()`) and the application's
`Label` remain strings, not these category types. No provider key, receipt name,
or deduplication retention needs to change for this migration.

## What the provider sees

| Outcome | Status |
| --- | --- |
| Handled, duplicate, unbound event, or `ErrSkip` from the source or decoder | 200, empty body |
| Source error matching a `Reject` mapping | the mapped 4xx |
| Source error wrapping `ErrRejected` | 400 |
| Body over the limit | 413 |
| Malformed body that cannot be read | 400 |
| Body read cut short by cancellation or a timeout | 503 |
| Any other source error, decode/conversion error without `ErrSkip`, or any handler error | 503 |

The receiver never redirects. Failure responses are Problem Details documents with fixed messages and no cause, because the sender may be probing verification. The receiver commits its own response, so an application-wide [`errormap`](../http-fundamentals/error-handling.md) cannot change it, even when it matches the cause or receipt class. Oversized bodies keep the `request.ErrBodyTooLarge` 413 representation; their returned error also matches `webhook.ErrRejected` and `request.ErrBodyTooLarge`.

Every receipt error still matches its class (`webhook.ErrRejected` or `webhook.ErrUnavailable`) and native cause with `errors.Is`. [`requestlog`](../observability/request-logging.md) records returned errors and 5xx statuses, preserving the cause without exposing it in the response. Response-writer hooks still run, and a mapper can still affect classification of the returned error in logs.

`webhook.ErrRejected` and `webhook.ErrUnavailable` are `problem.Details` values you can inspect like any other [error response](../http-fundamentals/error-handling.md).

## Configuration mistakes

`Handler` returns an error wrapping `webhook.ErrInvalid` for a zero source, an invalid event (including a mapped event with a nil converter), a nil accept function, a key bound twice, no event bound, a negative size, an invalid `Deduplicate` or `Reject`, a nil dispatcher, or an invalid job. Deduplication diagnostics identify a nil cache or invalid namespace. The first invalid setting is retained; later callbacks still run once on the same draft. Nothing is validated lazily at request time. A supplied nil configuration callback panics.

## Publishing events from a library or ark

A library can publish the source constructor and event definitions shown above. An [ark](../core-concepts/arks.md) can export a `BuilderFunc` that binds its handlers and leaves policy to the application. Both `billing` and its `Store` below are hypothetical application code, not shipped modules; `ledger.AcceptOrder` accepts a `webhook.Delivery[acmewebhook.Order]`:

```go
// Ark: billing.
func Webhooks(ledger *Store) webhook.BuilderFunc[acmewebhook.Envelope] {
	return func(receiver *webhook.Builder[acmewebhook.Envelope]) {
		receiver.Handle(acmewebhook.OrderPaid, ledger.AcceptOrder)
	}
}

```

Apply that binding in the application, alongside its own deduplication policy:

```go
handler, err := webhook.Handler(source, billing.Webhooks(ledger), func(receiver *webhook.Builder[acmewebhook.Envelope]) {
	receiver.Deduplicate(store, cache.NewNamespace[bool]("webhook.billing", func(namespace *cache.NamespaceBuilder) {
		namespace.TTL(72 * time.Hour)
	}))
})
```

Because callbacks run in order on one draft, an ark's binding and the application's policy compose. If both bind the same event, `Handler` returns `ErrInvalid`.

## Operating a receiver

The receiver handles one event per request. For provider batches, use a batch
payload and split it in the consumer. Meet the provider's response deadline by
handing slow work to a queue.

Challenge responses, interactive decisions, and decoding that fetches remote data
need application-owned handling. Queue verified deliveries, not raw bodies:
signature headers are not preserved in queue messages.

## Testing

To test queue handoff without a broker or provider call, use `queuetest.New()`
to record dispatches in memory. With your source and event defined, dispatch to
that fake:

Import `arkoren.dev/core/service/queue/queuetest` and
`arkoren.dev/core/service/queue`; `t` is the current `*testing.T`.

```go
fake := queuetest.New()
job := queue.NewJob[webhook.Delivery[acmewebhook.Order]]("order-paid")
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[acmewebhook.Envelope]) {
	receiver.Dispatch(acmewebhook.OrderPaid, queue.NewDispatcher(fake), job)
})
if err != nil {
	t.Fatal(err)
}
```

Build a valid signed request with your provider's fixtures. `signedRequest` is
your fixture helper, not an Arkoren API. Assert the response and queued delivery:

```go
response := handler.Record(signedRequest("evt-1", "o-1", 42))
if response.StatusCode != http.StatusOK {
	t.Fatalf("status = %d, want 200", response.StatusCode)
}
fake.AssertDispatched(t, job, func(got webhook.Delivery[acmewebhook.Order]) bool {
	return got.ID == "evt-1" && got.Payload.OrderID == "o-1" && got.Payload.Amount == 42
})
```

`Record` runs the request through the handler without a network. A wrong
signature answers 400 when your source wraps `ErrRejected`; expect 401 only
with the earlier `Reject` override. Calling `fake.FailWith(err)` before the
request makes acceptance answer 503. The fake proves the queue request, not
durability of an external broker.

Test [deduplication](#skipping-duplicates) with `cache.New(memorycache.New())` and the same event ID twice: the second response is 200 and the job is dispatched once. Use `testing/synctest` for timestamp checks. The [queue testing guide](../queues/overview.md#testing) shows consumption with `Drain`.

For every function and option, see [the `webhook` API reference](https://pkg.go.dev/arkoren.dev/core/http/webhook).
