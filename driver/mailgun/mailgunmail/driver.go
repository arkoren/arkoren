package mailgunmail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mailgun/mailgun-go/v5"

	"arkoren.dev/core/service/mail"
)

// settings holds the completed optional Mailgun construction state. It remains
// private because configuration is accepted only through Builder callbacks.
type settings struct {
	// endpoint overrides Mailgun's API base URL. The default is empty, which
	// uses the US endpoint; set the EU endpoint for EU-region accounts.
	endpoint string
}

// Driver submits [mail.Message] values through the Mailgun Go SDK. It borrows
// the SDK client without closing it; configure native settings before concurrent
// use. Construction performs no network request and zero Driver is not usable.
//
// Example:
//
//	driver, err := mailgunmail.New("mg.example.com", "private-key")
//	if err != nil {
//		panic(err)
//	}
//	_ = mail.New(driver)
type Driver struct {
	// domain is the Mailgun sending domain.
	domain string

	// client submits requests through the Mailgun SDK.
	client *mailgun.Client
}

// New creates a Mailgun API [Driver] that sends from domain, authenticating
// with apiKey.
//
// domain is the verified Mailgun sending domain and apiKey is the private API
// key. Callbacks apply [Builder.Endpoint]. No request is made until
// [Driver.Send].
//
// Callbacks run directly once, synchronously in order on one fresh draft before
// validation. Nil entries panic; expanded nil or empty slices omit edits.
// Retaining the draft cannot change the published driver's endpoint.
//
// New returns an error when domain or apiKey is empty (naming the missing
// setting) or the SDK rejects an endpoint containing an API-version path
// segment such as /v3. The SDK does not fully validate URL syntax at construction;
// other malformed endpoint errors can be deferred until Send.
//
// Example:
//
//	driver, err := mailgunmail.New("mg.example.com", "private-key", func(transport *mailgunmail.Builder) {
//		transport.Endpoint("https://api.eu.mailgun.net")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func New(domain, apiKey string, configure ...BuilderFunc) (*Driver, error) {
	transport := &Builder{}

	for _, configure := range configure {
		configure(transport)
	}

	config := transport.config

	if domain == "" {
		return nil, errors.New("mailgun domain is required")
	}

	if apiKey == "" {
		return nil, errors.New("mailgun API key is required")
	}

	client := mailgun.NewMailgun(apiKey)

	if err := configureEndpoint(client, config.endpoint); err != nil {
		return nil, err
	}

	return NewFrom(client, domain)
}

// configureEndpoint delegates an optional endpoint override to the SDK. Its
// validation rejects version path segments, not every malformed URL; request
// construction and transport errors remain Send failures rather than New errors.
func configureEndpoint(client *mailgun.Client, endpoint string) error {
	if endpoint == "" {
		return nil
	}

	if err := client.SetAPIBase(endpoint); err != nil {
		return fmt.Errorf("set mailgun API endpoint: %w", err)
	}

	return nil
}

// NewFrom creates a [Driver] from an existing caller-owned [mailgun.Client].
//
// The client carries the API key and endpoint but not the sending domain,
// which Mailgun requires for every message, so domain is a positional argument
// validated like in [New]. No callbacks apply: the endpoint must be set on
// the client by its owner.
//
// NewFrom returns an error when client is nil or domain is empty.
//
// Example:
//
//	client := mailgun.NewMailgun(os.Getenv("MAILGUN_API_KEY"))
//	driver, err := mailgunmail.NewFrom(client, "mg.example.com")
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func NewFrom(client *mailgun.Client, domain string) (*Driver, error) {
	if client == nil {
		return nil, errors.New("mailgun client is required")
	}

	if domain == "" {
		return nil, errors.New("mailgun domain is required")
	}

	return &Driver{
		domain: domain,
		client: client,
	}, nil
}

// Send validates an immutable [mail.Message] and submits its snapshot through
// the Mailgun SDK. The message must include a sender; validation and provider
// failures return an empty receipt and preserve their underlying errors.
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

	value := driver.newMessage(message)

	if err := addAttachments(value, message.Attachments()); err != nil {
		return mail.Receipt{}, err
	}

	response, err := driver.client.Send(ctx, value)

	if err != nil {
		return mail.Receipt{}, fmt.Errorf("send mailgun message: %w", err)
	}

	return mail.Receipt{
		ID: response.ID,
	}, nil
}

// newMessage maps a validated portable message to a fresh Mailgun SDK message.
// Recipient getters supply detached slices; Reply-To is read once because its
// snapshot is used both for the omission check and the provider header.
func (driver *Driver) newMessage(message mail.Message) *mailgun.PlainMessage {
	value := mailgun.NewMessage(
		driver.domain, message.From().String(), message.Subject(), message.TextBody(), addresses(message.To())...,
	)
	value.SetHTML(message.HTMLBody())

	for _, address := range message.CC() {
		value.AddCC(address.String())
	}

	for _, address := range message.BCC() {
		value.AddBCC(address.String())
	}

	replyTo := message.ReplyTo()

	if len(replyTo) != 0 {
		value.SetReplyTo(formats(replyTo))
	}

	return value
}

// addAttachments maps portable attachments to a Mailgun SDK message.
func addAttachments(message *mailgun.PlainMessage, attachments []mail.Attachment) error {
	for _, attachment := range attachments {
		if err := addAttachment(message, attachment); err != nil {
			return err
		}
	}

	return nil
}

// addAttachment adds one portable attachment to a Mailgun SDK message.
func addAttachment(message *mailgun.PlainMessage, attachment mail.Attachment) error {
	if attachment.Disposition == mail.AttachmentDispositionInline && attachment.ContentID != attachment.Filename {
		return errors.New("mailgun inline attachment content ID must equal filename")
	}

	reader := io.NopCloser(bytes.NewReader(attachment.Data))

	if attachment.Disposition == mail.AttachmentDispositionInline {
		message.AddReaderInline(attachment.Filename, reader)

		return nil
	}

	message.AddReaderAttachment(attachment.Filename, reader)

	return nil
}

// addresses formats mailboxes for provider recipient slices.
func addresses(addresses []mail.Address) []string {
	values := make([]string, 0, len(addresses))

	for _, address := range addresses {
		values = append(values, address.String())
	}

	return values
}

// formats joins Mailgun mailbox strings for Reply-To.
func formats(addresses []mail.Address) string {
	values := make([]string, 0, len(addresses))

	for _, address := range addresses {
		values = append(values, address.String())
	}

	return strings.Join(values, ",")
}

var _ mail.Driver = (*Driver)(nil)
