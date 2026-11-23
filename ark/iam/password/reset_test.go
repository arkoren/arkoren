package password_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/link"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/iam/password"
)

// TestResetLinksDieTogether verifies a repeat request does not invalidate a
// link already sent, and that one completed reset ends the others, because they
// were all bound to the password it replaced.
func TestResetLinksDieTogether(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		for range 3 {
			if err := sys.verifier.SendReset(ctx, password.ResetRequest{Identifier: "ada@example.com"}); err != nil {
				t.Fatalf("SendReset: %v", err)
			}
		}
		links := *sys.sent
		if len(links) != 3 {
			t.Fatalf("sent %d messages, want 3", len(links))
		}
		if _, err := sys.verifier.Reset(ctx, links[0].Signed, "reset horse ok"); err != nil {
			t.Fatalf("the first link: %v", err)
		}
		for _, message := range links[1:] {
			if _, err := sys.verifier.Reset(ctx, message.Signed, "other horse ok"); !errors.Is(err, password.ErrInvalidToken) {
				t.Fatalf("a link outlived a completed reset: %v", err)
			}
		}
	})
}

// TestResetLinkDiesWhenThePasswordChanges verifies a link sent before a change
// of password, which changes the hash it is bound to, stops working.
func TestResetLinkDiesWhenThePasswordChanges(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, password.ResetRequest{Identifier: "ada@example.com"})
		if err := sys.verifier.Set(ctx, "1", "changed horse ok"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if _, err := sys.verifier.Reset(ctx, (*sys.sent)[0].Signed, "reset horse ok"); !errors.Is(err, password.ErrInvalidToken) {
			t.Fatalf("Reset error = %v, want ErrInvalidToken", err)
		}
	})
}

// TestResetLinkExpired verifies a link past its expiry is the invalid answer.
// The link is signed inside a synctest bubble, whose clock starts in the year
// 2000, so it has expired by the real clock without any sleeping.
func TestResetLinkExpired(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		signer := newSigner(t, "")
		sys := newSignedSystem(t, db, instance, signer, nil)
		sys.registerAda(t, sys.client)
		rows, err := db.Select[string](ctx, query.Table(instance+"_password_hashes", func(statement *query.Builder) {
			statement.Select("hash").Where("subject", "1")
		}))
		if err != nil || len(rows) != 1 {
			t.Fatalf("read hash = %v, %v", rows, err)
		}
		stored, err := base64.StdEncoding.DecodeString(rows[0])
		if err != nil {
			t.Fatalf("decode hash: %v", err)
		}
		var expired string
		synctest.Test(t, func(t *testing.T) {
			expired, err = signer.Temporary("/password/reset?subject=1", time.Hour, link.Bind(stored))
			if err != nil {
				t.Fatalf("Temporary: %v", err)
			}
		})
		if _, err := sys.verifier.Reset(ctx, expired, "reset horse ok"); !errors.Is(err, password.ErrInvalidToken) {
			t.Fatalf("expired link error = %v, want ErrInvalidToken", err)
		}
		// The control: the same construction, signed by the real clock, works, so
		// the refusal above is the expiry and not a wrong binding.
		current, err := signer.Temporary("/password/reset?subject=1", time.Hour, link.Bind(stored))
		if err != nil {
			t.Fatalf("Temporary: %v", err)
		}
		if _, err := sys.verifier.Reset(ctx, current, "reset horse ok"); err != nil {
			t.Fatalf("the control link was refused: %v", err)
		}
	})
}

// TestResetRejectsTamperedLinks verifies a changed subject, a changed path, a
// stripped signature and garbage are all the one invalid-link answer.
func TestResetRejectsTamperedLinks(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, password.ResetRequest{Identifier: "ada@example.com"})
		signed := (*sys.sent)[0].Signed
		for _, tampered := range []string{
			strings.Replace(signed, "subject=1", "subject=2", 1),
			strings.Replace(signed, "/password/reset", "/email/verify", 1),
			signed + "x",
			"/password/reset?subject=1",
			"not a link",
			"",
		} {
			if _, err := sys.verifier.Reset(ctx, tampered, "reset horse ok"); !errors.Is(err, password.ErrInvalidToken) {
				t.Errorf("Reset(%q) error = %v, want ErrInvalidToken", tampered, err)
			}
		}
		if _, err := sys.verifier.Reset(ctx, signed, "reset horse ok"); err != nil {
			t.Fatalf("the genuine link stopped working: %v", err)
		}
	})
}

// TestResetLinkRoundTripsUnderAnOrigin verifies a signer with an origin delivers
// an absolute link that resets.
func TestResetLinkRoundTripsUnderAnOrigin(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSignedSystem(t, db, instance, newSigner(t, "https://app.example.com"), nil)
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, password.ResetRequest{Identifier: "ada@example.com"})
		message := (*sys.sent)[0]
		if !strings.HasPrefix(message.Signed, "https://app.example.com/password/reset?") {
			t.Fatalf("signed = %q, want an absolute link", message.Signed)
		}
		posted := postedValue(t, message.Link("https://app.example.com/reset-password"))
		if _, err := sys.verifier.Reset(ctx, posted, "reset horse ok"); err != nil {
			t.Fatalf("Reset: %v", err)
		}
	})
}

// TestResetLinkRoundTripsWithoutAnOrigin verifies a relative signer delivers a
// path-and-query link that resets.
func TestResetLinkRoundTripsWithoutAnOrigin(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, password.ResetRequest{Identifier: "ada@example.com"})
		message := (*sys.sent)[0]
		if !strings.HasPrefix(message.Signed, "/password/reset?") {
			t.Fatalf("signed = %q, want a relative link", message.Signed)
		}
		posted := postedValue(t, message.Link("https://app.example.com/reset-password"))
		if _, err := sys.verifier.Reset(ctx, posted, "reset horse ok"); err != nil {
			t.Fatalf("Reset: %v", err)
		}
	})
}

// TestConcurrentResetOneWins verifies two submits of one link do not both
// succeed: the conditional write on the hash the link was signed for lets
// exactly one through.
func TestConcurrentResetOneWins(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, password.ResetRequest{Identifier: "ada@example.com"})
		signed := (*sys.sent)[0].Signed
		var succeeded atomic.Int32
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()

				if _, err := sys.verifier.Reset(ctx, signed, "reset horse ok"); err == nil {
					succeeded.Add(1)
				}
			}()
		}
		wg.Wait()
		if got := succeeded.Load(); got != 1 {
			t.Fatalf("%d resets succeeded, want 1", got)
		}
	})
}

// TestConcurrentFirstPasswordResetOneWins verifies the same for a user who has
// no password yet: the insert that does nothing on conflict lets one through.
func TestConcurrentFirstPasswordResetOneWins(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		if _, err := sys.users.Create(ctx, "grace@example.com"); err != nil {
			t.Fatalf("Create: %v", err)
		}
		_ = sys.verifier.SendReset(ctx, password.ResetRequest{Identifier: "grace@example.com"})
		signed := (*sys.sent)[0].Signed
		var succeeded atomic.Int32
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()

				if _, err := sys.verifier.Reset(ctx, signed, "reset horse ok"); err == nil {
					succeeded.Add(1)
				}
			}()
		}
		wg.Wait()
		if got := succeeded.Load(); got != 1 {
			t.Fatalf("%d resets succeeded, want 1", got)
		}
	})
}

// TestProviderOnlyUserSetsAFirstPasswordByReset verifies a user who has no
// password, as one who signed up with a provider, can set one with a link, once.
func TestProviderOnlyUserSetsAFirstPasswordByReset(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		subject, err := sys.users.Create(ctx, "grace@example.com")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := sys.verifier.SendReset(ctx, password.ResetRequest{Identifier: "grace@example.com"}); err != nil {
			t.Fatalf("SendReset: %v", err)
		}
		signed := (*sys.sent)[0].Signed
		if got, err := sys.verifier.Reset(ctx, signed, "first horse ok"); err != nil || got != subject {
			t.Fatalf("Reset = %q, %v", got, err)
		}
		if err := sys.verifier.Check(ctx, subject, "first horse ok"); err != nil {
			t.Fatalf("the first password does not check: %v", err)
		}
		if _, err := sys.verifier.Reset(ctx, signed, "second horse ok"); !errors.Is(err, password.ErrInvalidToken) {
			t.Fatalf("reusing the link error = %v, want ErrInvalidToken", err)
		}
	})
}

// TestWeakPasswordDoesNotBurnTheLink verifies a password that fails the policy
// is refused before anything is consumed, so the same link still works.
func TestWeakPasswordDoesNotBurnTheLink(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, password.ResetRequest{Identifier: "ada@example.com"})
		signed := (*sys.sent)[0].Signed
		if _, err := sys.verifier.Reset(ctx, signed, "short"); !errors.Is(err, password.ErrInvalidPassword) {
			t.Fatalf("weak reset error = %v, want ErrInvalidPassword", err)
		}
		if _, err := sys.verifier.Reset(ctx, signed, "reset horse ok"); err != nil {
			t.Fatalf("the link did not survive a weak password: %v", err)
		}
	})
}

// TestResetPublishesTheEventOnce verifies a completed reset publishes WasReset
// and a second submit of the link does not.
func TestResetPublishesTheEventOnce(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		bus := event.New()
		var resets atomic.Int32
		if _, err := bus.Listen(password.WasReset, func(context.Context, password.UserEvent) error {
			resets.Add(1)

			return nil
		}); err != nil {
			t.Fatalf("Listen: %v", err)
		}
		sys := newConfiguredSystem(t, db, instance, func(passwords *password.Builder) {
			passwords.Events(bus)
		})
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, password.ResetRequest{Identifier: "ada@example.com"})
		signed := (*sys.sent)[0].Signed
		_, _ = sys.verifier.Reset(ctx, signed, "reset horse ok")
		_, _ = sys.verifier.Reset(ctx, signed, "other horse ok")
		if got := resets.Load(); got != 1 {
			t.Fatalf("WasReset published %d times, want 1", got)
		}
	})
}

// TestSendResetForAnUnknownIdentifierSendsNothing verifies an unknown
// identifier is no error, so a queue does not retry it, and delivers nothing.
func TestSendResetForAnUnknownIdentifierSendsNothing(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		if err := sys.verifier.SendReset(testContext(t), password.ResetRequest{Identifier: "nobody@example.com"}); err != nil {
			t.Fatalf("SendReset = %v, want nil", err)
		}
		if len(*sys.sent) != 0 {
			t.Fatalf("delivered %d messages for an unknown identifier", len(*sys.sent))
		}
	})
}

// TestSendResetReturnsTheDeliveryError verifies a failing mailer's error is
// returned, so a queue retries the request.
func TestSendResetReturnsTheDeliveryError(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		down := errors.New("smtp down")
		fake := mailtest.New()
		fake.FailWith(down)
		sys := newConfiguredSystem(t, db, instance, func(passwords *password.Builder) {
			passwords.Reset(newSigner(t, ""), newMailer(fake), resetPage)
		})
		sys.registerAda(t, sys.client)
		if err := sys.verifier.SendReset(testContext(t), password.ResetRequest{Identifier: "ada@example.com"}); !errors.Is(err, down) {
			t.Fatalf("SendReset error = %v, want the delivery error", err)
		}
	})
}

// TestResetRejectsIncompleteSettings verifies a Reset setting missing its
// signer, mailer, queue or page, or naming a page that is relative or carries a
// fragment, fails construction.
func TestResetRejectsIncompleteSettings(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		users := newFakeUsers()
		sys := newSystem(t, db, instance)
		signer := newSigner(t, "")
		mailer := newMailer(mailtest.New())
		dispatcher := queue.NewDispatcher(queuetest.New())
		invalid := []func(*password.Builder){
			func(passwords *password.Builder) { passwords.Queue(dispatcher).Reset(nil, mailer, resetPage) },
			func(passwords *password.Builder) { passwords.Queue(dispatcher).Reset(signer, nil, resetPage) },
			func(passwords *password.Builder) { passwords.Reset(signer, mailer, resetPage) },
			func(passwords *password.Builder) { passwords.Queue(dispatcher).Reset(signer, mailer, "") },
			func(passwords *password.Builder) {
				passwords.Queue(dispatcher).Reset(signer, mailer, "/reset-password")
			},
			func(passwords *password.Builder) { passwords.Queue(dispatcher).Reset(signer, mailer, resetPage+"#top") },
		}
		for index, configure := range invalid {
			_, err := password.NewVerifier(sys.provider, db, sys.store, testClientIP, sys.hasher, password.Users{
				Lookup: users.Lookup,
				Rotate: users.Rotate,
			}, configure)
			if !errors.Is(err, password.ErrInvalidConfig) {
				t.Fatalf("setting %d: NewVerifier error = %v, want ErrInvalidConfig", index, err)
			}
		}
	})
}

// TestRegisterJobsSendsTheStoredRequest verifies a request stored by
// RequestReset is sent by the handler RegisterJobs registers, with the job named
// after the instance on the queue "iam".
func TestRegisterJobsSendsTheStoredRequest(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, _ string) {
		fake := queuetest.New()
		sys := newConfiguredSystem(t, db, "tenant_a", func(passwords *password.Builder) {
			passwords.Queue(queue.NewDispatcher(fake))
		})
		sys.registerAda(t, sys.client)
		consumer := queue.NewConsumer(fake)
		if err := sys.verifier.RegisterJobs(consumer); err != nil {
			t.Fatalf("RegisterJobs: %v", err)
		}
		if err := sys.verifier.RequestReset(testContext(t), password.ResetRequest{Identifier: "ada@example.com"}); err != nil {
			t.Fatalf("RequestReset: %v", err)
		}
		dispatched := fake.Dispatched()
		if len(dispatched) != 1 || dispatched[0].Name != "iam-password-reset-tenant-a" || dispatched[0].Queue != "iam" {
			t.Fatalf("dispatched = %+v, want the reset job of tenant_a on queue iam", dispatched)
		}
		if err := fake.Drain(testContext(t), consumer); err != nil {
			t.Fatalf("Drain: %v", err)
		}
		if len(*sys.sent) != 1 || (*sys.sent)[0].Address != "ada@example.com" {
			t.Fatalf("sent = %+v, want one message to the stored address", *sys.sent)
		}
	})
}

// TestDefaultResetEmails verifies the default reset email and the notice to the
// owner of a taken identifier go to the stored address with the page link, and
// say different things.
func TestDefaultResetEmails(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		fake := mailtest.New()
		sys := newConfiguredSystem(t, db, instance, func(passwords *password.Builder) {
			passwords.Reset(newSigner(t, ""), newMailer(fake), resetPage)
		})
		sys.registerAda(t, sys.client)
		ctx := testContext(t)
		for _, request := range []password.ResetRequest{{Identifier: "ada@example.com"}, {Identifier: "ada@example.com", Notice: true}} {
			if err := sys.verifier.SendReset(ctx, request); err != nil {
				t.Fatalf("SendReset(%+v): %v", request, err)
			}
		}
		sent := fake.Sent()
		if len(sent) != 2 {
			t.Fatalf("sent %d emails, want 2", len(sent))
		}
		for _, message := range sent {
			if message.To()[0].Email != "ada@example.com" || message.Subject() == "" || !strings.Contains(message.TextBody(), resetPage+"#") {
				t.Fatalf("email = to %v subject %q body %q, want the stored address, a subject and the page link", message.To(), message.Subject(), message.TextBody())
			}
		}
		if sent[0].Subject() == sent[1].Subject() {
			t.Fatalf("the reset and the notice share the subject %q", sent[0].Subject())
		}
	})
}

// TestInvalidResetEmailFailsPermanently verifies an email the mailer rejects as
// malformed is a permanent failure, which a queue does not retry.
func TestInvalidResetEmailFailsPermanently(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newConfiguredSystem(t, db, instance, func(passwords *password.Builder) {
			passwords.Reset(newSigner(t, ""), newMailer(mailtest.New()), resetPage, func(reset *password.ResetBuilder) {
				reset.Email(func(password.ResetMessage, string) mail.Message {
					return mail.NewMessage(mail.Recipients{}, mail.Body{})
				})
			})
		})
		sys.registerAda(t, sys.client)
		err := sys.verifier.SendReset(testContext(t), password.ResetRequest{Identifier: "ada@example.com"})
		if !errors.Is(err, queue.ErrPermanent) || !errors.Is(err, mail.ErrInvalidMessage) {
			t.Fatalf("SendReset error = %v, want a permanent invalid-message failure", err)
		}
	})
}

// ExampleResetMessage_Link builds the reset link: the escaped signed value is
// the fragment, so it never reaches a server log.
func ExampleResetMessage_Link() {
	message := password.ResetMessage{Signed: "/password/reset?subject=u1&signature=abc"}
	fmt.Println(message.Link("https://app.example.com/reset"))
	// Output: https://app.example.com/reset#%2Fpassword%2Freset%3Fsubject%3Du1%26signature%3Dabc
}
