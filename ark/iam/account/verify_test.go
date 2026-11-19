package account_test

import (
	"context"
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
	"arkoren.dev/core/http/middleware/ratelimit"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam/account"
)

// sendLink creates a user, runs the worker for them and returns the subject and
// the signed value that was delivered.
func sendLink(t *testing.T, directory *account.Directory, box *outbox) (string, string) {
	t.Helper()

	ctx := testContext(t)
	subject, err := directory.Create(ctx, "ada@example.com")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := directory.SendVerification(ctx, account.VerifyRequest{Subject: subject}); err != nil {
		t.Fatalf("SendVerification: %v", err)
	}
	sent := box.sent()
	if len(sent) != 1 || sent[0].Address != "ada@example.com" || sent[0].Signed == "" || !sent[0].ExpiresAt.After(time.Now()) {
		t.Fatalf("delivered = %#v", sent)
	}

	return subject, sent[0].Signed
}

// TestVerifyLifecycle verifies a link verifies the email once, and a verified
// user is sent nothing more.
func TestVerifyLifecycle(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		box := &outbox{}
		directory := newDirectory(t, db, instance, box)
		subject, signed := sendLink(t, directory, box)
		got, err := directory.Verify(ctx, signed)
		if err != nil || got != subject {
			t.Fatalf("Verify = %q, %v", got, err)
		}
		record, _ := directory.Find(ctx, subject)
		if record.Claims["email_verified"] != true {
			t.Fatalf("claims = %#v", record.Claims)
		}
		if _, err := directory.Verify(ctx, signed); !errors.Is(err, account.ErrInvalidToken) {
			t.Fatalf("second Verify error = %v, want ErrInvalidToken", err)
		}
		if err := directory.SendVerification(ctx, account.VerifyRequest{Subject: subject}); err != nil || len(box.sent()) != 1 {
			t.Fatalf("SendVerification for a verified user sent %d messages, err %v", len(box.sent()), err)
		}
	})
}

// TestVerifyPublishesTheEventOnce verifies a second click on the link neither
// succeeds nor publishes EmailVerified again.
func TestVerifyPublishesTheEventOnce(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		bus := event.New()
		var verified atomic.Int32
		if _, err := bus.Listen(account.EmailVerified, func(context.Context, account.UserEvent) error {
			verified.Add(1)

			return nil
		}); err != nil {
			t.Fatalf("Listen: %v", err)
		}
		box := &outbox{}
		directory := newDirectoryWith(t, db, instance, newSigner(t, ""), box, bus)
		_, signed := sendLink(t, directory, box)
		_, _ = directory.Verify(testContext(t), signed)
		_, _ = directory.Verify(testContext(t), signed)
		if got := verified.Load(); got != 1 {
			t.Fatalf("EmailVerified published %d times, want 1", got)
		}
	})
}

// TestVerifyLinkRoundTripsUnderAnOrigin verifies a signer with an origin
// delivers an absolute link that verifies.
func TestVerifyLinkRoundTripsUnderAnOrigin(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		box := &outbox{}
		directory := newDirectoryWith(t, db, instance, newSigner(t, "https://app.example.com"), box, nil)
		subject, signed := sendLink(t, directory, box)
		if !strings.HasPrefix(signed, "https://app.example.com/email/verify?") {
			t.Fatalf("signed = %q, want an absolute link", signed)
		}
		posted := postedValue(t, box.sent()[0].Link("https://app.example.com/verify-email"))
		if got, err := directory.Verify(testContext(t), posted); err != nil || got != subject {
			t.Fatalf("Verify = %q, %v", got, err)
		}
	})
}

// TestVerifyLinkRoundTripsWithoutAnOrigin verifies a relative signer delivers a
// path-and-query link that verifies.
func TestVerifyLinkRoundTripsWithoutAnOrigin(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		box := &outbox{}
		directory := newDirectory(t, db, instance, box)
		subject, signed := sendLink(t, directory, box)
		if !strings.HasPrefix(signed, "/email/verify?") {
			t.Fatalf("signed = %q, want a relative link", signed)
		}
		posted := postedValue(t, box.sent()[0].Link("https://app.example.com/verify-email"))
		if got, err := directory.Verify(testContext(t), posted); err != nil || got != subject {
			t.Fatalf("Verify = %q, %v", got, err)
		}
	})
}

// TestVerifyRejectsTamperedLinks verifies a changed subject, a changed path and
// garbage are all the one invalid-link answer.
func TestVerifyRejectsTamperedLinks(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		box := &outbox{}
		directory := newDirectory(t, db, instance, box)
		subject, signed := sendLink(t, directory, box)
		other, err := directory.Create(ctx, "bob@example.com")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		for _, tampered := range []string{
			strings.Replace(signed, subject, other, 1),
			strings.Replace(signed, "/email/verify", "/password/reset", 1),
			signed + "x",
			"/email/verify?subject=" + subject,
			"not a link",
			"",
		} {
			if _, err := directory.Verify(ctx, tampered); !errors.Is(err, account.ErrInvalidToken) {
				t.Errorf("Verify(%q) error = %v, want ErrInvalidToken", tampered, err)
			}
		}
		if got, err := directory.Verify(ctx, signed); err != nil || got != subject {
			t.Fatalf("the genuine link stopped working: %q, %v", got, err)
		}
	})
}

// TestVerifyRejectsAnExpiredLink verifies a link past its expiry is the invalid
// answer. The link is signed inside a synctest bubble, whose clock starts in the
// year 2000, so it has expired by the real clock without any sleeping.
func TestVerifyRejectsAnExpiredLink(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		signer := newSigner(t, "")
		directory := newDirectoryWith(t, db, instance, signer, &outbox{}, nil)
		subject, _ := directory.Create(ctx, "ada@example.com")
		var expired string
		synctest.Test(t, func(t *testing.T) {
			var err error
			expired, err = signer.Temporary("/email/verify?subject="+subject, time.Hour, link.Bind([]byte("ada@example.com\x00false")))
			if err != nil {
				t.Fatalf("Temporary: %v", err)
			}
		})
		if _, err := directory.Verify(ctx, expired); !errors.Is(err, account.ErrInvalidToken) {
			t.Fatalf("expired link error = %v, want ErrInvalidToken", err)
		}
		// The control: the same construction, signed by the real clock, works, so
		// the refusal above is the expiry and not a wrong binding.
		current, err := signer.Temporary("/email/verify?subject="+subject, time.Hour, link.Bind([]byte("ada@example.com\x00false")))
		if err != nil {
			t.Fatalf("Temporary: %v", err)
		}
		if got, err := directory.Verify(ctx, current); err != nil || got != subject {
			t.Fatalf("the control link was refused: %q, %v", got, err)
		}
	})
}

// TestVerifyForRefusesAnotherAccount verifies a link opened under another
// account's session changes nothing and stays usable by its owner.
func TestVerifyForRefusesAnotherAccount(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		box := &outbox{}
		directory := newDirectory(t, db, instance, box)
		subject, signed := sendLink(t, directory, box)
		if _, err := directory.VerifyFor(ctx, "someone-else", signed); !errors.Is(err, account.ErrWrongAccount) {
			t.Fatalf("VerifyFor error = %v, want ErrWrongAccount", err)
		}
		if got, err := directory.VerifyFor(ctx, subject, signed); err != nil || got != subject {
			t.Fatalf("VerifyFor by the owner = %q, %v", got, err)
		}
	})
}

// TestVerifyConcurrentClicksOneWins verifies two simultaneous submits of one
// link verify the email exactly once.
func TestVerifyConcurrentClicksOneWins(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		box := &outbox{}
		directory := newDirectory(t, db, instance, box)
		_, signed := sendLink(t, directory, box)
		var succeeded atomic.Int32
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()

				if _, err := directory.Verify(testContext(t), signed); err == nil {
					succeeded.Add(1)
				}
			}()
		}
		wg.Wait()
		if got := succeeded.Load(); got != 1 {
			t.Fatalf("%d concurrent verifications succeeded, want 1", got)
		}
	})
}

// TestSendVerificationForAnUnknownSubjectSendsNothing verifies a user deleted
// since the dispatch is not an error a queue would retry.
func TestSendVerificationForAnUnknownSubjectSendsNothing(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		box := &outbox{}
		directory := newDirectory(t, db, instance, box)
		if err := directory.SendVerification(testContext(t), account.VerifyRequest{Subject: "missing"}); err != nil {
			t.Fatalf("SendVerification = %v, want nil", err)
		}
		if len(box.sent()) != 0 {
			t.Fatalf("delivered %d messages for an unknown subject", len(box.sent()))
		}
	})
}

// TestSendVerificationReturnsTheDeliveryError verifies a failing deliver
// function is returned, so a queue retries the request.
func TestSendVerificationReturnsTheDeliveryError(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		down := errors.New("smtp down")
		directory, err := account.NewDirectory(db, cache.New(memorycache.New()), ratelimit.TrustedProxyKey(), func(accounts *account.Builder) {
			accounts.Instance(instance)
			accounts.Verification(true, newSigner(t, ""), (&outbox{}).dispatch, func(context.Context, account.VerifyMessage) error { return down })
		})
		if err != nil {
			t.Fatalf("NewDirectory: %v", err)
		}
		migrateUp(t, db, directory.Migrations()...)
		subject, _ := directory.Create(testContext(t), "ada@example.com")
		if err := directory.SendVerification(testContext(t), account.VerifyRequest{Subject: subject}); !errors.Is(err, down) {
			t.Fatalf("error = %v, want the delivery error", err)
		}
	})
}

// TestRequestVerificationDispatchesTheSubject verifies the request side stores
// only the subject, and sends nothing itself.
func TestRequestVerificationDispatchesTheSubject(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		box := &outbox{}
		directory := newDirectory(t, db, instance, box)
		if err := directory.RequestVerification(testContext(t), "u1"); err != nil {
			t.Fatalf("RequestVerification: %v", err)
		}
		if len(box.dispatched) != 1 || box.dispatched[0].Subject != "u1" || len(box.sent()) != 0 {
			t.Fatalf("dispatched %v, delivered %d", box.dispatched, len(box.sent()))
		}
	})
}

// TestVerificationNeedsAllThree verifies a partial Verification setting fails
// construction.
func TestVerificationNeedsAllThree(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		_, err := account.NewDirectory(db, cache.New(memorycache.New()), ratelimit.TrustedProxyKey(), func(accounts *account.Builder) {
			accounts.Instance(instance)
			accounts.Verification(true, nil, (&outbox{}).dispatch, (&outbox{}).deliver)
		})
		if !errors.Is(err, account.ErrInvalidConfig) {
			t.Fatalf("error = %v, want ErrInvalidConfig", err)
		}
	})
}

// TestVerificationOperationsWithoutConfigurationFail verifies the worker and
// request operations return a configuration error, and that verifying a link
// cannot succeed, without Verification.
func TestVerificationOperationsWithoutConfigurationFail(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		directory, err := account.NewDirectory(db, cache.New(memorycache.New()), ratelimit.TrustedProxyKey(), func(accounts *account.Builder) {
			accounts.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewDirectory: %v", err)
		}
		ctx := testContext(t)
		if directory.VerificationEnabled() {
			t.Fatal("VerificationEnabled is true without Verification")
		}
		if err := directory.SendVerification(ctx, account.VerifyRequest{Subject: "x"}); !errors.Is(err, account.ErrInvalidConfig) {
			t.Fatalf("SendVerification error = %v, want ErrInvalidConfig", err)
		}
		if err := directory.RequestVerification(ctx, "x"); !errors.Is(err, account.ErrInvalidConfig) {
			t.Fatalf("RequestVerification error = %v, want ErrInvalidConfig", err)
		}
		if _, err := directory.Verify(ctx, "/email/verify?subject=x"); !errors.Is(err, account.ErrInvalidConfig) {
			t.Fatalf("Verify error = %v, want ErrInvalidConfig", err)
		}
	})
}

// ExampleVerifyMessage_Link builds the verification link: the escaped signed
// value is the fragment, so it never reaches a server log.
func ExampleVerifyMessage_Link() {
	message := account.VerifyMessage{Signed: "/email/verify?subject=u1&signature=abc"}
	fmt.Println(message.Link("https://app.example.com/verify"))
	// Output: https://app.example.com/verify#%2Femail%2Fverify%3Fsubject%3Du1%26signature%3Dabc
}
