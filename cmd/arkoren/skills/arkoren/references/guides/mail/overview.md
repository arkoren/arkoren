---
title: Mail Overview
nav: Overview
description: Compose a message, send it through the mailer, add attachments and inline images, understand validation, errors, and receipts, and test it.
order: 10
---

# Mail Overview

## Introduction

Transactional email is the mail your application sends because something
happened: a welcome message, a password reset, a receipt. Arkoren models it as
one portable `mail.Message` and one facade, the `*mail.Mailer`, that fills in
sender defaults, validates the message, and hands it to a driver. The driver is the
only part that knows how mail leaves the process: over SMTP, or through the
HTTP API of Resend, Mailgun, Postmark, or Amazon SES. Your code builds the same
message either way. Changing drivers leaves the sending call unchanged, but
provider restrictions still matter, especially for inline attachments.

The mailer is deliberately small. It sends one message per call, synchronously,
and returns a receipt. It does not queue, retry, render templates, or track
delivery. Those belong around it: render the body with whatever templating you
use, and when sending should not hold up a request, send from a [queue](../queues/overview.md)
job. [Mail Configuration](configuration.md) covers the mailer's defaults,
[Mail Events and Telemetry](events-and-telemetry.md) covers what it reports,
and [Mail Drivers](drivers.md) compares the drivers.

## Creating a mailer

A mailer wraps a driver. During development the log driver is the easiest
choice, since it needs no account and no server. In your existing Go module,
install core first:

```sh
go get arkoren.dev/core
```

Import the facade and bundled log driver to construct a mailer without delivery:

```go
import (
	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/slogmail"
)

mailer := mail.New(slogmail.New())
```

`New` never fails and connects to nothing, and neither the mailer nor any mail
driver has lifecycle hooks, so there is nothing to register with the
application. Build the mailer once and share it: it is safe for concurrent use,
and drivers connect when a message is sent, not when they are created.

Messages need a sender. If they share one, replace that baseline construction
with a sender default:

```go
mailer := mail.New(slogmail.New(), func(sender *mail.Builder) {
	sender.From(mail.Address{Name: "Acme", Email: "no-reply@acme.test"})
})
```

Otherwise set `MessageBuilder.From` on each message. The sending examples below
use the shared default; the log driver records mail rather than delivering it.

Focused examples show the ordinary success path and leave error results visible
without routine error checks. Handle those errors in your application; the
[receipts and errors](#receipts-and-errors) section explains retry decisions.
Here `ctx` is your request or queue-job context. A send timeout can leave provider
acceptance unknown; blindly retrying can deliver twice.

## Sending a message

A welcome message needs recipients and a body; the mailer above supplies the
sender. Pass those required inputs to `mail.NewMessage`, then send the published
message. Optional settings belong in a `*mail.MessageBuilder` callback:

```go
message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{{Name: "Ada", Email: "ada@example.com"}},
}, mail.Body{Text: "Hello Ada, your account is ready."}, func(letter *mail.MessageBuilder) {
	letter.Subject("Welcome")
})
receipt, err := mailer.Send(ctx, message)
```

`mail.Recipients` needs at least one address across `To`, `CC`, and `BCC`.
Every address needs an `Email`; `Name` is the optional display name.
`mail.Body` needs nonempty `Text`, `HTML`, or both. Neither required input can
be supplied or repaired through the optional builder. The zero `mail.Message`
is invalid; use `NewMessage`, not a struct literal.

The result is immutable. Inspect it with `Subject()`, `TextBody()`, `HTMLBody()`,
`From()`, `To()`, `CC()`, `BCC()`, `ReplyTo()`, and `Attachments()` rather than
fields. Construction snapshots recipient slices; optional address slices and
attachment bytes are copied when set and again when published. Slice getters
return independent copies, including each attachment's `Data`. Editing your
input, a retained draft, or a getter result cannot change a published message.

### HTML alternatives

For clients that cannot render HTML, supply a text alternative alongside the
HTML. With the same `mail` import, construct the two representations together:

```go
message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{{Email: "ada@example.com"}},
}, mail.Body{
	Text: "Hello Ada, your account is ready.",
	HTML: "<p>Hello Ada, your account is ready.</p>",
})
```

Send this through the configured `mailer` as above. The representations are
alternatives of one message, not two sends. HTML-only mail is also valid: omit
`Text`. Arkoren does not generate one representation from the other or sanitize
HTML; escape untrusted values when rendering your template.

### Recipient groups

For a visible copy and a hidden audit recipient, put each address in its group
when constructing the message:

```go
message := mail.NewMessage(mail.Recipients{
	To:  []mail.Address{{Email: "ada@example.com"}},
	CC:  []mail.Address{{Email: "support@acme.test"}},
	BCC: []mail.Address{{Email: "audit@acme.test"}},
}, mail.Body{Text: "Your account is ready."})
```

`BCC` recipients receive the message but appear in no header the other
recipients see. Every driver is required to keep it that way, whether it writes
the MIME itself or maps the field to a provider API. BCC-only mail is valid:
omit `To` and `CC` and supply the addresses in `Recipients.BCC`.

An empty `Subject` is valid. A subject that contains a line break is not,
because a line break in a header is how header injection works. The same check
applies to display names and addresses, so user input in them cannot inject
extra headers. The development log driver can still disclose BCC addresses in
logs; hidden delivery headers are not a log-redaction policy.

### Attachments

A regular attachment is a file the recipient can save. With the PDF already
loaded into `invoicePDF []byte`, attach it during message construction:

```go
message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{{Email: "ada@example.com"}},
}, mail.Body{Text: "Your invoice is attached."}, func(letter *mail.MessageBuilder) {
	letter.Attachments(mail.Attachment{
		Filename:    "invoice.pdf",
		ContentType: "application/pdf",
		Data:        invoicePDF,
	})
})
```

Send this message through `mailer`. All three attachment fields are required,
and empty `Data` is rejected. `Attachments` replaces the whole list rather than
appending; pass all files in one call. The bytes are copied and
held in memory and encoded into the message, so mail is the wrong channel for
large files, and servers and providers enforce their own size limits. For anything big, store the file with
[file storage](../file-storage/overview.md) and send a [signed URL](../file-storage/signed-urls.md).

For a text file, `ContentType` can include a character set and other media-type
parameters. Inside the message callback, attach UTF-8 notes like this:

```go
letter.Attachments(mail.Attachment{
	Filename:    "notes.txt",
	ContentType: "text/plain; charset=UTF-8; format=flowed",
	Data:        []byte("Meeting notes.\n"),
})
```

SMTP and SES use `mail.EncodeMIME`, which preserves those parameter values.
`Filename` supplies both the MIME `name` parameter and the disposition's
`filename`, overriding any `name` in `ContentType`. Empty `ContentType` is invalid;
it does not select `application/octet-stream`. The encoder can reject metadata
that passed construction but cannot be formatted as MIME: it returns an error
wrapping `mail.ErrInvalidMessage` and no encoded bytes. SMTP and SES stop before
submission in that case. Other provider mappings differ; see the
[compatibility matrix](drivers.md#compatibility-matrix).

An inline attachment is an image or other part that the HTML body embeds. Set
its disposition to inline, give it a `ContentID`, and reference the same ID
from the HTML with a `cid:` URL. Check your driver's inline support in the
matrix before relying on it. With image bytes already loaded into `logoPNG`,
construct the inline part:

```go
message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{{Email: "ada@example.com"}},
}, mail.Body{HTML: `<p>Thanks!</p><img src="cid:logo.png">`}, func(letter *mail.MessageBuilder) {
	letter.Attachments(mail.Attachment{
		Filename:    "logo.png",
		ContentType: "image/png",
		Data:        logoPNG,
		Disposition: mail.AttachmentDispositionInline,
		ContentID:   "logo.png",
	})
})
```

A content ID is required for an inline attachment and forbidden on a regular
one, and it cannot contain spaces, angle brackets, or line breaks. Mailgun
requires the content ID to equal the
filename ([Mail Drivers](drivers.md#mailgun) explains why). Using filename-based
IDs meets that restriction. [Postmark](drivers.md#postmark) adds the `cid:` prefix
only in its API payload; keep the message's `ContentID` bare, as above.

### MIME encoding

SMTP and SES call `mail.EncodeMIME` before submission. Other API drivers map
message fields to their provider's format instead. If you need raw MIME yourself,
pass a complete message, including `From`, and an explicit Message-ID; encoding
does not apply mailer defaults. With such a `message` already constructed:

```go
contents, err := mail.EncodeMIME(message, "<receipt-123@acme.test>")
```

On success, `contents` contains independently owned message bytes. A message can
pass `Validate()` but fail MIME wire checks: every physical line, including each
header's name and prefix, must fit within **998 bytes excluding CRLF**. The
encoder retains existing address and encoded-word folding, but rejects oversized
plain headers or attachment metadata with `mail.ErrInvalidMessage` and no output
bytes. It does not truncate caller content. SMTP and SES stop before submission
on encoding failure.

For inline parts, each `multipart/related` container declares a `type` parameter
matching its actual root: `text/plain` for text only, `text/html` for HTML only,
or `multipart/alternative` for both representations. This also applies when the
related container is nested inside `multipart/mixed` alongside regular attachments.

The supplied Message-ID must use the modern, uncommented, unfolded RFC 5322 form:
angle brackets around a dot-atom left side, `@`, and a dot-atom or bracketed
no-fold-literal right side. Dot-atoms use nonempty ASCII components separated by
single dots. Empty IDs, whitespace, comments, obsolete quoted forms, controls,
and malformed syntax fail with `mail.ErrInvalidMessage` and no output bytes.
Valid supplied IDs are preserved verbatim, including case and punctuation.

To generate an ID, call `mail.NewMessageID("mail.acme.test")`. It validates the
domain using the same right-side syntax; malformed input returns an empty ID and
`mail.ErrInvalidMessage`. Valid domain bytes are preserved, except that a raw IPv6
address such as `2001:db8::1` becomes `[IPv6:2001:db8::1]` in the generated ID.
Neither entry point checks DNS or domain ownership. `NewMessageID` can also return
an entropy-source error; `EncodeMIME` separately enforces the physical-line limit.

## Defaults and the mailer

The mailer fills in two things before it validates a message. If `From` is
empty, it uses the mailer's configured sender; if `ReplyTo` is empty, it uses
the configured reply-to list. A value you set on the message always wins. The
mailer works on a copy, so the message you pass in is not changed. Drivers never
apply these defaults themselves, which means a message sent straight to a driver
must be complete. [Mail Configuration](configuration.md) shows how to set them.

Use `message.Err()` to check construction without requiring a sender that the
mailer may supply. `message.Validate()` instead checks a complete message,
including `From`, without applying defaults. Both wrap `mail.ErrInvalidMessage`
on failure. An omitted sender can therefore pass `Err()` but fail `Validate()`;
it can still be sent through a mailer with a valid default. If both omit the
sender, `Send` fails before driver submission. An explicitly invalid `From` is
a construction error, not a request to use the default.

Callbacks run once, synchronously in order on one draft, even if the required
inputs are invalid. Optional setters return the same draft and replace previous
values. The first construction error remains even if a later callback replaces
the bad value. Supplied nil callbacks and callback panics propagate; expanding
a nil or empty callback slice supplies no edits.

## Receipts and errors

A successful `Send` returns a receipt whose `ID` is an opaque acceptance
identifier: the generated `Message-ID` for SMTP and the log driver, and the
provider's own identifier for the API drivers. Log it or store it next to the
record that caused the mail, because it is what you quote to a provider's
support when a message goes missing.

A receipt from a sending driver means the server or provider accepted the
message, not that the recipient got it. The log driver returns a receipt without
submitting mail anywhere. After remote acceptance, delivery can still bounce,
be deferred, or land in spam. Learning about that takes the provider's webhooks
or an inbox you control. Optional [provider webhook adapters](drivers.md)
authenticate later notifications; they do not run automatically when you
construct a mailer.

An invalid message fails before any network call, with an error that wraps
`mail.ErrInvalidMessage`. A context canceled before the driver's checks fails
before submission, wrapping the context's error instead. Other submission
errors come from driver-specific restrictions, SMTP, or the provider. When a
driver wraps a native error, use `errors.Is` or `errors.As` to inspect its cause
rather than matching diagnostic text.

The original causes stay with the caller of `Send`. Failure notifications now
expose a bounded `mail.FailedEvent.ErrorClass` string instead of the former
`Err` field; they do not contain the returned error or provider diagnostics.
See [failure event classification and migration](events-and-telemetry.md#events)
before updating listeners. Returned errors can still contain sensitive data;
review them before logging or forwarding them.

A driver panic is different from a returned failure: `Mailer.Send` propagates
the original panic and publishes neither `mail.Sent` nor `mail.Failed`. It still
finishes its owned telemetry. Absence of an event is not proof that remote
acceptance did not happen; do not retry a panic as though it were a confirmed
rejection. [Mail Events and Telemetry](events-and-telemetry.md#failures) explains
what remains observable.

With imports `context` and `errors`, classify failures before choosing a retry
policy:

```go
_, err := mailer.Send(ctx, message)
switch {
case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
	return err // stop this attempt; acceptance can remain uncertain
case errors.Is(err, mail.ErrInvalidMessage):
	return err // a bug in the caller: fix the message, do not retry
case err != nil:
	return err // classify provider errors before choosing a retry
}
```

The distinction matters when you retry. An invalid message never becomes valid
by waiting, while a refused connection or a provider outage can. If you send
from a queue job, return a permanent failure for the first kind and let the
queue consider retries for transient failures. Authentication failures and
provider rejections need correction, not unconditional retries. There is also
a sharp edge in retrying at all: a
timeout can mean the provider accepted the message after you stopped waiting, so
a retry can send it twice. Mail has no idempotency key in the mailer; if
duplicates matter, use your application's delivery records and the provider's
idempotency support where available. A local record alone cannot make a remote
send and a database update atomic.

## Native access

Sometimes you need the concrete driver, for example to reach a provider
client. `DriverAs` finds the first driver of a type in the chain, and sees
through drivers that wrap another:

With import `arkoren.dev/driver/resend/resendmail`, retrieve the client only
when the configured mailer contains that driver:

```go
if driver, ok := mailer.DriverAs[*resendmail.Driver](); ok {
	_ = driver.Client() // *resend.Client; use for provider-specific operations
}
```

Only some drivers expose a client; [Mail Drivers](drivers.md) says which.

## Testing

`mailtest.Fake` is a driver that records messages instead of sending them. It
validates like a real driver, so a test that sends an invalid message still
fails, and it returns receipts with sequential IDs. In a test with
`t *testing.T`, import `arkoren.dev/core/service/mail/mailtest` and exercise your
application's welcome message path. This focused test sends directly; in an
application test, invoke the application function with the same `mailer`:

```go
fake := mailtest.New()
mailer := mail.New(fake, func(mailer *mail.Builder) {
	mailer.From(mail.Address{Email: "no-reply@acme.test"})
})

message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{{Email: "ada@example.com"}},
}, mail.Body{Text: "Your account is ready."}, func(letter *mail.MessageBuilder) {
	letter.Subject("Welcome")
})
if _, err := mailer.Send(t.Context(), message); err != nil {
	t.Fatal(err)
}

fake.AssertSentCount(t, 1)
fake.AssertSent(t, func(message mail.Message) bool {
	return message.Subject() == "Welcome"
}, func(message mail.Message) bool {
	recipients := message.To()
	return len(recipients) == 1 && recipients[0].Email == "ada@example.com"
})
```

The assertions take predicates over the recorded `mail.Message`, and every
predicate must hold for the same message. `Sent()` returns an independent
history slice; its messages are immutable and their getters return detached
copies. This proves a submission request, not provider delivery. To test how
your code reacts to a failing provider,
call `fake.FailNext(err)` to fail one send, or `fake.FailWith(err)` to fail
every send until you call `FailWith(nil)`.

If you write a driver, `mailtest.Run` is the conformance suite it must pass.
See [Writing a Driver](../drivers/writing-a-driver.md) and [Testing](../testing/overview.md).

For every function and option, see [the `mail` API reference](https://pkg.go.dev/arkoren.dev/core/service/mail),
[`slogmail`](https://pkg.go.dev/arkoren.dev/core/service/mail/slogmail), and the pages for each
[driver](drivers.md).
