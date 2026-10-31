package mail_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
)

// TestMailReplyToCopiesInputAndRetainedDraft verifies replacement, input
// ownership and publication independently of Config's returned-slice copy.
func TestMailReplyToCopiesInputAndRetainedDraft(t *testing.T) {
	t.Parallel()

	addresses := []mail.Address{{Email: "support@example.com"}}
	var retained *mail.Builder
	mailer := mail.New(mailtest.New(), func(mailer *mail.Builder) {
		retained = mailer
		mailer.ReplyTo(mail.Address{Email: "replaced@example.com"})
		mailer.ReplyTo(addresses...)

		addresses[0].Email = "input-edited@example.com"
	})
	retained.ReplyTo(mail.Address{Email: "draft-edited@example.com"})

	config := mailer.Config()

	if len(config.ReplyTo) != 1 || config.ReplyTo[0].Email != "support@example.com" {
		t.Fatalf("ReplyTo = %#v", config.ReplyTo)
	}

	config.ReplyTo[0].Email = "snapshot-edited@example.com"

	if mailer.Config().ReplyTo[0].Email != "support@example.com" {
		t.Fatal("returned snapshot changed retained defaults")
	}
}

// TestMailReplyToEmptyReplacesDefaults verifies an empty setting clears the
// list instead of leaving old addresses or appending an empty policy.
func TestMailReplyToEmptyReplacesDefaults(t *testing.T) {
	t.Parallel()

	mailer := mail.New(nil, func(mailer *mail.Builder) {
		mailer.ReplyTo(mail.Address{Email: "support@example.com"})
		mailer.ReplyTo()
	})

	if len(mailer.Config().ReplyTo) != 0 {
		t.Fatal("empty ReplyTo did not clear defaults")
	}
}

// TestAddressStringQuotesDisplayName verifies Address uses RFC 5322 quoting.
func TestAddressStringQuotesDisplayName(t *testing.T) {
	t.Parallel()

	address := mail.Address{
		Name:  `Doe, "Jane"`,
		Email: "jane@example.com",
	}

	if value := address.String(); value != `"Doe, \"Jane\"" <jane@example.com>` {
		t.Fatalf("address = %q", value)
	}
}

// TestMailerSendAppliesDefaults verifies send applies configured message defaults.
func TestMailerSendAppliesDefaults(t *testing.T) {
	t.Parallel()

	driver := mailtest.New()
	mailer := mail.New(driver,
		func(mailer *mail.Builder) {
			mailer.From(mail.Address{Email: "noreply@example.com"})
		},
		func(mailer *mail.Builder) {
			mailer.ReplyTo(mail.Address{Email: "help@example.com"})
		},
	)

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."})

	receipt, err := mailer.Send(context.Background(), message)

	if err != nil {
		t.Fatal(err)
	}

	if receipt.ID != "fake-1" {
		t.Fatalf("receipt = %#v", receipt)
	}

	sent := driver.Sent()[0]

	if sent.From().Email != "noreply@example.com" {
		t.Fatalf("from = %#v", sent.From())
	}

	if len(sent.ReplyTo()) != 1 || sent.ReplyTo()[0].Email != "help@example.com" {
		t.Fatalf("reply-to = %#v", sent.ReplyTo())
	}

	if message.From() != (mail.Address{}) || len(message.ReplyTo()) != 0 {
		t.Fatal("default completion changed the original message")
	}
}

// TestMailerConfigReturnsCopy verifies defaults cannot mutate mailer state.
func TestMailerConfigReturnsCopy(t *testing.T) {
	t.Parallel()

	replyTo := mail.Address{Email: "help@example.com"}
	mailer := mail.New(mailtest.New(), func(mailer *mail.Builder) {
		mailer.ReplyTo(replyTo)
	})

	config := mailer.Config()
	config.ReplyTo[0].Email = "changed@example.com"

	if mailer.Config().ReplyTo[0].Email != "help@example.com" {
		t.Fatalf("config = %#v", mailer.Config())
	}
}

// TestMailerSendRejectsInvalidMessageBeforeDriver verifies validation precedes dispatch.
func TestMailerSendRejectsInvalidMessageBeforeDriver(t *testing.T) {
	t.Parallel()

	driver := mailtest.New()
	mailer := mail.New(driver)

	_, err := mailer.Send(context.Background(), mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Subject("bad\r\nBcc: attacker@example.com")
	}))

	if !errors.Is(err, mail.ErrInvalidMessage) {
		t.Fatalf("error = %v", err)
	}

	driver.AssertNothingSent(t)
}

// TestMessageValidateAcceptsInlineAttachment verifies valid inline attachments are accepted.
func TestMessageValidateAcceptsInlineAttachment(t *testing.T) {
	t.Parallel()

	err := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{HTML: `<img src="cid:logo">`}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename:    "logo.png",
			ContentType: "image/png",
			Data:        []byte("image"),
			Disposition: mail.AttachmentDispositionInline,
			ContentID:   "logo",
		})
	}).Validate()

	if err != nil {
		t.Fatal(err)
	}
}

// TestMessageValidateRejectsRegularAttachmentContentID verifies regular attachments reject content IDs.
func TestMessageValidateRejectsRegularAttachmentContentID(t *testing.T) {
	t.Parallel()

	err := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Attachments(mail.Attachment{
			Filename:    "report.pdf",
			ContentType: "application/pdf",
			Data:        []byte("report"),
			ContentID:   "report",
		})
	}).Validate()

	if !errors.Is(err, mail.ErrInvalidMessage) {
		t.Fatalf("error = %v", err)
	}
}

// TestMailPoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestMailPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *mail.Builder
	calls := 0
	component := mail.New(nil, func(mailer *mail.Builder) {
		calls++
		retained = mailer
	}, func(mailer *mail.Builder) {
		if calls != 1 || retained != mailer {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	mail.New(nil, func(mailer *mail.Builder) {
		if retained == mailer {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestMailNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestMailNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*mail.Builder)
	empty := []func(*mail.Builder){}

	if mail.New(nil, absent...) == nil || mail.New(nil, empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestMailNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestMailNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		mail.New(nil, nil, func(mailer *mail.Builder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestMailPolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestMailPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		mail.New(nil, func(mailer *mail.Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestMailFromReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMailFromReturnsDraft(t *testing.T) {
	t.Parallel()

	mailer := &mail.Builder{}

	if mailer.From(mail.Address{Email: "sender@example.com"}) != mailer {
		t.Fatal("setting returned a different draft")
	}
}

// TestMailReplyToReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMailReplyToReturnsDraft(t *testing.T) {
	t.Parallel()

	mailer := &mail.Builder{}

	if mailer.ReplyTo(mail.Address{Email: "support@example.com"}) != mailer {
		t.Fatal("setting returned a different draft")
	}
}

// TestMailTelemetryReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMailTelemetryReturnsDraft(t *testing.T) {
	t.Parallel()

	mailer := &mail.Builder{}

	if mailer.Telemetry(nil) != mailer {
		t.Fatal("setting returned a different draft")
	}
}

// TestMailEventsReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMailEventsReturnsDraft(t *testing.T) {
	t.Parallel()

	mailer := &mail.Builder{}

	if mailer.Events(nil) != mailer {
		t.Fatal("setting returned a different draft")
	}
}
