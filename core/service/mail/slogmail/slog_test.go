package slogmail_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
	"arkoren.dev/core/service/mail/slogmail"
)

// TestConformance verifies the driver passes the mail conformance suite.
func TestConformance(t *testing.T) {
	mailtest.Run(t, func(*testing.T) mail.Driver {
		return slogmail.New(func(mailer *slogmail.Builder) {
			mailer.Logger(slog.New(slog.DiscardHandler))
		})
	})
}

// TestSendLogsMessageWithoutAttachmentData verifies the log output.
func TestSendLogsMessageWithoutAttachmentData(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	var retained *slogmail.Builder
	driver := slogmail.New(func(mailer *slogmail.Builder) {
		retained = mailer
		mailer.Logger(logger)
	}, func(mailer *slogmail.Builder) {
		mailer.Detail(slogmail.DetailBody)
	})
	retained.Logger(slog.New(slog.DiscardHandler))
	retained.Detail(slogmail.DetailSummary)

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "b@example.com"}},
	}, mail.Body{Text: "body-text"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "a@example.com"})
		letter.Subject("Welcome")
		letter.Attachments(mail.Attachment{
			Filename:    "f.txt",
			ContentType: "text/plain",
			Data:        []byte("secret-bytes"),
		})
	})

	receipt, err := driver.Send(t.Context(), message)

	if err != nil {
		t.Fatal(err)
	}

	logged := output.String()

	for _, want := range []string{"Welcome", "body-text", "b@example.com", "f.txt", receipt.ID} {
		if !strings.Contains(logged, want) {
			t.Errorf("log %q does not contain %q", logged, want)
		}
	}

	if strings.Contains(logged, "secret-bytes") {
		t.Errorf("log contains attachment bytes: %q", logged)
	}
}

// TestSendOmitsBodyByDefault verifies summary detail hides bodies.
func TestSendOmitsBodyByDefault(t *testing.T) {
	var output bytes.Buffer
	driver := slogmail.New(func(mailer *slogmail.Builder) {
		mailer.Logger(slog.New(slog.NewTextHandler(&output, nil)))
	})

	_, err := driver.Send(t.Context(), mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "b@example.com"}},
	}, mail.Body{Text: "body-text"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "a@example.com"})
	}))

	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(output.String(), "body-text") {
		t.Errorf("log contains body: %q", output.String())
	}
}

// TestSlogMailPoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestSlogMailPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *slogmail.Builder
	calls := 0
	component := slogmail.New(func(mailer *slogmail.Builder) {
		calls++
		retained = mailer
	}, func(mailer *slogmail.Builder) {
		if calls != 1 || retained != mailer {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	slogmail.New(func(mailer *slogmail.Builder) {
		if retained == mailer {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestSlogMailNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestSlogMailNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*slogmail.Builder)
	empty := []func(*slogmail.Builder){}

	if slogmail.New(absent...) == nil || slogmail.New(empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestSlogMailNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestSlogMailNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		slogmail.New(nil, func(mailer *slogmail.Builder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestSlogMailPolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestSlogMailPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		slogmail.New(func(mailer *slogmail.Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestSlogMailLoggerReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestSlogMailLoggerReturnsDraft(t *testing.T) {
	t.Parallel()

	mailer := &slogmail.Builder{}

	if mailer.Logger(nil) != mailer {
		t.Fatal("setting returned a different draft")
	}
}

// TestSlogMailLevelReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestSlogMailLevelReturnsDraft(t *testing.T) {
	t.Parallel()

	mailer := &slogmail.Builder{}

	if mailer.Level(0) != mailer {
		t.Fatal("setting returned a different draft")
	}
}

// TestSlogMailDetailReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestSlogMailDetailReturnsDraft(t *testing.T) {
	t.Parallel()

	mailer := &slogmail.Builder{}

	if mailer.Detail("") != mailer {
		t.Fatal("setting returned a different draft")
	}
}

// Example logs a message instead of sending it.
func Example() {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.TimeKey || attr.Key == "id" {
				return slog.Attr{}
			}

			return attr
		},
	})

	driver := slogmail.New(func(mailer *slogmail.Builder) {
		mailer.Logger(slog.New(handler))
	})

	_, err := driver.Send(context.Background(), mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "person@example.com"}},
	}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "noreply@example.com"})
		letter.Subject("Welcome")
	}))
	fmt.Println(err)
	// Output:
	// level=INFO msg="mail message" from=<noreply@example.com> to=<person@example.com> cc="" bcc="" reply_to="" subject=Welcome text_bytes=6 html_bytes=0 attachments=""
	// <nil>
}
