package account_test

import (
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/http/middleware/ratelimit"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"

	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/account"
)

// TestVerifyLifecycle verifies a token verifies the email once and then is
// spent.
func TestVerifyLifecycle(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		var delivered []account.Message
		directory := newDirectory(t, db, instance, &delivered)
		subject, _ := directory.Create(ctx, "ada@example.com")
		if err := directory.SendVerification(ctx, subject); err != nil {
			t.Fatalf("SendVerification: %v", err)
		}
		if len(delivered) != 1 || delivered[0].Email != "ada@example.com" {
			t.Fatalf("delivered = %#v", delivered)
		}
		got, err := directory.Verify(ctx, delivered[0].Token)
		if err != nil || got != subject {
			t.Fatalf("Verify = %q, %v", got, err)
		}
		record, _ := directory.Find(ctx, subject)
		if record.Claims["email_verified"] != true {
			t.Fatalf("claims = %#v", record.Claims)
		}
		if _, err := directory.Verify(ctx, delivered[0].Token); !errors.Is(err, account.ErrInvalidToken) {
			t.Fatalf("second Verify error = %v, want ErrInvalidToken", err)
		}
		if err := directory.SendVerification(ctx, subject); err != nil || len(delivered) != 1 {
			t.Fatalf("SendVerification for a verified user sent %d messages, err %v", len(delivered), err)
		}
	})
}

// TestVerifyTokenReplacedAndExpired verifies a newer token replaces the older
// one and an expired token is refused.
func TestVerifyTokenReplacedAndExpired(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		var delivered []account.Message
		directory := newDirectory(t, db, instance, &delivered)
		subject, _ := directory.Create(ctx, "ada@example.com")
		_ = directory.SendVerification(ctx, subject)
		_ = directory.SendVerification(ctx, subject)
		if _, err := directory.Verify(ctx, delivered[0].Token); !errors.Is(err, account.ErrInvalidToken) {
			t.Fatalf("replaced token error = %v, want ErrInvalidToken", err)
		}
		past := time.Now().Add(-time.Hour).Unix()
		_, err := db.Exec(ctx, query.Table(instance+"_email_verifications", func(statement *query.Builder) {
			statement.Unfiltered().Update(map[string]any{"expires_at": past})
		}))
		if err != nil {
			t.Fatalf("expire token: %v", err)
		}
		if _, err := directory.Verify(ctx, delivered[1].Token); !errors.Is(err, account.ErrInvalidToken) {
			t.Fatalf("expired token error = %v, want ErrInvalidToken", err)
		}
	})
}

// TestSendVerificationWithoutDelivery verifies the configuration error.
func TestSendVerificationWithoutDelivery(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		directory, err := account.NewDirectory(db, cache.New(memorycache.New()), ratelimit.TrustedProxyKey(), func(accounts *account.Builder) {
			accounts.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewDirectory: %v", err)
		}
		if err := directory.SendVerification(testContext(t), "x"); !errors.Is(err, account.ErrNoDelivery) {
			t.Fatalf("error = %v, want ErrNoDelivery", err)
		}
	})
}
