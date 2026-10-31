package mailgunmail_test

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
	"arkoren.dev/driver/mailgun/mailgunmail"
)

// TestClientSendPostsDocumentedMultipartFields verifies Mailgun's multipart request fields.
func TestClientSendPostsDocumentedMultipartFields(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		verifyMailgunMultipartFields(t, request)
		writeMailgunAccepted(writer)
	}))
	t.Cleanup(server.Close)

	client, err := mailgunmail.New("example.com", "key", func(transport *mailgunmail.Builder) {
		transport.Endpoint(server.URL)
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Send(t.Context(), documentedMailgunMessage())

	if err != nil {
		t.Fatal(err)
	}
}

// documentedMailgunMessage creates the message used by the multipart contract test.
func documentedMailgunMessage() mail.Message {
	return mail.NewMessage(mail.Recipients{
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
		CC: []mail.Address{{
			Name:  "Copy, Carol",
			Email: "copy@example.com",
		}},
		BCC: []mail.Address{{
			Name:  `Hidden "Bob"`,
			Email: "hidden@example.com",
		}},
	}, mail.Body{Text: "Text body", HTML: "<p>HTML body</p>"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Name: `Sender, "Primary"`, Email: "sender@example.com"})
		letter.Subject("Hello")
		letter.ReplyTo(
			mail.Address{
				Name:  "Reply, Rachel",
				Email: "reply@example.com",
			},
			mail.Address{
				Name:  `Reply "Second"`,
				Email: "second-reply@example.com",
			},
		)
	})
}

// verifyMailgunMultipartFields verifies the documented Mailgun multipart fields.
func verifyMailgunMultipartFields(t *testing.T, request *http.Request) {
	t.Helper()

	assertMailgunMethod(t, request)
	assertMailgunPath(t, request)
	assertMailgunCredentials(t, request)
	assertMailgunContentType(t, request)

	if err := request.ParseMultipartForm(1 << 20); err != nil {
		t.Errorf("parse multipart form: %v", err)

		return
	}

	assertMailgunField(t, request.MultipartForm.Value, "from", `"Sender, \"Primary\"" <sender@example.com>`)
	assertMailgunFields(t, request.MultipartForm.Value, "to", []string{`"Doe, Jane" <person@example.com>`, `"Quote \"Recipient\"" <second@example.com>`})
	assertMailgunField(t, request.MultipartForm.Value, "cc", `"Copy, Carol" <copy@example.com>`)
	assertMailgunField(t, request.MultipartForm.Value, "bcc", `"Hidden \"Bob\"" <hidden@example.com>`)
	assertMailgunField(t, request.MultipartForm.Value, "h:Reply-To", `"Reply, Rachel" <reply@example.com>,"Reply \"Second\"" <second-reply@example.com>`)
	assertMailgunField(t, request.MultipartForm.Value, "subject", "Hello")
	assertMailgunField(t, request.MultipartForm.Value, "text", "Text body")
	assertMailgunField(t, request.MultipartForm.Value, "html", "<p>HTML body</p>")
}

// assertMailgunMethod verifies Mailgun uses its documented message method.
func assertMailgunMethod(t *testing.T, request *http.Request) {
	t.Helper()

	if request.Method != http.MethodPost {
		t.Errorf("request method = %q, want %q", request.Method, http.MethodPost)
	}
}

// assertMailgunPath verifies Mailgun uses its documented message path.
func assertMailgunPath(t *testing.T, request *http.Request) {
	t.Helper()

	if request.URL.Path != "/v3/example.com/messages" {
		t.Errorf("request path = %q", request.URL.Path)
	}
}

// assertMailgunCredentials verifies Mailgun uses the documented HTTP credentials.
func assertMailgunCredentials(t *testing.T, request *http.Request) {
	t.Helper()

	username, password, ok := request.BasicAuth()

	if !ok || username != "api" || password != "key" {
		t.Errorf("credentials = %q, %q, %t", username, password, ok)
	}
}

// assertMailgunContentType verifies Mailgun submits multipart form data.
func assertMailgunContentType(t *testing.T, request *http.Request) {
	t.Helper()

	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))

	if err != nil {
		t.Errorf("parse content type: %v", err)

		return
	}

	if contentType != "multipart/form-data" {
		t.Errorf("content type = %q", contentType)
	}
}

// assertMailgunField verifies one Mailgun multipart value.
func assertMailgunField(t *testing.T, values map[string][]string, name, expected string) {
	t.Helper()

	value := values[name]

	if len(value) != 1 || value[0] != expected {
		t.Errorf("%s = %#v", name, value)
	}
}

// assertMailgunFields verifies repeated Mailgun multipart values.
func assertMailgunFields(t *testing.T, values map[string][]string, name string, expected []string) {
	t.Helper()

	actual := values[name]

	if len(actual) != len(expected) {
		t.Fatalf("%s = %#v", name, actual)
	}

	for index, value := range expected {
		if actual[index] != value {
			t.Fatalf("%s = %#v", name, actual)
		}
	}
}

// writeMailgunAccepted writes a successful Mailgun API response.
func writeMailgunAccepted(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"id":"<accepted@example.com>","message":"Queued"}`))
}

// TestClientSendDocumentsMailgunInlineLimitations verifies that Mailgun emits
// inline attachments with SDK-selected content types.
func TestClientSendDocumentsMailgunInlineLimitations(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		verifyMailgunAttachments(t, request)
		writeMailgunAccepted(writer)
	}))
	t.Cleanup(server.Close)

	client, err := mailgunmail.New("example.com", "key", func(transport *mailgunmail.Builder) {
		transport.Endpoint(server.URL)
	})

	if err != nil {
		t.Fatal(err)
	}

	sendMailgunAttachments(t, client)
}

// verifyMailgunAttachments verifies Mailgun's regular and inline wire format.
func verifyMailgunAttachments(t *testing.T, request *http.Request) {
	t.Helper()

	if err := request.ParseMultipartForm(1 << 20); err != nil {
		t.Errorf("parse multipart form: %v", err)

		return
	}

	assertMailgunAttachment(t, multipartFile(t, request.MultipartForm.File, "attachment"), "report.txt", "report contents")
	assertMailgunAttachment(t, multipartFile(t, request.MultipartForm.File, "inline"), "logo.png", "inline image contents")
}

// multipartFile returns the sole multipart file stored under name.
func multipartFile(t *testing.T, files map[string][]*multipart.FileHeader, name string) *multipart.FileHeader {
	t.Helper()

	values := files[name]

	if len(values) != 1 {
		t.Errorf("%s attachments = %d", name, len(values))

		return nil
	}

	return values[0]
}

// assertMailgunAttachment verifies one uploaded Mailgun attachment.
func assertMailgunAttachment(t *testing.T, attachment *multipart.FileHeader, filename, expected string) {
	t.Helper()

	if attachment == nil {
		return
	}

	assertMailgunFilename(t, attachment, filename)
	assertMailgunAttachmentContentType(t, attachment)
	assertMailgunAttachmentContents(t, attachment, expected)
}

// assertMailgunFilename verifies an uploaded Mailgun attachment filename.
func assertMailgunFilename(t *testing.T, attachment *multipart.FileHeader, expected string) {
	t.Helper()

	if attachment.Filename != expected {
		t.Errorf("attachment filename = %q", attachment.Filename)
	}
}

// assertMailgunAttachmentContentType verifies that the Mailgun SDK selects the
// uploaded content type instead of preserving Attachment.ContentType.
func assertMailgunAttachmentContentType(t *testing.T, attachment *multipart.FileHeader) {
	t.Helper()

	if contentType := attachment.Header.Get("Content-Type"); contentType != "application/octet-stream" {
		t.Errorf("attachment content type = %q", contentType)
	}
}

// assertMailgunAttachmentContents verifies an uploaded attachment's contents.
func assertMailgunAttachmentContents(t *testing.T, attachment *multipart.FileHeader, expected string) {
	t.Helper()

	if contents := readMailgunAttachment(t, attachment); string(contents) != expected {
		t.Errorf("attachment contents = %q", contents)
	}
}

// readMailgunAttachment reads one multipart attachment and reports I/O errors.
func readMailgunAttachment(t *testing.T, attachment *multipart.FileHeader) []byte {
	t.Helper()

	body, err := attachment.Open()

	if err != nil {
		t.Errorf("open attachment: %v", err)

		return nil
	}

	contents, err := io.ReadAll(body)
	closeErr := body.Close()

	if err != nil {
		t.Errorf("read attachment: %v", err)
	}

	if closeErr != nil {
		t.Errorf("close attachment: %v", closeErr)
	}

	return contents
}

// sendMailgunAttachments submits the documented Mailgun attachment message.
func sendMailgunAttachments(t *testing.T, client *mailgunmail.Driver) {
	t.Helper()

	_, err := client.Send(t.Context(), mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "person@example.com",
		}},
	}, mail.Body{Text: "Body"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Subject("Hello")
		letter.Attachments(
			mail.Attachment{
				Filename:    "report.txt",
				ContentType: "text/plain",
				Data:        []byte("report contents"),
			},
			mail.Attachment{
				Filename:    "logo.png",
				ContentType: "image/png",
				Data:        []byte("inline image contents"),
				Disposition: mail.AttachmentDispositionInline,
				ContentID:   "logo.png",
			},
		)
	}))

	if err != nil {
		t.Fatal(err)
	}
}

// TestClientSendParsesMailgunReceipt verifies Mailgun receipt IDs are preserved.
func TestClientSendParsesMailgunReceipt(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"<accepted@example.com>","message":"Queued"}`))
	}))
	t.Cleanup(server.Close)

	client, err := mailgunmail.New("example.com", "key", func(transport *mailgunmail.Builder) {
		transport.Endpoint(server.URL)
	})

	if err != nil {
		t.Fatal(err)
	}

	receipt, err := client.Send(t.Context(), mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "person@example.com",
		}},
	}, mail.Body{Text: "Body"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Subject("Hello")
	}))

	if err != nil {
		t.Fatal(err)
	}

	if receipt.ID != "<accepted@example.com>" {
		t.Fatalf("receipt = %#v", receipt)
	}
}

// TestClientSendReturnsMailgunError verifies provider failures are returned.
func TestClientSendReturnsMailgunError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"message":"Forbidden"}`))
	}))
	t.Cleanup(server.Close)

	client, err := mailgunmail.New("example.com", "key", func(transport *mailgunmail.Builder) {
		transport.Endpoint(server.URL)
	})

	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Send(t.Context(), mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "person@example.com",
		}},
	}, mail.Body{Text: "Body"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Subject("Hello")
	}))

	assertMailgunError(t, err)
}

// assertMailgunError verifies the provider error response includes its status and body.
func assertMailgunError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("Send returned nil error")
	}

	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v", err)
	}

	if !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("error = %v", err)
	}
}

// TestClientSendRejectsMismatchedInlineContentID verifies unsupported inline IDs fail.
func TestClientSendRejectsMismatchedInlineContentID(t *testing.T) {
	t.Parallel()

	client, err := mailgunmail.New("example.com", "key")

	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Send(t.Context(), mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "person@example.com",
		}},
	}, mail.Body{Text: "Body"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename:    "logo.png",
			ContentType: "image/png",
			Data:        []byte("image"),
			Disposition: mail.AttachmentDispositionInline,
			ContentID:   "logo",
		})
	}))

	if err == nil || !strings.Contains(err.Error(), "content ID must equal filename") {
		t.Fatalf("error = %v", err)
	}
}

// TestDriverConformance verifies the driver passes the mail conformance suite.
func TestDriverConformance(t *testing.T) {
	mailtest.Run(t, func(t *testing.T) mail.Driver {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"<accepted@example.com>","message":"Queued"}`))
		}))
		t.Cleanup(server.Close)

		driver, err := mailgunmail.New("example.com", "key", func(transport *mailgunmail.Builder) {
			transport.Endpoint(server.URL)
		})

		if err != nil {
			t.Fatal(err)
		}

		return driver
	})
}

// TestMalformedEndpointFailsAtSend records the native SDK boundary: SetAPIBase
// rejects version suffixes, not every invalid URL. This malformed URL cannot
// trigger network I/O, and Send must return no receipt when request creation fails.
func TestMalformedEndpointFailsAtSend(t *testing.T) {
	t.Parallel()

	driver, err := mailgunmail.New("mg.example.com", "synthetic-key", func(transport *mailgunmail.Builder) {
		transport.Endpoint("://invalid")
	})

	if err != nil {
		t.Fatalf("SDK endpoint setup unexpectedly rejected the URL: %v", err)
	}

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "synthetic message"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	})

	receipt, err := driver.Send(t.Context(), message)

	if err == nil || receipt != (mail.Receipt{}) {
		t.Fatalf("malformed endpoint Send = %#v, %v; want empty receipt and error", receipt, err)
	}
}

// TestDriverFormattingDoesNotExposeAPIKey validates the driver boundary, not
// the borrowed SDK object's own formatting. The native accessor deliberately
// remains unsanitized and must not be treated as a safe diagnostic record.
func TestDriverFormattingDoesNotExposeAPIKey(t *testing.T) {
	t.Parallel()

	driver, err := mailgunmail.New("mg.example.com", "synthetic-api-key-marker")

	if err != nil {
		t.Fatal(err)
	}

	output := fmt.Sprintf("%v %+v %#v | %v %+v %#v", driver, driver, driver, *driver, *driver, *driver)

	if strings.Contains(output, "synthetic-api-key-marker") {
		t.Fatal("driver formatting exposed the Mailgun API key")
	}
}

// ExampleBuilderFunc demonstrates reusable endpoint policy and native HTTP
// configuration without sending a request or transferring transport ownership.
func ExampleBuilderFunc() {
	policies := []mailgunmail.BuilderFunc{func(transport *mailgunmail.Builder) {
		transport.Endpoint("https://api.eu.mailgun.net")
	}}

	driver, err := mailgunmail.New("mg.example.com", "private-key", policies...)

	if err != nil {
		fmt.Println(err)

		return
	}

	driver.Client().SetHTTPClient(&http.Client{Timeout: 10 * time.Second})
	fmt.Println(mail.New(driver).Driver() == driver)
	// Output: true
}
