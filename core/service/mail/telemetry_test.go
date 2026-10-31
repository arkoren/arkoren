package mail_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestNilBuilderTelemetryResolvesDefaultPerSend verifies explicit nil policies
// restore dynamic default lookup and do not capture telemetry during construction.
func TestNilBuilderTelemetryResolvesDefaultPerSend(t *testing.T) {
	mailer := mail.New(mailtest.New(), func(mailer *mail.Builder) {
		mailer.Telemetry(telemetry.New(nil))
		mailer.Telemetry(nil)
	})

	fake := telemetrytest.New()
	telemetry.SetDefault(fake.Telemetry(t))
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	_, err := mailer.Send(t.Context(), mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "recipient@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	}))

	if err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "mail send")
}

// secretAddress is an address that must never reach telemetry.
const secretAddress = "alice@example.com"

// failingDriver fails every send with err.
type failingDriver struct {
	// err is returned unchanged so tests can inspect telemetry classification.
	err error
}

// Send returns an empty receipt and the configured failure without delivering mail.
func (driver failingDriver) Send(context.Context, mail.Message) (mail.Receipt, error) {
	return mail.Receipt{}, driver.err
}

// newTraced wires the supplied driver to a started, test-owned telemetry fake.
func newTraced(t *testing.T, driver mail.Driver) (*mail.Mailer, *telemetrytest.Fake) {
	t.Helper()

	fake := telemetrytest.New()

	return mail.New(driver, func(mailer *mail.Builder) {
		mailer.Telemetry(fake.Telemetry(t))
	}, func(mailer *mail.Builder) {
		mailer.From(mail.Address{Email: "noreply@example.com"})
	}), fake
}

// TestSendEmitsSpanAndMetrics checks instrumentation for an accepted message.
// Recorded metadata must count recipients without disclosing their addresses.
func TestSendEmitsSpanAndMetrics(t *testing.T) {
	t.Parallel()

	mailer, fake := newTraced(t, mailtest.New())

	_, err := mailer.Send(t.Context(), mail.NewMessage(mail.Recipients{
		To:  []mail.Address{{Email: secretAddress}, {Email: "bob@example.com"}},
		BCC: []mail.Address{{Email: "carol@example.com"}},
	}, mail.Body{Text: "https://example.com/reset?token=secret"}, func(letter *mail.MessageBuilder) {
		letter.Subject("Reset your password")
		letter.Attachments(mail.Attachment{
			Filename:    "alice.pdf",
			ContentType: "application/pdf",
			Data:        []byte("x"),
		})
	}))

	if err != nil {
		t.Fatal(err)
	}

	fake.AssertSpanCount(t, 1)
	fake.AssertSpan(t, "mail send",
		telemetry.String("arkoren.mail.operation", "send"),
		telemetry.Int("arkoren.mail.recipients", 3),
		telemetry.Int("arkoren.mail.attachments", 1))
	fake.AssertCounter(t, "arkoren.mail.operations", 1, telemetry.String("arkoren.mail.operation", "send"))
	fake.AssertHistogram(t, "arkoren.mail.operation.duration", 1, telemetry.String("arkoren.mail.operation", "send"))

	span, _ := fake.Span("mail send")

	if span.Status == telemetry.Error {
		t.Error("successful send marked as error")
	}

	for _, attr := range span.Attrs {
		if text := attr.Text(); strings.Contains(text, "example.com") || strings.Contains(text, "secret") || strings.Contains(text, "alice") {
			t.Errorf("attribute %q carries message data: %q", attr.Key(), text)
		}
	}
}

// TestSendClassifiesErrors checks low-cardinality failure classification.
// Driver error text must not become a metric label or span attribute.
func TestSendClassifiesErrors(t *testing.T) {
	t.Parallel()

	relay := errors.New("550 mailbox alice@example.com rejected")
	mailer, fake := newTraced(t, failingDriver{err: relay})

	valid := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: secretAddress}},
	}, mail.Body{Text: "hi"})

	if _, err := mailer.Send(t.Context(), valid); !errors.Is(err, relay) {
		t.Fatalf("err = %v, want relay error", err)
	}

	if _, err := mailer.Send(t.Context(), mail.Message{}); !errors.Is(err, mail.ErrInvalidMessage) {
		t.Fatalf("err = %v, want invalid message", err)
	}

	fake.AssertSpan(t, "mail send", telemetry.String("error.type", "mail.ErrInvalidMessage"))
	fake.AssertSpan(t, "mail send", telemetry.String("error.type", "*errors.errorString"))
	fake.AssertCounter(t, "arkoren.mail.operations", 1, telemetry.String("error.type", "mail.ErrInvalidMessage"))
	fake.AssertCounter(t, "arkoren.mail.operations", 2, telemetry.String("arkoren.mail.operation", "send"))

	for _, span := range fake.Spans() {
		if span.Status != telemetry.Error || len(span.Events) != 0 {
			t.Errorf("status = %v, events = %d, want error and no events", span.Status, len(span.Events))
		}

		for _, attr := range span.Attrs {
			if strings.Contains(attr.Text(), "alice") {
				t.Errorf("attribute %q carries error text", attr.Key())
			}
		}
	}
}
