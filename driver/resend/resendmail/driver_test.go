package resendmail_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resend/resend-go/v3"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
	"arkoren.dev/driver/resend/resendmail"
)

// resendRequestTimeout bounds one local HTTP request in a Resend contract
// test.
const resendRequestTimeout = 5 * time.Second

// bytesFromJSONNumbers preserves the Resend SDK's numeric byte array payload
// as bytes after v2 decoding.
func bytesFromJSONNumbers(numbers []int) []byte {
	bytes := make([]byte, len(numbers))
	for index, number := range numbers {
		bytes[index] = byte(number)
	}

	return bytes
}

// TestClientSendUsesResendJSONRequestContract verifies the documented JSON
// request method, path, bearer authentication, and message fields.
func TestClientSendUsesResendJSONRequestContract(t *testing.T) {
	t.Parallel()

	sdk, ctx := newResendTestClient(t, verifyResendMessage)

	sendResendMessage(t, sdk, ctx, resendJSONMessage())
}

// newResendTestClient creates a Resend client backed by a contract server.
func newResendTestClient(t *testing.T, verify func(*testing.T, *http.Request)) (*resend.Client, context.Context) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		verify(t, request)
		writeResendAccepted(writer)
	}))
	t.Cleanup(server.Close)

	sdk := resend.NewClient("key")
	baseURL, err := url.Parse(server.URL)

	if err != nil {
		t.Fatal(err)
	}

	sdk.BaseURL = baseURL
	ctx, cancel := context.WithTimeout(t.Context(), resendRequestTimeout)
	t.Cleanup(cancel)

	return sdk, ctx
}

// writeResendAccepted writes a successful Resend API response.
func writeResendAccepted(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"id":"resend-id"}`))
}

// sendResendMessage submits message through sdk and fails the calling test on error.
func sendResendMessage(t *testing.T, sdk *resend.Client, ctx context.Context, message mail.Message) {
	t.Helper()

	_, err := resendmail.NewFrom(sdk).Send(ctx, message)

	if err != nil {
		t.Fatal(err)
	}
}

// resendJSONMessage creates the message used by the Resend request contract test.
func resendJSONMessage() mail.Message {
	return mail.NewMessage(mail.Recipients{
		To: []mail.Address{
			{
				Name:  "Doe, Jane",
				Email: "first@example.com",
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
			Email: "blind@example.com",
		}},
	}, mail.Body{Text: "Plain text.", HTML: "<p>HTML</p>"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Name: `Sender, "Primary"`, Email: "sender@example.com"})
		letter.Subject("Resend contract")
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

// verifyResendMessage verifies the documented Resend email request fields.
func verifyResendMessage(t *testing.T, request *http.Request) {
	t.Helper()

	assertResendHeaders(t, request)
	assertResendMessagePayload(t, request)
}

// assertResendHeaders verifies the documented Resend request headers.
func assertResendHeaders(t *testing.T, request *http.Request) {
	t.Helper()

	assertResendString(t, "method", request.Method, http.MethodPost)
	assertResendString(t, "path", request.URL.Path, "/emails")
	assertResendString(t, "authorization", request.Header.Get("Authorization"), "Bearer key")
	assertResendString(t, "content type", request.Header.Get("Content-Type"), "application/json")
}

// assertResendMessagePayload verifies documented Resend email request fields.
func assertResendMessagePayload(t *testing.T, request *http.Request) {
	t.Helper()

	var payload struct {
		// From preserves sender display-name quoting in one mailbox string.
		From string `json:"from"`
		// To preserves the primary recipient order in the SDK request.
		To []string `json:"to"`
		// CC holds individually formatted copy-recipient mailboxes.
		CC []string `json:"cc"`
		// BCC includes the hidden recipients passed to the provider.
		BCC []string `json:"bcc"`
		// ReplyTo is the comma-separated reply mailbox representation.
		ReplyTo string `json:"reply_to"`
		// Subject is the unmodified message subject.
		Subject string `json:"subject"`
		// Text is the plain-text alternative sent with the message.
		Text string `json:"text"`
		// HTML is the HTML alternative, separate from the plain-text body.
		HTML string `json:"html"`
	}

	if err := json.UnmarshalRead(request.Body, &payload); err != nil {
		t.Fatal(err)

		return
	}

	assertResendString(t, "from", payload.From, `"Sender, \"Primary\"" <sender@example.com>`)
	assertResendAddresses(t, "to", payload.To, []string{`"Doe, Jane" <first@example.com>`, `"Quote \"Recipient\"" <second@example.com>`})
	assertResendAddresses(t, "cc", payload.CC, []string{`"Copy, Carol" <copy@example.com>`})
	assertResendAddresses(t, "bcc", payload.BCC, []string{`"Hidden \"Bob\"" <blind@example.com>`})
	assertResendString(t, "reply_to", payload.ReplyTo, `"Reply, Rachel" <reply@example.com>,"Reply \"Second\"" <second-reply@example.com>`)
	assertResendString(t, "subject", payload.Subject, "Resend contract")
	assertResendString(t, "text", payload.Text, "Plain text.")
	assertResendString(t, "html", payload.HTML, "<p>HTML</p>")
}

// assertResendString verifies one Resend string request value.
func assertResendString(t *testing.T, name, actual, expected string) {
	t.Helper()

	if actual != expected {
		t.Fatalf("%s = %q", name, actual)
	}
}

// assertResendAddresses verifies one Resend mailbox list.
func assertResendAddresses(t *testing.T, name string, actual, expected []string) {
	t.Helper()

	if !slices.Equal(actual, expected) {
		t.Fatalf("%s = %#v", name, actual)
	}
}

// TestClientSendEncodesResendAttachments verifies Resend's JSON attachment
// encoding, content type, and inline content ID fields.
func TestClientSendEncodesResendAttachments(t *testing.T) {
	t.Parallel()

	sdk, ctx := newResendTestClient(t, verifyResendAttachments)

	sendResendMessage(t, sdk, ctx, resendAttachmentMessage())
}

// resendAttachmentPayload is one attachment decoded from a Resend request.
type resendAttachmentPayload struct {
	// Content is the numeric JSON representation of attachment bytes.
	Content []int `json:"content"`

	// Filename is the attachment filename.
	Filename string `json:"filename"`

	// ContentType is the attachment media type.
	ContentType string `json:"content_type"`

	// ContentID is the inline attachment identifier.
	ContentID string `json:"content_id"`
}

// verifyResendAttachments verifies documented regular and inline attachments.
func verifyResendAttachments(t *testing.T, request *http.Request) {
	t.Helper()

	var payload struct {
		// Attachments retains the encoded regular and inline attachment records.
		Attachments []resendAttachmentPayload `json:"attachments"`
	}

	if err := json.UnmarshalRead(request.Body, &payload); err != nil {
		t.Fatal(err)

		return
	}

	assertResendAttachments(t, payload.Attachments)
}

// assertResendAttachments verifies the regular and inline Resend attachments.
func assertResendAttachments(t *testing.T, attachments []resendAttachmentPayload) {
	t.Helper()

	if len(attachments) != 2 {
		t.Fatalf("attachments = %#v", attachments)
	}

	assertResendAttachment(t, attachments[0], "report", "report.txt", "text/plain", "")
	assertResendAttachment(t, attachments[1], "image", "logo.png", "image/png", "logo")
}

// assertResendAttachment verifies one Resend attachment payload.
func assertResendAttachment(t *testing.T, attachment resendAttachmentPayload, content, filename, contentType, contentID string) {
	t.Helper()

	if !matchesResendAttachment(attachment, content, filename, contentType, contentID) {
		t.Fatalf("attachment = %#v", attachment)
	}
}

// matchesResendAttachment reports whether attachment contains the expected fields.
func matchesResendAttachment(attachment resendAttachmentPayload, content, filename, contentType, contentID string) bool {
	return string(bytesFromJSONNumbers(attachment.Content)) == content &&
		attachment.Filename == filename &&
		attachment.ContentType == contentType &&
		attachment.ContentID == contentID
}

// resendAttachmentMessage creates the attachment message used by the contract test.
func resendAttachmentMessage() mail.Message {
	return mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "person@example.com",
		}},
	}, mail.Body{Text: "Body"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(
			mail.Attachment{
				Filename:    "report.txt",
				ContentType: "text/plain",
				Data:        []byte("report"),
			},
			mail.Attachment{
				Filename:    "logo.png",
				ContentType: "image/png",
				Data:        []byte("image"),
				Disposition: mail.AttachmentDispositionInline,
				ContentID:   "logo",
			},
		)
	})
}

// TestClientSendParsesResendReceipt verifies Resend response IDs become mail
// receipts.
func TestClientSendParsesResendReceipt(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resend-id"}`))
	}))
	t.Cleanup(server.Close)

	sdk := resend.NewClient("key")
	baseURL, err := url.Parse(server.URL)

	if err != nil {
		t.Fatal(err)
	}

	sdk.BaseURL = baseURL
	ctx, cancel := context.WithTimeout(t.Context(), resendRequestTimeout)
	t.Cleanup(cancel)

	receipt, err := resendmail.NewFrom(sdk).Send(ctx, mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "person@example.com",
		}},
	}, mail.Body{Text: "Body"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	}))

	if err != nil {
		t.Fatal(err)
	}

	if receipt.ID != "resend-id" {
		t.Fatalf("receipt = %#v", receipt)
	}
}

// TestClientSendReturnsResendError verifies documented non-success responses
// return the provider error.
func TestClientSendReturnsResendError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"message":"unauthorized"}`))
	}))
	t.Cleanup(server.Close)

	sdk := resend.NewClient("key")
	baseURL, err := url.Parse(server.URL)

	if err != nil {
		t.Fatal(err)
	}

	sdk.BaseURL = baseURL
	ctx, cancel := context.WithTimeout(t.Context(), resendRequestTimeout)
	t.Cleanup(cancel)

	_, err = resendmail.NewFrom(sdk).Send(ctx, mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "person@example.com",
		}},
	}, mail.Body{Text: "Body"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	}))

	if err == nil {
		t.Fatal("expected Resend error")
	}

	if err.Error() != "send resend message: [ERROR]: unauthorized" {
		t.Fatalf("error = %q", err)
	}
}

// TestDriverConformance verifies the driver passes the mail conformance suite.
func TestDriverConformance(t *testing.T) {
	mailtest.Run(t, func(t *testing.T) mail.Driver {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writeResendAccepted(writer)
		}))
		t.Cleanup(server.Close)

		sdk := resend.NewClient("key")
		baseURL, err := url.Parse(server.URL)

		if err != nil {
			t.Fatal(err)
		}

		sdk.BaseURL = baseURL

		return resendmail.NewFrom(sdk)
	})
}

// TestNewRequiresExplicitAPIKey preserves the settings-free constructor's
// required secret validation without introducing an environment fallback.
func TestNewRequiresExplicitAPIKey(t *testing.T) {
	t.Parallel()

	if _, err := resendmail.New(""); err == nil {
		t.Fatal("empty API key accepted")
	}
}

// TestNewCreatesNativeClient verifies the required-key constructor still
// publishes an SDK-backed driver without making a request or taking callbacks.
func TestNewCreatesNativeClient(t *testing.T) {
	t.Parallel()

	driver, err := resendmail.New("resend-key")

	if err != nil {
		t.Fatal(err)
	}

	if driver.Client() == nil {
		t.Fatal("native client missing")
	}
}

// TestNewFromBorrowsNativeClient verifies settings-free wrapping preserves the
// caller's live SDK object, including its endpoint configuration.
func TestNewFromBorrowsNativeClient(t *testing.T) {
	t.Parallel()

	client := resend.NewClient("resend-key")
	endpoint := &url.URL{
		Scheme: "https",
		Host:   "mail.example.test",
	}
	client.BaseURL = endpoint

	driver := resendmail.NewFrom(client)

	if driver.Client() != client || driver.Client().BaseURL != endpoint {
		t.Fatal("caller-owned client replaced or cloned")
	}
}

// TestNewFromRetainsNilWithoutValidation preserves the existing nonvalidating
// wrapper contract. It deliberately does not attempt to send through nil.
func TestNewFromRetainsNilWithoutValidation(t *testing.T) {
	t.Parallel()

	if driver := resendmail.NewFrom(nil); driver.Client() != nil {
		t.Fatal("NewFrom changed its nonvalidating contract")
	}
}

// TestDriverFormattingDoesNotExposeAPIKey checks the driver pointer and value,
// independently of SDK formatting. Native clients remain caller-owned objects
// with credentials and are explicitly not covered by this diagnostic boundary.
func TestDriverFormattingDoesNotExposeAPIKey(t *testing.T) {
	t.Parallel()

	driver, err := resendmail.New("synthetic-api-key-marker")

	if err != nil {
		t.Fatal(err)
	}

	output := fmt.Sprintf("%v %+v %#v | %v %+v %#v", driver, driver, driver, *driver, *driver, *driver)

	if strings.Contains(output, "synthetic-api-key-marker") {
		t.Fatal("driver formatting exposed the Resend API key")
	}
}

// ExampleNew constructs a facade without making a sending API request. The
// explicit synthetic credential makes the example independent of environment.
func ExampleNew() {
	driver, err := resendmail.New("api-key")

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(mail.New(driver).Driver() == driver)
	// Output: true
}

// ExampleNewFrom retains the native client for caller-owned setup and reuse.
// Merely obtaining the same client does not send mail or expose its API key.
func ExampleNewFrom() {
	client := resend.NewClient("api-key")
	driver := resendmail.NewFrom(client)

	fmt.Println(driver.Client() == client)
	// Output: true
}
