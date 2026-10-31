# Mail portability

`arkoren.dev/core/service/mail` defines portable message fields. Drivers can impose provider
constraints that callers must account for.

| Capability | Portable contract | Mailgun limitation |
| --- | --- | --- |
| Inline attachments | `ContentID` identifies HTML `cid:` content; `ContentType` describes attachment data. | `ContentID` must equal `Filename`, and the Mailgun SDK selects the uploaded content type instead of preserving `ContentType`. |

Install with `go get arkoren.dev/core`. Start with
[sending a message](../../../cmd/arkoren/skills/arkoren/references/guides/mail/overview.md),
then choose a [mail driver](../../../cmd/arkoren/skills/arkoren/references/guides/mail/drivers.md).
A successful send is acceptance, not delivery. The facade does not retry, queue,
or register provider notification endpoints automatically.

## MIME wire constraints

SMTP and SES use `mail.EncodeMIME`; provider API mappings are separate. The
encoder preserves attachment media-type parameters and declares the actual root
`type` for every `multipart/related` container. With both text and HTML bodies,
that root is `multipart/alternative`, including inside `multipart/mixed`.

Every emitted physical line is limited to 998 bytes excluding CRLF. The encoder
keeps its encoded-word and recipient-list folding. If a subject, individual
mailbox, filename, or other header still exceeds the limit, encoding returns
`ErrInvalidMessage` and no bytes rather than truncating or rewriting your data.
Construction can therefore succeed while MIME encoding fails. This is a wire
limit, not a new universal provider limit on `Message` declarations. Native MIME
readers accepting a message do not prove its physical-line conformance.

Supply an uncommented, unfolded modern RFC 5322 Message-ID: `<left@right>`, where
`left` is dot-atom-text and `right` is dot-atom-text or a bracketed no-fold-literal.
The encoder rejects empty IDs, empty dot components, whitespace, comments,
obsolete quoted forms, controls, and non-ASCII IDs with `ErrInvalidMessage` and
no bytes. Valid supplied IDs remain byte-for-byte unchanged.

`mail.NewMessageID` generates a random left side and validates the supplied
right-side syntax before generation. Raw IPv6 addresses become `[IPv6:address]`;
other accepted domain bytes, including case and existing brackets, are preserved.
Syntax validation does not check DNS, domain ownership, or provider policy.

## Observing failures without exposing recipients

Configure `mail.Builder.Events` to observe returned outcomes. `mail.FailedEvent`
contains `ErrorClass`, `Recipients`, and `Attachments`, not the diagnostic error.
The classification is `context.Canceled`, `context.DeadlineExceeded`,
`mail.ErrInvalidMessage`, or `error` for another failure, in that priority order.
It does not establish whether a provider accepted or delivered the message.

For an existing `bus *event.Bus`, with imports `context` and
`arkoren.dev/core/service/mail`, a listener can select an action without receiving
SMTP diagnostics that might name a hidden recipient.
`alerts.Notify(context.Context, string) error` is your
application-owned notification service:

```go
stop, err := bus.Listen(mail.Failed, func(ctx context.Context, observed mail.FailedEvent) error {
	if observed.ErrorClass == "mail.ErrInvalidMessage" {
		return nil // Correct construction at the sending call site.
	}
	return alerts.Notify(ctx, observed.ErrorClass) // Application-owned safe alert.
})
defer stop()
```

Migrate listeners that previously read `FailedEvent.Err` to `ErrorClass`; that
field is intentionally removed rather than retaining sensitive causes. The error
returned by `Mailer.Send` still preserves its original causes for `errors.Is` and
`errors.As`. Treat those diagnostics as sensitive at the call site. Driver panics
still propagate without either outcome event, and `Sent` still means acceptance,
not delivery.
