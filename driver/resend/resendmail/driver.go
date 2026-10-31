package resendmail

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/resend/resend-go/v3"

	"arkoren.dev/core/service/mail"
)

// Driver submits [mail.Message] values through the Resend Go SDK. It borrows
// the SDK object without closing it and maps portable message snapshots without
// changing the published message. A zero Driver cannot send messages.
//
// Example:
//
//	driver, err := resendmail.New("api-key")
//	if err != nil {
//		panic(err)
//	}
//	_ = mail.New(driver)
type Driver struct {
	// client submits requests through the Resend SDK.
	client *resend.Client
}

// New creates a Resend [Driver] authenticating with apiKey.
//
// apiKey is the Resend API key. The driver has no other settings, so New takes
// no options. No request is made until [Driver.Send].
//
// New returns an error when apiKey is empty.
//
// Example:
//
//	driver, err := resendmail.New("api-key")
//	if err != nil {
//		panic(err)
//	}
//	_ = mail.New(driver)
func New(apiKey string) (*Driver, error) {
	if apiKey == "" {
		return nil, errors.New("resend API key is required")
	}

	return NewFrom(resend.NewClient(apiKey)), nil
}

// NewFrom creates a [Driver] from an existing caller-owned [resend.Client],
// which already carries the API key. It performs no validation and takes no
// options. The supplied SDK client and its HTTP transport remain borrowed and
// are never closed or reconfigured. Configure them before concurrent use.
// Nil is retained for compatibility but cannot be used to send valid messages.
//
// Example:
//
//	client := resend.NewClient("api-key")
//	driver := resendmail.NewFrom(client)
//	_ = mail.New(driver)
func NewFrom(client *resend.Client) *Driver {
	return &Driver{
		client: client,
	}
}

// Send validates an immutable [mail.Message] and maps its snapshot to a Resend
// SDK request. The message must include a sender. Validation and provider
// failures return an empty receipt without changing the published message.
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

	request := &resend.SendEmailRequest{
		From:        message.From().String(),
		To:          formatAll(message.To()),
		Cc:          formatAll(message.CC()),
		Bcc:         formatAll(message.BCC()),
		ReplyTo:     formats(message.ReplyTo()),
		Subject:     message.Subject(),
		Text:        message.TextBody(),
		Html:        message.HTMLBody(),
		Attachments: attachments(message.Attachments()),
	}

	response, err := driver.client.Emails.SendWithContext(ctx, request)

	if err != nil {
		return mail.Receipt{}, fmt.Errorf("send resend message: %w", err)
	}

	return mail.Receipt{
		ID: response.Id,
	}, nil
}

// formatAll maps portable addresses to provider mailbox strings.
func formatAll(addresses []mail.Address) []string {
	values := make([]string, 0, len(addresses))

	for _, address := range addresses {
		values = append(values, address.String())
	}

	return values
}

// formats joins provider mailbox strings for Reply-To.
func formats(addresses []mail.Address) string {
	return strings.Join(formatAll(addresses), ",")
}

// attachments maps portable attachments to Resend SDK attachments.
func attachments(values []mail.Attachment) []*resend.Attachment {
	result := make([]*resend.Attachment, 0, len(values))

	for _, value := range values {
		attachment := &resend.Attachment{
			Content:     value.Data,
			Filename:    value.Filename,
			ContentType: value.ContentType,
		}

		if value.Disposition == mail.AttachmentDispositionInline {
			attachment.ContentId = value.ContentID
		}

		result = append(result, attachment)
	}

	return result
}

var _ mail.Driver = (*Driver)(nil)
