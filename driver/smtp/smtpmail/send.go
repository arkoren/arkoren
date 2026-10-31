package smtpmail

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/driver/smtp/smtpmail/internal/session"
)

// Send serializes a [mail.Message] as RFC 5322 MIME and submits it through SMTP.
// Context cancellation and Timeout interrupt dialing and every subsequent SMTP
// read or write, including TLS negotiation. A failed submission returns an empty
// receipt; cancellation errors also match ctx.Err() through errors.Is.
//
// A successful DATA acknowledgement means acceptance, not delivery. Cancellation
// while awaiting that acknowledgement leaves acceptance unknown; retrying can
// duplicate mail. QUIT failures after acceptance do not revoke a successful send.
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
//		return err // Acceptance can be unknown; do not retry blindly.
//	}
//	_ = receipt.ID
func (driver *Driver) Send(ctx context.Context, message mail.Message) (mail.Receipt, error) {
	messageID, contents, err := driver.prepareMessage(ctx, message)

	if err != nil {
		return mail.Receipt{}, err
	}

	// timeout bounds the complete network exchange, not just server setup.
	timeout := cmp.Or(driver.config.Timeout, DefaultTimeout)

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	client := &session.Client{
		Host: driver.host, Port: driver.port, Dial: driver.dial,
		Username: driver.config.Username, Password: driver.config.Password,
		StartTLS: driver.config.TLSMode == TLSModeStartTLS, ImplicitTLS: driver.config.TLSMode == TLSModeImplicit,
	}

	if err := client.Submit(ctx, message, contents); err != nil {
		return mail.Receipt{}, errors.Join(err, ctx.Err())
	}

	return mail.Receipt{
		ID: messageID,
	}, nil
}

// prepareMessage checks ctx and serializes message with a generated identifier.
func (driver *Driver) prepareMessage(ctx context.Context, message mail.Message) (string, []byte, error) {
	if err := mail.CheckSend(ctx, message); err != nil {
		return "", nil, err
	}

	messageID, err := mail.NewMessageID(driver.host)

	if err != nil {
		return "", nil, fmt.Errorf("generate message id: %w", err)
	}

	contents, err := mail.EncodeMIME(message, messageID)

	if err != nil {
		return "", nil, fmt.Errorf("encode message: %w", err)
	}

	return messageID, contents, nil
}
