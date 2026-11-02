---
title: Composing Messages
description: Build immutable email messages with recipient groups, alternative bodies, attachments, and inline images.
order: 30
summary:
  - Compose recipient groups and text or HTML alternatives from authorized application data.
  - Add downloadable files and inline images while respecting byte ownership and provider mappings.
  - Inspect immutable messages and distinguish construction errors from complete-message validation.
  - Encode raw MIME with explicit sender and Message-ID values and recognize wire-format limits.
---

# Composing Messages

A report-ready email can start as plain text and later gain HTML or an attached
file without changing how you submit it. Build those choices into a
`mail.Message` with `mail.NewMessage`. Recipients and body are required inputs;
the construction callback sets optional sender, subject, reply-to, and attachments.

Examples import `arkoren.dev/core/service/mail` and assume a
[configured mailer](drivers.md#connecting-mailers). Send each completed value
with `mailer.Send(ctx, message)` as shown in [Mail Overview](overview.md#sending-mail).
These are focused composition variants, not consecutive declarations to paste
into one scope.

## Message Inputs

Use application data that already identifies an authorized recipient and the
content they may receive. Mailbox syntax validation does not authorize sending
a private report:

```go
type ReportEmail struct {
	Recipient mail.Address
	Subject   string
	Text      string
	HTML      string
}

notice := ReportEmail{
	Recipient: mail.Address{Name: "Ada", Email: "ada@example.com"},
	Subject:   "Your weekly report",
	Text:      "Your weekly report is ready in Acme.",
	HTML:      "<p>Your weekly report is ready in Acme.</p>",
}
```

Start with the text representation and optional subject:

```go
message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{notice.Recipient},
}, mail.Body{Text: notice.Text}, func(letter *mail.MessageBuilder) {
	letter.Subject(notice.Subject)
})
err := message.Err()
```

`Err()` reports the first construction error, wrapping `mail.ErrInvalidMessage`,
without requiring a sender that the mailer can supply. At least one recipient
across all groups and one nonempty body representation are required. Neither
can be repaired by the optional builder. The zero message is invalid.

`Subject` may be empty, but CR or LF is rejected. Each `mail.Address` requires
a bare mailbox in `Email`; `Name` is an optional display name. Address and display
name line breaks are rejected to prevent header injection. No DNS lookup or
recipient-ownership check is performed.

## Recipient Groups

When support should see a copy while an audit recipient stays out of visible
headers, select the groups explicitly:

```go
recipients := mail.Recipients{
	To:  []mail.Address{notice.Recipient},
	CC:  []mail.Address{{Email: "support@acme.test"}},
	BCC: []mail.Address{{Email: "audit@acme.test"}},
}
message := mail.NewMessage(recipients, mail.Body{Text: notice.Text})
```

`CC` is visible to recipients; `BCC` is not placed in visible delivery headers.
BCC-only mail is valid. This boundary does not conceal addresses from the
provider, your application, or the [Log driver](drivers.md#log), whose summary
includes BCC. Treat recipient lists as sensitive data.

## Body Alternatives

If your template produces HTML, provide plain text too for clients that cannot
render it. Render and escape untrusted values before construction; Arkoren does
not render templates or sanitize HTML:

```go
message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{notice.Recipient},
}, mail.Body{Text: notice.Text, HTML: notice.HTML})
```

These are two representations of one email, not two submissions. Text-only
and HTML-only messages are also valid. Arkoren does not derive one representation
from the other; getters return exactly the supplied strings.

## File Attachments

If the report is small enough for your provider's encoded-message limit, attach
its already-loaded bytes. Here `reportPDF` is an application-owned `[]byte`
containing the authorized report:

```go
message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{notice.Recipient},
}, mail.Body{Text: "Your weekly report is attached."}, func(letter *mail.MessageBuilder) {
	letter.Attachments(mail.Attachment{
		Filename:    "weekly-report.pdf",
		ContentType: "application/pdf",
		Data:        reportPDF,
	})
})
```

Filename, content type, and nonempty unencoded data are all required. Empty
`ContentType` does not select `application/octet-stream`. Omitted disposition
becomes `mail.AttachmentDispositionAttachment`, a downloadable file.
`Attachments` replaces the whole list; pass all files in one call rather than
calling it repeatedly to append.

Construction copies attachment records and bytes, so later changes to
`reportPDF` cannot change the message. That copying and encoding consume memory;
for large reports, use [file storage](../file-storage/overview.md) and send an
[authorized signed URL](../file-storage/signed-urls.md) instead. Keep confidential
files out of diagnostic dumps even after construction.

Media types can include parameters such as `text/plain; charset=UTF-8`.
SMTP and SES preserve parameters through MIME encoding, except that `Filename`
supplies the `name` parameter as well as the disposition filename. Resend and
Postmark forward media types to their APIs; Mailgun's SDK selects them instead.
Check the [compatibility matrix](drivers.md#compatibility-matrix) before relying
on identical wire representations.

## Inline Images

To embed a logo rather than offer it only as a download, give the inline
attachment a bare content ID and reference it through a matching HTML `cid:` URL.
Here `logoPNG` contains an already-loaded image:

```go
message := mail.NewMessage(mail.Recipients{
	To: []mail.Address{notice.Recipient},
}, mail.Body{HTML: `<p>Your report is ready.</p><img src="cid:logo.png">`},
	func(letter *mail.MessageBuilder) {
		letter.Attachments(mail.Attachment{
			Filename:    "logo.png",
			ContentType: "image/png",
			Data:        logoPNG,
			Disposition: mail.AttachmentDispositionInline,
			ContentID:   "logo.png",
		})
	})
```

An inline part requires `ContentID`; a regular attachment must omit it. Content
IDs cannot contain CR, LF, spaces, or angle brackets. Supply the bare ID, not
the `cid:` URL or a bracketed header value.

Using the filename as the ID also meets [Mailgun's restriction](drivers.md#attachment-mapping).
[Postmark](drivers.md#postmark) adds `cid:` only in its native payload; your
message keeps the bare value. Driver acceptance does not guarantee that a
particular mail client displays the image.

## Immutable Values

A published message has getters, not mutable fields or setters. Read
`From()`, `To()`, `CC()`, `BCC()`, `ReplyTo()`, `Subject()`, `TextBody()`,
`HTMLBody()`, or `Attachments()`; construct another message to change content.
Recipient lists are captured before callbacks. Optional lists and attachment
bytes are copied when set and again at publication. Slice getters return
independent copies, including each attachment's bytes.

With a message that has at least one attachment, preparing an export copy does
not rewrite the original message:

```go
files := message.Attachments()
files[0].Filename = "download.pdf"
```

Only `files` changes; sending `message` still uses its original filename and bytes.
Retaining the construction draft cannot change the published value either.

Callbacks run once, synchronously in order on a shared draft, even when required
inputs are invalid. Optional setters replace previous values, but the first
construction error remains sticky after replacement. Fix invalid input by
constructing a new message, not by overwriting a bad setting later. Supplied nil
callbacks and callback panics propagate; expanding a nil or empty callback slice
supplies no edits.

`Validate()` checks a complete message including the sender, without applying
defaults. An omitted sender can pass `Err()` but fail `Validate()` until the
mailer completes it. Direct driver sends and MIME encoding require the complete
form. An explicitly invalid `MessageBuilder.From` is a construction error, not
a request to fall back to a [sender default](configuration.md).

## MIME Encoding

SMTP and SES encode MIME automatically. If an integration needs raw bytes,
construct a message with an explicit valid sender and pass a modern RFC 5322
Message-ID; this function does not apply mailer defaults:

```go
contents, err := mail.EncodeMIME(message, "<weekly-report-42@acme.test>")
```

On success, `contents` is newly owned sensitive message data. BCC is excluded
from visible headers. Text and HTML bodies are quoted-printable encoded, and
related multipart containers declare their actual root media type: text/plain,
text/html, or multipart/alternative. This also holds when regular attachments
place that container inside multipart/mixed.

Validation alone does not guarantee encoding. Unformattable metadata or any
physical line longer than **998 bytes excluding CRLF** returns an error wrapping
`mail.ErrInvalidMessage` and no output. Existing address and encoded-word folding
is retained; oversized plain headers and attachment metadata are rejected, not
truncated or rewritten to fit. SMTP and SES stop before submission on these errors.

The supplied Message-ID must be unfolded and uncommented, with angle brackets
around a dot-atom left side, `@`, and a dot-atom or bracketed no-fold-literal
right side. Dot-atoms contain nonempty ASCII components separated by single
dots. Empty IDs, whitespace, comments, controls, obsolete quoted forms, and
malformed syntax fail with `mail.ErrInvalidMessage` and no bytes. Accepted IDs
are preserved verbatim, including case and punctuation.

To generate an ID, call `mail.NewMessageID("mail.acme.test")`. It validates the
domain with the same right-side syntax and returns an empty ID with
`mail.ErrInvalidMessage` for malformed input. Valid domain bytes are preserved,
except raw IPv6 is wrapped as `[IPv6:address]`. Neither function checks DNS or
domain ownership; random-ID generation can also return an entropy-source error.
`EncodeMIME` checks the physical-line limit separately.

For every function and option, see the
[`mail` API reference](https://pkg.go.dev/arkoren.dev/core/service/mail).
