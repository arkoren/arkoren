package account_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/url"
	"slices"
	"sync"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/link"
	"arkoren.dev/core/http/middleware/ratelimit"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"
	sqlite "arkoren.dev/driver/sqlite/sqlitedatabase"
	"arkoren.dev/foundation/key"

	"arkoren.dev/ark/iam/account"
)

// verifyPage is the page of the application that verification links open.
const verifyPage = "https://app.example.com/verify-email"

// outbox records what a directory stores on its queue and sends through its
// mailer. Tests run the worker themselves, by calling SendVerification, so the
// order of dispatch and delivery stays explicit.
type outbox struct {
	mu         sync.Mutex
	queue      *queuetest.Fake
	dispatcher *queue.Dispatcher
	mail       *mailtest.Fake
	mailer     *mail.Mailer
	delivered  []account.VerifyMessage
}

// newOutbox returns an outbox over an in-memory queue and a recording mailer.
func newOutbox() *outbox {
	fake := queuetest.New()
	sent := mailtest.New()

	return &outbox{
		queue:      fake,
		dispatcher: queue.NewDispatcher(fake),
		mail:       sent,
		mailer: mail.New(sent, func(mailer *mail.Builder) {
			mailer.From(mail.Address{Email: "noreply@example.com"})
		}),
	}
}

// verification configures email verification of a directory with signer on the
// outbox: its queue, its mailer and an email function that records the message.
func (box *outbox) verification(signer *link.Signer) account.BuilderFunc {
	return func(accounts *account.Builder) {
		accounts.Queue(box.dispatcher)
		accounts.Verification(signer, box.mailer, verifyPage, func(verification *account.VerificationBuilder) {
			verification.Email(box.email)
		})
	}
}

// email records message and writes the email of an email verification.
func (box *outbox) email(message account.VerifyMessage, href string) mail.Message {
	box.mu.Lock()
	defer box.mu.Unlock()

	box.delivered = append(box.delivered, message)

	return mail.NewMessage(mail.Recipients{To: []mail.Address{{Email: message.Address}}}, mail.Body{Text: href})
}

// sent returns a copy of the delivered messages.
func (box *outbox) sent() []account.VerifyMessage {
	box.mu.Lock()
	defer box.mu.Unlock()

	return slices.Clone(box.delivered)
}

// dispatched returns the verification requests stored on the queue so far.
func (box *outbox) dispatched(t *testing.T) []account.VerifyRequest {
	t.Helper()

	var requests []account.VerifyRequest
	for _, message := range box.queue.Dispatched() {
		var request account.VerifyRequest
		if err := json.Unmarshal(message.Payload, &request); err != nil {
			t.Fatalf("decode %s payload %s: %v", message.Name, message.Payload, err)
		}
		requests = append(requests, request)
	}

	return requests
}

// newSigner returns a link signer over a fresh key: relative when origin is
// empty, otherwise absolute under origin.
func newSigner(t *testing.T, origin string) *link.Signer {
	t.Helper()

	secret, err := key.GenerateShared()
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	ring, err := key.NewRing(secret)
	if err != nil {
		t.Fatalf("NewRing: %v", err)
	}
	signer, err := link.New(ring, func(signer *link.Builder) {
		signer.Origin(origin)
	})
	if err != nil {
		t.Fatalf("link.New: %v", err)
	}

	return signer
}

// newDirectory builds a migrated directory on db whose verification stores its
// jobs and sends its emails through box, signing relative links.
func newDirectory(t *testing.T, db *database.Database, instance string, box *outbox) *account.Directory {
	t.Helper()

	return newDirectoryWith(t, db, instance, newSigner(t, ""), box, nil)
}

// newDirectoryWith is [newDirectory] with the signer and the events bus chosen.
func newDirectoryWith(t *testing.T, db *database.Database, instance string, signer *link.Signer, box *outbox, bus *event.Bus) *account.Directory {
	t.Helper()

	if box == nil {
		box = newOutbox()
	}
	directory, err := account.NewDirectory(db, cache.New(memorycache.New()), ratelimit.TrustedProxyKey(), func(accounts *account.Builder) {
		accounts.Instance(instance).Events(bus)
		box.verification(signer)(accounts)
	})
	if err != nil {
		t.Fatalf("NewDirectory: %v", err)
	}
	migrateUp(t, db, directory.Migrations()...)

	return directory
}

// TestCreateFindRoundTrip verifies a created user is found by subject and by
// email, with a normalized email and the record shape the guard relies on.
func TestCreateFindRoundTrip(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, newOutbox())
		subject, err := directory.Create(ctx, "  Ada@Example.COM ")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		found, address, err := directory.Lookup(ctx, "ada@example.com")
		if err != nil || found != subject || address != "ada@example.com" {
			t.Fatalf("Lookup = %q, %q, %v; want %q, ada@example.com", found, address, err, subject)
		}
		record, err := directory.Find(ctx, subject)
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		user, ok := record.Value.(account.Account)
		if !ok || user.Email != "ada@example.com" || user.Verified() {
			t.Fatalf("record value = %#v", record.Value)
		}
		if record.Version != "0" || record.Claims["email_verified"] != false {
			t.Fatalf("record = %#v", record)
		}
	})
}

// TestCreateDuplicateEmailIsTaken verifies an address is unique after
// normalization and the second create is the 409 problem.
func TestCreateDuplicateEmailIsTaken(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, newOutbox())
		if _, err := directory.Create(ctx, "ada@example.com"); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := directory.Create(ctx, "ADA@example.com"); !errors.Is(err, account.ErrEmailTaken) {
			t.Fatalf("second Create error = %v, want ErrEmailTaken", err)
		}
		if _, err := directory.CreateVerified(ctx, "ada@example.com"); !errors.Is(err, account.ErrEmailTaken) {
			t.Fatalf("CreateVerified error = %v, want ErrEmailTaken", err)
		}
	})
}

// TestCreateRejectsInvalidEmail verifies malformed addresses are refused and
// that Lookup reports them as an unknown user.
func TestCreateRejectsInvalidEmail(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, newOutbox())
		for _, email := range []string{"", "nobody", "Ada <ada@example.com>", "a@b@c"} {
			if _, err := directory.Create(ctx, email); !errors.Is(err, account.ErrInvalidEmail) {
				t.Errorf("Create(%q) error = %v, want ErrInvalidEmail", email, err)
			}
			if _, _, err := directory.Lookup(ctx, email); !errors.Is(err, auth.ErrUserNotFound) {
				t.Errorf("Lookup(%q) error = %v, want ErrUserNotFound", email, err)
			}
		}
	})
}

// TestFindUnknownAndInvalidSubject verifies the driver contract errors.
func TestFindUnknownAndInvalidSubject(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, newOutbox())
		if _, err := directory.Find(ctx, "missing"); !errors.Is(err, auth.ErrUserNotFound) {
			t.Errorf("Find(missing) error = %v, want ErrUserNotFound", err)
		}
		if _, err := directory.Find(ctx, ""); !errors.Is(err, auth.ErrInvalidSubject) {
			t.Errorf("Find(empty) error = %v, want ErrInvalidSubject", err)
		}
	})
}

// TestSubjectComparisonIsBytewise verifies subjects that differ only by case
// are different users, which a case-insensitive collation would merge.
func TestSubjectComparisonIsBytewise(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, newOutbox())
		subject, err := directory.Create(ctx, "ada@example.com")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		upper := ""
		for _, char := range subject {
			if char >= 'a' && char <= 'f' {
				char -= 'a' - 'A'
			}
			upper += string(char)
		}
		if upper == subject {
			t.Skip("generated subject has no letters")
		}
		if _, err := directory.Find(ctx, upper); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("Find(%q) error = %v, want ErrUserNotFound", upper, err)
		}
	})
}

// TestRotateChangesVersion verifies Rotate bumps the record version, and that an
// unknown subject is an error.
func TestRotateChangesVersion(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, newOutbox())
		subject, _ := directory.Create(ctx, "ada@example.com")
		before, _ := directory.Find(ctx, subject)
		if err := directory.Rotate(ctx, subject); err != nil {
			t.Fatalf("Rotate: %v", err)
		}
		after, _ := directory.Find(ctx, subject)
		if before.Version == after.Version {
			t.Fatalf("version did not change: %q", after.Version)
		}
		if err := directory.Rotate(ctx, "missing"); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("Rotate(missing) error = %v", err)
		}
	})
}

// TestDisabledUserIsDisabled verifies Disable and Enable toggle the driver
// error.
func TestDisabledUserIsDisabled(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, newOutbox())
		subject, _ := directory.Create(ctx, "ada@example.com")
		if err := directory.Disable(ctx, subject); err != nil {
			t.Fatalf("Disable: %v", err)
		}
		if _, err := directory.Find(ctx, subject); !errors.Is(err, auth.ErrUserDisabled) {
			t.Fatalf("Find error = %v, want ErrUserDisabled", err)
		}
		if err := directory.Enable(ctx, subject); err != nil {
			t.Fatalf("Enable: %v", err)
		}
		if _, err := directory.Find(ctx, subject); err != nil {
			t.Fatalf("Find after Enable: %v", err)
		}
	})
}

// TestLookupReturnsTheStoredAddress verifies a look-alike spelling finds the
// user but reports the stored address, which is where mail must go.
func TestLookupReturnsTheStoredAddress(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, newOutbox())
		subject, _ := directory.Create(ctx, "bob@gmail.com")
		found, address, err := directory.Lookup(ctx, "bob@gmaİl.com")
		if err != nil || found != subject || address != "bob@gmail.com" {
			t.Fatalf("Lookup = %q, %q, %v; want %q and the stored bob@gmail.com", found, address, err, subject)
		}
	})
}

// TestProveEmailReportsOnlyTheFirstProof verifies the atomic transition from
// unverified to verified, which also ends the links sent before.
func TestProveEmailReportsOnlyTheFirstProof(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		box := newOutbox()
		directory := newDirectory(t, db, instance, box)
		subject, _ := directory.Create(ctx, "ada@example.com")
		if err := directory.SendVerification(ctx, account.VerifyRequest{Subject: subject}); err != nil {
			t.Fatalf("SendVerification: %v", err)
		}
		changed, err := directory.ProveEmail(ctx, subject)
		if err != nil || !changed {
			t.Fatalf("first ProveEmail = %v, %v; want true", changed, err)
		}
		if changed, err := directory.ProveEmail(ctx, subject); err != nil || changed {
			t.Fatalf("second ProveEmail = %v, %v; want false", changed, err)
		}
		if _, err := directory.Verify(ctx, box.sent()[0].Signed); !errors.Is(err, account.ErrInvalidToken) {
			t.Fatalf("the link sent before survived: %v", err)
		}
		if _, err := directory.ProveEmail(ctx, "00000000000000000000000000000000"); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("unknown subject error = %v", err)
		}
	})
}

// TestDeleteRunsForgetHooks verifies Delete removes the user and runs the hooks
// of the provider in the same transaction, and that a failing hook keeps the
// user.
func TestDeleteRunsForgetHooks(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory := newDirectory(t, db, instance, newOutbox())
		subject, _ := directory.Create(ctx, "ada@example.com")
		var forgotten []string
		fail := true
		directory.Provider().OnForget(func(_ context.Context, subject string) error {
			if fail {
				return errors.New("hook failed")
			}
			forgotten = append(forgotten, subject)

			return nil
		})
		if err := directory.Delete(ctx, subject); err == nil {
			t.Fatal("Delete succeeded despite a failing hook")
		}
		if _, err := directory.Find(ctx, subject); err != nil {
			t.Fatalf("user vanished after a failed delete: %v", err)
		}
		fail = false
		if err := directory.Delete(ctx, subject); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if len(forgotten) != 1 || forgotten[0] != subject {
			t.Fatalf("forgotten = %v", forgotten)
		}
		if _, err := directory.Find(ctx, subject); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("Find after Delete error = %v", err)
		}
	})
}

// TestTwoInstancesSeparateTables verifies two instances over one database do
// not see each other's users.
func TestTwoInstancesSeparateTables(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		first := newDirectory(t, db, instance+"_a", nil)
		second := newDirectory(t, db, instance+"_b", nil)
		subject, _ := first.Create(ctx, "ada@example.com")
		if _, err := second.Find(ctx, subject); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("second instance found the user: %v", err)
		}
		if _, err := second.Create(ctx, "ada@example.com"); err != nil {
			t.Fatalf("same email in the second instance: %v", err)
		}
	})
}

// TestPingNamesMigrations verifies Ping fails with ErrNotMigrated before the
// migrations ran and succeeds after.
func TestPingNamesMigrations(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		directory, err := account.NewDirectory(db, cache.New(memorycache.New()), ratelimit.TrustedProxyKey(), func(accounts *account.Builder) {
			accounts.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewDirectory: %v", err)
		}
		err = directory.Ping(ctx)
		if !errors.Is(err, account.ErrNotMigrated) {
			t.Fatalf("Ping error = %v, want ErrNotMigrated", err)
		}
		migrateUp(t, db, directory.Migrations()...)
		if err := directory.Ping(ctx); err != nil {
			t.Fatalf("Ping after migrations: %v", err)
		}
	})
}

// TestNewDirectoryRejectsBadConfig verifies invalid instances and a nil
// database are configuration errors.
func TestNewDirectoryRejectsBadConfig(t *testing.T) {
	t.Parallel()

	if _, err := account.NewDirectory(nil, nil, nil); !errors.Is(err, account.ErrInvalidConfig) {
		t.Errorf("nil database error = %v", err)
	}
	driver, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	db := database.New(driver)
	if _, err := account.NewDirectory(db, nil, ratelimit.TrustedProxyKey()); !errors.Is(err, account.ErrInvalidConfig) {
		t.Errorf("nil store error = %v", err)
	}
	if _, err := account.NewDirectory(db, cache.New(memorycache.New()), nil); !errors.Is(err, account.ErrInvalidConfig) {
		t.Errorf("nil client address source error = %v", err)
	}
	for _, instance := range []string{"Upper", "1digit", "has-dash", "a23456789012345678901"} {
		_, err := account.NewDirectory(db, cache.New(memorycache.New()), ratelimit.TrustedProxyKey(), func(accounts *account.Builder) {
			accounts.Instance(instance)
		})
		if !errors.Is(err, account.ErrInvalidConfig) {
			t.Errorf("instance %q error = %v", instance, err)
		}
	}
}

// postedValue returns what a front end posts for a delivered link: the fragment
// of href, decoded as location.hash is with decodeURIComponent.
func postedValue(t *testing.T, href string) string {
	t.Helper()

	parsed, err := url.Parse(href)
	if err != nil || parsed.Fragment == "" {
		t.Fatalf("link %q has no fragment: %v", href, err)
	}

	return parsed.Fragment
}
