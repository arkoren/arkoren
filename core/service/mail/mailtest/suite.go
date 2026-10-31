package mailtest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"arkoren.dev/core/service/mail"
)

// Option changes one conformance suite setting.
type Option func(*suite)

// WithConcurrency sets how many goroutines send in parallel during the
// concurrency check. Values below one disable the check. The default is 4.
func WithConcurrency(n int) Option {
	return func(s *suite) {
		s.concurrency = n
	}
}

// suite holds the settings and driver factory of one [Run].
type suite struct {
	// newDriver creates the driver under test.
	newDriver func(t *testing.T) mail.Driver

	// concurrency is the number of parallel senders.
	concurrency int
}

// Run checks that the drivers created by newDriver follow the [mail.Driver]
// contract: they accept valid messages without modifying them, reject invalid
// messages with [mail.ErrInvalidMessage] before any I/O, honor context
// cancellation, and are safe for concurrent use. Delivery itself cannot be
// verified and is not checked.
//
// newDriver is called once per subtest and must return a driver whose backend
// accepts every valid message.
//
// # Example
//
//	func TestDriver(t *testing.T) {
//		mailtest.Run(t, func(t *testing.T) mail.Driver {
//			return mailtest.New()
//		})
//	}
func Run(t *testing.T, newDriver func(t *testing.T) mail.Driver, opts ...Option) {
	s := &suite{
		newDriver:   newDriver,
		concurrency: 4,
	}

	for _, opt := range opts {
		opt(s)
	}

	t.Run("AcceptsMinimalMessage", s.testAcceptsMinimalMessage)
	t.Run("AcceptsFullMessage", s.testAcceptsFullMessage)
	t.Run("AcceptsBCCOnly", s.testAcceptsBCCOnly)
	t.Run("AcceptsHTMLOnly", s.testAcceptsHTMLOnly)
	t.Run("DoesNotModifyMessage", s.testDoesNotModifyMessage)
	t.Run("RejectsInvalidMessages", s.testRejectsInvalidMessages)
	t.Run("RejectsCanceledContext", s.testRejectsCanceledContext)
	t.Run("ConcurrentSend", s.testConcurrentSend)
}

// minimalMessage returns the smallest valid message.
func minimalMessage() mail.Message {
	return mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	})
}

// fullMessage returns a valid message using every field.
func fullMessage() mail.Message {
	return mail.NewMessage(mail.Recipients{
		To: []mail.Address{{
			Name:  "Person",
			Email: "person@example.com",
		}},
		CC:  []mail.Address{{Email: "copy@example.com"}},
		BCC: []mail.Address{{Email: "hidden@example.com"}},
	}, mail.Body{Text: "Hello.", HTML: `<p>Hello.</p><img src="cid:logo">`}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Name: "Sender, Inc.", Email: "sender@example.com"})
		letter.ReplyTo(mail.Address{Email: "reply@example.com"})
		letter.Subject("Greetings é")
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
	})
}

// send sends message with a fresh driver and a live context.
func (suite *suite) send(t *testing.T, message mail.Message) (mail.Receipt, error) {
	t.Helper()

	return suite.newDriver(t).Send(t.Context(), message)
}

// testAcceptsMinimalMessage verifies the smallest valid message is accepted.
func (suite *suite) testAcceptsMinimalMessage(t *testing.T) {
	if _, err := suite.send(t, minimalMessage()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
}

// testAcceptsFullMessage verifies every message field is accepted.
func (suite *suite) testAcceptsFullMessage(t *testing.T) {
	if _, err := suite.send(t, fullMessage()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
}

// testAcceptsBCCOnly verifies a message may have only hidden recipients.
func (suite *suite) testAcceptsBCCOnly(t *testing.T) {
	message := mail.NewMessage(mail.Recipients{
		BCC: []mail.Address{{Email: "hidden@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	})

	if _, err := suite.send(t, message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
}

// testAcceptsHTMLOnly verifies text is not required when HTML is provided.
func (suite *suite) testAcceptsHTMLOnly(t *testing.T) {
	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{HTML: "<p>Hello.</p>"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	})

	if _, err := suite.send(t, message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
}

// testDoesNotModifyMessage verifies Send leaves the caller's message intact.
func (suite *suite) testDoesNotModifyMessage(t *testing.T) {
	message := fullMessage()
	// want snapshots public values, including detached attachment bytes, before
	// the driver runs. Comparing Message directly would share private storage.
	want := []any{
		message.From(), message.To(), message.CC(), message.BCC(), message.ReplyTo(),
		message.Subject(), message.TextBody(), message.HTMLBody(), message.Attachments(),
	}

	if _, err := suite.send(t, message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	got := []any{
		message.From(), message.To(), message.CC(), message.BCC(), message.ReplyTo(),
		message.Subject(), message.TextBody(), message.HTMLBody(), message.Attachments(),
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Send() modified the message:\n got %+v\nwant %+v", got, want)
	}
}

// testRejectsInvalidMessages verifies each invalid message fails with
// mail.ErrInvalidMessage and an empty receipt.
func (suite *suite) testRejectsInvalidMessages(t *testing.T) {
	for index, message := range invalidMessages() {
		receipt, err := suite.send(t, message)

		if !errors.Is(err, mail.ErrInvalidMessage) {
			t.Fatalf("message %d: Send() error = %v, want ErrInvalidMessage", index, err)
		}

		if receipt != (mail.Receipt{}) {
			t.Errorf("message %d: Send() receipt = %+v, want zero", index, receipt)
		}
	}
}

// invalidMessages exercises required declarations and every optional validation
// class. All values must fail with the same portable error and empty receipt.
func invalidMessages() []mail.Message {
	recipients := mail.Recipients{To: []mail.Address{{Email: "person@example.com"}}}
	body := mail.Body{Text: "Hello."}
	// sender completes fixtures whose optional setting alone must reject them.
	sender := func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	}

	return []mail.Message{
		{},
		mail.NewMessage(recipients, body), // Valid declaration, missing completed sender.
		mail.NewMessage(mail.Recipients{}, body, sender),
		mail.NewMessage(mail.Recipients{To: []mail.Address{{Email: "nope"}}}, body, sender),
		mail.NewMessage(mail.Recipients{CC: []mail.Address{{Email: "nope"}}}, body, sender),
		mail.NewMessage(mail.Recipients{BCC: []mail.Address{{Email: "nope"}}}, body, sender),
		mail.NewMessage(mail.Recipients{To: []mail.Address{{}}}, body, sender),
		mail.NewMessage(recipients, mail.Body{}, sender),
		mail.NewMessage(recipients, body, func(letter *mail.MessageBuilder) {
			letter.From(mail.Address{Email: "not-an-address"})
		}),
		mail.NewMessage(recipients, body, func(letter *mail.MessageBuilder) {
			letter.From(mail.Address{Name: "a\r\nBcc: x@example.com", Email: "sender@example.com"})
		}),
		mail.NewMessage(recipients, body, sender, func(letter *mail.MessageBuilder) {
			letter.ReplyTo(mail.Address{Email: "nope"})
		}),
		mail.NewMessage(recipients, body, sender, func(letter *mail.MessageBuilder) {
			letter.Subject("a\r\nBcc: x@example.com")
		}),
		mail.NewMessage(recipients, body, sender, func(letter *mail.MessageBuilder) {
			letter.Attachments(mail.Attachment{Filename: "report", ContentType: "text/plain"})
		}),
		mail.NewMessage(recipients, body, sender, func(letter *mail.MessageBuilder) {
			letter.Attachments(mail.Attachment{ContentType: "text/plain", Data: []byte("report")})
		}),
		mail.NewMessage(recipients, body, sender, func(letter *mail.MessageBuilder) {
			letter.Attachments(mail.Attachment{Filename: "report", ContentType: "text/", Data: []byte("report")})
		}),
		mail.NewMessage(recipients, body, sender, func(letter *mail.MessageBuilder) {
			letter.Attachments(mail.Attachment{
				Filename: "logo", ContentType: "image/png", Data: []byte("image"),
				Disposition: mail.AttachmentDispositionInline,
			})
		}),
		mail.NewMessage(recipients, body, sender, func(letter *mail.MessageBuilder) {
			letter.Attachments(mail.Attachment{
				Filename: "report", ContentType: "text/plain", Data: []byte("report"), ContentID: "x",
			})
		}),
		mail.NewMessage(recipients, body, sender, func(letter *mail.MessageBuilder) {
			letter.Attachments(mail.Attachment{
				Filename: "report", ContentType: "text/plain", Data: []byte("report"), Disposition: "invalid",
			})
		}),
	}
}

// testRejectsCanceledContext verifies a done context fails the send with the
// context error.
func (suite *suite) testRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := suite.newDriver(t).Send(ctx, minimalMessage())

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send() error = %v, want context.Canceled", err)
	}
}

// testConcurrentSend verifies one driver serves parallel senders.
func (suite *suite) testConcurrentSend(t *testing.T) {
	if suite.concurrency < 1 {
		t.Skip("concurrency check disabled")
	}

	driver := suite.newDriver(t)
	errs := make(chan error, suite.concurrency)

	var group sync.WaitGroup

	for range suite.concurrency {
		group.Go(func() {
			_, err := driver.Send(t.Context(), fullMessage())
			errs <- err
		})
	}

	group.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("Send() error = %v", err)
		}
	}
}
