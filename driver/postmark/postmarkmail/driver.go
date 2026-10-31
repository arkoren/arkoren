package postmarkmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"arkoren.dev/core/service/mail"
)

// settings holds Postmark's optional construction state. Its scalar values are
// copied at publication while the live HTTP client remains borrowed. Send copies
// that client per request to impose the driver's redirect policy.
type settings struct {
	// endpoint overrides Postmark's send-email API URL. The default is empty,
	// which uses https://api.postmarkapp.com/email.
	endpoint string

	// httpClient supplies Postmark's borrowed transport, cookie jar, and timeout.
	// Nil uses a client with a 30-second timeout. Send overrides CheckRedirect
	// on a per-request copy, leaving the supplied client unchanged. Pass a
	// transport from [telemetry.Telemetry.Transport] to trace the HTTP calls.
	//
	// [telemetry.Telemetry.Transport]: https://pkg.go.dev/arkoren.dev/core/service/telemetry#Telemetry.Transport
	httpClient *http.Client
}

// request is the Postmark send-email API payload.
type request struct {
	// From is the sender mailbox.
	From string `json:"From"`

	// To contains recipient mailboxes.
	To string `json:"To"`

	// CC contains carbon-copy recipient mailboxes.
	CC string `json:"Cc,omitempty"`

	// BCC contains blind-carbon-copy recipient mailboxes.
	BCC string `json:"Bcc,omitempty"`

	// ReplyTo contains reply-to mailboxes.
	ReplyTo string `json:"ReplyTo,omitempty"`

	// Subject is the message subject.
	Subject string `json:"Subject"`

	// TextBody is the plain-text body.
	TextBody string `json:"TextBody,omitempty"`

	// HTMLBody is the HTML body.
	HTMLBody string `json:"HtmlBody,omitempty"`

	// Attachments contains API attachment payloads.
	Attachments []attachment `json:"Attachments,omitempty"`
}

// attachment is one Postmark API attachment payload.
type attachment struct {
	// Name is the attachment filename.
	Name string `json:"Name"`

	// Content is the base64-encoded attachment data.
	Content string `json:"Content"`

	// ContentType is the attachment media type.
	ContentType string `json:"ContentType"`

	// ContentID is Postmark's cid:-prefixed inline image reference, matching
	// the HTML src. Portable mail.Attachment values keep only the bare ID.
	ContentID string `json:"ContentID,omitempty"`
}

// Driver submits [mail.Message] values through the Postmark API. Each Send
// applies the no-redirect policy without modifying a borrowed client. Format
// hides credentials and connection settings; a zero Driver is not usable.
//
// Example:
//
//	driver, err := postmarkmail.New("server-token")
//	if err != nil {
//		panic(err)
//	}
//	_ = mail.New(driver)
type Driver struct {
	// token authenticates requests to Postmark.
	token string

	// endpoint receives Postmark send-email requests.
	endpoint string

	// http is the borrowed request client. Send copies it before replacing its
	// redirect policy; its transport and cookie jar remain shared and unclosed.
	http *http.Client
}

// New creates a Postmark [Driver] authenticating with token.
//
// token is the Postmark server token, sent as X-Postmark-Server-Token. Callbacks
// apply [Builder.Endpoint] and [Builder.HTTPClient]. No request is made until
// [Driver.Send].
//
// Callbacks run directly once, synchronously in order on one fresh draft before
// validation. Nil entries panic; expanded nil or empty slices omit edits.
// Publication copies settings, not the supplied live HTTP client or its transport.
//
// Send never follows redirects, including same-origin redirects, so a redirect
// cannot forward the server token or replay the message. Responses with status
// 300 or higher return a submission error and an empty receipt. Each send uses
// a shallow client copy with its own redirect policy, ignoring any supplied
// CheckRedirect callback without modifying or closing the caller's client.
// The transport and cookie jar remain shared; configure them before use and
// trust custom transports with the token and message. A custom endpoint must
// be the trusted final URL, not a redirecting URL.
//
// New returns an error when token is empty.
//
// Example:
//
//	driver, err := postmarkmail.New("server-token")
//	if err != nil {
//		panic(err)
//	}
//	mailer := mail.New(driver)
//	_ = mailer // Use Send with a request context and an immutable message.
func New(token string, configure ...BuilderFunc) (*Driver, error) {
	transport := &Builder{}

	for _, configure := range configure {
		configure(transport)
	}

	config := transport.config

	if token == "" {
		return nil, errors.New("postmark token is required")
	}

	endpoint := config.endpoint

	if endpoint == "" {
		endpoint = "https://api.postmarkapp.com/email"
	}

	client := config.httpClient

	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
		}
	}

	return &Driver{
		token:    token,
		endpoint: endpoint,
		http:     client,
	}, nil
}

// Send validates an immutable [mail.Message] and submits its snapshot through
// Postmark. The message must include a sender. Validation, encoding, HTTP, and
// provider failures return an empty receipt without changing the message.
//
// Example using a configured driver and request context:
//
//	message := mail.NewMessage(mail.Recipients{
//		To: []mail.Address{{Email: "person@example.com"}},
//	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
//		letter.From(mail.Address{Email: "sender@example.com"})
//	})
//	receipt, err := driver.Send(ctx, message)
//	if err != nil {
//		return err
//	}
//	_ = receipt.ID
func (driver *Driver) Send(ctx context.Context, message mail.Message) (mail.Receipt, error) {
	if err := mail.CheckSend(ctx, message); err != nil {
		return mail.Receipt{}, err
	}

	payload, err := encodeMessage(message)

	if err != nil {
		return mail.Receipt{}, err
	}

	response, err := driver.sendRequest(ctx, payload)

	if err != nil {
		return mail.Receipt{}, err
	}

	defer response.Body.Close()

	return decodeReceipt(response)
}

// encodeMessage creates the native Postmark JSON payload from a validated
// message. Each container getter is read once to obtain detached recipients or
// attachment bytes; native wire field names remain independent of mail's API.
func encodeMessage(message mail.Message) ([]byte, error) {
	payload := request{
		From:        message.From().String(),
		To:          formats(message.To()),
		CC:          formats(message.CC()),
		BCC:         formats(message.BCC()),
		ReplyTo:     formats(message.ReplyTo()),
		Subject:     message.Subject(),
		TextBody:    message.TextBody(),
		HTMLBody:    message.HTMLBody(),
		Attachments: attachments(message.Attachments()),
	}
	data, err := json.Marshal(payload)

	if err != nil {
		return nil, fmt.Errorf("encode postmark message: %w", err)
	}

	return data, nil
}

// sendRequest posts a JSON Postmark message only to the configured endpoint.
// A per-request client copy rejects redirects without invoking the borrowed
// client's policy or changing its settings. Non-success responses are closed
// and returned as status/body errors; the caller closes successful responses.
func (driver *Driver) sendRequest(ctx context.Context, payload []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, driver.endpoint, bytes.NewReader(payload))

	if err != nil {
		return nil, err
	}

	request.Header.Set("X-Postmark-Server-Token", driver.token)
	request.Header.Set("Content-Type", "application/json")

	// client shares the borrowed transport and jar, but not its redirect policy.
	// ErrUseLastResponse preserves the original response for status error handling.
	client := *driver.http
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	response, err := client.Do(request)

	if err != nil {
		return nil, fmt.Errorf("send postmark message: %w", err)
	}

	if response.StatusCode >= 300 {
		defer response.Body.Close()

		// Postmark explains failures in a JSON body with ErrorCode and Message.
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))

		return nil, fmt.Errorf("send postmark message: status %s: %s", response.Status, bytes.TrimSpace(body))
	}

	return response, nil
}

// decodeReceipt decodes a successful Postmark response into a portable receipt.
func decodeReceipt(response *http.Response) (mail.Receipt, error) {
	var result struct {
		// MessageID is the provider-assigned receipt identifier returned on success.
		MessageID string `json:"MessageID"`
	}

	if err := json.UnmarshalRead(response.Body, &result); err != nil {
		return mail.Receipt{}, fmt.Errorf("decode postmark response: %w", err)
	}

	return mail.Receipt{
		ID: result.MessageID,
	}, nil
}

// formats renders mailboxes for Postmark.
func formats(addresses []mail.Address) string {
	values := make([]string, 0, len(addresses))

	for _, address := range addresses {
		values = append(values, address.String())
	}

	return strings.Join(values, ",")
}

// attachments maps portable attachments to Postmark API attachments. Postmark
// requires the full cid: reference rather than mail's bare ContentID; ordinary
// attachments omit that field. The conversion never changes the source message.
// See https://postmarkapp.com/developer/user-guide/send-email-with-api#inline-image-attachments.
func attachments(values []mail.Attachment) []attachment {
	result := make([]attachment, 0, len(values))

	for _, value := range values {
		item := attachment{
			Name:        value.Filename,
			Content:     base64.StdEncoding.EncodeToString(value.Data),
			ContentType: value.ContentType,
		}

		if value.Disposition == mail.AttachmentDispositionInline {
			item.ContentID = "cid:" + value.ContentID
		}

		result = append(result, item)
	}

	return result
}

var _ mail.Driver = (*Driver)(nil)
