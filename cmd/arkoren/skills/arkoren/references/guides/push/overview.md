---
title: Push Overview
nav: Overview
description: Submit push notifications through a portable sender and distinguish provider acceptance from browser display.
order: 10
---
# Push Overview

## Introduction

When an order ships, you can notify a customer even when your site is not open.
The `push` service submits a visible notification to a push provider. Your
application chooses the recipient; the browser's service worker displays the
notification. A successful send means provider acceptance, not delivery or display.

A **subscription** is the browser-issued endpoint and encryption keys needed for
Web Push. Keep it associated with the signed-in customer in your application's
storage. Arkoren does not register browsers, store devices, select recipients,
run background workers, or track delivery. Start with [browser integration](browser-integration.md)
to obtain a subscription, and use the [Web Push driver](drivers.md#web-push)
to submit notifications.

## Sending a notification

Install the core module from your application's module directory:

```sh
go get arkoren.dev/core
```

Wrap your [configured push driver](drivers.md#web-push) in a sender:

```go
import "arkoren.dev/core/service/push"

sender := push.New(driver)
```

The following focused examples use this `sender`, a `context.Context` named
`ctx`, and a `push.Subscription` named `subscription`
loaded from your application's authorized registration record. Import
the same `push` package. Focused snippets keep error assignments visible;
complete functions and security decisions check errors before proceeding.

`Subscription.Endpoint` must be an absolute HTTPS URL without URL credentials or
a literal `#`, including an empty trailing fragment delimiter. Percent-encoded
`%23` in the path or query is data, not a fragment, and is retained unchanged.
These structural checks do not establish a trusted network destination; the
[Web Push driver's endpoint policy](drivers.md#web-push) controls where requests
can connect.

To tell a customer that order 123 shipped, supply the recipient and title as
required arguments and add optional display text and application data:

```go
message := push.NewMessage(subscription, "Order shipped", func(notification *push.MessageBuilder) {
	notification.Body("Your order is on its way.")
	notification.Data(map[string]string{
		"order_id": "123",
		"url":      "/orders/123",
	})
})
receipt, err := sender.Send(ctx, message)
```

The title is required even when you only need application data. `Data` deliberately
accepts a string map, not arbitrary JSON: it is a portable application payload,
not a provider-specific notification object. The Web Push driver's
[flat JSON payload](browser-integration.md#displaying-the-notification) matches
the service worker in the browser guide. It does not contain the recipient.

`Message` is an immutable snapshot. `Data` replaces and copies the supplied map;
changing that map or retaining the construction draft does not change the message.
`To`, `Title`, `Body`, and `Data` read the published values; `Data` returns a copy.
`Err` reports construction errors and `Validate` checks the completed message.
`Send` validates before calling the driver, so an invalid message is not sent.

## Choosing retention

A customer can be offline when you send. The time to live (**TTL**) requests how
long a provider can retain a notification before discarding it; it is not a
delivery deadline or a promise that the provider stores it for that long.

By default the sender requests 24 hours. To keep a short-lived status update for
at most five minutes, import `time` and set a per-send option:

```go
receipt, err := sender.Send(ctx, message, push.TTL(5*time.Minute))
```

Omitting the option or passing zero uses the sender's default. A positive value
requests that duration; a negative value disables offline storage. Configure a
different sender default at construction with `push.Builder.TTL`; zero restores
24 hours and a negative default disables offline storage.

Web Push encodes TTL as whole seconds, rounded down, so a positive duration below
one second becomes zero. Providers can impose their own retention limits; the
portable contract does not impose one provider's maximum on every driver.

## Handling the result

`Receipt.ID` is an opaque acceptance identifier and can be empty. The Web Push
driver returns no identifier rather than exposing a provider URL that could be a
secret. Neither an ID nor a nil error confirms that the browser displayed anything.
Permission changes, offline expiry, operating-system settings, and service-worker
failures can all prevent display after acceptance.

Use `errors.Is` from the standard `errors` package to classify a failed send.
Check `push.ErrOutcomeUnknown` first: a wrapped or joined error can also match
cancellation, a deadline, or another provider failure while acceptance remains
uncertain. The returned error preserves all those causes; matching a context
error does not override the uncertainty.

| Error | Application action |
| --- | --- |
| `push.ErrOutcomeUnknown` | The provider might have accepted the notification. Do not automatically replay it. |
| `push.ErrInvalidMessage` | Fix the message or registration; do not replay it unchanged. |
| `push.ErrUnsupported` | Use a recipient supported by the configured driver. Web Push accepts subscriptions, not device tokens. |
| `push.ErrNotFound` | Invalidate the expired registration only if its stored version still matches the one you sent to. |
| `push.ErrRateLimited` | Apply your application's bounded scheduling policy; the sender does not retry. |
| `push.ErrUnavailable` | Report a provider or transport failure and apply an explicit application policy. |

For Web Push, HTTP 404 and 410 definitively identify an expired subscription.
Authentication failures are not evidence that the subscription expired: do not
delete registrations because the VAPID key or contact is wrong. Conditional
invalidation prevents an older send from deleting a registration refreshed while
the request was in flight.

> [!WARNING]
> A transport timeout can leave acceptance unknown. Automatic replay can display
> the same notification twice. Arkoren performs no retries; decide whether a
> duplicate is acceptable before scheduling another attempt.

Top-level errors redact sensitive recipient details, but unwrapping native causes
can reveal URLs or other secrets. Classify with `errors.Is`; do not log subscription
endpoints, keys, payloads, or unwrapped error chains in general application logs.

### Events and telemetry

For operational observations without logging recipient data, opt in at sender
construction. With your existing `driver`, import `arkoren.dev/core/event` and
pass a shared bus:

```go
events := event.New()
sender := push.New(driver, func(delivery *push.Builder) {
	delivery.Events(events)
})
```

Listen to `push.Accepted` and `push.Failed` on this bus to observe one driver
submission outcome per send. Their `push.SubmissionEvent` payload contains only
duration and a bounded error class, never message content, recipients, receipt
IDs, tokens, endpoints, keys, or native error text. Portable validation failures
emit nothing; nil `Events` disables notifications. A failed event with
`ErrorClass == "outcome_unknown"` means acceptance may have occurred, not that
delivery failed. Listener errors and panics do not change the returned result.
Acceptance events have an empty `ErrorClass`. Failure classification gives
`outcome_unknown` precedence over `canceled`, then `deadline_exceeded`.
Remaining push sentinels are checked in the table's
order and use their full names; other failures use `error`. No native error
cause is retained in the event.
Driver panics propagate without an outcome event; only returned driver results
produce notifications. Nil `Events` also skips event-related clock reads.
These synchronous, best-effort in-process events are not a durable audit log,
delivery confirmation, or an automatic retry mechanism.

Telemetry uses `error.type == "push.ErrOutcomeUnknown"` whenever the returned
error matches that sentinel, even if it also wraps cancellation or a deadline.
The event still uses `ErrorClass == "outcome_unknown"`; this telemetry precedence
does not replace the returned error or change notification semantics.
Set `push.Builder.Telemetry` to use your service explicitly, or leave it unset
to follow the process default. See the
[push span and metric reference](../observability/instrumented-components.md#push-notifications).

## Testing application sends

Use `pushtest.Fake` to prove that application code requested the intended
notification without network access. It does not prove encryption, provider
acceptance, browser delivery, or display. See [testing and verification](testing.md)
for a complete assertion and the real-browser checklist.

For every function and option, see the [push API reference](https://pkg.go.dev/arkoren.dev/core/service/push).
