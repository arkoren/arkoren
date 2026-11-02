---
title: Mail Overview
nav: Overview
description: Send transactional email through a portable facade and distinguish submission acceptance from recipient delivery.
order: 10
summary:
  - Build a welcome message from typed account data and submit it through a configured mailer.
  - Interpret opaque receipts as submission outcomes, not evidence of inbox delivery.
  - Classify failures without exposing provider diagnostics or blindly retrying unknown acceptance.
---

# Mail Overview

A welcome email or receipt starts with application data and ends with a request
to submit mail. Arkoren separates those responsibilities: an immutable
`mail.Message` describes the email, and a `*mail.Mailer` applies sender defaults,
validates it, and passes it to a driver.

The mailer sends one message synchronously per call. It does not render templates,
queue work, retry, or track inbox delivery. Choose and construct your backend on
[Drivers](drivers.md); use a [queue job](../queues/producing-jobs.md) when submission
should happen outside the request that triggered it.

## Sending Mail

Assume a configured `*mail.Mailer` named `mailer` with a
[default sender](configuration.md), and a nonnil request or job context named
`ctx`. Import `arkoren.dev/core/service/mail`. For a newly created account,
use the recipient data your application has already validated and authorized:

```go
type WelcomeEmail struct {
	Name  string
	Email string
}

welcome := WelcomeEmail{Name: "Ada", Email: "ada@example.com"}
```

Construct the recipient list and body together, then add the optional subject:

```go
message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{{Name: welcome.Name, Email: welcome.Email}},
}, mail.Body{Text: "Your account is ready."}, func(letter *mail.MessageBuilder) {
	letter.Subject("Welcome to Acme")
})
```

At least one address across `To`, `CC`, and `BCC`, and at least one nonempty text
or HTML body, are required. The omitted sender comes from the mailer. The zero
`mail.Message` is invalid; use `NewMessage` rather than a struct literal.

Submit that value through the facade:

```go
receipt, err := mailer.Send(ctx, message)
```

On success, `receipt` describes driver acceptance. `Send` fills missing sender
and reply-to defaults on a local copy; your original message stays unchanged.
Focused examples leave error results visible and omit routine propagation;
handle those errors in your application before using successful results.
For HTML, recipient groups, and files, continue with
[Composing Messages](composing-messages.md).

## Submission Outcomes

`receipt.ID` is an opaque submission identifier, not a delivery status or retry
idempotency key. SMTP and Log return generated message IDs; API drivers return
provider identifiers. The portable contract permits an empty ID. Store an
available identifier with your application's mail record when you need support
correlation, under an appropriate access and retention policy.

A sending server can accept mail that later bounces, is deferred, or lands in
spam. The Log driver simulates acceptance without sending anything. Optional
[provider notifications](drivers.md#provider-notifications) can report later
outcomes, but constructing an outbound mailer does not register a receiver.
Even a delivery notification is not proof that a person read the message.

## Handling Failures

Invalid messages fail before driver submission with an error wrapping
`mail.ErrInvalidMessage`. A driver's preflight also rejects an already canceled
or expired context before I/O. Native failures retain their wrapped causes;
use `errors.Is` or `errors.As`, not diagnostic-text matching. Returned errors
can contain recipients or provider diagnostics: redact them before logging or
forwarding them.

With standard-library imports `context` and `errors`, distinguish malformed
input from an interrupted submission:

```go
_, err := mailer.Send(ctx, message)
invalid := errors.Is(err, mail.ErrInvalidMessage)
interrupted := errors.Is(err, context.Canceled) ||
	errors.Is(err, context.DeadlineExceeded)
```

If `invalid` is true, correct the message or sender configuration rather than
retrying unchanged input. If `interrupted` is true, stop this attempt; after I/O
starts, acceptance can remain unknown. Other failures need provider-specific
classification, including credential and recipient rejections.

Neither the facade nor its drivers add a retry loop, although native SDK or
transport policies can retry. A timeout can follow remote acceptance, so another
attempt can send duplicate mail. There is no portable idempotency key or
exactly-once guarantee; an application record alone cannot atomically commit
both a remote submission and a local update.

A driver panic propagates unchanged without a `mail.Sent` or `mail.Failed`
event. Absence of an outcome event does not prove nonacceptance. See
[Events and Telemetry](events-and-telemetry.md) for the signals that remain.
To assert application behavior without sending mail, use the
[mail fake](../testing/fakes.md#assert-mail) and
[failure injection](../testing/fakes.md#inject-failures).

For every function and option, see the
[`mail` API reference](https://pkg.go.dev/arkoren.dev/core/service/mail).
