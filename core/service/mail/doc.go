// Package mail provides portable transactional email submission and MIME encoding.
//
// # Messages
//
// NewMessage takes required Recipients and Body records, followed by optional
// MessageBuilder callbacks for From, Subject, ReplyTo and Attachments. At least
// one recipient across To, CC and BCC and one text or HTML representation are
// required. Callbacks run directly once, synchronously in order, even for invalid
// inputs; nil entries panic. Err preserves the first domain error. Omitted From
// is valid during construction because Mailer.Send can supply its default;
// Validate, CheckSend and EncodeMIME require the completed sender. The zero
// Message is invalid.
//
//	message := mail.NewMessage(mail.Recipients{
//		To: []mail.Address{{Email: "person@example.com"}},
//	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
//		letter.Subject("Welcome")
//	})
//	if err := message.Err(); err != nil {
//		return err
//	}
//	if _, err := mailer.Send(ctx, message); err != nil {
//		return err
//	}
//
// Message owns its address lists, attachment records and attachment bytes.
// Construction detaches inputs and the completed draft; slice getters detach
// their results. Retained drafts and getter output cannot change a publication.
// Send completes defaults in a copy without replaying callbacks or mutating the
// caller's message. Drivers keep BCC destinations out of visible headers.
//
// # MIME encoding
//
// [EncodeMIME] serializes a message with a completed sender without applying
// mailer defaults. Attachment ContentType accepts media-type parameters, such
// as "text/plain; charset=UTF-8"; encoding preserves them except that Filename
// overrides any supplied "name" parameter and sets the disposition's filename.
// An empty ContentType is invalid, not an implicit application/octet-stream.
// Metadata that cannot be formatted as MIME returns an error wrapping
// [ErrInvalidMessage] and no encoded bytes. SMTP and SES drivers use this
// encoder; other provider mappings can have different attachment limits.
// Related multipart containers declare the actual root type: text/plain,
// text/html, or multipart/alternative for dual bodies, including nested containers.
// Every encoded physical line is limited to 998 bytes excluding CRLF. Existing
// encoded-word and recipient-list folding is retained; lines that still exceed
// the limit return ErrInvalidMessage and no partial bytes. Caller text, names,
// and attachment metadata are not truncated or rewritten to force acceptance.
// Supplied Message-IDs must use the uncommented, unfolded modern RFC 5322 form
// described by EncodeMIME and are preserved verbatim. NewMessageID validates its
// domain syntax and formats raw IPv6 addresses as [IPv6:address]. Neither API
// establishes domain ownership or provider acceptance.
// See [EncodeMIME] for an example.
//
// # Telemetry
//
// With [Builder.Telemetry], or [telemetry.Default] when it is not set, each
// [Mailer.Send] with a nonnil context emits a client span "mail send" with
// recipient and attachment counts, and the arkoren.mail.operations and
// arkoren.mail.operation.duration metrics, for every driver. These count
// attempts, not confirmed acceptances. A driver panic still ends the mailer's
// own span and records the attempt, without ending the caller's span or
// recording the panic value. A panic alone sets neither error.type nor an error
// status. Addresses, subject, body, attachments, headers and receipts are never
// recorded. Disabled telemetry costs nothing.
//
//	mailer := mail.New(driver, func(mailer *mail.Builder) {
//		mailer.Telemetry(app.Telemetry())
//	})
//
// # Events
//
// With [Builder.Events], [Mailer.Send] publishes [Sent] ("mail.sent") with the
// receipt after driver acceptance, or [Failed] ("mail.failed") with a bounded
// class for a returned validation or driver error. A nil context fails before
// telemetry or events. Send's returned error preserves the original causes.
// A driver panic propagates unchanged and publishes neither event; it is not
// converted into a returned error. Failure payloads contain only counts and
// [FailedEvent.ErrorClass], never diagnostic text, addresses, or message content.
// Classification does not establish whether a provider accepted a submission.
//
//	mailer := mail.New(driver, func(mailer *mail.Builder) {
//		mailer.Events(app.Events())
//	})
//
// New takes Builder policies for From, ReplyTo, Telemetry and Events. Policies
// run directly once in order on a fresh draft; nil entries panic and expanded
// nil/empty slices supply no edits. ReplyTo replaces and copies the input list;
// publication copies it again. Mailer.Config reports copied defaults, not a
// construction input. Drivers and reporting services remain shared dependencies.
package mail
