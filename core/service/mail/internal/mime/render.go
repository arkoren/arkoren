package mime

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"regexp"
	"strings"
	"time"
)

// maxMIMELineBytes bounds each emitted physical line, excluding CRLF. RFC 5322
// permits at most 998 characters; counting octets also respects SMTP's limit
// when caller-supplied mailbox text contains multibyte characters.
const maxMIMELineBytes = 998

// messageIDAtomPattern describes RFC 5322 dot-atom-text, not a DNS hostname.
// It excludes empty dot components while preserving all permitted atext bytes.
const messageIDAtomPattern = `[A-Za-z0-9!#$%&'*+/=?^_{|}~\x60-]+` +
	`(?:\.[A-Za-z0-9!#$%&'*+/=?^_{|}~\x60-]+)*`

// messageIDPattern accepts the modern outbound msg-id form: a dot-atom left
// side and a dot-atom or no-fold-literal right side. Obsolete quoted syntax,
// surrounding comments, folding, controls, and internationalized IDs are not
// emitted by this encoder. The literal branch uses RFC 5322's modern dtext.
var messageIDPattern = regexp.MustCompile(`^<` + messageIDAtomPattern + `@(?:` +
	messageIDAtomPattern + `|\[[\x21-\x5a\x5e-\x7e]*\])>$`)

// ValidID checks the exact outbound Message-ID grammar used by the renderer.
// It does not verify domain ownership or allocate random identity material.
func ValidID(value string) bool {
	return messageIDPattern.MatchString(value)
}

// Render encodes validated visible content and publishes bytes only after all
// MIME parts and physical lines pass their original checks. invalid is the
// caller's invalid-message sentinel, not a replacement error identity.
func Render(message Input, messageID string, invalid error) ([]byte, error) {
	if !ValidID(messageID) {
		return nil, fmt.Errorf("%w: invalid message ID syntax", invalid)
	}

	message.invalid = invalid

	var contents bytes.Buffer
	writeMessageHeaders(&contents, message, messageID)

	if err := writeMessageContent(&contents, message); err != nil {
		return nil, err
	}

	if err := checkMIMELines(contents.Bytes(), invalid); err != nil {
		return nil, err
	}

	return contents.Bytes(), nil
}

// checkMIMELines validates the completed wire representation before publication.
// The scan allocates no per-line copies and exposes no rejected header or body
// values in its error. Encoding failures never return partially written bytes.
func checkMIMELines(contents []byte, invalid error) error {
	for line := range bytes.SplitSeq(contents, []byte("\r\n")) {
		if len(line) > maxMIMELineBytes {
			return fmt.Errorf("%w: MIME line exceeds 998 bytes", invalid)
		}
	}

	return nil
}

// writeMessageHeaders writes the visible RFC 5322 message headers.
func writeMessageHeaders(writer *bytes.Buffer, message Input, messageID string) {
	writeHeader(writer, "Date", time.Now().Format(time.RFC1123Z))
	writeHeader(writer, "From", message.From)
	writeOptionalAddressHeaders(writer, message)
	writeHeader(writer, "Subject", foldEncodedWords(mime.QEncoding.Encode("UTF-8", message.Subject)))
	writeHeader(writer, "Message-ID", messageID)
	writeHeader(writer, "MIME-Version", "1.0")
}

// writeOptionalAddressHeaders writes the visible recipient headers that have
// addresses. A BCC-only message has no To header.
func writeOptionalAddressHeaders(writer *bytes.Buffer, message Input) {
	if len(message.To) != 0 {
		writeHeader(writer, "To", formatAddresses(message.To))
	}

	if len(message.CC) != 0 {
		writeHeader(writer, "Cc", formatAddresses(message.CC))
	}

	if len(message.ReplyTo) != 0 {
		writeHeader(writer, "Reply-To", formatAddresses(message.ReplyTo))
	}
}

// writeMessageContent groups regular attachments in mixed MIME and inline ones
// with their root body in related MIME. Splitting once preserves attachment
// order within each group without changing the immutable declaration.
func writeMessageContent(writer *bytes.Buffer, message Input) error {
	regular, inline := splitAttachments(message.Attachments)

	if len(regular)+len(inline) == 0 {
		return writeSingleBody(writer, message)
	}

	subtype := "related"

	if len(regular) != 0 {
		subtype = "mixed"
	}

	multipartWriter := multipart.NewWriter(writer)
	contentType := multipartContentType(subtype, multipartWriter.Boundary(), message)
	writeHeader(writer, "Content-Type", contentType)
	writer.WriteString("\r\n")

	if err := writeContentPart(multipartWriter, subtype, message, inline); err != nil {
		return err
	}

	if err := writeAttachments(multipartWriter, regular, message.invalid); err != nil {
		return err
	}

	return multipartWriter.Close()
}

// multipartContentType includes RFC 2387's required root type for related MIME.
// Dual bodies have an alternative root; otherwise the single body supplies the
// type. Boundary and subtype come from the encoder, not untrusted header text.
func multipartContentType(subtype, boundary string, message Input) string {
	params := map[string]string{"boundary": boundary}

	if subtype == "related" {
		params["type"] = "multipart/alternative"

		if message.TextBody == "" || message.HTMLBody == "" {
			params["type"], _ = singleBody(message)
		}
	}

	return mime.FormatMediaType("multipart/"+subtype, params)
}

// writeContentPart writes the body and inline attachments into writer. Inside
// multipart/mixed, inline attachments need their own multipart/related part
// so clients resolve cid: references instead of listing them as files.
func writeContentPart(writer *multipart.Writer, subtype string, message Input, inline []Attachment) error {
	if subtype != "mixed" || len(inline) == 0 {
		return writeContent(writer, message, inline)
	}

	related, err := createMultipart(writer, "related", message)

	if err != nil {
		return err
	}

	if err := writeContent(related, message, inline); err != nil {
		return err
	}

	return related.Close()
}

// writeAttachments writes each attachment into writer.
func writeAttachments(writer *multipart.Writer, attachments []Attachment, invalid error) error {
	for _, attachment := range attachments {
		if err := writeAttachment(writer, attachment, invalid); err != nil {
			return err
		}
	}

	return nil
}

// writeContent writes the body and inline attachments into parent.
func writeContent(parent *multipart.Writer, message Input, inline []Attachment) error {
	if err := writeContentBody(parent, message); err != nil {
		return err
	}

	return writeAttachments(parent, inline, message.invalid)
}

// writeContentBody writes the text and HTML body representation.
func writeContentBody(parent *multipart.Writer, message Input) error {
	if message.TextBody != "" && message.HTMLBody != "" {
		return writeAlternative(parent, message)
	}

	contentType, body := singleBody(message)

	return writeBodyPart(parent, contentType, body)
}

// singleBody returns the MIME type and content for a single-part body.
func singleBody(message Input) (string, string) {
	if message.HTMLBody != "" {
		return "text/html", message.HTMLBody
	}

	return "text/plain", message.TextBody
}

// writeAlternative writes a multipart alternative part into parent.
func writeAlternative(parent *multipart.Writer, message Input) error {
	alternative, err := createMultipart(parent, "alternative", message)

	if err != nil {
		return err
	}

	return writeAlternativeBodies(alternative, message)
}

// createMultipart creates a nested multipart writer with the shared header
// policy, including related root type. Part or boundary errors propagate before
// the caller writes content; the caller closes the returned writer on success.
func createMultipart(parent *multipart.Writer, subtype string, message Input) (*multipart.Writer, error) {
	probe := multipart.NewWriter(io.Discard)
	boundary := probe.Boundary()
	header := makeMIMEHeader(multipartContentType(subtype, boundary, message), "")

	part, err := parent.CreatePart(header)

	if err != nil {
		return nil, err
	}

	alternative := multipart.NewWriter(part)

	if err := alternative.SetBoundary(boundary); err != nil {
		return nil, err
	}

	return alternative, nil
}

// writeAlternativeBodies writes and closes the alternative body parts.
func writeAlternativeBodies(writer *multipart.Writer, message Input) error {
	if err := writeBodyPart(writer, "text/plain", message.TextBody); err != nil {
		return err
	}

	if err := writeBodyPart(writer, "text/html", message.HTMLBody); err != nil {
		return err
	}

	return writer.Close()
}

// writeSingleBody writes a non-multipart text or HTML body.
func writeSingleBody(writer *bytes.Buffer, message Input) error {
	if message.TextBody != "" && message.HTMLBody != "" {
		return writeStandaloneAlternative(writer, message)
	}

	contentType, body := singleBody(message)
	writeHeader(writer, "Content-Type", contentType+`; charset="UTF-8"`)
	writeHeader(writer, "Content-Transfer-Encoding", "quoted-printable")
	writer.WriteString("\r\n")

	return writeQuotedPrintable(writer, body)
}

// writeStandaloneAlternative writes a top-level multipart alternative body.
func writeStandaloneAlternative(writer *bytes.Buffer, message Input) error {
	alternative := multipart.NewWriter(writer)
	contentType := mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": alternative.Boundary()})
	writeHeader(writer, "Content-Type", contentType)
	writer.WriteString("\r\n")

	return writeAlternativeBodies(alternative, message)
}

// writeBodyPart writes one text or HTML MIME part.
func writeBodyPart(writer *multipart.Writer, contentType, body string) error {
	part, err := writer.CreatePart(makeMIMEHeader(contentType+`; charset="UTF-8"`, "quoted-printable"))

	if err != nil {
		return err
	}

	return writeQuotedPrintable(part, body)
}

// writeQuotedPrintable writes body quoted-printable encoded. Text-mode
// encoding turns every line ending into CRLF and soft-wraps long lines.
func writeQuotedPrintable(writer io.Writer, body string) error {
	encoder := quotedprintable.NewWriter(writer)

	if _, err := io.WriteString(encoder, body); err != nil {
		return err
	}

	return encoder.Close()
}

// writeAttachment writes one base64-encoded MIME attachment. It formats the
// declared media type before creating the part so invalid metadata cannot emit
// an empty Content-Type. Inline placement retains the attachment's content ID.
func writeAttachment(writer *multipart.Writer, attachment Attachment, invalid error) error {
	contentType, err := formatAttachmentContentType(attachment, invalid)

	if err != nil {
		return err
	}

	disposition := "attachment"

	if attachment.Inline {
		disposition = "inline"
	}

	header := makeMIMEHeader(contentType, "base64")
	header.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": attachment.Filename}))

	if attachment.Inline {
		header.Set("Content-ID", "<"+attachment.ContentID+">")
	}

	part, err := writer.CreatePart(header)

	if err != nil {
		return err
	}

	return writeBase64(part, attachment.Data)
}

// formatAttachmentContentType preserves parsed media-type parameters while
// making Filename authoritative for the legacy "name" parameter. Formatting
// receives only the bare media type, not the caller's parameterized input.
// Parse and format failures wrap the supplied invalid-message sentinel instead
// of returning an empty header; the attachment itself is never changed.
func formatAttachmentContentType(attachment Attachment, invalid error) (string, error) {
	mediaType, params, err := mime.ParseMediaType(attachment.ContentType)

	if err != nil {
		return "", fmt.Errorf("%w: invalid attachment content type: %w", invalid, err)
	}

	params["name"] = attachment.Filename
	contentType := mime.FormatMediaType(mediaType, params)

	if contentType == "" {
		return "", fmt.Errorf("%w: attachment content type cannot be formatted", invalid)
	}

	return contentType, nil
}

// writeBase64 writes MIME base64 data with RFC 2045 line endings.
func writeBase64(writer io.Writer, data []byte) error {
	encoded := base64.StdEncoding.EncodeToString(data)

	for len(encoded) > 76 {
		if _, err := io.WriteString(writer, encoded[:76]+"\r\n"); err != nil {
			return err
		}

		encoded = encoded[76:]
	}

	_, err := io.WriteString(writer, encoded+"\r\n")

	return err
}

// makeMIMEHeader constructs the headers common to MIME parts.
func makeMIMEHeader(contentType, transferEncoding string) textproto.MIMEHeader {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", contentType)

	if transferEncoding != "" {
		header.Set("Content-Transfer-Encoding", transferEncoding)
	}

	return header
}

// splitAttachments separates regular and inline attachments.
func splitAttachments(attachments []Attachment) ([]Attachment, []Attachment) {
	regular := make([]Attachment, 0, len(attachments))
	inline := make([]Attachment, 0, len(attachments))

	for _, attachment := range attachments {
		if attachment.Inline {
			inline = append(inline, attachment)
			continue
		}

		regular = append(regular, attachment)
	}

	return regular, inline
}

// writeHeader writes one RFC 5322 header line.
func writeHeader(writer *bytes.Buffer, name, value string) {
	writer.WriteString(name)
	writer.WriteString(": ")
	writer.WriteString(value)
	writer.WriteString("\r\n")
}

// formatAddresses formats one RFC 5322 mailbox per folded line to bound list
// growth. An individually oversized mailbox is not rewritten; checkMIMELines
// rejects the completed encoding if that line still exceeds the wire limit.
func formatAddresses(addresses []string) string {
	return strings.Join(addresses, ",\r\n ")
}

// foldEncodedWords puts each RFC 2047 encoded word on its own folded line,
// keeping long encoded subjects within the header line limit.
func foldEncodedWords(value string) string {
	return strings.ReplaceAll(value, "?= =?", "?=\r\n =?")
}
