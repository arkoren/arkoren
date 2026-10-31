---
title: Mail Events and Telemetry
nav: Events & Telemetry
description: Observe returned submission outcomes with events, measure send attempts, and understand panic and privacy boundaries.
order: 30
---

# Mail Events and Telemetry

## Introduction

When a customer reports a missing receipt, you need to know whether sending
failed and how long the provider took to respond. The mailer reports those
outcomes in two ways. **Events** announce the outcome of a send so your code
can react: alert on a failure, store the receipt.
**Telemetry** measures each send with a span and a duration, so you can see
which sends are slow and which fail.

These are submission outcomes, not delivery notifications: `mail.Sent` means
the driver accepted the message. With the log driver, no mail leaves the process.
To learn about later delivery or bounces, configure an optional
[provider webhook receiver](drivers.md) separately.

Events require an explicit bus; telemetry uses an explicit service or the
[process default](../observability/telemetry.md#the-process-default), initially
a no-op. Neither can change the result of a send: a listener that fails is
logged by the bus, and telemetry only observes.
Both also avoid mail's sensitive parts. Addresses, subjects, bodies, and
attachment names or contents are not telemetry attributes or dedicated event
fields. Failure events retain only a bounded classification and counts, not
driver diagnostics. [What is recorded](#what-is-recorded-and-what-is-not) lists
the telemetry fields. With `arkoren.dev/core/service/mail` imported, an existing `mail.Driver`
named `driver`, and your `*application.Application` named `app`, enable both:

```go
mailer := mail.New(driver, func(mailer *mail.Builder) {
	mailer.Events(app.Events())
	mailer.Telemetry(app.Telemetry())
})
```

## Events

With a bus and a nonnil context, the mailer publishes one of two events when
`Send` returns normally:

| Event | Name | Published when |
| --- | --- | --- |
| `mail.Sent` | `mail.sent` | the driver accepted the message |
| `mail.Failed` | `mail.failed` | validation or the driver returned an error, including a canceled or expired context |

`mail.Sent` carries a `mail.SentEvent` with the `Receipt` and the counts
`Recipients` (to, cc, and bcc together) and `Attachments`. `mail.Failed` carries
a `mail.FailedEvent` with the same counts and a bounded `ErrorClass` string.
It contains no raw error, cause, diagnostic text, or arbitrary provider type name.
[Built-in Events](../events/built-in-events.md#mail)
lists them next to the other services' events.

> [!NOTE]
> This is a breaking payload API change: `FailedEvent.ErrorClass` replaces `Err`.
> Compare the string in listeners instead of calling `errors.Is` on a payload
> error. `Mailer.Send` still returns the original error with its causes intact
> for `errors.Is` and `errors.As`.

Classification checks wrapped and joined causes in this priority order; the
first matching class wins:

| `ErrorClass` | Selected when |
| --- | --- |
| `context.Canceled` | The returned error matches `context.Canceled`. |
| `context.DeadlineExceeded` | It matches the deadline error but not cancellation. |
| `mail.ErrInvalidMessage` | It matches invalid input but neither context error. |
| `error` | No known class matches; native diagnostics remain with the caller. |

Validation errors are published too; distinguish them with
`observed.ErrorClass == "mail.ErrInvalidMessage"`. A nil context fails before
telemetry or events, and a mailer without a bus publishes nothing. If the driver panics,
the original panic propagates without `mail.sent` or `mail.failed`: a panic is
neither observed acceptance nor a returned rejection. Events follow returned
outcomes, so a retry that succeeds publishes `mail.failed` for the first attempt
and `mail.sent` for the second.

> [!WARNING]
> The bounded event does not redact the separate error returned by `Send`.
> That error can quote a rejected recipient or other provider diagnostics.
> Review and redact returned errors before forwarding them to third-party logs
> or alerts; do not attach the raw error chain to this event.

Listening is the ordinary event bus call, covered in [Events](../events/overview.md).
With `context` imported, page someone for a failure other than an invalid message.
Here `alerts.Notify(context.Context, string, string) error` is your
application-owned notification service, accepting the fixed class rather than
provider diagnostics:

```go
_, err := app.Events().Listen(mail.Failed,
	func(ctx context.Context, observed mail.FailedEvent) error {
		if observed.ErrorClass == "mail.ErrInvalidMessage" {
			return nil
		}

		return alerts.Notify(ctx, "mail failed", observed.ErrorClass)
	})
```

To retain receipts for support questions, supply your application-owned
`receipts.Record(context.Context, string, int) error` implementation:

```go
_, err := app.Events().Listen(mail.Sent,
	func(ctx context.Context, e mail.SentEvent) error {
		return receipts.Record(ctx, e.Receipt.ID, e.Recipients)
	})
```

The details that matter when you write listeners:

- A listener runs synchronously on the goroutine that called `Send`, before
  `Send` returns, so a slow listener slows the caller. Hand slow work to a
  [queue](../queues/overview.md).
- A listener receives the context of the call.
- A listener's error or panic is logged by the bus and never changes what
  `Send` returns; the submission outcome is already known. A failing listener does
  stop the listeners after it.
- The event does not identify the message. It has counts and a receipt, so a
  listener cannot tell which email failed. If you need that, wrap the call
  that sends and log there, where you know the message.

## Telemetry

With telemetry started and a nonnil context, each `Send` records one client span
named `mail send` and updates two metrics. Disabled instrumentation records
nothing and adds no telemetry allocations; the driver still has its normal
submission costs.
Validation failures are recorded too, since the span starts before the
message is checked.

### Spans

| Attribute | Value |
| --- | --- |
| `arkoren.mail.operation` | `send` |
| `arkoren.mail.recipients` | To, cc, and bcc counted together; span only |
| `arkoren.mail.attachments` | Number of attachments; span only |
| `error.type` | Set for a returned error, not for a driver panic |

The span covers validation and driver submission: the provider API request or
SMTP's connection, STARTTLS, authentication, and message exchange. It ends before
outcome listeners run, so a slow listener increases caller latency without
extending this span. The span sits under
the span of whatever called `Send`, such as the request that triggered the mail
or the queue job.

### Metrics

The counter `arkoren.mail.operations` counts attempts, not accepted messages.
The histogram `arkoren.mail.operation.duration`, in seconds, records how long
they took. Both use the keys `arkoren.mail.operation` and `error.type`. The
buckets run from ten milliseconds to a minute, since a provider API and a slow relay differ by orders
of magnitude. There is no attribute for the driver and none for the kind of
mail, so you cannot split the metrics by provider or by "welcome" versus
"receipt". If you need that, record your own bounded labels; separate mailer
instances alone do not add those labels. [Instrumented components](../observability/instrumented-components.md#mail)
lists these next to the other components, and [Telemetry](../observability/telemetry.md)
explains how to export them.

### Failures

A returned error sets the span status to error and `error.type` to the class of
the error: `mail.ErrInvalidMessage` for validation, `context.Canceled` or
`context.DeadlineExceeded` for a matching context error, and otherwise the Go
type of the innermost wrapped error. The text of the error is never recorded.
Context cancellation takes precedence over deadlines, then invalid input.
Unlike an event's `ErrorClass`, telemetry's fallback can be a native Go type
name rather than the fixed string `error`.

Use `error.type` to separate invalid messages from submission failures. A rise
in `mail.ErrInvalidMessage` points to message construction or mailer setup.
Other classes include cancellation and deadlines as well as provider or network
failures; inspect the class before deciding where to investigate.

If the driver panics, the mailer ends its own span and records the attempt's
count and duration before letting the original panic propagate. It does not end
the caller's parent span. A panic alone leaves the span status unset and adds no
`error.type`; the mailer does not record the panic value, an exception event, or
a panic log. These observations measure an interrupted attempt, not acceptance,
rejection, or recovery. Instrument panic handling at the application's existing
recovery boundary if you need a separate signal; do not infer it from mail
failure events.

### HTTP detail

The span covers one `Send` and says nothing about the requests underneath it. To
see them, give the driver an HTTP client whose transport is
`app.Telemetry().Transport(nil)`. Postmark accepts a client with
`postmarkmail.Builder.HTTPClient` in its construction callback; for Resend, Mailgun, and SES, build the vendor
client with that HTTP client and pass it to `resendmail.NewFrom`,
`mailgunmail.NewFrom`, or `awsmail.NewFrom`. Those spans then nest under the
`mail send` span. SMTP does not use HTTP, so it shows only the facade span.

## What is recorded and what is not

Telemetry records the operation, the number of recipients and attachments, the duration,
and the class of an error. Never recorded: addresses, the subject, bodies,
attachment names and contents, headers, the text of an error, and the receipt
ID. The receipt ID is left out because it identifies one message at the
provider; correlate through your own record instead, for example by logging the
receipt next to the trace ID when you handle `mail.sent`.

## Testing

To assert what your code reports, use the in-memory telemetry fake from
`arkoren.dev/core/service/telemetry/telemetrytest`. Also import
`arkoren.dev/core/service/telemetry` and
`arkoren.dev/core/service/mail/mailtest`. With `t *testing.T`, record signals
instead of exporting them:

```go
fake := telemetrytest.New()
mailer := mail.New(mailtest.New(), func(mailer *mail.Builder) {
	mailer.From(mail.Address{Email: "no-reply@acme.test"})
	mailer.Telemetry(fake.Telemetry(t))
})
```

In a test with `t *testing.T`, send one message and assert the reported signals:

```go
message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{{Email: "ada@example.com"}},
}, mail.Body{Text: "Hello"})
_, err := mailer.Send(t.Context(), message)
if err != nil {
	t.Fatal(err)
}
fake.AssertSpan(t, "mail send", telemetry.String("arkoren.mail.operation", "send"))
fake.AssertCounter(t, "arkoren.mail.operations", 1,
	telemetry.String("arkoren.mail.operation", "send"),
)
```

Events need no fake: create an `event.New()` bus, listen to `mail.Sent` and
`mail.Failed`, pass the bus with `Builder.Events`, and assert on what the listener
collected. To produce a `mail.failed` event on demand, make the fake driver
fail with `FailNext`. See [Testing](../testing/overview.md).

For every function and option, see [the `mail` API reference](https://pkg.go.dev/arkoren.dev/core/service/mail),
[the `event` API reference](https://pkg.go.dev/arkoren.dev/core/event), and
[the `telemetry` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry).
