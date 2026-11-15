package access_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access"
	"arkoren.dev/ark/access/twofactor"
)

// TestVictimsVerifyClickDoesNotVerifyTheAttackersAccount verifies the verify
// link only works in a session of the account it belongs to: the victim, who is
// not logged in as the attacker's account, cannot verify it by clicking the
// genuine email, so the account stays unverified until a reset, which purges
// what the attacker attached.
func TestVictimsVerifyClickDoesNotVerifyTheAttackersAccount(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
			iam.TwoFactor(cipher(t), "Example")
			iam.Provider(fakeProvider{name: "fake"})
		})
		register(t, sys.client, "victim@example.com")
		subject, _, err := sys.iam.Directory().Lookup(ctx, "victim@example.com")
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if err := sys.iam.Links().Link(ctx, subject, auth.External{Issuer: "https://fake.example", Subject: "attacker-g"}); err != nil {
			t.Fatalf("Link: %v", err)
		}
		enrollment, _ := sys.iam.Factors().Enroll(ctx, subject, "victim@example.com")
		if _, err := sys.iam.Factors().Activate(ctx, subject, code(t, enrollment.Secret, 0)); err != nil {
			t.Fatalf("Activate: %v", err)
		}

		// The victim opens the genuine link without being logged in as the account.
		token := sys.box.last(t, access.KindVerify).Token
		victim := sys.client.NewDevice()
		if reply := victim.Post("/email/verify", `{"token":"`+token+`"}`); reply.Status != http.StatusUnauthorized {
			t.Fatalf("verify while logged out = %d %s, want 401", reply.Status, reply.Body)
		}
		if ok, _ := sys.iam.Directory().Verified(ctx, subject); ok {
			t.Fatal("the victim's click verified the attacker's account")
		}
		// Another user's session does not verify it either.
		register(t, victim, "other@example.com")
		if reply := victim.Post("/email/verify", `{"token":"`+token+`"}`); reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "account:verify-wrong-account") {
			t.Fatalf("verify as another user = %d %s, want 403 verify-wrong-account", reply.Status, reply.Body)
		}

		// Only the reset proves the mailbox, and it purges.
		reset := sys.client.NewDevice()
		reset.Post("/password/forgot", `{"email":"victim@example.com"}`)
		sys.settle(t)
		resetToken := sys.box.last(t, access.KindReset).Token
		if reply := reset.Post("/password/reset", `{"token":"`+resetToken+`","password":"victim horse ok"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("reset = %d %s", reply.Status, reply.Body)
		}
		if connections, _ := sys.iam.Links().List(ctx, subject); len(connections) != 0 {
			t.Fatalf("the attacker's connection survived: %v", connections)
		}
		if enrolled, _ := sys.iam.Factors().Enrolled(ctx, subject); enrolled {
			t.Fatal("the attacker's authenticator survived")
		}
		if reply := reset.Post("/login", `{"email":"victim@example.com","password":"victim horse ok"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("victim login = %d %s", reply.Status, reply.Body)
		}
	})
}

// TestEnablingTwoFactorFailsClosed verifies a failing step during enabling
// rolls the activation back: two-factor stays off and no code was shown.
func TestEnablingTwoFactorFailsClosed(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
			iam.TwoFactor(cipher(t), "Example", func(factors *twofactor.Builder) {
				factors.OnEnable(func(context.Context, string) error { return errors.New("revocation down") })
			})
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		setup := sys.client.Post("/two-factor/setup", "")
		enable := sys.client.Post("/two-factor", `{"code":"`+code(t, secretOf(setup.Body), 0)+`"}`)
		if enable.Status != http.StatusInternalServerError || strings.Contains(enable.Body, "recovery_codes") {
			t.Fatalf("enable with a failing step = %d %s, want 500 and no codes", enable.Status, enable.Body)
		}
		if enrolled, _ := sys.iam.Factors().Enrolled(testContext(t), mustSubject(t, sys)); enrolled {
			t.Fatal("two-factor was left enabled after a failed enabling")
		}
	})
}

// TestOwnUsersNeedTheVerifiedHooks verifies the configuration rules for
// Users.Verified and Users.ProveEmail.
func TestOwnUsersNeedTheVerifiedHooks(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		users := newOwnUsers(t)
		provider, err := auth.NewUserProvider(users.driver)
		if err != nil {
			t.Fatalf("NewUserProvider: %v", err)
		}
		verified := func(context.Context, string) (bool, error) { return true, nil }
		prove := func(context.Context, string) (bool, error) { return false, nil }
		for name, own := range map[string]access.Users{
			"create without hooks": {Provider: provider, Lookup: users.lookup, Create: users.create, Rotate: users.rotate},
			"only Verified":        {Provider: provider, Lookup: users.lookup, Rotate: users.rotate, Verified: verified},
			"only ProveEmail":      {Provider: provider, Lookup: users.lookup, Rotate: users.rotate, ProveEmail: prove},
		} {
			_, err := access.New(db, cache.New(memorycache.New()), func(iam *access.Builder) {
				iam.Instance(instance)
				iam.TrustRemoteAddr()
				iam.Users(own)
				iam.Password(hasher())
			})
			if !errors.Is(err, access.ErrInvalidConfig) {
				t.Errorf("%s: New error = %v, want ErrInvalidConfig", name, err)
			}
		}
	})
}

// TestOnEmailProvenRunsInTheReset verifies the application hook runs after the
// built-in purge when a reset proves an unverified mailbox, and not for a
// verified one.
func TestOnEmailProvenRunsInTheReset(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		var proven []string
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
			iam.OnEmailProven(func(_ context.Context, subject string) error {
				proven = append(proven, subject)

				return nil
			})
		})
		register(t, sys.client, "ada@example.com")
		registerVerified(t, sys, sys.client.NewDevice(), "bob@example.com")
		for _, email := range []string{"ada@example.com", "bob@example.com"} {
			sys.client.NewDevice().Post("/password/forgot", `{"email":"`+email+`"}`)
			sys.settle(t)
			token := sys.box.last(t, access.KindReset).Token
			sys.client.NewDevice().Post("/password/reset", `{"token":"`+token+`","password":"reset horse ok"}`)
		}
		ada, _, _ := sys.iam.Directory().Lookup(testContext(t), "ada@example.com")
		if len(proven) != 1 || proven[0] != ada {
			t.Fatalf("OnEmailProven ran for %v, want only the unverified ada (%s)", proven, ada)
		}
	})
}
