package mail_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	stdmail "net/mail"
	"strings"
	"testing"

	"arkoren.dev/core/service/mail"
)

// TestEncodeMIMEOmitsBCCAndPreservesMultipartContent verifies that MIME output
// carries visible message content without exposing hidden recipients.
func TestEncodeMIMEOmitsBCCAndPreservesMultipartContent(t *testing.T) {
	t.Parallel()

	contents, err := mail.EncodeMIME(mail.NewMessage(mail.Recipients{
		To:  []mail.Address{{Email: "person@example.com"}},
		BCC: []mail.Address{{Email: "hidden@example.com"}},
	}, mail.Body{Text: "Hello.", HTML: `<img src="cid:logo">`}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.ReplyTo(mail.Address{Email: "replies@example.com"})
		letter.Subject("Report")
		letter.Attachments(
			mail.Attachment{
				Filename:    "report.txt",
				ContentType: "text/plain",
				Data:        []byte("report"),
			},
			mail.Attachment{
				Filename:    "logo",
				ContentType: "image/png",
				Data:        []byte("image"),
				Disposition: mail.AttachmentDispositionInline,
				ContentID:   "logo",
			},
		)
	}), "<message@example.com>")

	if err != nil {
		t.Fatal(err)
	}

	value := string(contents)

	if strings.Contains(value, "Bcc:") || strings.Contains(value, "hidden@example.com") {
		t.Fatalf("message exposes BCC recipient: %q", value)
	}

	if !strings.Contains(value, "Message-ID: <message@example.com>") {
		t.Fatalf("message = %q", value)
	}

	if !strings.Contains(value, "<logo>") {
		t.Fatalf("message omits inline content ID: %q", value)
	}

	parsed, err := stdmail.ReadMessage(bytes.NewReader(contents))

	if err != nil {
		t.Fatal(err)
	}

	parts, err := collectParts(parsed.Header.Get("Content-Type"), parsed.Header.Get("Content-Transfer-Encoding"), parsed.Body)

	if err != nil {
		t.Fatal(err)
	}

	if !containsPart(parts, "text/plain", "Hello.") {
		t.Fatalf("parts omit text body: %#v", parts)
	}

	if !containsPart(parts, "text/html", `<img src="cid:logo">`) {
		t.Fatalf("parts omit HTML body: %#v", parts)
	}

	if !containsPart(parts, "text/plain", "report") {
		t.Fatalf("parts omit decoded attachment: %#v", parts)
	}

	if !containsPart(parts, "image/png", "image") {
		t.Fatalf("parts omit inline attachment: %#v", parts)
	}
}

// TestEncodeMIMEQuotesDisplayNames verifies MIME headers use Address.String.
func TestEncodeMIMEQuotesDisplayNames(t *testing.T) {
	t.Parallel()

	contents, err := mail.EncodeMIME(mail.NewMessage(mail.Recipients{
		To: []mail.Address{
			{
				Name:  "Doe, Jane",
				Email: "person@example.com",
			},
			{
				Name:  `Quote "Recipient"`,
				Email: "second@example.com",
			},
		},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{
			Name: `Doe, "Jane"`, Email: "jane@example.com",
		})
	}), "<message@example.com>")

	if err != nil {
		t.Fatal(err)
	}

	value := string(contents)

	if !strings.Contains(value, `From: "Doe, \"Jane\"" <jane@example.com>`+"\r\n") {
		t.Fatalf("message = %q", value)
	}

	if !strings.Contains(value, `To: "Doe, Jane" <person@example.com>,`+"\r\n "+`"Quote \"Recipient\"" <second@example.com>`+"\r\n") {
		t.Fatalf("message = %q", value)
	}
}

// TestEncodeMIMERejectsInvalidMessage verifies that validation precedes encoding.
func TestEncodeMIMERejectsInvalidMessage(t *testing.T) {
	t.Parallel()

	_, err := mail.EncodeMIME(mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Subject("bad\r\nBcc: attacker@example.com")
	}), "<message@example.com>")

	if !errors.Is(err, mail.ErrInvalidMessage) {
		t.Fatalf("error = %v", err)
	}
}

// mimePart stores one decoded MIME leaf part for assertions.
type mimePart struct {
	// contentType identifies the decoded leaf body's MIME representation.
	contentType string
	// body holds the leaf content after its transfer encoding is decoded.
	body string
}

// collectParts recursively decodes multipart bodies into their leaf parts.
func collectParts(contentType, transferEncoding string, body io.Reader) ([]mimePart, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)

	if err != nil {
		return nil, err
	}

	if !strings.HasPrefix(mediaType, "multipart/") {
		if transferEncoding == "base64" {
			body = base64.NewDecoder(base64.StdEncoding, body)
		}

		contents, err := io.ReadAll(body)

		if err != nil {
			return nil, err
		}

		return []mimePart{{
			contentType: mediaType,
			body:        string(contents),
		}}, nil
	}

	reader := multipart.NewReader(body, params["boundary"])
	parts := []mimePart{}

	for {
		part, err := reader.NextPart()

		if errors.Is(err, io.EOF) {
			return parts, nil
		}

		if err != nil {
			return nil, err
		}

		children, err := collectParts(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part)

		if err != nil {
			return nil, err
		}

		parts = append(parts, children...)
	}
}

// containsPart reports whether parts include a decoded leaf with the given data.
func containsPart(parts []mimePart, contentType, body string) bool {
	for _, part := range parts {
		if part.contentType == contentType && part.body == body {
			return true
		}
	}

	return false
}

// TestEncodeMIMENormalizesBareLineFeeds checks canonical MIME line endings.
// Body text must use CRLF without introducing duplicate carriage returns.
func TestEncodeMIMENormalizesBareLineFeeds(t *testing.T) {
	t.Parallel()

	contents, err := mail.EncodeMIME(mail.NewMessage(mail.Recipients{
		BCC: []mail.Address{{Email: "hidden@example.com"}},
	}, mail.Body{Text: "line one\nline two\n" + strings.Repeat("x", 2000)}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "from@example.com"})
	}), "<message@example.com>")

	if err != nil {
		t.Fatal(err)
	}

	value := string(contents)

	if strings.Contains(strings.ReplaceAll(value, "\r\n", ""), "\n") {
		t.Fatal("message contains a bare line feed")
	}

	for line := range strings.SplitSeq(value, "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line of %d bytes", len(line))
		}
	}

	if strings.Contains(value, "\r\nTo:") || !strings.HasPrefix(value, "Date: ") {
		t.Fatalf("headers = %q", value[:200])
	}
}

// TestEncodeMIMENestsInlineAttachmentsInRelated checks inline-content grouping.
// Inline files must accompany the HTML body inside a related multipart.
func TestEncodeMIMENestsInlineAttachmentsInRelated(t *testing.T) {
	t.Parallel()

	contents, err := mail.EncodeMIME(mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "to@example.com"}},
	}, mail.Body{HTML: `<img src="cid:logo">`}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "from@example.com"})
		letter.Attachments(
			mail.Attachment{
				Filename:    "logo.png",
				ContentType: "image/png",
				Data:        []byte("png"),
				Disposition: mail.AttachmentDispositionInline,
				ContentID:   "logo",
			},
			mail.Attachment{
				Filename:    "report.pdf",
				ContentType: "application/pdf",
				Data:        []byte("pdf"),
				Disposition: mail.AttachmentDispositionAttachment,
			},
		)
	}), "<message@example.com>")

	if err != nil {
		t.Fatal(err)
	}

	value := string(contents)
	related := strings.Index(value, "multipart/related")
	logo := strings.Index(value, "Content-Id: <logo>")

	if related < 0 || logo < related {
		t.Fatalf("inline attachment not inside multipart/related: %q", value)
	}
}

// TestValidateRejectsInvalidAttachmentContentType covers malformed MIME metadata.
// Attachment validation must fail before an invalid Content-Type is encoded.
func TestValidateRejectsInvalidAttachmentContentType(t *testing.T) {
	t.Parallel()

	err := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "to@example.com"}},
	}, mail.Body{Text: "hi"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "from@example.com"})
		letter.Attachments(mail.Attachment{
			Filename:    "a",
			ContentType: "not a type",
			Data:        []byte("x"),
			Disposition: mail.AttachmentDispositionAttachment,
		})
	}).Validate()

	if err == nil {
		t.Fatal("invalid content type accepted")
	}
}

// TestValidateDoesNotWriteIntoToSlice protects caller-owned recipient storage.
// Validation must not append other recipients into the To slice's spare capacity.
func TestValidateDoesNotWriteIntoToSlice(t *testing.T) {
	t.Parallel()

	to := make([]mail.Address, 1, 4)
	to[0] = mail.Address{Email: "to@example.com"}
	spare := to[:2]
	spare[1] = mail.Address{Email: "keep@example.com"}

	err := mail.NewMessage(mail.Recipients{
		To: to,
		CC: []mail.Address{{Email: "cc@example.com"}},
	}, mail.Body{Text: "hi"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "from@example.com"})
	}).Validate()

	if err != nil {
		t.Fatal(err)
	}

	if spare[1].Email != "keep@example.com" {
		t.Fatalf("spare capacity overwritten with %q", spare[1].Email)
	}
}

// TestEncodeMIMERejectsUnfoldedASCIIHeader applies RFC 5322's 998-character
// physical-line limit even though an otherwise valid declaration permits it.
func TestEncodeMIMERejectsUnfoldedASCIIHeader(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Subject(strings.Repeat("x", 1000))
	})

	if err := message.Validate(); err != nil {
		t.Fatal(err)
	}

	contents, err := mail.EncodeMIME(message, "<message@example.com>")

	if !errors.Is(err, mail.ErrInvalidMessage) {
		t.Fatalf("oversized header error = %v, want ErrInvalidMessage", err)
	}

	if contents != nil {
		t.Fatal("rejected encoding returned partial bytes")
	}
}

// TestEncodeMIMEAcceptsSubjectAtWireLimit keeps the limit inclusive, counting
// the Subject field prefix without changing caller text or the supplied ID.
func TestEncodeMIMEAcceptsSubjectAtWireLimit(t *testing.T) {
	t.Parallel()

	subject := strings.Repeat("x", 989)
	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Subject(subject)
	})

	contents, err := mail.EncodeMIME(message, "<message@example.com>")

	if err != nil {
		t.Fatal(err)
	}

	parsed, err := stdmail.ReadMessage(bytes.NewReader(contents))

	if err != nil {
		t.Fatal(err)
	}

	if parsed.Header.Get("Subject") != subject || parsed.Header.Get("Message-ID") != "<message@example.com>" {
		t.Fatal("encoding changed the subject or supplied identifier")
	}
}

// TestEncodeMIMEPreservesFoldedUnicodeSubject verifies legal encoded-word
// folding and quoted-printable body wrapping survive the physical-line guard.
func TestEncodeMIMEPreservesFoldedUnicodeSubject(t *testing.T) {
	t.Parallel()

	subject := strings.Repeat("Résumé ", 300)
	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{Text: strings.Repeat("body", 1000)}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Subject(subject)
	})

	contents, err := mail.EncodeMIME(message, "<message@example.com>")

	if err != nil {
		t.Fatal(err)
	}

	parsed, err := stdmail.ReadMessage(bytes.NewReader(contents))

	if err != nil {
		t.Fatal(err)
	}

	decoded, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))

	if err != nil {
		t.Fatal(err)
	}

	if decoded != subject || message.Subject() != subject {
		t.Fatal("encoded-word folding changed the caller's subject")
	}

	for line := range bytes.SplitSeq(contents, []byte("\r\n")) {
		if len(line) > 998 {
			t.Fatal("encoded MIME exceeded the RFC physical-line limit")
		}
	}
}

// TestEncodeMIMERejectsOversizedAttachmentHeader also applies the wire limit
// inside multipart parts, without changing or truncating the attachment name.
func TestEncodeMIMERejectsOversizedAttachmentHeader(t *testing.T) {
	t.Parallel()

	filename := strings.Repeat("x", 1000) + ".txt"
	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{Text: "Attached."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename: filename, ContentType: "text/plain", Data: []byte("notes"),
		})
	})

	contents, err := mail.EncodeMIME(message, "<message@example.com>")

	if !errors.Is(err, mail.ErrInvalidMessage) || contents != nil {
		t.Fatalf("oversized attachment header: bytes = %d, error = %v", len(contents), err)
	}

	if message.Attachments()[0].Filename != filename {
		t.Fatal("failed encoding modified the attachment filename")
	}
}

// TestMIMEReaderDoesNotEnforceWireLineLimit records why successful native
// header parsing alone cannot establish RFC 5322 physical-line conformance.
func TestMIMEReaderDoesNotEnforceWireLineLimit(t *testing.T) {
	t.Parallel()

	subject := strings.Repeat("x", 1000)

	parsed, err := stdmail.ReadMessage(strings.NewReader("Subject: " + subject + "\r\n\r\nHello."))

	if err != nil {
		t.Fatal(err)
	}

	if parsed.Header.Get("Subject") != subject {
		t.Fatal("native reader changed the over-limit fixture")
	}
}

// TestMIMEReaderDoesNotValidateMessageIDSyntax distinguishes header extraction
// from msg-id validation: a parsed field can still violate the Message-ID grammar.
func TestMIMEReaderDoesNotValidateMessageIDSyntax(t *testing.T) {
	t.Parallel()

	id := "<message@a..b>"

	parsed, err := stdmail.ReadMessage(strings.NewReader("Message-ID: " + id + "\r\n\r\nHello."))

	if err != nil {
		t.Fatal(err)
	}

	if parsed.Header.Get("Message-ID") != id {
		t.Fatal("native reader changed the invalid-identifier fixture")
	}
}

// TestEncodeMIMERelatedDeclaresHTMLRoot follows RFC 2387's required type
// parameter through native header, multipart, and body readers.
func TestEncodeMIMERelatedDeclaresHTMLRoot(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{HTML: `<img src="cid:logo">`}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename: "logo.png", ContentType: "image/png", Data: []byte("image"),
			Disposition: mail.AttachmentDispositionInline, ContentID: "logo",
		})
	})

	contents, err := mail.EncodeMIME(message, "<message@example.com>")

	if err != nil {
		t.Fatal(err)
	}

	parsed, err := stdmail.ReadMessage(bytes.NewReader(contents))

	if err != nil {
		t.Fatal(err)
	}

	root, rootType := readRelatedRoot(t, parsed.Header.Get("Content-Type"), parsed.Body)

	body, err := io.ReadAll(root)

	if err != nil {
		t.Fatal(err)
	}

	if rootType != "text/html" || string(body) != `<img src="cid:logo">` {
		t.Fatal("declared related type did not match its unchanged root body")
	}
}

// TestEncodeMIMERelatedDeclaresTextRoot keeps inline grouping valid for a
// text-only declaration without inventing an HTML body or changing content.
func TestEncodeMIMERelatedDeclaresTextRoot(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{Text: "See attachment."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename: "logo.png", ContentType: "image/png", Data: []byte("image"),
			Disposition: mail.AttachmentDispositionInline, ContentID: "logo",
		})
	})

	contents, err := mail.EncodeMIME(message, "<message@example.com>")

	if err != nil {
		t.Fatal(err)
	}

	parsed, err := stdmail.ReadMessage(bytes.NewReader(contents))

	if err != nil {
		t.Fatal(err)
	}

	root, rootType := readRelatedRoot(t, parsed.Header.Get("Content-Type"), parsed.Body)

	body, err := io.ReadAll(root)

	if err != nil {
		t.Fatal(err)
	}

	if rootType != "text/plain" || string(body) != "See attachment." {
		t.Fatal("text-only related root changed")
	}
}

// TestEncodeMIMENestedRelatedDeclaresAlternativeRoot checks the nested mixed /
// related / alternative structure, not just the top-level related header.
func TestEncodeMIMENestedRelatedDeclaresAlternativeRoot(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{Text: "Logo attached.", HTML: `<img src="cid:logo">`}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename: "logo.png", ContentType: "image/png", Data: []byte("image"),
			Disposition: mail.AttachmentDispositionInline, ContentID: "logo",
		}, mail.Attachment{Filename: "notes.txt", ContentType: "text/plain", Data: []byte("notes")})
	})

	contents, err := mail.EncodeMIME(message, "<message@example.com>")

	if err != nil {
		t.Fatal(err)
	}

	parsed, err := stdmail.ReadMessage(bytes.NewReader(contents))

	if err != nil {
		t.Fatal(err)
	}

	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))

	if err != nil {
		t.Fatal(err)
	}

	if mediaType != "multipart/mixed" {
		t.Fatalf("outer MIME type = %q, want multipart/mixed", mediaType)
	}

	related, err := multipart.NewReader(parsed.Body, params["boundary"]).NextPart()

	if err != nil {
		t.Fatal(err)
	}

	root, rootType := readRelatedRoot(t, related.Header.Get("Content-Type"), related)

	if rootType != "multipart/alternative" {
		t.Fatalf("related root type = %q, want multipart/alternative", rootType)
	}

	parts, err := collectParts(root.Header.Get("Content-Type"), root.Header.Get("Content-Transfer-Encoding"), root)

	if err != nil {
		t.Fatal(err)
	}

	if !containsPart(parts, "text/plain", "Logo attached.") || !containsPart(parts, "text/html", `<img src="cid:logo">`) {
		t.Fatal("alternative root lost a body representation")
	}
}

// readRelatedRoot reads the declared related container and its first part with
// native MIME readers. Both top-level and nested tests use the same RFC 2387
// invariant: the required type parameter must equal the actual root media type.
func readRelatedRoot(t *testing.T, contentType string, body io.Reader) (*multipart.Part, string) {
	t.Helper()

	mediaType, params, err := mime.ParseMediaType(contentType)

	if err != nil {
		t.Fatal(err)
	}

	if mediaType != "multipart/related" {
		t.Fatalf("container type = %q, want multipart/related", mediaType)
	}

	root, err := multipart.NewReader(body, params["boundary"]).NextPart()

	if err != nil {
		t.Fatal(err)
	}

	rootType, _, err := mime.ParseMediaType(root.Header.Get("Content-Type"))

	if err != nil {
		t.Fatal(err)
	}

	if params["type"] != rootType {
		t.Fatalf("declared related type = %q, actual root = %q", params["type"], rootType)
	}

	return root, rootType
}

// TestNewMessageIDRejectsInvalidDomainSyntax rejects malformed id-right inputs
// before generating a token, rather than successfully returning invalid wire IDs.
func TestNewMessageIDRejectsInvalidDomainSyntax(t *testing.T) {
	t.Parallel()

	for _, domain := range []string{"", "a..b", "a:b", "bad\tdomain", "bad\x00domain",
		".example.com", "example.com.", "müller.example", "[bad\\literal]"} {
		id, err := mail.NewMessageID(domain)

		if !errors.Is(err, mail.ErrInvalidMessage) || id != "" {
			t.Fatalf("invalid Message-ID domain accepted: id = %q, error = %v", id, err)
		}
	}
}

// TestEncodeMIMERejectsMalformedMessageID rejects invalid supplied identifiers
// instead of relying on a native header reader to validate msg-id grammar.
func TestEncodeMIMERejectsMalformedMessageID(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	})

	for _, id := range []string{"", "message@example.com", "<a..b@example.com>", "<message@a..b>",
		"<message@::1>", "<message@bad\tdomain>", "<message@example.com>\r\nBcc: hidden@example.com",
		" <message@example.com>", "<message@example.com> (comment)", "<\"quoted left\"@example.com>",
		"<réf@example.com>"} {
		contents, err := mail.EncodeMIME(message, id)

		if !errors.Is(err, mail.ErrInvalidMessage) || contents != nil {
			t.Fatalf("invalid supplied Message-ID accepted: bytes = %d, error = %v", len(contents), err)
		}
	}
}

// TestNewMessageIDBracketsRawIPv6 keeps a valid SMTP host usable without
// emitting the invalid unbracketed id-right shown by the original audit.
func TestNewMessageIDBracketsRawIPv6(t *testing.T) {
	t.Parallel()

	id, err := mail.NewMessageID("2001:db8::1")

	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(id, "@[IPv6:2001:db8::1]>") {
		t.Fatalf("IPv6 Message-ID is not bracketed: %q", id)
	}

	if _, err := stdmail.ParseAddress(id); err != nil {
		t.Fatalf("native address parser rejected generated ID: %v", err)
	}
}

// TestNewMessageIDPreservesValidDomainBytes does not canonicalize case or
// already-bracketed literals while validating modern outbound id-right syntax.
func TestNewMessageIDPreservesValidDomainBytes(t *testing.T) {
	t.Parallel()

	for _, domain := range []string{"Mail.EXAMPLE.com", "192.0.2.1", "[2001:db8::1]"} {
		id, err := mail.NewMessageID(domain)

		if err != nil {
			t.Fatal(err)
		}

		if !strings.HasSuffix(id, "@"+domain+">") {
			t.Fatalf("generated ID changed domain bytes: %q", id)
		}
	}
}

// TestEncodeMIMEPreservesValidSuppliedMessageIDs protects valid atext, case,
// punctuation, and literals against normalization or unintended wire-ID changes.
func TestEncodeMIMEPreservesValidSuppliedMessageIDs(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	})

	for _, id := range []string{"<a.B+tag@EXAMPLE.com>", "<a!#$%&'*+-/=?^_`{|}~@example.com>", "<message@[2001:db8::1]>"} {
		contents, err := mail.EncodeMIME(message, id)

		if err != nil {
			t.Fatal(err)
		}

		parsed, err := stdmail.ReadMessage(bytes.NewReader(contents))

		if err != nil {
			t.Fatal(err)
		}

		if parsed.Header.Get("Message-ID") != id {
			t.Fatal("encoding changed the supplied Message-ID")
		}
	}
}

// TestEncodeMIMEAttachmentPreservesContentTypeParameters checks the public
// construction-to-wire path, including quoted values and decoded payload bytes.
func TestEncodeMIMEAttachmentPreservesContentTypeParameters(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{Text: "Notes attached."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename:    "notes.txt",
			ContentType: `text/plain; charset=UTF-8; format=flowed; profile="https://example.com/notes; v=1"`,
			Data:        []byte("meeting notes"),
		})
	})

	part := readAttachmentTypePart(t, message)

	mediaType, params, err := mime.ParseMediaType(part.Header.Get("Content-Type"))

	if err != nil {
		t.Fatal(err)
	}

	if mediaType != "text/plain" || !maps.Equal(params, map[string]string{
		"charset": "UTF-8",
		"format":  "flowed",
		"profile": "https://example.com/notes; v=1",
		"name":    "notes.txt",
	}) {
		t.Fatalf("attachment content type = %q, parameters = %#v", mediaType, params)
	}

	if encoding := part.Header.Get("Content-Transfer-Encoding"); encoding != "base64" {
		t.Fatalf("attachment transfer encoding = %q", encoding)
	}

	data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "meeting notes" {
		t.Fatalf("attachment data = %q", data)
	}
}

// TestEncodeMIMEAttachmentFilenameOverridesContentTypeName keeps the declared
// filename authoritative in both headers, including non-ASCII filenames.
func TestEncodeMIMEAttachmentFilenameOverridesContentTypeName(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{Text: "Report attached."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename:    "résumé final.txt",
			ContentType: `TEXT/PLAIN; CHARSET=UTF-8; NAME="ignored.txt"`,
			Data:        []byte("report"),
		})
	})

	part := readAttachmentTypePart(t, message)

	mediaType, params, err := mime.ParseMediaType(part.Header.Get("Content-Type"))

	if err != nil {
		t.Fatal(err)
	}

	if mediaType != "text/plain" || !maps.Equal(params, map[string]string{
		"charset": "UTF-8",
		"name":    "résumé final.txt",
	}) {
		t.Fatalf("attachment content type = %q, parameters = %#v", mediaType, params)
	}

	disposition, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))

	if err != nil {
		t.Fatal(err)
	}

	if disposition != "attachment" || !maps.Equal(params, map[string]string{"filename": "résumé final.txt"}) {
		t.Fatalf("attachment disposition = %q, parameters = %#v", disposition, params)
	}
}

// TestEncodeMIMEInlineAttachmentPreservesContentTypeParameters checks that
// related parts retain their media metadata without changing CID placement.
func TestEncodeMIMEInlineAttachmentPreservesContentTypeParameters(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{HTML: `<img src="cid:logo">`}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename:    "logo.svg",
			ContentType: "image/svg+xml; charset=UTF-8",
			Data:        []byte("<svg/>"),
			Disposition: mail.AttachmentDispositionInline,
			ContentID:   "logo",
		})
	})

	part := readAttachmentTypePart(t, message)

	mediaType, params, err := mime.ParseMediaType(part.Header.Get("Content-Type"))

	if err != nil {
		t.Fatal(err)
	}

	if mediaType != "image/svg+xml" || !maps.Equal(params, map[string]string{"charset": "UTF-8", "name": "logo.svg"}) {
		t.Fatalf("inline content type = %q, parameters = %#v", mediaType, params)
	}

	if contentID := part.Header.Get("Content-ID"); contentID != "<logo>" {
		t.Fatalf("inline content ID = %q", contentID)
	}
}

// TestEncodeMIMEAttachmentAcceptsBareContentTypeAndDefaultDisposition protects
// the existing parameter-free MIME type and omitted disposition behavior.
func TestEncodeMIMEAttachmentAcceptsBareContentTypeAndDefaultDisposition(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{Text: "Notes attached."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename:    "notes.txt",
			ContentType: "text/plain",
			Data:        []byte("meeting notes"),
		})
	})

	part := readAttachmentTypePart(t, message)

	mediaType, params, err := mime.ParseMediaType(part.Header.Get("Content-Type"))

	if err != nil {
		t.Fatal(err)
	}

	if mediaType != "text/plain" || !maps.Equal(params, map[string]string{"name": "notes.txt"}) {
		t.Fatalf("attachment content type = %q, parameters = %#v", mediaType, params)
	}

	disposition, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))

	if err != nil {
		t.Fatal(err)
	}

	if disposition != "attachment" || !maps.Equal(params, map[string]string{"filename": "notes.txt"}) {
		t.Fatalf("attachment disposition = %q, parameters = %#v", disposition, params)
	}
}

// TestEncodeMIMEAttachmentRejectsUnformattableContentType checks a parameter
// accepted by construction but not MIME formatting; no partial bytes may escape.
func TestEncodeMIMEAttachmentRejectsUnformattableContentType(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "reader@example.com"}},
	}, mail.Body{Text: "Notes attached."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename:    "notes.txt",
			ContentType: "text/plain; *=utf-8''invalid",
			Data:        []byte("meeting notes"),
		})
	})

	if err := message.Err(); err != nil {
		t.Fatalf("construction unexpectedly rejected content type: %v", err)
	}

	contents, err := mail.EncodeMIME(message, "<notes@example.com>")

	if !errors.Is(err, mail.ErrInvalidMessage) {
		t.Fatalf("encoding error = %v, want ErrInvalidMessage", err)
	}

	if contents != nil {
		t.Fatalf("failed encoding returned %d partial bytes", len(contents))
	}
}

// readAttachmentTypePart encodes a public single-body, single-attachment fixture
// and returns its attachment through the standard library's multipart parser.
// The returned body still carries its declared transfer encoding.
func readAttachmentTypePart(t *testing.T, message mail.Message) *multipart.Part {
	t.Helper()

	if err := message.Err(); err != nil {
		t.Fatal(err)
	}

	contents, err := mail.EncodeMIME(message, "<notes@example.com>")

	if err != nil {
		t.Fatal(err)
	}

	parsed, err := stdmail.ReadMessage(bytes.NewReader(contents))

	if err != nil {
		t.Fatal(err)
	}

	_, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))

	if err != nil {
		t.Fatal(err)
	}

	reader := multipart.NewReader(parsed.Body, params["boundary"])

	if _, err := reader.NextPart(); err != nil {
		t.Fatal(err)
	}

	part, err := reader.NextPart()

	if err != nil {
		t.Fatal(err)
	}

	return part
}
