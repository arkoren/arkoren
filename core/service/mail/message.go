package mail

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// Recipients supplies the required delivery destinations to [NewMessage]. At
// least one address across the three lists is required; construction copies them.
//
// Example:
//
//	recipients := mail.Recipients{To: []mail.Address{{Email: "person@example.com"}}}
//	message := mail.NewMessage(recipients, mail.Body{Text: "Hello."})
//	_ = message.Err()
type Recipients struct {
	// To contains primary recipients. NewMessage snapshots the slice.
	//
	// Example:
	//
	//	recipients := mail.Recipients{To: []mail.Address{{Email: "person@example.com"}}}
	To []Address
	// CC contains visible carbon-copy recipients, independently of To.
	//
	// Example:
	//
	//	recipients := mail.Recipients{CC: []mail.Address{{Email: "copy@example.com"}}}
	CC []Address
	// BCC contains hidden recipients, never exposed in visible MIME headers.
	// A message may have only BCC recipients.
	//
	// Example:
	//
	//	recipients := mail.Recipients{BCC: []mail.Address{{Email: "hidden@example.com"}}}
	BCC []Address
}

// Body supplies the required message content to [NewMessage]. At least one
// representation must be nonempty; HTML is not generated from Text or vice versa.
//
// Example:
//
//	message := mail.NewMessage(recipients, mail.Body{Text: "Hello.", HTML: "<p>Hello.</p>"})
//	_ = message.Err()
type Body struct {
	// Text is the plain-text representation; empty is valid when HTML is set.
	//
	// Example:
	//
	//	body := mail.Body{Text: "Hello."}
	Text string
	// HTML is the HTML representation; empty is valid when Text is set.
	//
	// Example:
	//
	//	body := mail.Body{HTML: "<p>Hello.</p>"}
	HTML string
}

// Message is an immutable transactional email declaration. Construct it with
// [NewMessage]; its zero value is invalid. Slice getters return detached copies,
// including attachment data. Copies of Message safely share private storage.
//
// Example:
//
//	message := mail.NewMessage(mail.Recipients{
//		To: []mail.Address{{Email: "person@example.com"}},
//	}, mail.Body{Text: "Hello."})
//	if err := message.Err(); err != nil {
//		return err
//	}
type Message struct {
	// from is the explicit sender or the sender completed by Mailer.Send.
	from Address
	// to owns the primary-recipient snapshot.
	to []Address
	// cc owns the visible-copy snapshot.
	cc []Address
	// bcc owns the hidden-recipient snapshot.
	bcc []Address
	// replyTo owns the optional reply destinations.
	replyTo []Address
	// subject is the optional single-line subject.
	subject string
	// textBody stores the positional plain-text representation.
	textBody string
	// htmlBody stores the positional HTML representation.
	htmlBody string
	// attachments owns attachment records and their payload bytes.
	attachments []Attachment
	// err preserves the first invalid positional or optional input.
	err error
}

// MessageBuilder edits optional message settings during [NewMessage]. Required
// recipients and content are positional and cannot be replaced by a callback.
// Retaining this draft cannot change a published Message.
//
// Example:
//
//	message := mail.NewMessage(recipients, body, func(letter *mail.MessageBuilder) {
//		letter.Subject("Welcome").From(mail.Address{Email: "sender@example.com"})
//	})
//	_ = message.Err()
type MessageBuilder struct {
	// message is unpublished state, detached again when construction finishes.
	message Message
}

// MessageBuilderFunc configures optional settings on one construction draft.
// Callbacks run directly once in order, including for invalid declarations.
//
// Example:
//
//	var policy mail.MessageBuilderFunc = func(letter *mail.MessageBuilder) {
//		letter.Subject("Welcome")
//	}
//	message := mail.NewMessage(recipients, body, policy)
//	_ = message.Err()
type MessageBuilderFunc = func(*MessageBuilder)

// NewMessage snapshots recipients and body, then invokes configure directly once,
// synchronously in order on one fresh draft. Nil entries and application panics
// propagate; expanded nil or empty slices omit edits. Positional validation runs
// first, recipients before body. The first domain error stays sticky even when a
// later optional setting replaces its value. Publication detaches draft storage.
// Omitted From is valid here because [Mailer.Send] may supply it; [Message.Validate]
// requires a completed sender before submission. Construction performs no I/O.
//
// Example:
//
//	message := mail.NewMessage(mail.Recipients{
//		To: []mail.Address{{Email: "person@example.com"}},
//	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
//		letter.Subject("Welcome")
//	})
//	if err := message.Err(); err != nil {
//		return err
//	}
func NewMessage(recipients Recipients, body Body, configure ...MessageBuilderFunc) Message {
	letter := &MessageBuilder{message: Message{
		to: slices.Clone(recipients.To), cc: slices.Clone(recipients.CC), bcc: slices.Clone(recipients.BCC),
		textBody: body.Text, htmlBody: body.HTML,
	}}
	letter.record(validateRecipients(recipients))

	if body.Text == "" && body.HTML == "" {
		letter.record(fmt.Errorf("%w: a text or HTML body is required", ErrInvalidMessage))
	}

	for _, configure := range configure {
		configure(letter)
	}

	return cloneMessage(letter.message)
}

// From replaces the explicit sender and validates it immediately. Omit this
// setting to use mailer defaults; an explicitly empty address is invalid.
//
// Example:
//
//	message := mail.NewMessage(recipients, body, func(letter *mail.MessageBuilder) {
//		letter.From(mail.Address{Email: "sender@example.com"})
//	})
//	_ = message.Err()
func (letter *MessageBuilder) From(from Address) *MessageBuilder {
	letter.message.from = from
	letter.record(validateAddresses([]Address{from}, "from address"))

	return letter
}

// Subject replaces the optional subject. Empty is valid; CR or LF records a
// sticky domain error to prevent injection into message headers.
// Construction imposes no provider length limit. EncodeMIME rejects a physical
// header line over 998 bytes if the existing encoded-word folding cannot fit it;
// it does not truncate or rewrite plain subject text to force acceptance.
//
// Example:
//
//	message := mail.NewMessage(recipients, body, func(letter *mail.MessageBuilder) {
//		letter.Subject("Welcome")
//	})
//	_ = message.Subject()
func (letter *MessageBuilder) Subject(subject string) *MessageBuilder {
	letter.message.subject = subject

	if strings.ContainsAny(subject, "\r\n") {
		letter.record(fmt.Errorf("%w: subject contains a line break", ErrInvalidMessage))
	}

	return letter
}

// ReplyTo replaces and snapshots reply destinations. Empty clears the setting,
// allowing mailer defaults. Invalid addresses remain errors after replacement.
//
// Example:
//
//	message := mail.NewMessage(recipients, body, func(letter *mail.MessageBuilder) {
//		letter.ReplyTo(mail.Address{Email: "support@example.com"})
//	})
//	_ = message.ReplyTo()
func (letter *MessageBuilder) ReplyTo(addresses ...Address) *MessageBuilder {
	letter.message.replyTo = slices.Clone(addresses)
	letter.record(validateAddresses(addresses, "reply-to address"))

	return letter
}

// Attachments replaces and deeply snapshots the attachment records and payloads.
// Empty clears the list. Invalid MIME metadata or empty data records an error.
//
// Example:
//
//	message := mail.NewMessage(recipients, body, func(letter *mail.MessageBuilder) {
//		letter.Attachments(mail.Attachment{
//			Filename: "report.txt", ContentType: "text/plain", Data: []byte("report"),
//		})
//	})
//	_ = message.Attachments()
func (letter *MessageBuilder) Attachments(attachments ...Attachment) *MessageBuilder {
	letter.message.attachments = cloneAttachments(attachments)

	for _, attachment := range letter.message.attachments {
		if err := validateAttachment(attachment); err != nil {
			letter.record(fmt.Errorf("%w: invalid attachment: %w", ErrInvalidMessage, err))
		}
	}

	return letter
}

// record keeps the first domain error, including errors from replaced settings.
// Callback execution is never skipped because an earlier input was invalid.
func (letter *MessageBuilder) record(err error) {
	if letter.message.err == nil {
		letter.message.err = err
	}
}

// From returns the sender, or the zero address when omitted before completion.
// It contains only strings and can be changed without affecting the message.
//
// Example:
//
//	sender := message.From()
//	_ = sender.Email
func (message Message) From() Address {
	return message.from
}

// To returns a detached primary-recipient slice. Empty lists return nil when
// constructed from nil; editing the result never changes the message.
//
// Example:
//
//	recipients := message.To()
//	_ = len(recipients)
func (message Message) To() []Address {
	return slices.Clone(message.to)
}

// CC returns detached visible-copy recipients. Their order matches construction;
// editing or appending to the result does not change the message.
//
// Example:
//
//	copies := message.CC()
//	_ = len(copies)
func (message Message) CC() []Address {
	return slices.Clone(message.cc)
}

// BCC returns detached hidden recipients in construction order. Drivers must
// deliver to them without exposing them in visible headers.
//
// Example:
//
//	hidden := message.BCC()
//	_ = len(hidden)
func (message Message) BCC() []Address {
	return slices.Clone(message.bcc)
}

// ReplyTo returns detached reply destinations in construction order. Empty means
// Mailer.Send may supply its defaults; modifying the result is safe.
//
// Example:
//
//	replies := message.ReplyTo()
//	_ = len(replies)
func (message Message) ReplyTo() []Address {
	return slices.Clone(message.replyTo)
}

// Subject returns the optional subject without MIME encoding. Empty is valid;
// invalid declarations can still be inspected, so check Err before use.
//
// Example:
//
//	subject := message.Subject()
//	_ = subject
func (message Message) Subject() string {
	return message.subject
}

// TextBody returns the plain-text representation unchanged. It may be empty for
// an HTML-only declaration; no conversion from HTML is performed.
//
// Example:
//
//	text := message.TextBody()
//	_ = text
func (message Message) TextBody() string {
	return message.textBody
}

// HTMLBody returns the HTML representation unchanged. It may be empty for a
// text-only declaration; no conversion from text is performed.
//
// Example:
//
//	html := message.HTMLBody()
//	_ = html
func (message Message) HTMLBody() string {
	return message.htmlBody
}

// Attachments returns detached attachment records, including independent Data
// bytes for each one. Modifying the returned records or bytes is safe.
//
// Example:
//
//	files := message.Attachments()
//	_ = len(files)
func (message Message) Attachments() []Attachment {
	return cloneAttachments(message.attachments)
}

// Err returns the first construction error, wrapping [ErrInvalidMessage], or nil
// for a valid declaration. Unlike Validate, it permits an omitted From. The zero
// Message is invalid because it has no recipients.
//
// Example:
//
//	if err := message.Err(); err != nil {
//		return err
//	}
func (message Message) Err() error {
	if message.err != nil {
		return message.err
	}

	if len(message.to)+len(message.cc)+len(message.bcc) == 0 {
		return fmt.Errorf("%w: at least one recipient is required", ErrInvalidMessage)
	}

	return nil
}

// Validate checks the declaration and completed sender and reply-to addresses
// without I/O. Use Err before mailer default completion; direct drivers and MIME
// encoding require Validate. Errors wrap [ErrInvalidMessage].
//
// Example:
//
//	if err := message.Validate(); err != nil {
//		return err
//	}
func (message Message) Validate() error {
	if err := message.Err(); err != nil {
		return err
	}

	if err := validateAddresses([]Address{message.from}, "from address"); err != nil {
		return err
	}

	return validateAddresses(message.replyTo, "reply-to address")
}

// validateRecipients checks required destinations in To, CC, BCC order without
// appending into caller slices. Body validation follows this entire step.
func validateRecipients(recipients Recipients) error {
	if len(recipients.To)+len(recipients.CC)+len(recipients.BCC) == 0 {
		return fmt.Errorf("%w: at least one recipient is required", ErrInvalidMessage)
	}

	for _, addresses := range [][]Address{recipients.To, recipients.CC, recipients.BCC} {
		if err := validateAddresses(addresses, "recipient"); err != nil {
			return err
		}
	}

	return nil
}

// validateAddresses supplies a portable domain error with the address role, but
// never includes an address value in the error message.
func validateAddresses(addresses []Address, role string) error {
	for _, address := range addresses {
		if err := validateAddress(address); err != nil {
			return fmt.Errorf("%w: invalid %s: %w", ErrInvalidMessage, role, err)
		}
	}

	return nil
}

// cloneAttachments owns both the records and their payloads at capture,
// publication and getter boundaries. Strings require no additional copying.
// Empty dispositions normalize to downloadable attachments on the copied records.
func cloneAttachments(attachments []Attachment) []Attachment {
	result := slices.Clone(attachments)

	for index := range result {
		result[index].Data = slices.Clone(result[index].Data)
		result[index].Disposition = cmp.Or(result[index].Disposition, AttachmentDispositionAttachment)
	}

	return result
}

// cloneMessage detaches all owned containers before publishing a draft. The
// stored error remains shared and callbacks are neither retained nor replayed.
func cloneMessage(message Message) Message {
	message.to = slices.Clone(message.to)
	message.cc = slices.Clone(message.cc)
	message.bcc = slices.Clone(message.bcc)
	message.replyTo = slices.Clone(message.replyTo)
	message.attachments = cloneAttachments(message.attachments)

	return message
}
