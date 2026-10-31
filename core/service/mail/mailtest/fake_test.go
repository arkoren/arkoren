package mailtest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
)

// TestFakeConformance verifies Fake passes the driver conformance suite.
func TestFakeConformance(t *testing.T) {
	mailtest.Run(t, func(*testing.T) mail.Driver {
		return mailtest.New()
	})
}

// TestFakeRecordsDeepCopy verifies later caller mutations do not alter records.
func TestFakeRecordsDeepCopy(t *testing.T) {
	fake := mailtest.New()
	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "b@example.com"}},
	}, mail.Body{Text: "hi"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "a@example.com"})
		letter.Attachments(mail.Attachment{
			Filename:    "f",
			ContentType: "text/plain",
			Data:        []byte("data"),
		})
	})

	if _, err := fake.Send(t.Context(), message); err != nil {
		t.Fatal(err)
	}

	message.To()[0].Email = "changed@example.com"
	message.Attachments()[0].Data[0] = 'X'
	fake.Sent()[0].To()[0].Email = "also-changed@example.com"
	fake.Sent()[0].Attachments()[0].Data[0] = 'Y'
	fake.Sent()[0] = mail.Message{}

	fake.AssertSent(t, func(m mail.Message) bool {
		return m.To()[0].Email == "b@example.com" && string(m.Attachments()[0].Data) == "data"
	})
}

// TestFakeFailureInjection verifies FailNext, FailWith, and Reset.
func TestFakeFailureInjection(t *testing.T) {
	fake := mailtest.New()
	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "b@example.com"}},
	}, mail.Body{Text: "hi"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "a@example.com"})
	})

	boom := errors.New("boom")

	fake.FailNext(boom)

	if _, err := fake.Send(t.Context(), message); !errors.Is(err, boom) {
		t.Fatalf("first Send() error = %v, want boom", err)
	}

	if _, err := fake.Send(t.Context(), message); err != nil {
		t.Fatalf("second Send() error = %v", err)
	}

	fake.FailWith(boom)

	if _, err := fake.Send(t.Context(), message); !errors.Is(err, boom) {
		t.Fatalf("third Send() error = %v, want boom", err)
	}

	fake.Reset()

	fake.AssertNothingSent(t)

	if _, err := fake.Send(t.Context(), message); err != nil {
		t.Fatalf("Send() after Reset error = %v", err)
	}

	fake.AssertSentCount(t, 1)
	fake.AssertNotSent(t, func(message mail.Message) bool {
		return message.Subject() == "other"
	})
}

// TestFakeAssertionsRequireEveryPredicate verifies all predicates must match
// and that no predicate matches any message.
func TestFakeAssertionsRequireEveryPredicate(t *testing.T) {
	fake := mailtest.New()
	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "b@example.com"}},
	}, mail.Body{Text: "hi"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "a@example.com"})
		letter.Subject("hello")
	})

	fake.AssertNotSent(t)

	if _, err := fake.Send(t.Context(), message); err != nil {
		t.Fatal(err)
	}

	hello := func(message mail.Message) bool {
		return message.Subject() == "hello"
	}

	other := func(message mail.Message) bool {
		return message.Subject() == "other"
	}

	fake.AssertSent(t)
	fake.AssertSent(t, hello, hello)
	fake.AssertNotSent(t, hello, other)
}

// ExampleFake records a message and inspects it.
func ExampleFake() {
	fake := mailtest.New()
	mailer := mail.New(fake, func(mailer *mail.Builder) {
		mailer.From(mail.Address{Email: "noreply@example.com"})
	})

	_, err := mailer.Send(context.Background(), mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.Subject("Welcome")
	}))

	if err != nil {
		fmt.Println(err)

		return
	}

	sent := fake.Sent()
	fmt.Println(err, len(sent), sent[0].Subject())
	// Output: <nil> 1 Welcome
}
