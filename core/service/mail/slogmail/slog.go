package slogmail

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"arkoren.dev/core/service/mail"
)

// Detail selects how much of a message the [Driver] logs.
// Empty input selects DetailSummary. Other unknown values also omit bodies;
// only DetailBody enables logging potentially sensitive message text.
//
//	Example: _ = slogmail.New(func(mailer *slogmail.Builder) {
//		mailer.Detail(slogmail.DetailSummary)
//	})
type Detail string

const (
	// DetailSummary logs addresses, subject, body sizes, and attachment
	// metadata.
	//
	// Example: _ = slogmail.New(func(mailer *slogmail.Builder) {
	//	mailer.Detail(slogmail.DetailSummary)
	// })
	DetailSummary Detail = "summary"

	// DetailBody additionally logs the text and HTML bodies.
	// Use it only when logging message contents is acceptable.
	//
	// Example: _ = slogmail.New(func(mailer *slogmail.Builder) {
	//	mailer.Detail(slogmail.DetailBody)
	// })
	DetailBody Detail = "body"
)

// settings configures a slog [Driver].
type settings struct {
	// Logger receives one record per message. Nil uses [slog.Default].
	Logger *slog.Logger

	// Level is the level of each record. The zero value is [slog.LevelInfo].
	Level slog.Level

	// Detail selects how much of each message is logged.
	Detail Detail
}

// Driver implements [mail.Driver] by logging messages instead of sending them.
// It is meant for local development: even the default summary logs addresses,
// including BCC, and the subject. Construct it with New; it owns no lifecycle.
// Its receipt records this simulated acceptance, not external submission.
//
// Example:
//
//	driver := slogmail.New(func(mailer *slogmail.Builder) {
//		mailer.Logger(slog.New(slog.NewTextHandler(io.Discard, nil)))
//	})
//	mailer := mail.New(driver)
//	_ = mailer.Driver()
type Driver struct {
	// config holds the effective configuration.
	config settings
}

var _ mail.Driver = (*Driver)(nil)

// New creates a logging [Driver] from construction policies.
//
// Construction callbacks run directly once, synchronously in order on one
// fresh draft. Nil entries panic; expanded nil or empty slices omit edits.
// Publication detaches owned settings, not live dependencies or captures.
//
// Example:
//
//	slogmail.New(func(mailer *slogmail.Builder) {
//		mailer.Detail(slogmail.DetailBody)
//	})
func New(configure ...BuilderFunc) *Driver {
	return &Driver{config: newSettings(configure...)}
}

// Send checks message with [mail.CheckSend], logs it, and returns a receipt with a generated
// message ID. It returns the context error when ctx is done and an error
// wrapping [mail.ErrInvalidMessage] when message is invalid. Nothing from
// message is retained and attachment bytes are never logged.
// The context must be nonnil. Nil Logger resolves slog.Default per call;
// logger filtering can suppress the record without failing this simulated send.
// Message-ID generation errors return a zero receipt with the original cause.
//
// Example:
//
//	driver := slogmail.New(func(mailer *slogmail.Builder) {
//		mailer.Logger(slog.New(slog.NewTextHandler(io.Discard, nil)))
//	})
//	receipt, err := driver.Send(ctx, message) // message includes From.
//	if err != nil {
//		return err
//	}
//	_ = receipt.ID
func (driver *Driver) Send(ctx context.Context, message mail.Message) (mail.Receipt, error) {
	if err := mail.CheckSend(ctx, message); err != nil {
		return mail.Receipt{}, err
	}

	messageID, err := mail.NewMessageID("slog.invalid")

	if err != nil {
		return mail.Receipt{}, fmt.Errorf("generate message id: %w", err)
	}

	logger := driver.config.Logger

	if logger == nil {
		logger = slog.Default()
	}

	logger.LogAttrs(ctx, driver.config.Level, "mail message", driver.attrs(message, messageID)...)

	return mail.Receipt{ID: messageID}, nil
}

// attrs describes message as log attributes, without attachment data.
func (driver *Driver) attrs(message mail.Message, messageID string) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("id", messageID),
		slog.String("from", message.From().String()),
		slog.String("to", formatAll(message.To())),
		slog.String("cc", formatAll(message.CC())),
		slog.String("bcc", formatAll(message.BCC())),
		slog.String("reply_to", formatAll(message.ReplyTo())),
		slog.String("subject", message.Subject()),
		slog.Int("text_bytes", len(message.TextBody())),
		slog.Int("html_bytes", len(message.HTMLBody())),
		slog.String("attachments", describeAttachments(message.Attachments())),
	}

	if driver.config.Detail == DetailBody {
		attrs = append(attrs,
			slog.String("text", message.TextBody()),
			slog.String("html", message.HTMLBody()),
		)
	}

	return attrs
}

// formatAll renders addresses as comma-separated RFC 5322 mailboxes.
func formatAll(addresses []mail.Address) string {
	values := make([]string, 0, len(addresses))

	for _, address := range addresses {
		values = append(values, address.String())
	}

	return strings.Join(values, ", ")
}

// describeAttachments summarizes attachments by name, type, and size.
func describeAttachments(attachments []mail.Attachment) string {
	values := make([]string, 0, len(attachments))

	for _, attachment := range attachments {
		values = append(values, fmt.Sprintf("%s (%s, %d bytes)", attachment.Filename, attachment.ContentType, len(attachment.Data)))
	}

	return strings.Join(values, ", ")
}
