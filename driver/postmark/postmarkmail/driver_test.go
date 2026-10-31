package postmarkmail_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
	"arkoren.dev/driver/postmark/postmarkmail"
)

// TestConstructionDefersMalformedURLToSend preserves Postmark's original URL
// error timing; an invalid endpoint fails request creation without network I/O.
func TestConstructionDefersMalformedURLToSend(t *testing.T) {
	t.Parallel()

	driver, err := postmarkmail.New("token", func(transport *postmarkmail.Builder) {
		transport.Endpoint("://invalid")
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := driver.Send(t.Context(), documentedPostmarkMessage()); err == nil {
		t.Fatal("malformed URL accepted by Send")
	}
}

// TestClientSendPostsDocumentedJSON verifies Send uses Postmark's email API
// method, path, authentication header, and JSON field names.
func TestClientSendPostsDocumentedJSON(t *testing.T) {
	t.Parallel()

	client, ctx := newPostmarkTestClient(t, verifyPostmarkJSON)

	sendPostmarkMessage(t, client, ctx, documentedPostmarkMessage())
}

// newPostmarkTestClient creates a Postmark client backed by a contract server.
func newPostmarkTestClient(t *testing.T, verify func(*testing.T, *http.Request)) (*postmarkmail.Driver, context.Context) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		verify(t, request)
		writePostmarkAccepted(writer)
	}))
	t.Cleanup(server.Close)

	client, err := postmarkmail.New("postmark-token", func(transport *postmarkmail.Builder) {
		transport.Endpoint(server.URL + "/email")
	})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	return client, ctx
}

// writePostmarkAccepted writes a successful Postmark API response.
func writePostmarkAccepted(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"MessageID":"postmark-id"}`))
}

// sendPostmarkMessage submits message and fails the calling test on error.
func sendPostmarkMessage(t *testing.T, client *postmarkmail.Driver, ctx context.Context, message mail.Message) {
	t.Helper()

	_, err := client.Send(ctx, message)

	if err != nil {
		t.Fatal(err)
	}
}

// documentedPostmarkMessage creates the message used by the JSON contract test.
func documentedPostmarkMessage() mail.Message {
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
	}, mail.Body{Text: "Hello.", HTML: "<strong>Hello.</strong>"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Name: `Sender, "Primary"`, Email: "sender@example.com"})
		letter.Subject("Postmark contract")
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

// verifyPostmarkJSON verifies Postmark's documented request method and fields.
func verifyPostmarkJSON(t *testing.T, request *http.Request) {
	t.Helper()

	assertPostmarkHeaders(t, request)
	assertPostmarkJSONPayload(t, request)
}

// assertPostmarkHeaders verifies Postmark's documented request headers.
func assertPostmarkHeaders(t *testing.T, request *http.Request) {
	t.Helper()

	assertPostmarkString(t, "method", request.Method, http.MethodPost)
	assertPostmarkString(t, "path", request.URL.Path, "/email")
	assertPostmarkString(t, "server token", request.Header.Get("X-Postmark-Server-Token"), "postmark-token")
	assertPostmarkString(t, "content type", request.Header.Get("Content-Type"), "application/json")
}

// assertPostmarkJSONPayload verifies Postmark's documented JSON payload fields.
func assertPostmarkJSONPayload(t *testing.T, request *http.Request) {
	t.Helper()

	var payload struct {
		// From is the formatted sender, including display-name quoting.
		From string `json:"From"`
		// To contains the comma-separated primary recipient mailboxes.
		To string `json:"To"`
		// CC contains the provider's copy-recipient representation.
		CC string `json:"Cc"`
		// BCC contains hidden recipients included in the provider request.
		BCC string `json:"Bcc"`
		// ReplyTo preserves the order and formatting of reply mailboxes.
		ReplyTo string `json:"ReplyTo"`
		// Subject is the unmodified message subject.
		Subject string `json:"Subject"`
		// TextBody is the plain-text alternative sent to Postmark.
		TextBody string `json:"TextBody"`
		// HTMLBody is the HTML alternative sent alongside plain text.
		HTMLBody string `json:"HtmlBody"`
	}

	if err := json.UnmarshalRead(request.Body, &payload); err != nil {
		t.Fatal(err)

		return
	}

	assertPostmarkString(t, "from", payload.From, `"Sender, \"Primary\"" <sender@example.com>`)
	assertPostmarkString(t, "to", payload.To, `"Doe, Jane" <person@example.com>,"Quote \"Recipient\"" <second@example.com>`)
	assertPostmarkString(t, "cc", payload.CC, `"Copy, Carol" <copy@example.com>`)
	assertPostmarkString(t, "bcc", payload.BCC, `"Hidden \"Bob\"" <hidden@example.com>`)
	assertPostmarkString(t, "reply to", payload.ReplyTo, `"Reply, Rachel" <reply@example.com>,"Reply \"Second\"" <second-reply@example.com>`)
	assertPostmarkString(t, "subject", payload.Subject, "Postmark contract")
	assertPostmarkString(t, "text body", payload.TextBody, "Hello.")
	assertPostmarkString(t, "html body", payload.HTMLBody, "<strong>Hello.</strong>")
}

// assertPostmarkString verifies one Postmark string field.
func assertPostmarkString(t *testing.T, name, actual, expected string) {
	t.Helper()

	if actual != expected {
		t.Fatalf("%s = %q", name, actual)
	}
}

// TestClientSendEncodesPostmarkAttachment verifies Send uses the documented
// base64 attachment content and content type fields.
func TestClientSendEncodesPostmarkAttachment(t *testing.T) {
	t.Parallel()

	client, ctx := newPostmarkTestClient(t, verifyPostmarkAttachment)

	sendPostmarkMessage(t, client, ctx, postmarkAttachmentMessage())
}

// verifyPostmarkAttachment verifies the documented regular attachment payload.
func verifyPostmarkAttachment(t *testing.T, request *http.Request) {
	t.Helper()

	assertPostmarkAttachment(t, postmarkAttachments(t, request), "report.txt", "cmVwb3J0", "text/plain", "")
}

// postmarkAttachmentMessage creates the regular attachment contract message.
func postmarkAttachmentMessage() mail.Message {
	return mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "person@example.com",
		}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename:    "report.txt",
			ContentType: "text/plain",
			Data:        []byte("report"),
		})
	})
}

// TestClientSendEncodesPostmarkInlineAttachment verifies Send supplies a
// ContentID for the documented inline attachment representation.
func TestClientSendEncodesPostmarkInlineAttachment(t *testing.T) {
	t.Parallel()

	client, ctx := newPostmarkTestClient(t, verifyPostmarkInlineAttachment)

	sendPostmarkMessage(t, client, ctx, postmarkInlineAttachmentMessage())
}

// verifyPostmarkInlineAttachment verifies the documented inline attachment payload.
func verifyPostmarkInlineAttachment(t *testing.T, request *http.Request) {
	t.Helper()

	assertPostmarkAttachment(t, postmarkAttachments(t, request), "logo.png", "aW1hZ2U=", "image/png", "cid:logo")
}

// postmarkInlineAttachmentMessage creates the inline attachment contract message.
func postmarkInlineAttachmentMessage() mail.Message {
	return mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "person@example.com",
		}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename:    "logo.png",
			ContentType: "image/png",
			Data:        []byte("image"),
			Disposition: mail.AttachmentDispositionInline,
			ContentID:   "logo",
		})
	})
}

// postmarkAttachments decodes the attachment portion of a Postmark request.
func postmarkAttachments(t *testing.T, request *http.Request) []struct {
	// Name is the filename exposed to the recipient.
	Name string `json:"Name"`
	// Content is the base64 representation of the attachment bytes.
	Content string `json:"Content"`
	// ContentType is the declared media type, not inferred from the filename.
	ContentType string `json:"ContentType"`
	// ContentID identifies inline attachments referenced by HTML.
	ContentID string `json:"ContentID"`
} {
	t.Helper()

	var payload struct {
		// Attachments preserves the outgoing attachment order for assertions.
		Attachments []struct {
			// Name is the filename exposed to the recipient.
			Name string `json:"Name"`
			// Content is the base64 representation of the attachment bytes.
			Content string `json:"Content"`
			// ContentType carries the declared attachment media type.
			ContentType string `json:"ContentType"`
			// ContentID is empty for ordinary, non-inline attachments.
			ContentID string `json:"ContentID"`
		} `json:"Attachments"`
	}

	if err := json.UnmarshalRead(request.Body, &payload); err != nil {
		t.Fatal(err)
	}

	return payload.Attachments
}

// assertPostmarkAttachment verifies the sole Postmark attachment payload.
func assertPostmarkAttachment(t *testing.T, attachments []struct {
	// Name is compared with the expected recipient-facing filename.
	Name string `json:"Name"`
	// Content is compared in base64 form to detect encoding changes.
	Content string `json:"Content"`
	// ContentType is compared with the original attachment's media type.
	ContentType string `json:"ContentType"`
	// ContentID distinguishes ordinary and inline attachment payloads.
	ContentID string `json:"ContentID"`
}, name, content, contentType, contentID string) {
	t.Helper()

	if len(attachments) != 1 {
		t.Fatalf("attachment count = %d", len(attachments))
	}

	attachment := attachments[0]

	if !matchesPostmarkAttachment(attachment, name, content, contentType, contentID) {
		t.Fatalf("attachment = %#v", attachment)
	}
}

// matchesPostmarkAttachment reports whether attachment contains the expected fields.
func matchesPostmarkAttachment(attachment struct {
	// Name is the filename decoded from the provider request.
	Name string `json:"Name"`
	// Content is the encoded attachment body decoded from JSON.
	Content string `json:"Content"`
	// ContentType identifies the attachment's declared media type.
	ContentType string `json:"ContentType"`
	// ContentID is the optional inline reference identifier.
	ContentID string `json:"ContentID"`
}, name, content, contentType, contentID string) bool {
	return attachment.Name == name &&
		attachment.Content == content &&
		attachment.ContentType == contentType &&
		attachment.ContentID == contentID
}

// TestClientSendParsesPostmarkReceipt verifies Send returns the provider's
// documented MessageID as the portable receipt ID.
func TestClientSendParsesPostmarkReceipt(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"MessageID":"postmark-receipt"}`))
	}))
	t.Cleanup(server.Close)

	client, err := postmarkmail.New("postmark-token", func(transport *postmarkmail.Builder) {
		transport.Endpoint(server.URL + "/email")
	})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	receipt, err := client.Send(ctx, mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "person@example.com",
		}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	}))

	if err != nil {
		t.Fatal(err)
	}

	if receipt.ID != "postmark-receipt" {
		t.Fatalf("receipt ID = %q", receipt.ID)
	}
}

// TestClientSendReturnsPostmarkError verifies documented non-success provider
// responses do not produce a receipt.
func TestClientSendReturnsPostmarkError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	client, err := postmarkmail.New("postmark-token", func(transport *postmarkmail.Builder) {
		transport.Endpoint(server.URL + "/email")
	})

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	receipt, err := client.Send(ctx, mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Email: "person@example.com",
		}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	}))

	assertPostmarkError(t, receipt, err)
}

// assertPostmarkError verifies a non-success Postmark response has no receipt.
func assertPostmarkError(t *testing.T, receipt mail.Receipt, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected Postmark error")
	}

	if !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("error = %q", err)
	}

	if receipt.ID != "" {
		t.Fatalf("receipt ID = %q", receipt.ID)
	}
}

// TestDriverConformance verifies the driver passes the mail conformance suite.
func TestDriverConformance(t *testing.T) {
	mailtest.Run(t, func(t *testing.T) mail.Driver {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writePostmarkAccepted(writer)
		}))
		t.Cleanup(server.Close)

		driver, err := postmarkmail.New("token", func(transport *postmarkmail.Builder) {
			transport.Endpoint(server.URL + "/email")
		})

		if err != nil {
			t.Fatal(err)
		}

		return driver
	})
}

// ExampleNew constructs the default driver and mail facade without network I/O.
// A synthetic token is sufficient until Send; production callers supply their
// own server token and handle construction errors before using the mailer.
func ExampleNew() {
	driver, err := postmarkmail.New("server-token")

	if err != nil {
		fmt.Println(err)

		return
	}

	mailer := mail.New(driver)
	fmt.Println(mailer.Driver() == driver)
	// Output: true
}

// TestSendPrefixesPostmarkInlineCID checks the wire format in Postmark's Email
// API and inline-image guide: ContentID includes "cid:" and equals the HTML src.
// The portable message retains its bare ID, and ordinary attachments omit it.
func TestSendPrefixesPostmarkInlineCID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)

		if err != nil {
			t.Error(err)
		}

		want := `{"From":"<sender@example.com>","To":"<person@example.com>","Subject":"","HtmlBody":"<img src=\"cid:logo\">","Attachments":[{"Name":"logo.png","Content":"aW1hZ2U=","ContentType":"image/png","ContentID":"cid:logo"},{"Name":"report.txt","Content":"cmVwb3J0","ContentType":"text/plain"}]}`

		if string(body) != want {
			t.Errorf("Postmark payload = %s, want %s", body, want)
		}

		_, _ = writer.Write([]byte(`{"MessageID":"inline-id"}`))
	}))
	t.Cleanup(server.Close)

	driver, err := postmarkmail.New("synthetic-token", func(transport *postmarkmail.Builder) {
		transport.Endpoint(server.URL + "/email")
	})

	if err != nil {
		t.Fatal(err)
	}

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{HTML: `<img src="cid:logo">`}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(
			mail.Attachment{
				Filename: "logo.png", ContentType: "image/png", Data: []byte("image"),
				Disposition: mail.AttachmentDispositionInline, ContentID: "logo",
			},
			mail.Attachment{Filename: "report.txt", ContentType: "text/plain", Data: []byte("report")},
		)
	})

	receipt, err := driver.Send(t.Context(), message)

	if err != nil || receipt.ID != "inline-id" {
		t.Fatalf("Send = %#v, %v", receipt, err)
	}

	if message.Attachments()[0].ContentID != "logo" {
		t.Fatal("Send changed the portable content ID")
	}
}

// TestSendRejectsRedirectsWithDefaultClient checks every redirect followed by
// net/http against two loopback origins. No real provider or credential is used;
// a redirect must return a submission error without contacting its destination.
func TestSendRejectsRedirectsWithDefaultClient(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		assertSendRejectsRedirect(t, status, nil)
	}
}

// TestSendRejectsRedirectsWithBorrowedClient ensures an ordinary supplied client
// cannot forward the server token or replay a message after a redirect. The
// driver's policy must not overwrite the caller's nil CheckRedirect setting.
func TestSendRejectsRedirectsWithBorrowedClient(t *testing.T) {
	t.Parallel()

	client := &http.Client{Timeout: time.Second}

	for _, status := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		assertSendRejectsRedirect(t, status, client)
	}

	if client.CheckRedirect != nil {
		t.Fatal("Send mutated the borrowed client's redirect policy")
	}
}

// TestSendRejectsRedirectsDespiteBorrowedPolicy verifies driver policy takes
// precedence over a permissive caller callback without invoking or replacing it.
// The caller can still use that callback independently after the send fails.
func TestSendRejectsRedirectsDespiteBorrowedPolicy(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	client := &http.Client{
		Timeout: time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			calls.Add(1)

			return nil
		},
	}

	assertSendRejectsRedirect(t, http.StatusTemporaryRedirect, client)

	if calls.Load() != 0 {
		t.Fatal("Send invoked the borrowed client's redirect callback")
	}

	if err := client.CheckRedirect(nil, nil); err != nil {
		t.Fatal(err)
	}

	if calls.Load() != 1 {
		t.Fatal("Send replaced the borrowed client's redirect callback")
	}
}

// TestSendRejectsSameOriginRedirect verifies the policy also rejects relative
// redirects within the configured origin. A message is not replayed to another
// path merely because that path shares a host with the trusted endpoint.
func TestSendRejectsSameOriginRedirect(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)

		if request.URL.Path == "/email" {
			writer.Header().Set("Location", "/capture")
			writer.WriteHeader(http.StatusPermanentRedirect)

			return
		}

		_, _ = writer.Write([]byte(`{"MessageID":"redirected-id"}`))
	}))
	t.Cleanup(server.Close)

	driver, err := postmarkmail.New("synthetic-server-token", func(transport *postmarkmail.Builder) {
		transport.Endpoint(server.URL + "/email")
	})

	if err != nil {
		t.Fatal(err)
	}

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "private synthetic message"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	})

	receipt, err := driver.Send(t.Context(), message)

	if err == nil || !strings.Contains(err.Error(), "status 308 Permanent Redirect") {
		t.Errorf("redirect error = %v, want original status", err)
	}

	if receipt != (mail.Receipt{}) || calls.Load() != 1 {
		t.Fatalf("receipt = %#v, requests = %d; want empty receipt and one request", receipt, calls.Load())
	}
}

// assertSendRejectsRedirect exercises the public Driver.Send boundary with a
// synthetic token and message. Its independent loopback destination records any
// forwarded credential or body; the original status and response body must be
// reported as the submission error, with an empty receipt and no second request.
func assertSendRejectsRedirect(t *testing.T, status int, client *http.Client) {
	t.Helper()

	var destinationCalls atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		destinationCalls.Add(1)
		body, err := io.ReadAll(request.Body)

		t.Errorf("redirect %d destination received %s, token %q, body %q, read error %v",
			status, request.Method, request.Header.Get("X-Postmark-Server-Token"), body, err)

		_, _ = writer.Write([]byte(`{"MessageID":"redirected-id"}`))
	}))
	t.Cleanup(destination.Close)

	var originCalls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		originCalls.Add(1)

		if request.Method != http.MethodPost || request.URL.Path != "/custom-email" {
			t.Errorf("initial request = %s %s", request.Method, request.URL.Path)
		}

		if request.Header.Get("X-Postmark-Server-Token") != "synthetic-server-token" {
			t.Error("initial request did not carry the synthetic token")
		}

		writer.Header().Set("Location", destination.URL+"/capture")
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte("redirect response"))
	}))
	t.Cleanup(origin.Close)

	driver, err := postmarkmail.New("synthetic-server-token", func(transport *postmarkmail.Builder) {
		transport.Endpoint(origin.URL + "/custom-email")
		transport.HTTPClient(client)
	})

	if err != nil {
		t.Fatal(err)
	}

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "private synthetic message"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	})

	receipt, err := driver.Send(t.Context(), message)

	want := fmt.Sprintf("send postmark message: status %d %s: redirect response", status, http.StatusText(status))

	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("redirect %d error = %v, want %q", status, err, want)
	}

	if receipt != (mail.Receipt{}) {
		t.Errorf("redirect %d receipt = %#v, want empty", status, receipt)
	}

	if originCalls.Load() != 1 || destinationCalls.Load() != 0 {
		t.Errorf("redirect %d requests: origin = %d, destination = %d; want 1, 0",
			status, originCalls.Load(), destinationCalls.Load())
	}
}
