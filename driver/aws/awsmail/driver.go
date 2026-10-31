package awsmail

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"arkoren.dev/core/service/mail"
)

// settings holds optional SES settings until the native client is constructed.
// It is copied from the draft and is not an exported configuration input.
type settings struct {
	// Region selects the AWS region; an empty value uses the default chain.
	Region string
}

// client submits SES v2 send-email requests.
type client interface {
	// SendEmail sends an SES v2 email request.
	SendEmail(context.Context, *sesv2.SendEmailInput, ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// Driver submits [mail.Message] values through Amazon SES v2.
// It borrows its native client and reports submission acceptance, not delivery.
// Use the mail facade when you need sender defaults and operation observations.
//
// Example:
//
//	driver := awsmail.NewFrom(sesv2.NewFromConfig(cfg))
//	mailer := mail.New(driver)
//	_ = mailer
type Driver struct {
	// client submits SES v2 send-email requests.
	client client
}

// New creates an SES v2 Driver using the AWS default credential chain. It runs
// callbacks directly once in order before loading credentials. Nil entries
// panic; expanded nil or empty slices supply no callbacks.
//
// Example:
//
//	driver, err := awsmail.New(context.Background(), func(mailer *awsmail.Builder) {
//		mailer.Region("eu-west-1")
//	})
//	if err != nil {
//		return err
//	}
//	_ = driver
func New(ctx context.Context, configure ...BuilderFunc) (*Driver, error) {
	config := configured(configure...)

	options := []func(*awsconfig.LoadOptions) error{}

	if config.Region != "" {
		options = append(options, awsconfig.WithRegion(config.Region))
	}

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, options...)

	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}

	return NewFrom(sesv2.NewFromConfig(awsConfig)), nil
}

// NewFrom wraps an existing SES v2 client without copying or closing it. The
// client already carries Region and other native settings, so this constructor
// has no optional settings or construction callbacks.
//
// Example:
//
//	driver := awsmail.NewFrom(sesv2.NewFromConfig(cfg))
//	_ = driver
func NewFrom(client client) *Driver {
	return &Driver{
		client: client,
	}
}

// Send validates and serializes an immutable [mail.Message] as raw MIME for
// SES v2. The sender must be set. All recipients, including BCC, enter the SES
// envelope; MIME encoding keeps BCC out of the message headers. Validation,
// encoding, and provider failures return an empty receipt and wrapped errors.
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

	messageID, err := mail.NewMessageID(emailDomain(message.From().Email))

	if err != nil {
		return mail.Receipt{}, fmt.Errorf("generate message ID: %w", err)
	}

	raw, err := mail.EncodeMIME(message, messageID)

	if err != nil {
		return mail.Receipt{}, fmt.Errorf("encode SES message: %w", err)
	}

	input := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(message.From().Email),
		Destination: &types.Destination{
			ToAddresses:  emails(message.To()),
			CcAddresses:  emails(message.CC()),
			BccAddresses: emails(message.BCC()),
		},
		Content: &types.EmailContent{
			Raw: &types.RawMessage{
				Data: raw,
			},
		},
	}

	replyTo := message.ReplyTo()

	if len(replyTo) != 0 {
		input.ReplyToAddresses = emails(replyTo)
	}

	output, err := driver.client.SendEmail(ctx, input)

	if err != nil {
		return mail.Receipt{}, fmt.Errorf("send SES message: %w", err)
	}

	return mail.Receipt{
		ID: aws.ToString(output.MessageId),
	}, nil
}

// emailDomain returns the domain portion of an email address.
func emailDomain(email string) string {
	_, domain, found := strings.Cut(email, "@")

	if !found {
		return email
	}

	return domain
}

// emails maps mail addresses to SES mailbox strings.
func emails(addresses []mail.Address) []string {
	values := make([]string, 0, len(addresses))

	for _, address := range addresses {
		values = append(values, address.Email)
	}

	return values
}

var _ mail.Driver = (*Driver)(nil)

// Client returns the underlying SES client, or nil when the driver was
// built from a test double. The client remains shared and caller-owned; direct
// native sends bypass the facade's validation, events, and telemetry.
//
// Example:
//
//	if native := driver.Client(); native != nil {
//		_ = native.Options().Region
//	}
func (driver *Driver) Client() *sesv2.Client {
	client, _ := driver.client.(*sesv2.Client)

	return client
}
