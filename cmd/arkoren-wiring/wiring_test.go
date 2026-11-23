package wiring_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/ark/iam"
	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
)

// register signs ada up on b, verifies the email with the delivered link and
// fails unless she is logged in.
func register(t *testing.T, sys *system, b *browser) {
	t.Helper()

	if r := b.post("/register", `{"email":"ada@example.com","password":"correct horse"}`); r.status != http.StatusAccepted {
		t.Fatalf("register = %d %s", r.status, r.body)
	}
	if r := b.post("/login", `{"email":"ada@example.com","password":"correct horse"}`); r.status != http.StatusNoContent {
		t.Fatalf("login = %d %s", r.status, r.body)
	}
	token := sys.verificationToken()
	if r := b.post("/email/verify", `{"token":"`+token+`"}`); r.status != http.StatusNoContent {
		t.Fatalf("verify = %d %s", r.status, r.body)
	}
}

// enableTOTP enrolls an authenticator on b and returns its secret and the
// recovery codes.
func enableTOTP(t *testing.T, b *browser) (string, string) {
	t.Helper()

	setup := b.post("/two-factor/setup", "")
	if setup.status != http.StatusOK {
		t.Fatalf("setup = %d %s", setup.status, setup.body)
	}
	secret := field(t, setup.body, "secret")
	enable := b.post("/two-factor", `{"code":"`+code(t, secret, 0)+`"}`)
	if enable.status != http.StatusOK {
		t.Fatalf("enable = %d %s", enable.status, enable.body)
	}
	_, rest, _ := strings.Cut(enable.body, `["`)
	recovery, _, _ := strings.Cut(rest, `"`)

	return secret, recovery
}

// TestPasswordAndTOTPEndToEnd runs the password and two-factor flows through
// the facade: register, enable TOTP, then log in on other devices with a code
// and with a recovery code.
func TestPasswordAndTOTPEndToEnd(t *testing.T) {
	t.Parallel()

	sys := newSystem(t)
	register(t, sys, sys.browser)
	secret, recovery := enableTOTP(t, sys.browser)
	if me := sys.browser.get("/me"); !strings.HasSuffix(me.body, "|multi|session") {
		t.Fatalf("/me after enabling = %q", me.body)
	}

	phone := sys.browser.device()
	login := phone.post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
	if login.status != http.StatusAccepted || !strings.Contains(login.body, `"otp","recovery"`) {
		t.Fatalf("login = %d %s", login.status, login.body)
	}
	if me := phone.get("/me"); me.body != "anonymous" {
		t.Fatalf("a pending login is identified: %q", me.body)
	}
	if r := phone.post("/login/challenge", `{"method":"otp","code":"`+code(t, secret, 30*time.Second)+`"}`); r.status != http.StatusNoContent {
		t.Fatalf("challenge = %d %s", r.status, r.body)
	}
	if me := phone.get("/me"); !strings.HasSuffix(me.body, "|multi|session") {
		t.Fatalf("/me after the challenge = %q", me.body)
	}

	laptop := sys.browser.device()
	laptop.post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
	if r := laptop.post("/login/challenge", `{"method":"recovery","code":"`+recovery+`"}`); r.status != http.StatusNoContent {
		t.Fatalf("recovery challenge = %d %s", r.status, r.body)
	}
}

// ownUsers is the application's own user store.
type ownUsers struct {
	mu       sync.Mutex
	driver   *memoryauth.Driver
	subjects map[string]string
	versions map[string]int
}

// create adds a user.
func (users *ownUsers) create(_ context.Context, identifier string, _ bool) (string, error) {
	users.mu.Lock()
	defer users.mu.Unlock()

	subject := "u" + strconv.Itoa(len(users.subjects)+1)
	users.subjects[identifier] = subject

	return subject, users.driver.Put(auth.UserRecord{Subject: subject, Version: "0"})
}

// lookup finds a user by identifier.
func (users *ownUsers) lookup(_ context.Context, identifier string) (string, string, error) {
	users.mu.Lock()
	defer users.mu.Unlock()

	subject, ok := users.subjects[identifier]
	if !ok {
		return "", "", fmt.Errorf("own users: %w", auth.ErrUserNotFound)
	}

	return subject, identifier, nil
}

// rotate bumps a user's version.
func (users *ownUsers) rotate(_ context.Context, subject string) error {
	users.mu.Lock()
	defer users.mu.Unlock()

	users.versions[subject]++

	return users.driver.Put(auth.UserRecord{Subject: subject, Version: strconv.Itoa(users.versions[subject])})
}

// TestOwnUsersThroughAccessUsers runs the facade over the application's own
// users: no users table, password login, and TOTP for a user the facade never
// created.
func TestOwnUsersThroughAccessUsers(t *testing.T) {
	t.Parallel()

	users := &ownUsers{driver: memoryauth.New(), subjects: map[string]string{}, versions: map[string]int{}}
	if _, err := users.create(t.Context(), "ada@example.com", true); err != nil {
		t.Fatalf("create: %v", err)
	}
	provider, err := auth.NewUserProvider(users.driver)
	if err != nil {
		t.Fatalf("NewUserProvider: %v", err)
	}
	sys := newSystemWith(t, false, true, func(identity *iam.Builder) {
		identity.Users(iam.Users{Provider: provider, Lookup: users.lookup, Create: users.create, Rotate: users.rotate,
			Verified:   func(context.Context, string) (bool, error) { return true, nil },
			ProveEmail: func(context.Context, string) (bool, error) { return false, nil },
		})
	})
	for _, definition := range sys.identity.Migrations() {
		if strings.HasSuffix(definition.Group(), ":account") {
			t.Fatalf("a users table is migrated: %s", definition.ID())
		}
	}
	if err := sys.identity.Verifier().Set(t.Context(), "u1", "correct horse"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if r := sys.browser.post("/login", `{"email":"ada@example.com","password":"correct horse"}`); r.status != http.StatusNoContent {
		t.Fatalf("login = %d %s", r.status, r.body)
	}
	enableTOTP(t, sys.browser)
	if me := sys.browser.get("/me"); me.body != "u1|multi|session" {
		t.Fatalf("/me = %q", me.body)
	}
	if r := sys.browser.device().post("/login", `{"email":"nobody@example.com","password":"correct horse"}`); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown user login = %d", r.status)
	}
}

// socialLogin runs the browser login with the fake provider on b as subject.
func socialLogin(t *testing.T, sys *system, b *browser, subject, email string) reply {
	t.Helper()

	sys.provider.as(subject, email)
	state, nonce := authorize(t, b, "/auth/google")

	return b.get("/auth/google/callback?state=" + state + "&code=" + nonce)
}

// TestSocialLoginWithoutSignUpIsRefused runs the provider login with sign-up
// left at its default: an unknown identity is refused with 403 and no user.
func TestSocialLoginWithoutSignUpIsRefused(t *testing.T) {
	t.Parallel()

	sys := newSystemWith(t, true, false)
	social := sys.browser.device()
	if r := socialLogin(t, sys, social, "g1", "grace@example.com"); r.status != http.StatusForbidden {
		t.Fatalf("social login = %d %s, want 403", r.status, r.body)
	}
	if me := social.get("/me"); me.body != "anonymous" {
		t.Fatalf("/me after the refused login = %q", me.body)
	}
}

// TestSocialLoginThenConnect logs in with the provider (creating a verified
// user), then connects the provider to an existing password user.
func TestSocialLoginThenConnect(t *testing.T) {
	t.Parallel()

	sys := newSystem(t)
	social := sys.browser.device()
	if r := socialLogin(t, sys, social, "g1", "grace@example.com"); r.status >= http.StatusBadRequest {
		t.Fatalf("social login = %d %s", r.status, r.body)
	}
	me := social.get("/me")
	if !strings.HasSuffix(me.body, "|single|session") {
		t.Fatalf("/me after the social login = %q", me.body)
	}
	subject, _, _ := strings.Cut(me.body, "|")
	record, err := sys.identity.UserProvider().Find(t.Context(), subject)
	if err != nil || record.Claims["email_verified"] != true || record.Claims["email"] != "grace@example.com" {
		t.Fatalf("record = %+v, %v", record, err)
	}

	ada := sys.browser.device()
	register(t, sys, ada)
	if connect := ada.post("/connections/google", ""); connect.status != http.StatusSeeOther || connect.header.Get("Location") != "/auth/google" {
		t.Fatalf("connect = %d %q", connect.status, connect.header.Get("Location"))
	}
	if r := socialLogin(t, sys, ada, "g2", "someone-else@example.com"); r.status >= 400 {
		t.Fatalf("connecting callback = %d %s", r.status, r.body)
	}
	list := ada.get("/connections")
	if list.status != http.StatusOK || !strings.Contains(list.body, issuer) {
		t.Fatalf("connections = %d %s", list.status, list.body)
	}
	adaSubject, _, _ := strings.Cut(ada.get("/me").body, "|")
	other := sys.browser.device()
	socialLogin(t, sys, other, "g2", "someone-else@example.com")
	if got, _, _ := strings.Cut(other.get("/me").body, "|"); got != adaSubject {
		t.Fatalf("the connected identity logged in as %q, want %q", got, adaSubject)
	}
}

// TestNativeLoginThenDeviceTokenThenMe logs a phone in with a native ID token,
// exchanges the login for a device token and reads GET /me with it.
func TestNativeLoginThenDeviceTokenThenMe(t *testing.T) {
	t.Parallel()

	sys := newSystem(t)
	phone := sys.browser.device()
	nonce := field(t, phone.get("/auth/google/nonce").body, "nonce")
	idToken := sys.provider.token(nativeAudience, "g7", "ada@example.com", nonce)
	native := phone.post("/auth/google/native", `{"id_token":"`+idToken+`"}`)
	if native.status != http.StatusNoContent {
		t.Fatalf("native login = %d %s", native.status, native.body)
	}
	exchange := phone.post("/device-tokens", `{"device":"Pixel 9"}`)
	if exchange.status != http.StatusCreated {
		t.Fatalf("exchange = %d %s", exchange.status, exchange.body)
	}
	secret := field(t, exchange.body, "secret")
	if me := phone.get("/me"); me.body != "anonymous" {
		t.Fatalf("the session survived the exchange: %q", me.body)
	}
	me := sys.browser.device().get("/me", "Authorization: Bearer "+secret)
	if !strings.HasSuffix(me.body, "|none|token") {
		t.Fatalf("GET /me with the device token = %d %q, want a token identity", me.status, me.body)
	}
	if r := sys.browser.device().do(http.MethodDelete, "/device-tokens/current", "", "Authorization: Bearer "+secret); r.status != http.StatusNoContent {
		t.Fatalf("device logout = %d", r.status)
	}
}

// TestSilentReloginNotFreshForTOTPUser verifies a provider login that
// re-uses a session does not make a user with an authenticator fresh again: only
// a code (a step-up method) does. The window is shortened so the user's own
// proof goes stale after a real 1.2 seconds, because a server round trip is the
// only clock this end-to-end test has.
func TestSilentReloginNotFreshForTOTPUser(t *testing.T) {
	t.Parallel()

	sys := newSystem(t, func(identity *iam.Builder) {
		identity.Guard(func(policy *auth.GuardBuilder) {
			policy.FreshFor(time.Second)
		})
	})
	register(t, sys, sys.browser)
	secret, _ := enableTOTP(t, sys.browser)
	sys.provider.as("g3", "ada@example.com")
	sys.browser.post("/connections/google", "")
	state, nonce := authorize(t, sys.browser, "/auth/google")
	if r := sys.browser.get("/auth/google/callback?state=" + state + "&code=" + nonce); r.status >= 400 {
		t.Fatalf("connecting callback = %d %s", r.status, r.body)
	}

	time.Sleep(1200 * time.Millisecond)
	if r := sys.browser.do(http.MethodDelete, "/two-factor", ""); r.status != http.StatusForbidden || !strings.Contains(r.body, `"otp"`) {
		t.Fatalf("stale proof = %d %s, want 403 listing otp", r.status, r.body)
	}
	state, nonce = authorize(t, sys.browser, "/auth/google")
	if r := sys.browser.get("/auth/google/callback?state=" + state + "&code=" + nonce); r.status >= 400 {
		t.Fatalf("silent login = %d %s", r.status, r.body)
	}
	if r := sys.browser.do(http.MethodDelete, "/two-factor", ""); r.status != http.StatusForbidden {
		t.Fatalf("after a silent re-login = %d %s, want still 403", r.status, r.body)
	}
	if r := sys.browser.post("/confirm", `{"method":"otp","code":"`+code(t, secret, 30*time.Second)+`"}`); r.status != http.StatusNoContent {
		t.Fatalf("confirm = %d %s", r.status, r.body)
	}
	if r := sys.browser.do(http.MethodDelete, "/two-factor", ""); r.status != http.StatusNoContent {
		t.Fatalf("after confirming a code = %d %s", r.status, r.body)
	}
}
