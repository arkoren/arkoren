package awsmail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	stdmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
)

// acceptClient accepts every SES request.
type acceptClient struct{}

// SendEmail accepts input and returns a fixed message ID.
func (acceptClient) SendEmail(context.Context, *sesv2.SendEmailInput, ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	return &sesv2.SendEmailOutput{
		MessageId: aws.String("ses-id"),
	}, nil
}

// TestDriverConformance verifies the driver passes the mail conformance suite.
func TestDriverConformance(t *testing.T) {
	mailtest.Run(t, func(*testing.T) mail.Driver {
		return NewFrom(acceptClient{})
	})
}

// captureClient records the SES send input for assertions.
type captureClient struct {
	// input retains the latest raw MIME request for address and body assertions.
	input *sesv2.SendEmailInput

	// err injects an SES failure; nil returns the deterministic ses-id receipt.
	err error
}

// SendEmail captures input and returns the configured result.
func (client *captureClient) SendEmail(_ context.Context, input *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	client.input = input

	if client.err != nil {
		return nil, client.err
	}

	return &sesv2.SendEmailOutput{
		MessageId: aws.String("ses-id"),
	}, nil
}

// MiniStack test constants configure the isolated SES v2 emulator client.
const (
	miniStackEndpoint  = "http://127.0.0.1:14569"
	operationTimeout   = time.Minute
	awsRegion          = "us-east-1"
	awsAccessKeyID     = "000000000000"
	awsSecretAccessKey = "test"
)

// TestClientSendUsesRawMIME verifies SES receives the portable MIME contract.
func TestClientSendUsesRawMIME(t *testing.T) {
	t.Parallel()

	sdk := &captureClient{}

	receipt := sendRawMIME(t, sdk)

	assertRawMIME(t, sdk, receipt)
}

// sendRawMIME submits a message that exercises the SES portable MIME contract.
func sendRawMIME(t *testing.T, sdk *captureClient) mail.Receipt {
	t.Helper()

	receipt, err := NewFrom(sdk).Send(t.Context(), mail.NewMessage(mail.Recipients{
		To: []mail.Address{
			{
				Email: "person@example.com",
			},
		},
		CC: []mail.Address{
			{
				Email: "copy@example.com",
			},
		},
		BCC: []mail.Address{
			{
				Email: "hidden@example.com",
			},
		},
	}, mail.Body{Text: "Hello.", HTML: `<img src="cid:logo">`}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.ReplyTo(mail.Address{Email: "reply@example.com"})
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
	}))

	if err != nil {
		t.Fatal(err)
	}

	return receipt
}

// assertRawMIME verifies the SES raw MIME input and portable receipt.
func assertRawMIME(t *testing.T, sdk *captureClient, receipt mail.Receipt) {
	t.Helper()

	if receipt.ID != "ses-id" {
		t.Fatalf("receipt = %#v", receipt)
	}

	if sdk.input.Content.Raw == nil {
		t.Fatal("SES content is not raw MIME")
	}

	assertSESDestinations(t, sdk.input)
	assertRawMIMEContents(t, sdk.input.Content.Raw.Data)
}

// assertSESDestinations verifies raw MIME destination headers sent to SES.
func assertSESDestinations(t *testing.T, input *sesv2.SendEmailInput) {
	t.Helper()

	assertSESAddresses(t, "to", input.Destination.ToAddresses, "person@example.com")
	assertSESAddresses(t, "cc", input.Destination.CcAddresses, "copy@example.com")
	assertSESAddresses(t, "bcc", input.Destination.BccAddresses, "hidden@example.com")
	assertSESAddresses(t, "reply-to", input.ReplyToAddresses, "reply@example.com")
}

// assertSESAddresses verifies a single SES mailbox list entry.
func assertSESAddresses(t *testing.T, label string, addresses []string, expected string) {
	t.Helper()

	if len(addresses) != 1 || addresses[0] != expected {
		t.Fatalf("%s = %#v", label, addresses)
	}
}

// assertRawMIMEContents verifies MIME content is complete and does not expose BCC.
func assertRawMIMEContents(t *testing.T, contents []byte) {
	t.Helper()

	parsed, err := stdmail.ReadMessage(strings.NewReader(string(contents)))

	if err != nil {
		t.Fatal(err)
	}

	if parsed.Header.Get("Bcc") != "" {
		t.Fatalf("raw MIME exposes BCC: %q", parsed.Header.Get("Bcc"))
	}

	raw := string(contents)

	if !hasExpectedMIMEContent(raw) {
		t.Fatalf("raw MIME = %q", raw)
	}
}

// hasExpectedMIMEContent reports whether raw contains the portable MIME parts.
func hasExpectedMIMEContent(raw string) bool {
	return strings.Contains(raw, "Hello.") &&
		strings.Contains(raw, `<img src=3D"cid:logo">`) &&
		strings.Contains(raw, "cmVwb3J0") &&
		hasInlineContentID(raw)
}

// hasInlineContentID reports whether raw contains the inline attachment header.
func hasInlineContentID(raw string) bool {
	return strings.Contains(raw, "Content-ID: <logo>") || strings.Contains(raw, "Content-Id: <logo>")
}

// TestClientSendWrapsSESError verifies provider failures retain their cause.
func TestClientSendWrapsSESError(t *testing.T) {
	t.Parallel()

	sdk := &captureClient{
		err: errors.New("unavailable"),
	}

	_, err := NewFrom(sdk).Send(t.Context(), mail.NewMessage(mail.Recipients{
		To: []mail.Address{
			{
				Email: "person@example.com",
			},
		},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	}))

	if !errors.Is(err, sdk.err) {
		t.Fatalf("error = %v", err)
	}
}

// TestClientSendDeliversRawMIMEThroughMiniStack verifies the driver sends a
// raw MIME message to the local SES v2 emulator.
func TestClientSendDeliversRawMIMEThroughMiniStack(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Parallel()

	client, ctx := newMiniStackSESClient(t)
	name := newMiniStackTestName(t)

	receipt, err := client.Send(ctx, miniStackMessage(name))

	if err != nil {
		t.Fatalf("send raw MIME message: %v", err)
	}

	if receipt.ID == "" {
		t.Fatal("receipt ID is empty")
	}
}

// newMiniStackSESClient returns an SES client for the Compose MiniStack service.
func newMiniStackSESClient(t *testing.T) (*Driver, context.Context) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), operationTimeout)
	t.Cleanup(cancel)

	return NewFrom(newSESClient()), ctx
}

// newSESClient creates an SES SDK client pointed at the Compose MiniStack service.
func newSESClient() *sesv2.Client {
	return sesv2.NewFromConfig(
		aws.Config{
			Region:      awsRegion,
			Credentials: credentials.NewStaticCredentialsProvider(awsAccessKeyID, awsSecretAccessKey, ""),
		},
		func(options *sesv2.Options) {
			options.BaseEndpoint = aws.String(miniStackEndpoint)
		},
	)
}

// miniStackMessage creates a unique message for SES MiniStack verification.
func miniStackMessage(name string) mail.Message {
	return mail.NewMessage(mail.Recipients{
		To: []mail.Address{
			{
				Email: fmt.Sprintf("recipient-%s@example.test", name),
			},
		},
	}, mail.Body{Text: fmt.Sprintf("MiniStack SES body %s", name)}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: fmt.Sprintf("sender-%s@example.test", name)})
		letter.Subject(fmt.Sprintf("MiniStack SES %s", name))
	})
}

// newMiniStackTestName returns a sanitized random name for MiniStack resources.
func newMiniStackTestName(t *testing.T) string {
	t.Helper()

	random := make([]byte, 8)

	if _, err := rand.Read(random); err != nil {
		t.Fatalf("generate MiniStack test name: %v", err)
	}

	name := strings.Map(sanitizeMiniStackName, strings.ToLower(t.Name()))

	return fmt.Sprintf("arkoren-%s-%s", name, hex.EncodeToString(random))
}

// sanitizeMiniStackName replaces unsupported MiniStack name characters.
func sanitizeMiniStackName(character rune) rune {
	if strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789", character) {
		return character
	}

	return '-'
}
