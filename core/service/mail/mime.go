package mail

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"

	"arkoren.dev/core/service/mail/internal/mime"
)

// EncodeMIME validates message and serializes it as an RFC 5322 MIME message.
// BCC recipients are intentionally excluded from the visible message headers.
// Bodies are quoted-printable encoded, which keeps CRLF line endings and
// lines within the SMTP limit.
// Attachment media-type parameters are preserved, except that the "name"
// parameter uses [Attachment.Filename], matching Content-Disposition's filename.
// Invalid attachment metadata, including values that cannot be formatted as
// MIME, returns an error wrapping [ErrInvalidMessage] and no output bytes.
// The message must include From; mailer defaults are not applied here.
// Successful calls return newly owned bytes, independent of message and other
// encodings. The output contains message content and must be handled accordingly.
//
// messageID must be an uncommented, unfolded modern RFC 5322 msg-id: angle
// brackets enclosing a dot-atom left side, @, and a dot-atom or bracketed
// no-fold-literal right side. Supplied IDs are preserved verbatim; empty IDs,
// whitespace, comments, obsolete quoted forms, and invalid syntax return
// ErrInvalidMessage and no bytes. Domain ownership is not verified.
//
// Every emitted physical line must fit within 998 bytes excluding CRLF. Longer
// lines return ErrInvalidMessage and no bytes. Existing encoded-word/address
// folding is retained, but oversized plain headers are rejected rather than
// rewriting caller content or metadata.
// Related multipart containers declare the actual root media type, including
// multipart/alternative when the message contains both text and HTML bodies.
//
// Example:
//
//	message := mail.NewMessage(mail.Recipients{
//		To: []mail.Address{{Email: "reader@example.com"}},
//	}, mail.Body{Text: "Notes attached."}, func(letter *mail.MessageBuilder) {
//		letter.From(mail.Address{Email: "sender@example.com"})
//		letter.Attachments(mail.Attachment{
//			Filename: "notes.txt", ContentType: "text/plain; charset=UTF-8",
//			Data: []byte("meeting notes"),
//		})
//	})
//	contents, err := mail.EncodeMIME(message, "<notes@example.com>")
//	if err != nil {
//		return err
//	}
//	_ = contents
func EncodeMIME(message Message, messageID string) ([]byte, error) {
	if err := message.Validate(); err != nil {
		return nil, err
	}

	if !mime.ValidID(messageID) {
		return nil, fmt.Errorf("%w: invalid message ID syntax", ErrInvalidMessage)
	}

	return mime.Render(projectMIME(message), messageID, ErrInvalidMessage)
}

// NewMessageID builds a randomized Message-ID with 128 bits of random identity.
// domain must be RFC 5322 dot-atom-text or a bracketed no-fold-literal. Raw IPv6
// addresses use [IPv6:address] notation; other accepted domain bytes are preserved
// verbatim. The tag avoids the untagged IPv6 form rejected by Go's mailbox parser.
// Empty input, empty dot components, whitespace, controls, and other malformed
// syntax return an empty ID and ErrInvalidMessage. This validates outbound syntax,
// not DNS, domain ownership, global uniqueness, or provider-specific limits.
// EncodeMIME separately checks the physical-line limit; entropy errors propagate.
//
// Example:
//
//	id, err := mail.NewMessageID("mail.example.com")
//	if err != nil {
//		return err
//	}
//	contents, err := mail.EncodeMIME(message, id)
//	if err != nil {
//		return err
//	}
//	_ = contents
func NewMessageID(domain string) (string, error) {
	if address, err := netip.ParseAddr(domain); err == nil && address.Is6() {
		domain = "[IPv6:" + domain + "]"
	}

	// A fixed valid left side reuses the exact wire grammar for id-right input
	// validation, before entropy is requested or any identifier is published.
	if !mime.ValidID("<id@" + domain + ">") {
		return "", fmt.Errorf("%w: invalid message ID domain", ErrInvalidMessage)
	}

	value := make([]byte, 16)

	if _, err := rand.Read(value); err != nil {
		return "", err
	}

	return "<" + hex.EncodeToString(value) + "@" + domain + ">", nil
}

// projectMIME borrows immutable message bytes for one synchronous render. BCC
// remains an envelope concern and cannot enter the visible-content projection.
func projectMIME(message Message) mime.Input {
	attachments := make([]mime.Attachment, len(message.attachments))

	for index, attachment := range message.attachments {
		attachments[index] = mime.Attachment{
			Filename:    attachment.Filename,
			ContentType: attachment.ContentType,
			Data:        attachment.Data,
			Inline:      attachment.Disposition == AttachmentDispositionInline,
			ContentID:   attachment.ContentID,
		}
	}

	return mime.Input{
		From:        message.from.String(),
		To:          mimeAddresses(message.to),
		CC:          mimeAddresses(message.cc),
		ReplyTo:     mimeAddresses(message.replyTo),
		Subject:     message.subject,
		TextBody:    message.textBody,
		HTMLBody:    message.htmlBody,
		Attachments: attachments,
	}
}

// mimeAddresses formats each concrete mailbox before crossing the renderer seam.
func mimeAddresses(addresses []Address) []string {
	values := make([]string, 0, len(addresses))

	for _, address := range addresses {
		values = append(values, address.String())
	}

	return values
}
