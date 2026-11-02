---
title: Overview
description: Submit typed push messages, choose retention, and distinguish acceptance from browser display.
order: 10
summary:
  - Build immutable messages from typed application input and authorized recipients.
  - Choose offline retention and handle uncertain acceptance without automatic retries.
  - Observe submission outcomes without treating them as delivery or display confirmation.
---
# Overview

Push notifications tell a customer about an update even when your site is not
open. A `push.Sender` submits one message to its configured driver; the browser's
service worker displays it. Provider acceptance does not establish delivery or
display, and push is not a durable notification inbox.

A **subscription** contains a browser-issued endpoint and encryption keys. Your
application stores it for the signed-in user, authorizes recipient selection,
and handles replacement or removal. Arkoren does not register devices, store
subscriptions, route between providers, or retry sends. Configure a sender in
[Drivers](drivers.md) and obtain subscriptions through
[Browser Integration](browser-integration.md).

## Preparing Messages

These focused examples assume a configured `sender`, a `context.Context` named
`ctx`, and a `push.Subscription` named `subscription` loaded from an authorized
registration record. Import `arkoren.dev/core/service/push`. Snippets retain
error assignments but omit routine propagation; complete functions and security
decisions handle errors before proceeding.

For an order update, keep application input typed before translating it into
the message's string metadata:

```go
type OrderNotice struct {
	OrderID string
	Path    string
}

notice := OrderNotice{OrderID: "123", Path: "/orders/123"}
```

Choose the subscription on the server, not from an arbitrary recipient in the
request. Required arguments identify that recipient and a nonempty UTF-8 title;
the construction callback adds optional body text and application data:

```go
message := push.NewMessage(subscription, "Order shipped", func(notification *push.MessageBuilder) {
	notification.Body("Your order is on its way.")
	notification.Data(map[string]string{
		"order_id": notice.OrderID,
		"url":      notice.Path,
	})
})
```

`Data` accepts string values, not arbitrary JSON or a provider notification
object. Body text and metadata keys and values must also be valid UTF-8.
`Message` is an immutable snapshot: construction copies the map, and
`Data()` returns a detached copy. `Data` replaces earlier metadata rather than
merging it. `Err()` and `Validate()` expose the first declaration error, even if
a later callback supplies a valid replacement.

Pass a `push.Subscription` or `push.Token` value, not a pointer. Tokens are
interpreted only by a driver that supports them; the Web Push driver accepts
subscriptions only. Subscription validation checks HTTPS structure and browser
encryption keys without network I/O. It does not approve a destination for
outgoing requests; [driver endpoint policy](drivers.md#endpoint-policy) does that.

## Submitting Notifications

Submit the published message once through the sender:

```go
receipt, err := sender.Send(ctx, message)
```

`Send` validates before calling the driver. Invalid portable input never reaches
it. A nil error means acceptance; `Receipt.ID` is optional opaque acceptance
metadata and can be empty. Web Push deliberately returns an empty ID rather than
exposing a sensitive provider URL.

Do not put sensitive information in display text: notifications can appear on a
locked device. Keep subscription endpoints, keys, native tokens, payloads, and
receipt IDs out of general logs and telemetry. Top-level diagnostics redact
recipient details, but explicitly unwrapped native errors can still contain URLs.

## Choosing Retention

The time to live (**TTL**) requests how long the provider can retain an update
while the browser is offline. It is neither a delivery deadline nor a promise
that the provider stores or displays the notification.

The sender requests 24 hours by default. For a status update that becomes stale
after five minutes, import `time` and override retention for this send:

```go
receipt, err := sender.Send(ctx, message, push.TTL(5*time.Minute))
```

An omitted option or `push.TTL(0)` inherits the sender default; a negative option
requests no offline storage. No-store does not prevent immediate delivery.
[Drivers](drivers.md#sender-settings) shows how to change the sender default.
Direct driver calls differ: raw `push.SendConfig{TTL: 0}` means no-store, and a
negative raw TTL is invalid. Web Push rounds positive durations down to whole
seconds, so a duration below one second also becomes no-store.

## Handling Outcomes

Classify failures with standard-library `errors.Is`. Check
`push.ErrOutcomeUnknown` before cancellation or provider failures: a wrapped or
joined error can match several causes without resolving whether acceptance
occurred. Every failed send returns a zero receipt.

| Error | Application Response |
| --- | --- |
| `push.ErrOutcomeUnknown` | Acceptance is uncertain; decide whether a duplicate is acceptable before replaying. |
| `push.ErrInvalidMessage` | Correct input, endpoint policy, payload size, or retention before resubmission. |
| `push.ErrUnsupported` | Choose a recipient or operation supported by the configured driver. |
| `push.ErrNotFound` | Invalidate only the stored registration version used for this send. |
| `push.ErrRateLimited` | Apply an explicit bounded scheduling policy. |
| `push.ErrUnavailable` | Report availability failure and apply application policy; do not delete the registration. |

Web Push maps authoritative HTTP 404/410 responses to `ErrNotFound`.
Authentication failures do not prove expiry: repair credentials rather than
deleting live subscriptions. Conditional invalidation prevents an older send
from deleting a registration refreshed while the request was in flight.

> [!WARNING]
> A timeout after dispatch can leave acceptance unknown. Automatic replay can
> produce duplicate notifications. Neither the sender nor the Web Push driver
> retries automatically or provides exactly-once delivery.

Permission revocation, offline expiry, operating-system settings, and worker
failures can prevent display after acceptance. Use [Testing](testing.md) to check
application submission and browser display as separate outcomes.

## Observing Submissions

For optional in-process observations, configure `push.Builder.Events` with a
shared event bus as shown in [sender settings](drivers.md#sender-settings).
`push.Accepted` and `push.Failed` report returned driver outcomes, not delivery.
Their `SubmissionEvent` contains only duration and a bounded error class.

Portable preflight failures emit no event. A failed event with
`ErrorClass == "outcome_unknown"` means acceptance could have occurred.
Acceptance has an empty class; other classes identify cancellation, deadline,
push sentinels, or an unclassified error. Listener failures and panics do not
change `Send`'s result; driver panics propagate without an outcome event. These
best-effort observations are not a durable audit log or retry mechanism.

Telemetry follows the process default on each call unless explicitly selected.
It records bounded operation/error classes, never message, recipient, receipt,
or native error text. Unknown acceptance takes classification precedence over
joined cancellation or deadline errors without replacing their returned causes.
See the [push instruments](../observability/instrumented-components.md#push-notifications)
for span and metric names.

For message, recipient, and sender operations, see the
[push API reference](https://pkg.go.dev/arkoren.dev/core/service/push).
