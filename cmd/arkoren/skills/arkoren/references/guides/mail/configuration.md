---
title: Mail Configuration
nav: Configuration
description: Set sender and reply-to defaults, override them per message, connect the mailer to events and telemetry, and keep credentials out of logs.
order: 20
---

# Mail Configuration

## Introduction

Most messages share a sender and a place for replies, even when you change mail
providers between environments. Set those defaults on the mailer and keep the
provider's connection settings on the driver.

Mail has two layers of configuration. The **mailer** holds what is shared by
every message your application sends: a default sender, default reply-to
addresses, and the event bus and telemetry it reports to. The **driver** holds
how mail leaves the process: a server address, an API key, a region. They are
set in different places and never overlap, so a message can be sent through a
different driver without changing what the mailer fills in.

The mailer follows the same shape as the rest of Arkoren: `mail.New` takes the
driver and then `Builder` policies, and never fails for message defaults.
Construction callback misuse still panics. The driver's own settings are on
[Mail Drivers](drivers.md). For the policy contract, see
[Configuration](../first-steps/configuration.md).

Examples assume an existing `mail.Driver` named `driver`, configured as shown on
the linked driver page. Check its construction error before creating a mailer.

## Sender defaults

An application usually sends from one address, so set it once on the mailer:

```go
import "arkoren.dev/core/service/mail"

mailer := mail.New(driver, func(sender *mail.Builder) {
	sender.From(mail.Address{Name: "Acme", Email: "no-reply@acme.test"})
})
```

Every message that omits the `MessageBuilder.From` setting is sent from
that address. If replies should go elsewhere, add this setting inside the same
construction callback:

```go
sender.ReplyTo(mail.Address{Name: "Acme Support", Email: "support@acme.test"})
```

Every message with an empty `ReplyTo()` list gets those reply-to addresses.
`Builder.ReplyTo` takes any number of addresses.

Choose the sender with your provider in mind. Resend, Mailgun, Postmark, and
Amazon SES all require the sending domain or address to be verified in your
account, and they reject mail from anything else. With SMTP, the relay decides.
A sender the provider refuses fails the send, not the construction of the
mailer.

## Overriding per message

The defaults apply only to what a message leaves unset. Optional settings on the
message's construction draft win, so one mailer can send ordinary mail and mail
on behalf of a person:

```go
message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{{Email: "grace@example.com"}},
}, mail.Body{Text: "Meet Grace."}, func(letter *mail.MessageBuilder) {
	letter.From(mail.Address{Name: "Ada via Acme", Email: "ada@acme.test"})
	letter.ReplyTo(mail.Address{Email: "ada@example.com"})
	letter.Subject("Introduction")
})
```

Send `message` through the configured `mailer`. The two defaults are checked
separately: a message with its own `From()` still inherits the mailer's reply-to,
which may not be what you want. If a message
should have no reply-to at all, an empty list cannot say so while the mailer
has a default; send it from a mailer that has none. A message cannot add to the
default reply-to list either, only replace it. Calling `MessageBuilder.ReplyTo()`
with no addresses restores an empty message setting, so the mailer can supply
its default; it does not suppress that default.

The mailer fills in defaults on a copy, so the message you pass in is unchanged.
`mailer.Config()` also returns an independent copy of the sender defaults;
editing that record does not reconfigure the mailer. Its event bus and telemetry
service remain shared, as described under [Reusing sender policy](#reusing-sender-policy).

## Migrating mutable messages

If your application previously built `mail.Message` literals or assigned its
fields, move recipients into the required `mail.Recipients` argument and body
strings into the required `mail.Body` argument of `mail.NewMessage`. Use
`MessageBuilder.From`, `Subject`, `ReplyTo`, and `Attachments` only for optional
settings, as in the example above. There are no mutable exported message fields
or post-construction setters.

Replace reads such as `message.Subject` with `message.Subject()` and
`message.To` with `message.To()`. To change a message, construct another one;
editing a returned slice does not edit the original. Input recipient slices,
reply-to slices, attachment records, and attachment bytes are copied at capture
and publication. Getters also detach slices and attachment bytes, so you can
reuse or modify your inputs without changing an already-published message.

For validation before mailer defaults, call `message.Err()`, not `Validate()`.
An omitted sender is valid at construction. `Mailer.Send` fills missing
`From` and `ReplyTo` on a local copy, then fully validates it without changing
your original. Calling `driver.Send` directly or `mail.EncodeMIME` bypasses
default completion: set `From` in the message builder and require
`message.Validate()` to succeed. The zero message, empty recipients, and an
empty body remain invalid. A bad optional setting leaves a sticky construction
error even if a later callback replaces it.

## Events and telemetry

To alert on failed sends or measure provider latency, add the event bus and
telemetry to your sender policy when constructing the mailer. Here `app`
is your existing `*application.Application`, and `from` is a `mail.Address`:

```go
mailer := mail.New(driver, func(mailer *mail.Builder) {
	mailer.From(from)
	mailer.Events(app.Events())
	mailer.Telemetry(app.Telemetry())
})
```

Without `Builder.Events` the mailer publishes nothing. Without `Builder.Telemetry` it
follows the process-wide default telemetry at the moment each message is sent,
which records nothing until that telemetry is started. [Mail Events and Telemetry](events-and-telemetry.md)
describes what each one reports.

## Reusing sender policy

If several mailers need the same sender defaults, keep them in one policy
function. Here `from` and `support` are `mail.Address` values and `bus` is an
existing `*event.Bus`. Apply local settings afterwards:

```go
sender := func(mailer *mail.Builder) {
	mailer.From(from)
	mailer.ReplyTo(support)
}
mailer := mail.New(driver, sender, func(mailer *mail.Builder) {
	mailer.Events(bus)
})
```

Policies run once in order on one fresh draft. `ReplyTo` copies its input and
replaces the list; no arguments clear it. Publication copies the completed list
again, so neither input edits nor later draft edits change the mailer.
`Mailer.Config()` remains a reporting record with an independent reply-to slice,
not an input to construction. Live telemetry and event buses remain shared.

## Secrets

Mail credentials are the SMTP password and the API keys and tokens of the
providers. Treat them like any other secret: read them from the environment or
a secret store when the application starts, never from source or a committed
file.

With imports `os` and `arkoren.dev/driver/smtp/smtpmail`, load relay credentials
when constructing the driver:

```go
driver, err := smtpmail.New("smtp.example.com", 587,
	func(transport *smtpmail.Builder) {
		transport.Username(os.Getenv("SMTP_USER"))
		transport.Password(os.Getenv("SMTP_PASSWORD"))
	},
)
```

The SMTP driver's configuration prints a nonempty password as `[REDACTED]`
through `fmt` and `slog`; an empty password stays empty. The username and other
reporting fields remain visible and can contain personal data.
Resend, Mailgun, and Postmark take their key or token as a positional argument
and keep it inside the driver or its client. SES uses the AWS credential chain
or a caller-configured client. SMTP and Postmark driver values format as fixed
type labels through `fmt`, including copied values. These protections do not
redact borrowed native clients, returned errors, message contents, or arbitrary
serialization; do not dump those objects or log credentials yourself.
[Secrets](../secrets/overview.md) shows how to load credentials from a secret
store at startup.

The same care applies to what you put in messages. A password reset link in a
message body is a credential for as long as it lives, and the log driver prints
bodies when you ask it to; use that only in development.

## Choosing the driver by environment

A common setup logs mail in development and sends it in production. With imports
`os`, `arkoren.dev/core/service/mail/slogmail`, and
`arkoren.dev/driver/resend/resendmail`, choose the driver when building the mailer:

```go
func newMailDriver(env string) (mail.Driver, error) {
	if env == "production" {
		return resendmail.New(os.Getenv("RESEND_API_KEY"))
	}

	return slogmail.New(), nil
}
```

This helper returns a constructor error for missing production credentials;
check it before passing the driver to `mail.New`. Nonproduction values log mail
instead of sending it. Because every driver accepts the same message, sending code does not
change between the two. For a real SMTP server in development that catches mail
instead of delivering it, see [the SMTP driver](drivers.md#smtp), which runs against Mailpit.

For every function and option, see [the `mail` API reference](https://pkg.go.dev/arkoren.dev/core/service/mail).
