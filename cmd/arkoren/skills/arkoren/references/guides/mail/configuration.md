---
title: Sender Defaults
description: Share sender and reply-to addresses across messages and override them without mutating published values.
order: 40
summary:
  - Set shared sender and reply-to defaults without mixing in provider connection settings.
  - Override message addresses deliberately and understand empty reply-to inheritance.
  - Reuse ordered construction policies while preserving detached message and configuration snapshots.
---

# Sender Defaults

Most application mail shares a sender and a place for replies. Configure those
addresses once on the mailer, then set explicit message values only when a
particular email needs a different policy. Provider credentials and transport
settings belong to the [driver](drivers.md), not these defaults.

Examples import `arkoren.dev/core/service/mail` and assume an existing
`mail.Driver` named `driver` whose construction succeeded. `mail.New` returns
a mailer without validating its address defaults; sending applies defaults
and validates the completed message before driver submission.

## Shared Addresses

To send notifications from one address while directing replies to support,
construct the mailer with both defaults:

```go
mailer := mail.New(driver, func(sender *mail.Builder) {
	sender.From(mail.Address{Name: "Acme", Email: "notifications@acme.test"})
	sender.ReplyTo(mail.Address{Name: "Acme Support", Email: "support@acme.test"})
})
```

Every message that omits its sender inherits `From`. Every empty message
reply-to list inherits the configured `ReplyTo` list. The two decisions are
independent. Without either a default sender or an explicit valid message
sender, `Send` fails with an error wrapping `mail.ErrInvalidMessage`.

Choose an address your provider permits: API providers require a verified
domain or sender, and SMTP relays apply their own rules. Syntactic validity
does not prove sending permission. A provider can reject the completed sender
at submission even when local validation passes.

`Builder.ReplyTo` accepts multiple addresses and replaces the whole list.
Calling it without arguments clears the mailer's default reply-to policy.
An empty `Builder.From` similarly restores the empty default, leaving normal
send-time validation in place.

## Message Overrides

A support reply can use a distinct approved sender and route replies to its
assigned team. Treat this as application policy, not permission for an arbitrary
request to choose an identity. Here `reply` is already validated, its recipient
authorized, and its sender selected from addresses your provider allows:

```go
type SupportReply struct {
	Recipient mail.Address
	Sender    mail.Address
	ReplyTo   mail.Address
	Text      string
}

reply := SupportReply{
	Recipient: mail.Address{Email: "ada@example.com"},
	Sender:    mail.Address{Name: "Acme Support", Email: "help@acme.test"},
	ReplyTo:   mail.Address{Email: "team@acme.test"},
	Text:      "Your report access has been restored.",
}
```

Set these optional addresses while constructing the immutable message, then
submit it with your nonnil request or job context `ctx`:

```go
message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{reply.Recipient},
}, mail.Body{Text: reply.Text}, func(letter *mail.MessageBuilder) {
	letter.From(reply.Sender)
	letter.ReplyTo(reply.ReplyTo)
	letter.Subject("Your support request")
})
receipt, err := mailer.Send(ctx, message)
```

On success the driver accepts the message with those addresses; the receipt
does not establish inbox delivery.
An explicit sender wins over the default, and a nonempty reply-to list replaces
the default rather than adding to it. A message that overrides only `From`
still inherits the mailer's reply-to list.

An empty message reply-to list cannot suppress a configured default.
`MessageBuilder.ReplyTo()` clears the message setting, so inheritance resumes.
If an email must have no reply-to header, use a mailer with no default reply-to.
An explicitly empty or malformed `MessageBuilder.From` records a sticky
construction error; it does not request fallback to the default.

Defaults are applied to a local copy. Sending does not change your original
message, replay its callbacks, or replace its getters with completed values.
Use `message.Err()` to check construction before defaults; `message.Validate()`
requires a complete sender without applying them. See
[immutable composition](composing-messages.md#immutable-values) for ownership
and error behavior.

## Reusable Policy

If multiple mailers share these addresses, reuse a construction policy and
apply local overrides afterward. With existing `mail.Address` values `from`
and `support`:

```go
policy := func(sender *mail.Builder) {
	sender.From(from)
	sender.ReplyTo(support)
}
mailer := mail.New(driver, policy, func(sender *mail.Builder) {
	sender.From(mail.Address{Name: "Acme Reports", Email: "reports@acme.test"})
})
```

Callbacks run once, synchronously in order on one fresh draft; the later sender
wins while the reply-to policy remains. Supplied nil callbacks and callback
panics propagate; expanding a nil or empty callback slice supplies no edits.

Reply-to input slices are copied when set and again when publishing the mailer.
Retained drafts and later input edits cannot reconfigure it. `mailer.Config()`
is a reporting snapshot with its own reply-to slice, not a constructor input or
runtime setter. Its event bus, telemetry service, and the driver remain shared
dependencies rather than cloned resources. Configure those reporting services
on [Events and Telemetry](events-and-telemetry.md).

For every function and option, see the
[`mail` API reference](https://pkg.go.dev/arkoren.dev/core/service/mail).
