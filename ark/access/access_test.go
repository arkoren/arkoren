package access_test

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access"
	"arkoren.dev/ark/access/apitoken"
)

// secretOf extracts the "secret" member of a setup reply.
func secretOf(body string) string {
	_, rest, _ := strings.Cut(body, `"secret":"`)
	secret, _, _ := strings.Cut(rest, `"`)

	return secret
}

// TestRegisterVerifyAndRequireVerified verifies registration logs the user in,
// delivers a verification link and the link verifies the email.
func TestRegisterVerifyAndRequireVerified(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
		})
		register(t, sys.client, "ada@example.com")
		if reply := sys.client.Do(http.MethodGet, "/me", ""); !strings.HasSuffix(reply.Body, "|1|true") {
			t.Fatalf("/me after register = %q", reply.Body)
		}
		message := sys.box.last(t, access.KindVerify)
		if message.Identifier != "ada@example.com" {
			t.Fatalf("verification sent to %q", message.Identifier)
		}
		record, _ := sys.iam.UserProvider().Find(testContext(t), mustSubject(t, sys))
		if record.Claims["email_verified"] != false {
			t.Fatalf("claims before verifying = %v", record.Claims)
		}
		if reply := sys.client.Post("/email/verify", `{"token":"`+message.Token+`"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("verify = %d %s", reply.Status, reply.Body)
		}
		record, _ = sys.iam.UserProvider().Find(testContext(t), mustSubject(t, sys))
		if record.Claims["email_verified"] != true {
			t.Fatalf("claims after verifying = %v", record.Claims)
		}
		if reply := sys.client.Post("/register", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusAccepted {
			t.Fatalf("duplicate register = %d, want 202", reply.Status)
		}
	})
}

// mustSubject returns the subject of ada@example.com.
func mustSubject(t *testing.T, sys *app) string {
	t.Helper()

	subject, _, err := sys.iam.Directory().Lookup(testContext(t), "ada@example.com")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	return subject
}

// TestTwoFactorEnableChallengeAndRecovery verifies the whole TOTP path: enable
// on one device, then log in on another with a code and with a recovery code.
func TestTwoFactorEnableChallengeAndRecovery(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
			iam.TwoFactor(cipher(t), "Example")
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		setup := sys.client.Post("/two-factor/setup", "")
		if setup.Status != http.StatusOK {
			t.Fatalf("setup = %d %s", setup.Status, setup.Body)
		}
		secret := secretOf(setup.Body)
		enable := sys.client.Post("/two-factor", `{"code":"`+code(t, secret, 0)+`"}`)
		if enable.Status != http.StatusOK || !strings.Contains(enable.Body, "recovery_codes") {
			t.Fatalf("enable = %d %s", enable.Status, enable.Body)
		}
		if reply := sys.client.Do(http.MethodGet, "/me", ""); !strings.Contains(reply.Body, "|2|") {
			t.Fatalf("/me after enabling = %q, want level multi", reply.Body)
		}
		recovery := between(enable.Body, `["`, `"`)

		other := sys.client.NewDevice()
		login := other.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
		if login.Status != http.StatusAccepted || !strings.Contains(login.Body, `"otp","recovery"`) {
			t.Fatalf("login = %d %s", login.Status, login.Body)
		}
		if reply := other.Do(http.MethodGet, "/login/pending", ""); reply.Status != http.StatusOK {
			t.Fatalf("pending = %d %s", reply.Status, reply.Body)
		}
		if reply := other.Post("/login/challenge", `{"method":"otp","code":"`+code(t, secret, 30*time.Second)+`"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("challenge = %d %s", reply.Status, reply.Body)
		}
		third := sys.client.NewDevice()
		third.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
		if reply := third.Post("/login/challenge", `{"method":"recovery","code":"`+recovery+`"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("recovery challenge = %d %s", reply.Status, reply.Body)
		}
		fourth := sys.client.NewDevice()
		fourth.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
		if reply := fourth.Post("/login/challenge", `{"method":"recovery","code":"`+recovery+`"}`); reply.Status != http.StatusForbidden {
			t.Fatalf("a spent recovery code = %d, want 403", reply.Status)
		}
	})
}

// between returns the text between two markers.
func between(text, from, to string) string {
	_, rest, _ := strings.Cut(text, from)
	value, _, _ := strings.Cut(rest, to)

	return value
}

// TestPasswordChangeEndsOtherDevices verifies a change keeps the caller, ends
// the other device and a TOTP user must be fresh to change.
func TestPasswordChangeEndsOtherDevices(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		other := sys.client.NewDevice()
		other.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
		change := sys.client.Do(http.MethodPut, "/password", `{"current_password":"correct horse","password":"another horse"}`)
		if change.Status != http.StatusNoContent {
			t.Fatalf("change = %d %s", change.Status, change.Body)
		}
		if reply := sys.client.Do(http.MethodGet, "/me", ""); !strings.HasSuffix(reply.Body, "|true") || strings.HasPrefix(reply.Body, "anonymous") {
			t.Fatalf("caller /me = %q", reply.Body)
		}
		if reply := other.Do(http.MethodGet, "/me", ""); reply.Body != "anonymous" {
			t.Fatalf("other device /me = %q, want its login ended", reply.Body)
		}
	})
}

// TestForgotAndResetFlow verifies the reset link ends every login and the user
// logs in again with the new password.
func TestForgotAndResetFlow(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		if reply := sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`); reply.Status != http.StatusAccepted {
			t.Fatalf("forgot = %d", reply.Status)
		}
		sys.settle(t)
		token := sys.box.last(t, access.KindReset).Token
		reset := sys.client.NewDevice().Post("/password/reset", `{"token":"`+token+`","password":"reset horse ok"}`)
		if reset.Status != http.StatusNoContent {
			t.Fatalf("reset = %d %s", reset.Status, reset.Body)
		}
		if reply := sys.client.Do(http.MethodGet, "/me", ""); reply.Body != "anonymous" {
			t.Fatalf("the old session survived the reset: %q", reply.Body)
		}
		login := sys.client.NewDevice().Post("/login", `{"email":"ada@example.com","password":"reset horse ok"}`)
		if login.Status != http.StatusNoContent {
			t.Fatalf("login after reset = %d", login.Status)
		}
	})
}

// TestTokensExchangeAndBearer verifies a login trades for a device token that
// then authenticates API calls and logs out through its own route.
func TestTokensExchangeAndBearer(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
			iam.Tokens(func(tokens *apitoken.Builder) {
				tokens.Scopes("posts:read")
			})
		})
		device := sys.client.NewDevice()
		registerVerified(t, sys, device, "ada@example.com")
		exchange := device.Post("/device-tokens", `{"device":"Pixel 9"}`)
		if exchange.Status != http.StatusCreated {
			t.Fatalf("exchange = %d %s", exchange.Status, exchange.Body)
		}
		secret := between(exchange.Body, `"secret":"`, `"`)
		header := "Authorization: Bearer " + secret
		me := sys.client.NewDevice().Do(http.MethodGet, "/me", "", header)
		if !strings.HasSuffix(me.Body, "|0|false") {
			t.Fatalf("/me with the token = %q, want level 0 and no session", me.Body)
		}
		if reply := sys.client.NewDevice().Do(http.MethodPut, "/password", `{"password":"another horse"}`, header); reply.Status != http.StatusUnauthorized {
			t.Fatalf("a token changed the password: %d", reply.Status)
		}
		if reply := sys.client.NewDevice().Do(http.MethodDelete, "/device-tokens/current", "", header); reply.Status != http.StatusNoContent {
			t.Fatalf("device logout = %d", reply.Status)
		}
		if reply := sys.client.NewDevice().Do(http.MethodGet, "/me", "", header); reply.Status != http.StatusUnauthorized {
			t.Fatalf("the revoked token still works: %d", reply.Status)
		}
	})
}

// TestResetEndsTokens verifies a password reset ends the user's API tokens.
func TestResetEndsTokens(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
			iam.Tokens()
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		_, secret, err := sys.iam.Issuer().Issue(testContext(t), mustSubject(t, sys), apitoken.Request{Name: "ci"})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		header := "Authorization: Bearer " + secret
		if reply := sys.client.NewDevice().Do(http.MethodGet, "/me", "", header); reply.Status != http.StatusOK {
			t.Fatalf("token before reset = %d", reply.Status)
		}
		sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`)
		sys.settle(t)
		token := sys.box.last(t, access.KindReset).Token
		sys.client.NewDevice().Post("/password/reset", `{"token":"`+token+`","password":"reset horse ok"}`)
		if reply := sys.client.NewDevice().Do(http.MethodGet, "/me", "", header); reply.Status != http.StatusUnauthorized {
			t.Fatalf("token after reset = %d, want 401", reply.Status)
		}
	})
}

// TestSessionsListAndRevoke verifies the session routes.
func TestSessionsListAndRevoke(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		other := sys.client.NewDevice()
		other.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
		list := sys.client.Do(http.MethodGet, "/sessions", "")
		if list.Status != http.StatusOK || strings.Count(list.Body, `"id"`) != 2 || !strings.Contains(list.Body, `"current":true`) {
			t.Fatalf("sessions = %d %s", list.Status, list.Body)
		}
		if reply := sys.client.Do(http.MethodDelete, "/sessions", ""); reply.Status != http.StatusNoContent {
			t.Fatalf("delete others = %d", reply.Status)
		}
		if reply := other.Do(http.MethodGet, "/me", ""); reply.Body != "anonymous" {
			t.Fatalf("the other login survived: %q", reply.Body)
		}
		id := between(sys.client.Do(http.MethodGet, "/sessions", "").Body, `"id":"`, `"`)
		if reply := sys.client.Do(http.MethodDelete, "/sessions/"+id, ""); reply.Status != http.StatusNoContent {
			t.Fatalf("delete own = %d", reply.Status)
		}
		if reply := sys.client.Do(http.MethodGet, "/me", ""); reply.Body != "anonymous" {
			t.Fatalf("the revoked login survived: %q", reply.Body)
		}
	})
}

// TestDeleteAccountRemovesEveryPackagesRows verifies account deletion needs a
// fresh login and removes the user and every package's rows.
func TestDeleteAccountRemovesEveryPackagesRows(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
			iam.TwoFactor(cipher(t), "Example")
			iam.Tokens()
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		subject := mustSubject(t, sys)
		setup := sys.client.Post("/two-factor/setup", "")
		sys.client.Post("/two-factor", `{"code":"`+code(t, secretOf(setup.Body), 0)+`"}`)
		_, secret, _ := sys.iam.Issuer().Issue(ctx, subject, apitoken.Request{Name: "ci"})

		if reply := sys.client.NewDevice().Do(http.MethodDelete, "/account", ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("anonymous delete = %d, want 401", reply.Status)
		}
		if reply := sys.client.Do(http.MethodDelete, "/account", ""); reply.Status != http.StatusNoContent {
			t.Fatalf("delete = %d %s", reply.Status, reply.Body)
		}
		if _, err := sys.iam.Directory().Find(ctx, subject); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("the user survived: %v", err)
		}
		if has, _ := sys.iam.Verifier().Has(ctx, subject); has {
			t.Fatal("the password hash survived")
		}
		if enabled, _ := sys.iam.Factors().Enrolled(ctx, subject); enabled {
			t.Fatal("the authenticator survived")
		}
		if tokens, _ := sys.iam.Issuer().Tokens(ctx, subject); len(tokens) != 0 {
			t.Fatalf("tokens survived: %+v", tokens)
		}
		if reply := sys.client.NewDevice().Do(http.MethodGet, "/me", "", "Authorization: Bearer "+secret); reply.Status != http.StatusUnauthorized {
			t.Fatalf("the deleted user's token works: %d", reply.Status)
		}
	})
}

// TestConfirmWithPassword verifies the shared confirm route accepts the password
// and answers a wrong one with core's 403.
func TestConfirmWithPassword(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		stale := sys.client.NewDevice()
		stale.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
		// A fresh password login is fresh; confirming works the same way.
		if reply := stale.Post("/confirm", `{"method":"pwd","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("confirm = %d %s", reply.Status, reply.Body)
		}
		if reply := stale.Post("/confirm", `{"method":"pwd","password":"nope nope"}`); reply.Status != http.StatusForbidden {
			t.Fatalf("wrong confirm = %d, want 403", reply.Status)
		}
	})
}

// TestTwoInstancesShareOneDatabase verifies two facades with different
// instances keep their users apart.
func TestTwoInstancesShareOneDatabase(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		first := newApp(t, db, instance+"_a", func(iam *access.Builder) {
			iam.Password(hasher())
			iam.TwoFactor(cipher(t), "Example")
			iam.Tokens()
		})
		second := newApp(t, db, instance+"_b", func(iam *access.Builder) {
			iam.Password(hasher())
			iam.TwoFactor(cipher(t), "Example")
			iam.Tokens()
		})
		register(t, first.client, "ada@example.com")
		register(t, second.client, "ada@example.com")
		if reply := second.client.NewDevice().Post("/login", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("login on the second instance = %d", reply.Status)
		}
	})
}

// TestNewRejectsBadConfig verifies the configuration errors.
func TestNewRejectsBadConfig(t *testing.T) {
	t.Parallel()

	if _, err := access.New(nil, nil); !errors.Is(err, access.ErrInvalidConfig) {
		t.Errorf("missing dependencies error = %v", err)
	}
	if _, err := newFacade(t, func(iam *access.Builder) { iam.Instance("Bad-Name") }); !errors.Is(err, access.ErrInvalidConfig) {
		t.Errorf("invalid instance error = %v", err)
	}
	if _, err := newFacade(t, func(iam *access.Builder) { iam.SessionLifetime(-1) }); !errors.Is(err, access.ErrInvalidConfig) {
		t.Errorf("negative lifetime error = %v", err)
	}
	if _, err := newFacade(t, func(iam *access.Builder) { iam.TwoFactor(cipher(t), "bad:issuer") }); !errors.Is(err, access.ErrInvalidConfig) {
		t.Errorf("invalid issuer error = %v", err)
	}
}

// TestMustAcceptPanicsForUnregisteredMethod verifies the startup check.
func TestMustAcceptPanicsForUnregisteredMethod(t *testing.T) {
	t.Parallel()

	iam, err := newFacade(t, func(iam *access.Builder) {
		iam.Password(hasher())
		iam.TwoFactor(cipher(t), "Example")
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	iam.MustAccept(auth.MethodPassword, auth.MethodOTP, "recovery")
	if !panics(func() { iam.MustAccept("passkey") }) {
		t.Fatal("MustAccept accepted an unregistered method")
	}
}

// TestMigrationsOnlyForEnabledFeatures verifies Migrations lists the groups of
// the enabled features and Ping names the migrations when they have not run.
func TestMigrationsOnlyForEnabledFeatures(t *testing.T) {
	t.Parallel()

	groups := func(iam *access.Access) string {
		seen := []string{}
		for _, definition := range iam.Migrations() {
			group := strings.TrimPrefix(definition.Group(), "arkoren.dev:ark/access:")
			if len(seen) == 0 || seen[len(seen)-1] != group {
				seen = append(seen, group)
			}
		}

		return strings.Join(seen, ",")
	}
	all, err := newFacade(t, func(iam *access.Builder) {
		iam.Password(hasher())
		iam.TwoFactor(cipher(t), "Example")
		iam.Tokens()
		iam.Provider(fakeProvider{name: "fake"})
		iam.Instance("tenant_a")
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := groups(all), "account:tenant_a,password:tenant_a,twofactor:tenant_a,apitoken:tenant_a,federation:tenant_a"; got != want {
		t.Errorf("groups = %s, want %s", got, want)
	}
	passwordOnly, _ := newFacade(t, func(iam *access.Builder) { iam.Password(hasher()) })
	if got, want := groups(passwordOnly), "account,password"; got != want {
		t.Errorf("groups = %s, want %s", got, want)
	}
	if err := passwordOnly.Ping(context.Background()); !errors.Is(err, access.ErrNotMigrated) {
		t.Errorf("Ping before migrating error = %v, want ErrNotMigrated", err)
	}
}

// TestPackagesDoNotImportEachOther verifies the public packages of the module
// import only core, foundation, internal helpers and the standard library, and
// never one another or the root.
func TestPackagesDoNotImportEachOther(t *testing.T) {
	t.Parallel()

	output, err := exec.Command("go", "list", "-f", "{{.ImportPath}}: {{join .Imports \" \"}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	const module = "arkoren.dev/ark/access"
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		path, imports, _ := strings.Cut(line, ": ")
		if strings.Contains(path, "/internal/") {
			continue
		}
		for imported := range strings.FieldsSeq(imports) {
			switch {
			case strings.HasPrefix(imported, "arkoren.dev/ark/") && !strings.HasPrefix(imported, module):
				t.Errorf("%s imports another ark: %s", path, imported)
			case strings.HasPrefix(imported, module) && path != module && !strings.Contains(imported, "/internal/"):
				t.Errorf("%s imports a sibling package: %s", path, imported)
			}
		}
	}
}

// TestRegisterTakenAddressNotifiesTheOwner verifies registering a taken address
// answers like a new one and sends its owner a registration notice whose token
// resets the password, and that the notice is the only trace.
func TestRegisterTakenAddressNotifiesTheOwner(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) { iam.Password(hasher()) })
		fresh := sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"correct horse"}`)
		taken := sys.client.NewDevice().Post("/register", `{"email":"Ada@example.com","password":"another horse"}`)
		if fresh.Status != http.StatusAccepted || taken.Status != fresh.Status || taken.Body != fresh.Body {
			t.Fatalf("new = %d %q, taken = %d %q, want the same 202", fresh.Status, fresh.Body, taken.Status, taken.Body)
		}
		sys.settle(t)
		notice := sys.box.last(t, access.KindRegistration)
		if notice.Identifier != "ada@example.com" || notice.Token == "" {
			t.Fatalf("notice = %+v, want a token for the stored address", notice)
		}
		if reply := sys.client.NewDevice().Post("/password/reset", `{"token":"`+notice.Token+`","password":"reset horse ok"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("reset with the notice's token = %d %s", reply.Status, reply.Body)
		}
	})
}
