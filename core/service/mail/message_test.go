package mail_test

import (
	"encoding/json/v2"
	"errors"
	"reflect"
	"strings"
	"testing"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
)

// TestNewMessageRequiresRecipients verifies empty recipient lists are rejected
// before the body or optional settings, including on the zero published value.
func TestNewMessageRequiresRecipients(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{}, mail.Body{}, func(letter *mail.MessageBuilder) {
		letter.Subject("bad\r\nsubject")
	})

	if !errors.Is(message.Err(), mail.ErrInvalidMessage) || !strings.Contains(message.Err().Error(), "recipient") {
		t.Fatalf("Err() = %v, want missing recipients", message.Err())
	}
}

// TestMessageZeroIsInvalid verifies neither inspection nor submission can treat
// an unconstructed value as a valid declaration, even with mailer defaults.
func TestMessageZeroIsInvalid(t *testing.T) {
	t.Parallel()

	var message mail.Message

	if !errors.Is(message.Err(), mail.ErrInvalidMessage) {
		t.Fatalf("Err() = %v", message.Err())
	}

	if !errors.Is(message.Validate(), mail.ErrInvalidMessage) {
		t.Fatalf("Validate() = %v", message.Validate())
	}

	driver := mailtest.New()
	mailer := mail.New(driver, func(mailer *mail.Builder) {
		mailer.From(mail.Address{Email: "sender@example.com"})
	})

	if _, err := mailer.Send(t.Context(), message); !errors.Is(err, mail.ErrInvalidMessage) {
		t.Fatalf("Send() = %v", err)
	}

	driver.AssertNothingSent(t)
}

// TestNewMessageRequiresBody verifies both empty representations are invalid
// and their positional error precedes any invalid optional setting.
func TestNewMessageRequiresBody(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "invalid"})
	})

	if !errors.Is(message.Err(), mail.ErrInvalidMessage) || !strings.Contains(message.Err().Error(), "body") {
		t.Fatalf("Err() = %v, want missing body", message.Err())
	}
}

// TestNewMessageHTMLAndBCCOnly verifies neither a visible recipient nor a text
// fallback is required, and that omitted From is valid until submission.
func TestNewMessageHTMLAndBCCOnly(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		BCC: []mail.Address{{Email: "hidden@example.com"}},
	}, mail.Body{HTML: "<p>Hello.</p>"})

	if err := message.Err(); err != nil {
		t.Fatal(err)
	}

	if message.HTMLBody() != "<p>Hello.</p>" || message.TextBody() != "" {
		t.Fatal("constructor changed body representations")
	}

	if err := mail.CheckSend(t.Context(), message); !errors.Is(err, mail.ErrInvalidMessage) {
		t.Fatalf("CheckSend() = %v, want missing completed sender", err)
	}
}

// TestNewMessageSnapshotsRecipientsBeforeCallbacks verifies all positional lists
// are owned before application code can mutate them during construction.
func TestNewMessageSnapshotsRecipientsBeforeCallbacks(t *testing.T) {
	t.Parallel()

	recipients := mail.Recipients{
		To:  []mail.Address{{Email: "to@example.com"}},
		CC:  []mail.Address{{Email: "cc@example.com"}},
		BCC: []mail.Address{{Email: "bcc@example.com"}},
	}
	message := mail.NewMessage(recipients, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		recipients.To[0].Email = "changed@example.com"
		recipients.CC[0].Email = "changed@example.com"
		recipients.BCC[0].Email = "changed@example.com"
	})

	if message.To()[0].Email != "to@example.com" || message.CC()[0].Email != "cc@example.com" {
		t.Fatal("visible recipient inputs were retained")
	}

	if message.BCC()[0].Email != "bcc@example.com" {
		t.Fatal("hidden recipient input was retained")
	}
}

// TestNewMessageSnapshotsOptionalInputs verifies capture owns reply-to addresses,
// attachment metadata and bytes, even when inputs change inside the callback.
func TestNewMessageSnapshotsOptionalInputs(t *testing.T) {
	t.Parallel()

	replies := []mail.Address{{Email: "reply@example.com"}}
	files := []mail.Attachment{{Filename: "report", ContentType: "text/plain", Data: []byte("data")}}
	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.ReplyTo(replies...)
		letter.Attachments(files...)

		replies[0].Email = "changed@example.com"
		files[0].Filename = "changed"
		files[0].Data[0] = 'X'
	})

	if message.ReplyTo()[0].Email != "reply@example.com" {
		t.Fatal("reply-to input was retained")
	}

	attachment := message.Attachments()[0]

	if attachment.Filename != "report" || string(attachment.Data) != "data" {
		t.Fatalf("attachment input was retained: %#v", attachment)
	}
}

// TestMessageGettersDetachContainers verifies every list accessor, including
// attachment bytes, returns independent storage rather than mutable backdoors.
func TestMessageGettersDetachContainers(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To:  []mail.Address{{Email: "to@example.com"}},
		CC:  []mail.Address{{Email: "cc@example.com"}},
		BCC: []mail.Address{{Email: "bcc@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.ReplyTo(mail.Address{Email: "reply@example.com"})
		letter.Attachments(mail.Attachment{Filename: "report", ContentType: "text/plain", Data: []byte("data")})
	})

	message.To()[0].Email = "changed@example.com"
	message.CC()[0].Email = "changed@example.com"
	message.BCC()[0].Email = "changed@example.com"
	message.ReplyTo()[0].Email = "changed@example.com"
	message.Attachments()[0].Filename = "changed"
	message.Attachments()[0].Data[0] = 'X'
	got := []string{message.To()[0].Email, message.CC()[0].Email, message.BCC()[0].Email, message.ReplyTo()[0].Email}
	want := []string{"to@example.com", "cc@example.com", "bcc@example.com", "reply@example.com"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}

	attachment := message.Attachments()[0]

	if attachment.Filename != "report" || string(attachment.Data) != "data" {
		t.Fatalf("getter changed attachment: %#v", attachment)
	}

	if err := message.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestNewMessageRetainedDraftCannotChangePublication verifies all optional
// replacements and new draft errors stay local after publication.
func TestNewMessageRetainedDraftCannotChangePublication(t *testing.T) {
	t.Parallel()

	var retained *mail.MessageBuilder
	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		retained = letter
		letter.From(mail.Address{Email: "sender@example.com"})
		letter.Subject("Welcome")
		letter.ReplyTo(mail.Address{Email: "reply@example.com"})
		letter.Attachments(mail.Attachment{Filename: "report", ContentType: "text/plain", Data: []byte("data")})
	})
	retained.From(mail.Address{Email: "changed@example.com"}).Subject("bad\r\nsubject")
	retained.ReplyTo()
	retained.Attachments()

	if message.From().Email != "sender@example.com" || message.Subject() != "Welcome" {
		t.Fatal("retained draft changed scalar settings")
	}

	if len(message.ReplyTo()) != 1 || len(message.Attachments()) != 1 {
		t.Fatal("retained draft changed optional containers")
	}

	if err := message.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestNewMessageCallbacksRunOnceInOrder verifies invalid declarations still run
// every callback synchronously on one fresh draft, with last valid values winning.
func TestNewMessageCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *mail.MessageBuilder
	calls := 0
	message := mail.NewMessage(mail.Recipients{}, mail.Body{}, func(letter *mail.MessageBuilder) {
		retained = letter
		calls++
		letter.Subject("first")
	}, func(letter *mail.MessageBuilder) {
		if calls != 1 || letter != retained {
			t.Fatal("callbacks did not share the ordered draft")
		}

		calls++
		letter.Subject("last")
	})

	if calls != 2 || message.Subject() != "last" {
		t.Fatalf("calls = %d, subject = %q", calls, message.Subject())
	}

	mail.NewMessage(mail.Recipients{}, mail.Body{}, func(letter *mail.MessageBuilder) {
		if retained == letter {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestNewMessageNilExpansionOmitsCallbacks verifies nil and allocated empty
// callback slices do not count as supplied nil entries or change validation.
func TestNewMessageNilExpansionOmitsCallbacks(t *testing.T) {
	t.Parallel()

	var absent []mail.MessageBuilderFunc
	empty := []mail.MessageBuilderFunc{}
	recipients := mail.Recipients{To: []mail.Address{{Email: "person@example.com"}}}

	if err := mail.NewMessage(recipients, mail.Body{Text: "Hello."}, absent...).Err(); err != nil {
		t.Fatal(err)
	}

	if err := mail.NewMessage(recipients, mail.Body{Text: "Hello."}, empty...).Err(); err != nil {
		t.Fatal(err)
	}
}

// TestNewMessageNilCallbackPanics verifies invalid required inputs do not hide
// callback misuse, and later callbacks do not execute after the panic.
func TestNewMessageNilCallbackPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		mail.NewMessage(mail.Recipients{}, mail.Body{}, nil, func(letter *mail.MessageBuilder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestNewMessageCallbackPanicPropagates preserves application panic identity
// rather than converting callback failures into domain validation errors.
func TestNewMessageCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		mail.NewMessage(mail.Recipients{}, mail.Body{}, func(letter *mail.MessageBuilder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestMessageSubjectErrorIsSticky verifies replacing a rejected header cannot
// erase its error or let a later invalid optional input change error precedence.
func TestMessageSubjectErrorIsSticky(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.Subject("bad\r\nsubject").Subject("Welcome")
		letter.From(mail.Address{Email: "invalid"})
	})

	if !strings.Contains(message.Err().Error(), "subject") || message.Subject() != "Welcome" {
		t.Fatalf("subject = %q, Err() = %v", message.Subject(), message.Err())
	}

	if !errors.Is(message.Validate(), message.Err()) {
		t.Fatal("submission discarded the construction error")
	}
}

// TestMessageFromErrorIsSticky verifies explicitly empty senders are errors,
// unlike omission, and later valid replacement cannot clear that error.
func TestMessageFromErrorIsSticky(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{}).From(mail.Address{Email: "sender@example.com"})
	})

	if !errors.Is(message.Err(), mail.ErrInvalidMessage) || !strings.Contains(message.Err().Error(), "from") {
		t.Fatalf("Err() = %v", message.Err())
	}
}

// TestMessageReplyToErrorIsSticky verifies clearing a rejected reply address
// does not make the declaration valid after the invalid input was captured.
func TestMessageReplyToErrorIsSticky(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.ReplyTo(mail.Address{Email: "invalid"}).ReplyTo()
	})

	if !errors.Is(message.Err(), mail.ErrInvalidMessage) || !strings.Contains(message.Err().Error(), "reply-to") {
		t.Fatalf("Err() = %v", message.Err())
	}
}

// TestMessageAttachmentErrorIsSticky verifies a rejected attachment remains
// invalid even when a later callback clears the attachment list.
func TestMessageAttachmentErrorIsSticky(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.Attachments(mail.Attachment{}).Attachments()
	})

	if !errors.Is(message.Err(), mail.ErrInvalidMessage) || !strings.Contains(message.Err().Error(), "attachment") {
		t.Fatalf("Err() = %v", message.Err())
	}
}

// TestMessageBuilderSettingsReplaceAndReturnDraft verifies optional settings
// replace rather than append and consistently return the same construction draft.
func TestMessageBuilderSettingsReplaceAndReturnDraft(t *testing.T) {
	t.Parallel()

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		if letter.From(mail.Address{Email: "sender@example.com"}) != letter {
			t.Fatal("From returned another draft")
		}

		if letter.Subject("old").Subject("") != letter {
			t.Fatal("Subject returned another draft")
		}

		if letter.ReplyTo(mail.Address{Email: "old@example.com"}).ReplyTo() != letter {
			t.Fatal("ReplyTo returned another draft")
		}

		file := mail.Attachment{Filename: "report", ContentType: "text/plain", Data: []byte("data")}

		if letter.Attachments(file).Attachments() != letter {
			t.Fatal("Attachments returned another draft")
		}
	})

	if message.Subject() != "" || len(message.ReplyTo()) != 0 || len(message.Attachments()) != 0 {
		t.Fatal("empty settings did not replace previous values")
	}

	if err := message.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestMailerCompletesWithoutReplayingCallbacks verifies repeated sends do not
// reconfigure the immutable message or overwrite its explicit reply destinations.
func TestMailerCompletesWithoutReplayingCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		calls++
		letter.ReplyTo(mail.Address{Email: "explicit@example.com"})
	})

	driver := mailtest.New()
	mailer := mail.New(driver, func(mailer *mail.Builder) {
		mailer.From(mail.Address{Email: "sender@example.com"})
		mailer.ReplyTo(mail.Address{Email: "default@example.com"})
	})

	for range 2 {
		if _, err := mailer.Send(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}

	if calls != 1 || message.From() != (mail.Address{}) {
		t.Fatalf("callbacks = %d, original From = %v", calls, message.From())
	}

	driver.AssertSent(t, func(sent mail.Message) bool {
		return sent.From().Email == "sender@example.com" && sent.ReplyTo()[0].Email == "explicit@example.com"
	})
}

// TestMailerRejectsInvalidReplyToDefault verifies declaration validation does
// not accidentally bypass invalid address defaults supplied at the send boundary.
func TestMailerRejectsInvalidReplyToDefault(t *testing.T) {
	t.Parallel()

	driver := mailtest.New()
	mailer := mail.New(driver, func(mailer *mail.Builder) {
		mailer.From(mail.Address{Email: "sender@example.com"})
		mailer.ReplyTo(mail.Address{Email: "invalid"})
	})

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."})

	if _, err := mailer.Send(t.Context(), message); !errors.Is(err, mail.ErrInvalidMessage) {
		t.Fatalf("Send() = %v", err)
	}

	driver.AssertNothingSent(t)
}

// TestMailerPreservesExplicitSender verifies repeated From calls replace the
// value and mailer defaults do not override an explicit completed sender.
func TestMailerPreservesExplicitSender(t *testing.T) {
	t.Parallel()

	driver := mailtest.New()
	mailer := mail.New(driver, func(mailer *mail.Builder) {
		mailer.From(mail.Address{Email: "default@example.com"})
	})

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "old@example.com"})
		letter.From(mail.Address{Email: "explicit@example.com"})
	})

	if _, err := mailer.Send(t.Context(), message); err != nil {
		t.Fatal(err)
	}

	driver.AssertSent(t, func(sent mail.Message) bool {
		return sent.From().Email == "explicit@example.com"
	})
}

// TestNewMessageRejectsRecipientHeaderInjection verifies every positional list
// validates address display names before an injected header can reach a driver.
func TestNewMessageRejectsRecipientHeaderInjection(t *testing.T) {
	t.Parallel()

	address := mail.Address{Name: "Person\r\nBcc: attacker@example.com", Email: "person@example.com"}

	for _, recipients := range []mail.Recipients{
		{To: []mail.Address{address}},
		{CC: []mail.Address{address}},
		{BCC: []mail.Address{address}},
	} {
		message := mail.NewMessage(recipients, mail.Body{Text: "Hello."})

		if !errors.Is(message.Err(), mail.ErrInvalidMessage) {
			t.Fatalf("Err() = %v, want rejected recipient header", message.Err())
		}
	}
}

// TestNewMessageRejectsAttachmentHeaderInjection verifies filename and MIME type
// line breaks fail construction instead of being passed to MIME header writers.
func TestNewMessageRejectsAttachmentHeaderInjection(t *testing.T) {
	t.Parallel()

	recipients := mail.Recipients{To: []mail.Address{{Email: "person@example.com"}}}

	for _, attachment := range []mail.Attachment{
		{Filename: "report\r\nBcc: attacker@example.com", ContentType: "text/plain", Data: []byte("data")},
		{Filename: "report", ContentType: "text/plain\r\nBcc: attacker@example.com", Data: []byte("data")},
	} {
		message := mail.NewMessage(recipients, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
			letter.Attachments(attachment)
		})

		if !errors.Is(message.Err(), mail.ErrInvalidMessage) {
			t.Fatalf("Err() = %v, want rejected attachment header", message.Err())
		}
	}
}

// TestAttachmentDispositionsJSONUsesStrings keeps attachment metadata portable
// across API and storage boundaries, including the unset default sentinel.
func TestAttachmentDispositionsJSONUsesStrings(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal([]mail.AttachmentDisposition{"", mail.AttachmentDispositionAttachment,
		mail.AttachmentDispositionInline})

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `["","attachment","inline"]` {
		t.Fatalf("dispositions JSON = %s", data)
	}
}

// TestEmptyAttachmentDispositionDefaultsToAttachment preserves omitted regular
// attachment policy without changing the caller's original records.
func TestEmptyAttachmentDispositionDefaultsToAttachment(t *testing.T) {
	t.Parallel()

	attachments := []mail.Attachment{{Filename: "report.txt", ContentType: "text/plain", Data: []byte("report")}}
	message := mail.NewMessage(mail.Recipients{To: []mail.Address{{Email: "reader@example.com"}}},
		mail.Body{Text: "Report attached."}, func(letter *mail.MessageBuilder) {
			letter.Attachments(attachments...)
		})

	if err := message.Err(); err != nil {
		t.Fatal(err)
	}

	if message.Attachments()[0].Disposition != mail.AttachmentDispositionAttachment {
		t.Fatalf("disposition = %q", message.Attachments()[0].Disposition)
	}

	if attachments[0].Disposition != "" {
		t.Fatal("default normalization mutated caller attachment")
	}
}
