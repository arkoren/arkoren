package iam_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/foundation/totp"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/ark/iam/internal/apptest"
	"arkoren.dev/ark/iam/twofactor"
)

// TestResetDefeatsPreRegistrationHijack verifies the whole pre-registration
// attack: the attacker registers the victim's address, attaches a provider
// login, an authenticator and tokens, and the victim's password reset removes
// all of it, so neither the attacker's provider login nor their code works.
func TestResetDefeatsPreRegistrationHijack(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t))
			identity.Tokens()
			identity.Provider(fakeProvider{name: "fake"}, signUp)
		})
		attacker := sys.client
		register(t, attacker, "victim@example.com")

		// The gate: an unverified account can attach nothing through the routes.
		for _, route := range []struct{ method, path, body string }{
			{http.MethodPost, "/two-factor/setup", ""},
			{http.MethodPost, "/two-factor", `{"code":"123456"}`},
			{http.MethodPost, "/tokens", `{"name":"x"}`},
			{http.MethodPost, "/device-tokens", `{"device":"x"}`},
			{http.MethodPost, "/connections/fake", ""},
		} {
			reply := attacker.Do(route.method, route.path, route.body)
			if reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "account:email-unverified") {
				t.Fatalf("%s %s unverified = %d %s, want 403 email-unverified", route.method, route.path, reply.Status, reply.Body)
			}
		}

		// Plant what an attacker had before the gate could stop them (an
		// upgrade, an application's own route, a verified-then-changed address).
		subject, _, err := sys.identity.Directory().Lookup(ctx, "victim@example.com")
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if err := sys.identity.Links().Link(ctx, subject, auth.External{Issuer: "https://fake.example", Subject: "attacker-g"}); err != nil {
			t.Fatalf("Link: %v", err)
		}
		enrollment, err := sys.identity.Factors().Enroll(ctx, subject, "victim@example.com")
		if err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		if _, err := sys.identity.Factors().Activate(ctx, subject, code(t, enrollment.Secret, 0)); err != nil {
			t.Fatalf("Activate: %v", err)
		}
		if _, _, err := sys.identity.Issuer().Issue(ctx, subject, apitoken.Request{Name: "attacker"}); err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if _, _, err := sys.identity.Issuer().IssueDevice(ctx, subject, "attacker phone"); err != nil {
			t.Fatalf("IssueDevice: %v", err)
		}

		// The victim, who owns the mailbox, resets the password typed as they like.
		victim := sys.client.NewDevice()
		if reply := victim.Post("/password/forgot", `{"email":"Victim@Example.com"}`); reply.Status != http.StatusAccepted {
			t.Fatalf("forgot = %d", reply.Status)
		}
		message := sys.box.lastReset(t)
		if message.Address != "victim@example.com" {
			t.Fatalf("reset sent to %q", message.Address)
		}
		if reply := victim.Post("/password/reset", `{"token":"`+message.Signed+`","password":"victim horse ok"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("reset = %d %s", reply.Status, reply.Body)
		}

		if reply := victim.Post("/login", `{"email":"victim@example.com","password":"victim horse ok"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("victim login = %d %s, want a plain login without the attacker's second factor", reply.Status, reply.Body)
		}
		if reply := sys.client.NewDevice().Post("/login", `{"email":"victim@example.com","password":"correct horse"}`); reply.Status != http.StatusUnprocessableEntity {
			t.Fatalf("attacker's password login = %d, want 422", reply.Status)
		}
		if reply := attacker.Do(http.MethodGet, "/whoami", ""); reply.Body != "anonymous" {
			t.Fatalf("attacker old session = %q, want anonymous", reply.Body)
		}
		google := sys.client.NewDevice()
		google.Do(http.MethodGet, callbackPath("fake", "attacker-g", "victim@example.com", false), "")
		if reply := google.Do(http.MethodGet, "/whoami", ""); reply.Body != "anonymous" {
			t.Fatalf("the attacker's provider login still logs in: %q", reply.Body)
		}
		if connections, _ := sys.identity.Links().List(ctx, subject); len(connections) != 0 {
			t.Fatalf("connections after reset = %v", connections)
		}
		if enrolled, _ := sys.identity.Factors().Enrolled(ctx, subject); enrolled {
			t.Fatal("the attacker's authenticator survived the reset")
		}
		if tokens, _ := sys.identity.Issuer().Tokens(ctx, subject); len(tokens) != 0 {
			t.Fatalf("tokens after reset = %v", tokens)
		}
		record, _ := sys.identity.UserProvider().Find(ctx, subject)
		if record.Claims["email_verified"] != true {
			t.Fatalf("claims after reset = %v, want a verified email", record.Claims)
		}
	})
}

// TestResetOfVerifiedAccountKeepsItsFactors verifies the purge applies only to
// an unverified email: a verified user who resets keeps the authenticator.
func TestResetOfVerifiedAccountKeepsItsFactors(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t))
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		subject := mustSubject(t, sys)
		enrollment, _ := sys.identity.Factors().Enroll(ctx, subject, "ada")
		if _, err := sys.identity.Factors().Activate(ctx, subject, code(t, enrollment.Secret, 0)); err != nil {
			t.Fatalf("Activate: %v", err)
		}
		sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`)
		token := sys.box.lastReset(t).Signed
		sys.client.NewDevice().Post("/password/reset", `{"token":"`+token+`","password":"reset horse ok"}`)
		if enrolled, _ := sys.identity.Factors().Enrolled(ctx, subject); !enrolled {
			t.Fatal("a verified user's authenticator was removed by a reset")
		}
	})
}

// TestMailGoesToTheStoredAddress verifies the reset and verification emails are
// written to the address stored with the user, not to the text of the request:
// "bob@gmaİl.com" lowercases to bob@gmail.com for the lookup, and the link must
// not go to the look-alike the attacker typed.
func TestMailGoesToTheStoredAddress(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
		})
		registerVerified(t, sys, sys.client, "bob@gmail.com")
		if reply := sys.client.NewDevice().Post("/password/forgot", `{"email":"bob@gmaİl.com"}`); reply.Status != http.StatusAccepted {
			t.Fatalf("forgot = %d", reply.Status)
		}
		if got := sys.box.lastReset(t).Address; got != "bob@gmail.com" {
			t.Fatalf("reset sent to %q, want the stored bob@gmail.com", got)
		}
		if got := sys.box.dispatchedResets; len(got) != 1 || got[0].Identifier != "bob@gmaİl.com" {
			t.Fatalf("dispatched = %+v, want the identifier exactly as typed", got)
		}
	})
}

// TestTwoFactorNeedsRecentCodeNotPassword verifies a session that was logged in
// long ago (its second factor is older than the step-up window) cannot disable
// two-factor or regenerate recovery codes with only a fresh password confirm.
func TestTwoFactorNeedsRecentCodeNotPassword(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			// A one-nanosecond window makes every code "old" without sleeping.
			identity.TwoFactor(totp.New("Example"), cipher(t), func(factors *twofactor.Builder) {
				factors.StepUpWindow(time.Nanosecond)
			})
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		setup := sys.client.Post("/two-factor/setup", "")
		enable := sys.client.Post("/two-factor", `{"code":"`+code(t, secretOf(setup.Body), 0)+`"}`)
		if enable.Status != http.StatusOK {
			t.Fatalf("enable = %d %s", enable.Status, enable.Body)
		}
		if reply := sys.client.Post("/confirm", `{"method":"pwd","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("password confirm = %d %s", reply.Status, reply.Body)
		}
		for _, route := range []struct{ method, path string }{
			{http.MethodDelete, "/two-factor"},
			{http.MethodPost, "/two-factor/recovery-codes"},
		} {
			reply := sys.client.Do(route.method, route.path, "")
			if reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, `"methods":["otp"]`) {
				t.Fatalf("%s %s with an old code = %d %s, want 403 with methods [otp]", route.method, route.path, reply.Status, reply.Body)
			}
		}
		if enrolled, _ := sys.identity.Factors().Enrolled(testContext(t), mustSubject(t, sys)); !enrolled {
			t.Fatal("two-factor was disabled with a password alone")
		}
	})
}

// TestTwoFactorWithRecentCodeDisablesAndEnableEndsOtherLogins verifies the
// allowed path (a code from this session) and that enabling two-factor ends
// the user's other logins.
func TestTwoFactorWithRecentCodeDisablesAndEnableEndsOtherLogins(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t))
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		other := sys.client.NewDevice()
		if reply := other.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("other login = %d", reply.Status)
		}
		setup := sys.client.Post("/two-factor/setup", "")
		if reply := sys.client.Post("/two-factor", `{"code":"`+code(t, secretOf(setup.Body), 0)+`"}`); reply.Status != http.StatusOK {
			t.Fatalf("enable = %d %s", reply.Status, reply.Body)
		}
		if reply := other.Do(http.MethodGet, "/whoami", ""); reply.Body != "anonymous" {
			t.Fatalf("another login after enabling = %q, want anonymous", reply.Body)
		}
		if reply := sys.client.Do(http.MethodGet, "/whoami", ""); reply.Body == "anonymous" {
			t.Fatalf("the enabling session = %d, want kept", reply.Status)
		}
		if reply := sys.client.Post("/two-factor/recovery-codes", ""); reply.Status != http.StatusOK {
			t.Fatalf("regenerate with a recent code = %d %s", reply.Status, reply.Body)
		}
		if reply := sys.client.Do(http.MethodDelete, "/two-factor", ""); reply.Status != http.StatusNoContent {
			t.Fatalf("disable with a recent code = %d %s", reply.Status, reply.Body)
		}
	})
}

// TestEnablingTwoFactorRevokesAllTokens verifies enabling two-factor ends
// device and personal tokens, which were issued without the new factor.
func TestEnablingTwoFactorRevokesAllTokens(t *testing.T) {
	t.Parallel()

	checkEnablingTwoFactorTokens(t, false, http.StatusUnauthorized)
}

// TestEnablingTwoFactorKeepsPersonalTokensWhenOptedIn verifies
// KeepPersonalTokens limits enabling two-factor to device tokens.
func TestEnablingTwoFactorKeepsPersonalTokensWhenOptedIn(t *testing.T) {
	t.Parallel()

	checkEnablingTwoFactorTokens(t, true, http.StatusOK)
}

// checkEnablingTwoFactorTokens enables two-factor for a user holding a personal
// and a device token, with or without KeepPersonalTokens, and checks the device
// token is rejected and the personal one answers personalStatus.
func checkEnablingTwoFactorTokens(t *testing.T, keepPersonal bool, personalStatus int) {
	t.Helper()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t))
			identity.Tokens()
			if keepPersonal {
				identity.KeepPersonalTokens()
			}
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		subject := mustSubject(t, sys)
		_, personal, err := sys.identity.Issuer().Issue(ctx, subject, apitoken.Request{Name: "ci"})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		_, device, err := sys.identity.Issuer().IssueDevice(ctx, subject, "phone")
		if err != nil {
			t.Fatalf("IssueDevice: %v", err)
		}
		setup := sys.client.Post("/two-factor/setup", "")
		if reply := sys.client.Post("/two-factor", `{"code":"`+code(t, secretOf(setup.Body), 0)+`"}`); reply.Status != http.StatusOK {
			t.Fatalf("enable = %d %s", reply.Status, reply.Body)
		}
		status := func(secret string) int {
			return sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", "Authorization: Bearer "+secret).Status
		}
		if got := status(device); got != http.StatusUnauthorized {
			t.Fatalf("device token after enabling two-factor = %d, want 401", got)
		}
		if got := status(personal); got != personalStatus {
			t.Fatalf("personal token after enabling two-factor = %d, want %d", got, personalStatus)
		}
	})
}

// TestPasswordChangeRevokesAllTokens verifies a change ends device and personal
// tokens by default.
func TestPasswordChangeRevokesAllTokens(t *testing.T) {
	t.Parallel()

	checkPasswordChangeTokens(t, false, http.StatusUnauthorized)
}

// TestPasswordChangeKeepsPersonalTokensWhenOptedIn verifies KeepPersonalTokens
// limits a change to device tokens.
func TestPasswordChangeKeepsPersonalTokensWhenOptedIn(t *testing.T) {
	t.Parallel()

	checkPasswordChangeTokens(t, true, http.StatusOK)
}

// checkPasswordChangeTokens changes the password of a user holding a personal
// and a device token, with or without KeepPersonalTokens, and checks
// the device token is rejected and the personal one answers personalStatus.
func checkPasswordChangeTokens(t *testing.T, keepPersonal bool, personalStatus int) {
	t.Helper()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.Tokens()
			if keepPersonal {
				identity.KeepPersonalTokens()
			}
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		subject := mustSubject(t, sys)
		_, personal, err := sys.identity.Issuer().Issue(ctx, subject, apitoken.Request{Name: "ci"})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		_, device, err := sys.identity.Issuer().IssueDevice(ctx, subject, "phone")
		if err != nil {
			t.Fatalf("IssueDevice: %v", err)
		}
		if reply := sys.client.Do(http.MethodPut, "/password", `{"current_password":"correct horse","password":"changed horse ok"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("change = %d %s", reply.Status, reply.Body)
		}
		status := func(secret string) int {
			return sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", "Authorization: Bearer "+secret).Status
		}
		if got := status(device); got != http.StatusUnauthorized {
			t.Fatalf("device token after a password change = %d, want 401", got)
		}
		if got := status(personal); got != personalStatus {
			t.Fatalf("personal token after a password change = %d, want %d", got, personalStatus)
		}
	})
}

// TestForgotRegisterResendAreThrottledAndUniform verifies the per-identifier and
// per-address limits and that the answer for an unknown user is the same 202.
func TestForgotRegisterResendAreThrottledAndUniform(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
		})
		post := func(path, body, ip string) apptest.Reply {
			return sys.client.Do(http.MethodPost, path, body, "X-Test-IP: "+ip)
		}
		// A known and an unknown user get the same empty 202.
		if reply := post("/register", `{"email":"ada@example.com","password":"correct horse"}`, "10.0.0.1"); reply.Status != http.StatusAccepted {
			t.Fatalf("register = %d %s, want 202", reply.Status, reply.Body)
		}
		for _, email := range []string{"ada@example.com", "nobody@example.com"} {
			if reply := post("/password/forgot", `{"email":"`+email+`"}`, "10.0.0.2"); reply.Status != http.StatusAccepted || reply.Body != "" {
				t.Fatalf("forgot %s = %d %q, want an empty 202", email, reply.Status, reply.Body)
			}
		}

		// Per identifier as typed: the sixth request is throttled, known or not.
		for i := range 6 {
			reply := post("/password/forgot", `{"email":"limit@example.com"}`, "10.0.1."+strconv.Itoa(i))
			want := http.StatusAccepted
			if i == 5 {
				want = http.StatusTooManyRequests
			}
			if reply.Status != want {
				t.Fatalf("forgot #%d = %d, want %d", i+1, reply.Status, want)
			}
		}
		// Per client address: 30 requests for different identifiers, then 429.
		for i := range 31 {
			reply := post("/password/forgot", `{"email":"u`+strconv.Itoa(i)+`@example.com"}`, "10.9.9.9")
			if (reply.Status == http.StatusTooManyRequests) != (i == 30) {
				t.Fatalf("forgot from one address #%d = %d", i+1, reply.Status)
			}
		}
		// Registration: per identifier as typed.
		for i := range 6 {
			reply := post("/register", `{"email":"dup@example.com","password":"correct horse"}`, "10.0.2."+strconv.Itoa(i))
			if (reply.Status == http.StatusTooManyRequests) != (i == 5) {
				t.Fatalf("register #%d = %d %s", i+1, reply.Status, reply.Body)
			}
		}
		// Resend: per user.
		sys.client.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
		for i := range 6 {
			reply := sys.client.Do(http.MethodPost, "/email/verify/resend", "", "X-Test-IP: 10.0.3."+strconv.Itoa(i))
			want := http.StatusAccepted
			if i == 5 {
				want = http.StatusTooManyRequests
			}
			if reply.Status != want {
				t.Fatalf("resend #%d = %d %s, want %d", i+1, reply.Status, reply.Body, want)
			}
		}
	})
}

// TestLoginLockoutNeedsTheSameAddressAndCountsOnlyFailures verifies an attacker
// failing from one address cannot lock the real user out from another, that
// distributed failures hit the looser per-identifier cap, and that successful
// logins never count.
func TestLoginLockoutNeedsTheSameAddressAndCountsOnlyFailures(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
		})
		register(t, sys.client, "ada@example.com")
		login := func(password, ip string) int {
			return sys.client.NewDevice().Do(http.MethodPost, "/login", `{"email":"ada@example.com","password":"`+password+`"}`, "X-Test-IP: "+ip).Status
		}
		for range 15 {
			if got := login("correct horse", "10.0.0.5"); got != http.StatusNoContent {
				t.Fatalf("successful login = %d: successes must not count", got)
			}
		}
		for range 10 {
			if got := login("wrong horse!", "6.6.6.6"); got != http.StatusUnprocessableEntity {
				t.Fatalf("failed login = %d", got)
			}
		}
		if got := login("wrong horse!", "6.6.6.6"); got != http.StatusTooManyRequests {
			t.Fatalf("eleventh failure from one address = %d, want 429", got)
		}
		if got := login("correct horse", "10.0.0.5"); got != http.StatusNoContent {
			t.Fatalf("the user from another address = %d, want 204: the attacker locked them out", got)
		}
		// Distributed failures: each address stays under its own limit.
		for i := range 50 {
			got := login("wrong horse!", "20.0."+strconv.Itoa(i/10)+"."+strconv.Itoa(i%10))
			if got != http.StatusUnprocessableEntity && got != http.StatusTooManyRequests {
				t.Fatalf("distributed failure #%d = %d", i+1, got)
			}
		}
		if got := login("correct horse", "10.0.0.6"); got != http.StatusTooManyRequests {
			t.Fatalf("login after a distributed guess = %d, want the per-identifier cap (429)", got)
		}
	})
}

// TestOwnUsersCreateGetsVerifiedFlagAndGateHooks verifies Users.Create receives
// auth.External.EmailVerified, that Users.Verified gates the routes, and that
// Users.ProveEmail drives the purge on reset.
func TestOwnUsersCreateGetsVerifiedFlagAndGateHooks(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		users := newOwnUsers(t)
		provider, err := auth.NewUserProvider(users.driver)
		if err != nil {
			t.Fatalf("NewUserProvider: %v", err)
		}
		var created []bool
		proven := false
		verified := false
		sys := newAppWith(t, db, instance, wire{reset: true}, func(identity *iam.Builder) {
			identity.Users(iam.Users{
				Provider: provider,
				Lookup:   users.lookup,
				Create: func(ctx context.Context, identifier string, isVerified bool) (string, error) {
					created = append(created, isVerified)

					return users.create(ctx, identifier, isVerified)
				},
				Rotate:   users.rotate,
				Verified: func(context.Context, string) (bool, error) { return verified, nil },
				ProveEmail: func(context.Context, string) (bool, error) {
					proven = true

					return true, nil
				},
			})
			identity.Password(hasher(), registration)
			identity.Tokens()
			identity.Provider(fakeProvider{name: "fake"}, signUp)
		})
		sys.client.NewDevice().Do(http.MethodGet, callbackPath("fake", "g1", "a@example.com", true), "")
		if len(created) != 1 || !created[0] {
			t.Fatalf("Create verified flags = %v, want [true]", created)
		}
		if err := sys.identity.Verifier().Set(ctx, "u1", "correct horse"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		device := sys.client.NewDevice()
		if reply := device.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("login = %d %s", reply.Status, reply.Body)
		}
		if reply := device.Post("/tokens", `{"name":"x"}`); reply.Status != http.StatusForbidden {
			t.Fatalf("tokens while Verified is false = %d, want 403", reply.Status)
		}
		verified = true
		if reply := device.Post("/tokens", `{"name":"x"}`); reply.Status != http.StatusCreated {
			t.Fatalf("tokens while Verified is true = %d %s", reply.Status, reply.Body)
		}
		sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`)
		token := sys.box.lastReset(t).Signed
		sys.client.NewDevice().Post("/password/reset", `{"token":"`+token+`","password":"reset horse ok"}`)
		if !proven {
			t.Fatal("Users.ProveEmail was not called by the reset")
		}
		if tokens, _ := sys.identity.Issuer().Tokens(ctx, "u1"); len(tokens) != 0 {
			t.Fatalf("tokens after a reset of an unverified user = %v", tokens)
		}
	})
}

// lockSecondFactor enables two-factor for ada and uses up the second-factor
// attempts of a pending login with wrong codes, so the guard publishes
// [auth.SecondFactorLocked].
func lockSecondFactor(t *testing.T, sys *app) {
	t.Helper()

	registerVerified(t, sys, sys.client, "ada@example.com")
	setup := sys.client.Post("/two-factor/setup", "")
	if reply := sys.client.Post("/two-factor", `{"code":"`+code(t, secretOf(setup.Body), 0)+`"}`); reply.Status != http.StatusOK {
		t.Fatalf("enable = %d %s", reply.Status, reply.Body)
	}
	attacker := sys.client.NewDevice()
	if reply := attacker.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusAccepted {
		t.Fatalf("login = %d %s", reply.Status, reply.Body)
	}
	for range 5 {
		attacker.Post("/login/challenge", `{"method":"otp","code":"000000"}`)
	}
}

// TestSecondFactorLockoutCountsAgainstTheLoginThrottle verifies that after a
// lockout a correct password is no longer refunded, so starting pending logins
// ends in 429 instead of costing nothing, with no event bus set.
func TestSecondFactorLockoutCountsAgainstTheLoginThrottle(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t))
		})
		lockSecondFactor(t, sys)
		for i := range 12 {
			reply := sys.client.NewDevice().Post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
			if reply.Status == http.StatusTooManyRequests {
				return
			}
			if reply.Status != http.StatusAccepted {
				t.Fatalf("login #%d during the lockout = %d %s, want 202", i+1, reply.Status, reply.Body)
			}
		}
		t.Fatal("twelve pending logins during a lockout were never throttled")
	})
}

// TestSecondFactorLockoutReachesTheApplicationBus verifies the bus set with
// Events still receives auth.SecondFactorLocked from the facade's guard.
func TestSecondFactorLockoutReachesTheApplicationBus(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		bus := event.New()
		locked := make(chan string, 1)
		if _, err := bus.Listen(auth.SecondFactorLocked, func(_ context.Context, lockout auth.SecondFactorLockedEvent) error {
			locked <- lockout.Subject

			return nil
		}); err != nil {
			t.Fatalf("Listen: %v", err)
		}
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t))
			identity.Events(bus)
		})
		lockSecondFactor(t, sys)
		select {
		case subject := <-locked:
			if subject != mustSubject(t, sys) {
				t.Fatalf("locked subject = %q", subject)
			}
		default:
			t.Fatal("the application's bus did not receive SecondFactorLocked")
		}
	})
}

// brokenLockouts is a cache driver whose reads of lockout entries fail once
// armed, as when the cache is down.
type brokenLockouts struct {
	cache.Driver
	armed atomic.Bool
}

// Get fails for a lockout entry once armed.
func (driver *brokenLockouts) Get(ctx context.Context, key string) ([]byte, error) {
	if driver.armed.Load() && strings.HasPrefix(key, "arkoren:auth:lockouts") {
		return nil, errors.New("cache down")
	}

	return driver.Driver.Get(ctx, key)
}

// TestLoginFailsClosedWhenTheLockoutCannotBeRead verifies a correct password is
// not accepted, and its attempt is not refunded, when the guard cannot say
// whether the user is locked out.
func TestLoginFailsClosedWhenTheLockoutCannotBeRead(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		driver := &brokenLockouts{Driver: memorycache.New()}
		sys := newAppOn(t, db, instance, cache.New(driver), both, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
		})
		register(t, sys.client, "ada@example.com")
		driver.armed.Store(true)
		reply := sys.client.NewDevice().Post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
		if reply.Status != http.StatusInternalServerError {
			t.Fatalf("login with an unreadable lockout = %d %s, want 500", reply.Status, reply.Body)
		}
	})
}
