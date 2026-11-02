---
title: Events and Telemetry
nav: Events & Telemetry
description: Observe mail submission attempts with bounded outcome events, spans, and metrics while preserving privacy and uncertainty boundaries.
order: 50
summary:
  - Attach shared observation handles and react to synchronous submission outcome events.
  - Interpret bounded failure classes, send spans, and attempt metrics without inferring delivery.
  - Understand panic and privacy boundaries and add transport-level HTTP tracing when needed.
---

# Events and Telemetry

When an expected email is missing, first distinguish a rejected submission from
an accepted one. Mail outcome events report what `Send` returned; telemetry
measures the attempt. Neither tells you whether an inbox received the email.
For later delivery or bounce notifications, configure the optional
[provider receiver](drivers.md#provider-notifications) separately.

## Observation Setup

Events require an explicit bus. Telemetry uses an explicit service or the
[shared process handle](../observability/telemetry.md#shared-handle), resolved at
each send; it records nothing until its instrumentation is active. With import
`arkoren.dev/core/service/mail`, an existing driver named `driver`, and your
`*application.Application` named `app`, attach the application's handles:

```go
mailer := mail.New(driver, func(sender *mail.Builder) {
	sender.From(mail.Address{Email: "notifications@acme.test"})
	sender.Events(app.Events())
	sender.Telemetry(app.Telemetry())
})
```

The bus and telemetry service remain shared, not cloned or managed by the
mailer. Omitted or nil `Events` publishes nothing; omitted or nil `Telemetry`
follows the process default, including later changes. Direct driver or native
client calls bypass these facade signals.

## Events

With a configured bus and nonnil context, a normally returning `Send` publishes
one outcome synchronously before returning:

| Topic | Payload | Meaning |
| --- | --- | --- |
| `mail.Sent` (`mail.sent`) | `mail.SentEvent` | Driver acceptance, not recipient delivery. |
| `mail.Failed` (`mail.failed`) | `mail.FailedEvent` | A returned validation or driver error. |

Both payloads count recipient entries across To, CC, and BCC, including repeats,
and count regular and inline attachments together. `SentEvent` adds the
`Receipt`, whose opaque ID can be empty. `FailedEvent` instead adds a bounded
`ErrorClass`; it carries no error object, diagnostic text, or native type name.
Log-driver success still publishes `Sent` without sending mail externally.

Classification follows wrapped and joined causes in this priority order:

| `ErrorClass` | Matching Cause |
| --- | --- |
| `context.Canceled` | Cancellation, even if other known causes also match. |
| `context.DeadlineExceeded` | Deadline expiry without cancellation. |
| `mail.ErrInvalidMessage` | Invalid input without either context class. |
| `error` | Any other returned failure. |

To alert on submission failures without exporting provider diagnostics, listen
using the standard-library `context` import. Here `alerts.Notify` is your
application-owned method with signature
`Notify(context.Context, string, string) error`:

```go
unsubscribe, err := app.Events().Listen(mail.Failed,
	func(ctx context.Context, observed mail.FailedEvent) error {
		if observed.ErrorClass == "mail.ErrInvalidMessage" {
			return nil
		}

		return alerts.Notify(ctx, "Mail submission failed", observed.ErrorClass)
	})
```

Handle the registration error and retain `unsubscribe` for the listener's
lifetime. This example omits invalid-input alerts so application validation can
report them separately; it does not classify every other failure as retryable.
See [listener ownership](../events/listeners.md) for registration and removal.

If you want an acceptance feed for support, a separate listener can call your
`receipts.Record(context.Context, string, int) error` method:

```go
unsubscribe, err := app.Events().Listen(mail.Sent,
	func(ctx context.Context, observed mail.SentEvent) error {
		return receipts.Record(ctx, observed.Receipt.ID, observed.Recipients)
	})
```

This is an alternative registration fragment, not a redeclaration in the same
scope. Receipts identify submissions, not your application record. For durable
correlation with an order or account, record the result around the original
`Send` call where that identity is known; best-effort events are not an outbox.

Listeners run on the sending goroutine with its context. Slow listeners delay
the caller; hand slow work to a [queue](../queues/overview.md). A listener error
or panic is logged by the bus and stops later listeners, but cannot change the
submission result. A retry produces a separate outcome for each normally
returning attempt. A nil context fails before any event or telemetry.

## Send Telemetry

When enabled, each nonnil-context call creates one client span named `mail send`,
starting before validation and ending after submission but before outcome
listeners. It is a child of the calling request/job span when one exists.
SMTP connection, TLS, authentication, and message exchange are inside that
duration; listener latency is not.

| Span Attribute | Value |
| --- | --- |
| `arkoren.mail.operation` | `send` |
| `arkoren.mail.recipients` | Total To, CC, and BCC entries |
| `arkoren.mail.attachments` | Regular and inline attachment count |
| `error.type` | Class of a returned error; absent for success or a driver panic |

The counter `arkoren.mail.operations` counts attempts, not accepted messages.
The histogram `arkoren.mail.operation.duration` records seconds. Both use
`arkoren.mail.operation` and `error.type`; recipient and attachment counts are
span-only. Duration bucket boundaries are 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5,
10, 30, and 60 seconds.

No provider or message-purpose label is added automatically. Separate mailer
instances do not split metrics into welcome/report or SMTP/API groups. If you
need that comparison, add your own bounded application labels without addresses
or individual message IDs. See [instrumented components](../observability/instrumented-components.md#mail)
for the shared inventory and [Telemetry](../observability/telemetry.md) for export
setup. Disabled instrumentation records nothing and adds no telemetry allocations;
normal driver submission costs remain.

## Failure Signals

Returned validation failures are observed even though no driver submission
occurs. A returned error marks the span as error and sets `error.type` to
`context.Canceled`, `context.DeadlineExceeded`, or `mail.ErrInvalidMessage`, in
that precedence order. Otherwise telemetry uses the innermost wrapped Go error
type. Unlike event `ErrorClass`, its fallback is not restricted to the string
`error`. Error text is not recorded.

A rise in invalid-message failures points to composition or sender setup.
Context interruption and other provider/network classes need separate
investigation; a failure signal alone is not evidence that remote acceptance
did not happen. `Send` still returns the original error with causes available
through `errors.Is` and `errors.As`.

A driver panic propagates unchanged and publishes neither outcome event.
The mailer still ends its own span and records count and duration without
ending the parent's span. A panic alone leaves span status unset and adds no
`error.type`, panic value, exception event, or panic log. These signals measure
an interrupted attempt, not successful submission or confirmed rejection. Add
panic reporting at your existing application recovery boundary rather than
inferring it from missing mail events or retrying blindly.

## HTTP Detail

If a slow attempt needs transport-level investigation, supply an HTTP client
whose transport comes from your telemetry service. With standard-library
imports `net/http` and `time`:

```go
client := &http.Client{
	Timeout:   10 * time.Second,
	Transport: app.Telemetry().Transport(nil),
}
```

Configure it before concurrent use. Postmark accepts it through
`postmarkmail.Builder.HTTPClient` during construction. For Resend, Mailgun,
or SES, configure the native SDK with this client before `NewFrom`; the
[driver sections](drivers.md) explain the respective constructor and borrowed
ownership. HTTP spans nest under `mail send`. SMTP does not use HTTP and has
only the facade span.

## Privacy Boundaries

Mail's facade telemetry omits addresses, subject, text/HTML, attachment names and
bytes, headers, error text, and receipt ID. Events omit message content and
addresses; success events deliberately retain the opaque receipt, while failure
events retain only a fixed class and counts.

Those omissions do not redact the separate error returned by `Send`, data in
your context, native-client diagnostics, or the Log driver's records. A returned
error can quote a rejected recipient or up to part of a provider response.
Select and redact diagnostics before sending them to logs or alerts, and do not
attach the raw error chain to a bounded failure event.

Verify application assertions with the [mail fake](../testing/fakes.md#assert-mail),
[injected failures](../testing/fakes.md#inject-failures), and
[captured telemetry](../testing/fakes.md#capture-telemetry). These tests can prove
requested submission and recorded signals, not real provider delivery.

For every function and option, see the
[`mail` API](https://pkg.go.dev/arkoren.dev/core/service/mail),
[`event` API](https://pkg.go.dev/arkoren.dev/core/event), and
[`telemetry` API](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
