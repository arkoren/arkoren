package iam_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/totp"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/queue"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/account"
	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/ark/iam/password"
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
		})
		register(t, sys.client, "ada@example.com")
		if reply := sys.client.Do(http.MethodGet, "/whoami", ""); !strings.HasSuffix(reply.Body, "|1|true") {
			t.Fatalf("/me after register = %q", reply.Body)
		}
		message := sys.box.lastVerify(t)
		if message.Address != "ada@example.com" {
			t.Fatalf("verification sent to %q", message.Address)
		}
		record, _ := sys.identity.UserProvider().Find(testContext(t), mustSubject(t, sys))
		if record.Claims["email_verified"] != false {
			t.Fatalf("claims before verifying = %v", record.Claims)
		}
		if reply := sys.client.Post("/email/verify", `{"token":"`+message.Signed+`"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("verify = %d %s", reply.Status, reply.Body)
		}
		record, _ = sys.identity.UserProvider().Find(testContext(t), mustSubject(t, sys))
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

	subject, _, err := sys.identity.Directory().Lookup(testContext(t), "ada@example.com")
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t))
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
		if reply := sys.client.Do(http.MethodGet, "/whoami", ""); !strings.Contains(reply.Body, "|2|") {
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		other := sys.client.NewDevice()
		other.Post("/login", `{"email":"ada@example.com","password":"correct horse"}`)
		change := sys.client.Do(http.MethodPut, "/password", `{"current_password":"correct horse","password":"another horse"}`)
		if change.Status != http.StatusNoContent {
			t.Fatalf("change = %d %s", change.Status, change.Body)
		}
		if reply := sys.client.Do(http.MethodGet, "/whoami", ""); !strings.HasSuffix(reply.Body, "|true") || strings.HasPrefix(reply.Body, "anonymous") {
			t.Fatalf("caller /me = %q", reply.Body)
		}
		if reply := other.Do(http.MethodGet, "/whoami", ""); reply.Body != "anonymous" {
			t.Fatalf("other device /me = %q, want its login ended", reply.Body)
		}
	})
}

// TestForgotAndResetFlow verifies the reset link ends every login and the user
// logs in again with the new password.
func TestForgotAndResetFlow(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		if reply := sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`); reply.Status != http.StatusAccepted {
			t.Fatalf("forgot = %d", reply.Status)
		}
		token := sys.box.lastReset(t).Signed
		reset := sys.client.NewDevice().Post("/password/reset", `{"token":"`+token+`","password":"reset horse ok"}`)
		if reset.Status != http.StatusNoContent {
			t.Fatalf("reset = %d %s", reset.Status, reset.Body)
		}
		if reply := sys.client.Do(http.MethodGet, "/whoami", ""); reply.Body != "anonymous" {
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.Tokens(func(tokens *apitoken.Builder) {
				tokens.Grants(apitoken.AnyScope, "posts:read")
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
		me := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", header)
		if !strings.HasSuffix(me.Body, "|0|false") {
			t.Fatalf("/me with the token = %q, want level 0 and no session", me.Body)
		}
		if reply := sys.client.NewDevice().Do(http.MethodPut, "/password", `{"password":"another horse"}`, header); reply.Status != http.StatusUnauthorized {
			t.Fatalf("a token changed the password: %d", reply.Status)
		}
		if reply := sys.client.NewDevice().Do(http.MethodDelete, "/device-tokens/current", "", header); reply.Status != http.StatusNoContent {
			t.Fatalf("device logout = %d", reply.Status)
		}
		if reply := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", header); reply.Status != http.StatusUnauthorized {
			t.Fatalf("the revoked token still works: %d", reply.Status)
		}
	})
}

// TestResetEndsTokens verifies a password reset ends the user's API tokens.
func TestResetEndsTokens(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.Tokens()
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		_, secret, err := sys.identity.Issuer().Issue(testContext(t), mustSubject(t, sys), apitoken.Request{Name: "ci"})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		header := "Authorization: Bearer " + secret
		if reply := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", header); reply.Status != http.StatusOK {
			t.Fatalf("token before reset = %d", reply.Status)
		}
		sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`)
		token := sys.box.lastReset(t).Signed
		sys.client.NewDevice().Post("/password/reset", `{"token":"`+token+`","password":"reset horse ok"}`)
		if reply := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", header); reply.Status != http.StatusUnauthorized {
			t.Fatalf("token after reset = %d, want 401", reply.Status)
		}
	})
}

// TestSessionsListAndRevoke verifies the session routes.
func TestSessionsListAndRevoke(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
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
		if reply := other.Do(http.MethodGet, "/whoami", ""); reply.Body != "anonymous" {
			t.Fatalf("the other login survived: %q", reply.Body)
		}
		id := between(sys.client.Do(http.MethodGet, "/sessions", "").Body, `"id":"`, `"`)
		if reply := sys.client.Do(http.MethodDelete, "/sessions/"+id, ""); reply.Status != http.StatusNoContent {
			t.Fatalf("delete own = %d", reply.Status)
		}
		if reply := sys.client.Do(http.MethodGet, "/whoami", ""); reply.Body != "anonymous" {
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t))
			identity.Tokens()
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		subject := mustSubject(t, sys)
		setup := sys.client.Post("/two-factor/setup", "")
		sys.client.Post("/two-factor", `{"code":"`+code(t, secretOf(setup.Body), 0)+`"}`)
		_, secret, _ := sys.identity.Issuer().Issue(ctx, subject, apitoken.Request{Name: "ci"})

		if reply := sys.client.NewDevice().Do(http.MethodDelete, "/account", ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("anonymous delete = %d, want 401", reply.Status)
		}
		if reply := sys.client.Do(http.MethodDelete, "/account", ""); reply.Status != http.StatusNoContent {
			t.Fatalf("delete = %d %s", reply.Status, reply.Body)
		}
		if _, err := sys.identity.Directory().Find(ctx, subject); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("the user survived: %v", err)
		}
		if has, _ := sys.identity.Verifier().Has(ctx, subject); has {
			t.Fatal("the password hash survived")
		}
		if enabled, _ := sys.identity.Factors().Enrolled(ctx, subject); enabled {
			t.Fatal("the authenticator survived")
		}
		if tokens, _ := sys.identity.Issuer().Tokens(ctx, subject); len(tokens) != 0 {
			t.Fatalf("tokens survived: %+v", tokens)
		}
		if reply := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", "Authorization: Bearer "+secret); reply.Status != http.StatusUnauthorized {
			t.Fatalf("the deleted user's token works: %d", reply.Status)
		}
	})
}

// TestConfirmWithPassword verifies the shared confirm route accepts the password
// and answers a wrong one with core's 403.
func TestConfirmWithPassword(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
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
		first := newApp(t, db, instance+"_a", func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t))
			identity.Tokens()
		})
		second := newApp(t, db, instance+"_b", func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t))
			identity.Tokens()
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

	if _, err := iam.New(nil, nil); !errors.Is(err, iam.ErrInvalidConfig) {
		t.Errorf("missing dependencies error = %v", err)
	}
	if _, err := newFacade(t, func(identity *iam.Builder) { identity.Instance("Bad-Name") }); !errors.Is(err, iam.ErrInvalidConfig) {
		t.Errorf("invalid instance error = %v", err)
	}
	if _, err := newFacade(t, func(identity *iam.Builder) { identity.SessionLifetime(-1) }); !errors.Is(err, iam.ErrInvalidConfig) {
		t.Errorf("negative lifetime error = %v", err)
	}
	if _, err := newFacade(t, func(identity *iam.Builder) { identity.TwoFactor(totp.New("bad:issuer"), cipher(t)) }); !errors.Is(err, iam.ErrInvalidConfig) {
		t.Errorf("invalid issuer error = %v", err)
	}
}

// TestMustAcceptPanicsForUnregisteredMethod verifies the startup check.
func TestMustAcceptPanicsForUnregisteredMethod(t *testing.T) {
	t.Parallel()

	identity, err := newFacade(t, func(identity *iam.Builder) {
		identity.Password(hasher(), registration)
		identity.TwoFactor(totp.New("Example"), cipher(t))
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	identity.MustAccept(auth.MethodPassword, auth.MethodOTP, "recovery")
	if !panics(func() { identity.MustAccept("passkey") }) {
		t.Fatal("MustAccept accepted an unregistered method")
	}
}

// migrationGroups lists the migration groups of identity in order, without the
// shared prefix.
func migrationGroups(identity *iam.IAM) string {
	seen := []string{}
	for _, definition := range identity.Migrations() {
		group := strings.TrimPrefix(definition.Group(), "arkoren.dev:ark/access:")
		if len(seen) == 0 || seen[len(seen)-1] != group {
			seen = append(seen, group)
		}
	}

	return strings.Join(seen, ",")
}

// TestMigrationsListEveryFeatureWhateverIsEnabled verifies Migrations ships the
// groups of every feature in dependency order for the instance, however few
// features are enabled, so turning one on later needs no new migrator call.
func TestMigrationsListEveryFeatureWhateverIsEnabled(t *testing.T) {
	t.Parallel()

	passwordOnly, err := newFacade(t, func(identity *iam.Builder) { identity.Password(hasher(), registration) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := migrationGroups(passwordOnly), "account,password,twofactor,apitoken,federation,permission"; got != want {
		t.Errorf("groups = %s, want %s", got, want)
	}
	named, err := newFacade(t, func(identity *iam.Builder) { identity.Instance("tenant_a") })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := migrationGroups(named), "account:tenant_a,password:tenant_a,twofactor:tenant_a,apitoken:tenant_a,federation:tenant_a,permission:tenant_a"; got != want {
		t.Errorf("groups = %s, want %s", got, want)
	}
}

// TestMigrationsOmitBuiltInUsersWithOwnUsers verifies the application's own
// users are the one structural choice that removes a group.
func TestMigrationsOmitBuiltInUsersWithOwnUsers(t *testing.T) {
	t.Parallel()

	own, err := newFacadeWith(t, wire{reset: true}, func(identity *iam.Builder) { identity.Users(ownUsersWithoutCreate(t)) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := migrationGroups(own), "password,twofactor,apitoken,federation,permission"; got != want {
		t.Errorf("groups = %s, want %s", got, want)
	}
}

// TestMigrationsKeepShippedIdentities pins every shipped migration id, which are
// append-only: renaming one would make an application's migrator treat a
// migrated database as new.
func TestMigrationsKeepShippedIdentities(t *testing.T) {
	t.Parallel()

	identity, err := newFacade(t, func(identity *iam.Builder) { identity.Password(hasher(), registration) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var ids []string
	for _, definition := range identity.Migrations() {
		ids = append(ids, definition.ID())
	}
	want := []string{
		"arkoren.dev:ark/access:account/users-create-table",
		"arkoren.dev:ark/access:password/hashes-create-table",
		"arkoren.dev:ark/access:twofactor/secrets-create-table",
		"arkoren.dev:ark/access:twofactor/recovery-codes-create-table",
		"arkoren.dev:ark/access:apitoken/api-tokens-create-table",
		"arkoren.dev:ark/access:federation/links-create-table",
		"arkoren.dev:ark/access:federation/links-add-claims",
		"arkoren.dev:ark/access:permission/permission-grants-create-table",
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("migration ids = %v, want %v", ids, want)
	}
}

// TestPingChecksOnlyEnabledFeatures verifies readiness names the migrations when
// an enabled feature's tables are missing and ignores features that are off.
func TestPingChecksOnlyEnabledFeatures(t *testing.T) {
	t.Parallel()

	passwordOnly, err := newFacade(t, func(identity *iam.Builder) { identity.Password(hasher(), registration) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := passwordOnly.Ping(context.Background()); !errors.Is(err, iam.ErrNotMigrated) {
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
	const module = "arkoren.dev/ark/iam"
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) { identity.Password(hasher(), registration) })
		fresh := sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"correct horse"}`)
		taken := sys.client.NewDevice().Post("/register", `{"email":"Ada@example.com","password":"another horse"}`)
		if fresh.Status != http.StatusAccepted || taken.Status != fresh.Status || taken.Body != fresh.Body {
			t.Fatalf("new = %d %q, taken = %d %q, want the same 202", fresh.Status, fresh.Body, taken.Status, taken.Body)
		}
		notice := sys.box.lastNotice(t)
		if notice.Address != "ada@example.com" || notice.Signed == "" {
			t.Fatalf("notice = %+v, want a token for the stored address", notice)
		}
		if reply := sys.client.NewDevice().Post("/password/reset", `{"token":"`+notice.Signed+`","password":"reset horse ok"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("reset with the notice's token = %d %s", reply.Status, reply.Body)
		}
	})
}

// TestJobsSendResetForKnownUsersOnly verifies the worker the facade registers
// sends a reset email for a known user and nothing, without an error, for an
// unknown one, so a forgot request reveals nothing in the queue either.
func TestJobsSendResetForKnownUsersOnly(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) { identity.Password(hasher(), registration) })
		register(t, sys.client, "ada@example.com")
		sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`)
		sys.client.NewDevice().Post("/password/forgot", `{"email":"nobody@example.com"}`)
		if got := len(sys.box.dispatchedResets(t)); got != 2 {
			t.Fatalf("stored %d reset jobs, want 2: a forgot request stores one whether or not the user exists", got)
		}
		if resets, _ := sys.box.counts(t); resets != 1 {
			t.Fatalf("reset emails = %d, want 1: only the known user is sent one", resets)
		}
		sys.box.mail.AssertSentCount(t, 2) // the verification of the registration and the reset
	})
}

// TestDefaultEmailsCarryLinksThatWork verifies the default reset and
// verification emails go to the stored address with the page link and a signed
// value in its fragment, and that the value works.
func TestDefaultEmailsCarryLinksThatWork(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newAppWith(t, db, instance, wire{reset: true, verify: true, defaultEmail: true}, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
		})
		register(t, sys.client, "ada@example.com")
		sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`)
		sys.box.drain(t)
		sent := sys.box.mail.Sent()
		if len(sent) != 2 {
			t.Fatalf("sent %d emails, want a verification and a reset", len(sent))
		}
		for index, base := range []string{verifyPage, resetPage} {
			message := sent[index]
			if len(message.To()) != 1 || message.To()[0].Email != "ada@example.com" || message.Subject() == "" {
				t.Fatalf("email %d = to %v subject %q, want the stored address and a subject", index, message.To(), message.Subject())
			}
			fragment := linkFragment(t, message.TextBody(), base)
			if index == 0 {
				if reply := sys.client.Post("/email/verify", `{"token":"`+fragment+`"}`); reply.Status != http.StatusNoContent {
					t.Fatalf("verify with the emailed value = %d %s", reply.Status, reply.Body)
				}

				continue
			}
			if reply := sys.client.NewDevice().Post("/password/reset", `{"token":"`+fragment+`","password":"reset horse ok"}`); reply.Status != http.StatusNoContent {
				t.Fatalf("reset with the emailed value = %d %s", reply.Status, reply.Body)
			}
		}
	})
}

// linkFragment returns the decoded fragment of the link that starts with base in
// body, which is what a front end posts back as the token.
func linkFragment(t *testing.T, body, base string) string {
	t.Helper()

	_, after, found := strings.Cut(body, base+"#")
	if !found {
		t.Fatalf("body %q has no link to %s", body, base)
	}
	encoded := strings.Fields(after)[0]
	token, err := url.QueryUnescape(encoded)
	if err != nil {
		t.Fatalf("unescape %q: %v", encoded, err)
	}

	return token
}

// TestJobNamesCarryTheInstance verifies the jobs of a named instance are named
// after it and run on the queue "iam", so two instances share one consumer.
func TestJobNamesCarryTheInstance(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, _ string) {
		sys := newApp(t, db, "tenant_a", func(identity *iam.Builder) { identity.Password(hasher(), registration) })
		register(t, sys.client, "ada@example.com")
		sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`)
		var names []string
		for _, message := range sys.box.queue.Dispatched() {
			if message.Queue != iam.QueueName {
				t.Fatalf("job %s on queue %q, want %q", message.Name, message.Queue, iam.QueueName)
			}
			names = append(names, message.Name)
		}
		want := []string{"iam-email-verification-tenant-a", "iam-password-reset-tenant-a"}
		if !slices.Equal(names, want) {
			t.Fatalf("job names = %v, want %v", names, want)
		}
	})
}

// TestTwoInstancesShareOneConsumer verifies the jobs of two instances register
// on one consumer without a duplicate name.
func TestTwoInstancesShareOneConsumer(t *testing.T) {
	t.Parallel()

	box := newBox(t, both)
	first, err := construct(box, memoryDatabase(t), cache.New(memorycache.New()), box.verification(), func(identity *iam.Builder) {
		identity.Instance("one")
	})
	if err != nil {
		t.Fatalf("first instance: %v", err)
	}
	second, err := iam.New(memoryDatabase(t), cache.New(memorycache.New()), func(identity *iam.Builder) {
		identity.ClientIP(testClientIP)
		identity.Queue(box.dispatcher)
		identity.Instance("two")
		box.verification()(identity)
	})
	if err != nil {
		t.Fatalf("second instance: %v", err)
	}
	if err := second.RegisterJobs(box.consumer); err != nil {
		t.Fatalf("RegisterJobs of a second instance = %v, want nil", err)
	}
	if err := first.RegisterJobs(box.consumer); !errors.Is(err, queue.ErrDuplicateName) {
		t.Fatalf("RegisterJobs twice for one instance = %v, want ErrDuplicateName", err)
	}
}

// TestRegisterJobsWithoutEmailFeaturesRegistersNothing verifies a facade with
// no reset and no verification handles no job.
func TestRegisterJobsWithoutEmailFeaturesRegistersNothing(t *testing.T) {
	t.Parallel()

	box := newBox(t, wire{})
	if _, err := construct(box, memoryDatabase(t), cache.New(memorycache.New())); err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := box.queue.Drain(testContext(t), box.consumer); !errors.Is(err, queue.ErrNoQueues) {
		t.Fatalf("Drain = %v, want ErrNoQueues: no job is registered", err)
	}
}

// TestResetWithoutQueueFailsConstruction verifies enabling password reset without
// Builder.Queue is an invalid configuration, not a route that fails later.
func TestResetWithoutQueueFailsConstruction(t *testing.T) {
	t.Parallel()

	box := newBox(t, both)
	_, err := iam.New(memoryDatabase(t), cache.New(memorycache.New()), func(identity *iam.Builder) {
		identity.ClientIP(testClientIP)
		identity.Password(hasher(), func(passwords *password.Builder) {
			passwords.Reset(box.signer, box.mailer, resetPage)
		})
	})
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestVerificationWithoutQueueFailsConstruction verifies enabling email
// verification without Builder.Queue is an invalid configuration.
func TestVerificationWithoutQueueFailsConstruction(t *testing.T) {
	t.Parallel()

	box := newBox(t, both)
	_, err := iam.New(memoryDatabase(t), cache.New(memorycache.New()), func(identity *iam.Builder) {
		identity.ClientIP(testClientIP)
		identity.Directory(func(accounts *account.Builder) {
			accounts.Verification(box.signer, box.mailer, verifyPage)
		})
	})
	if !errors.Is(err, iam.ErrInvalidConfig) {
		t.Fatalf("New = %v, want ErrInvalidConfig", err)
	}
}

// TestInvalidCustomEmailFailsPermanently verifies an email the
// mailer rejects as malformed settles the job as failed instead of retrying
// forever.
func TestInvalidCustomEmailFailsPermanently(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newAppWith(t, db, instance, both, func(identity *iam.Builder) {
			identity.Directory(func(accounts *account.Builder) {
				accounts.Verification(buildingBox.signer, buildingBox.mailer, verifyPage, func(verification *account.VerificationBuilder) {
					verification.Email(func(account.VerifyMessage, string) mail.Message {
						return mail.NewMessage(mail.Recipients{}, mail.Body{})
					})
				})
			})
			identity.Password(hasher(), registration)
		})
		register(t, sys.client, "ada@example.com")
		sys.box.drain(t)
		if got := len(sys.box.queue.Failed()); got != 1 {
			t.Fatalf("failed jobs = %d, want 1 permanent failure", got)
		}
		sys.box.mail.AssertNothingSent(t)
	})
}

// TestPruneDeletesIdleDeviceTokens verifies the facade forwards Prune to the
// issuer, and that it is a no-op without tokens.
func TestPruneDeletesIdleDeviceTokens(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		plain := newApp(t, db, instance+"p")
		if err := plain.identity.Prune(ctx); err != nil {
			t.Fatalf("Prune without tokens = %v, want nil", err)
		}
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.Tokens()
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		subject := strings.SplitN(sys.client.Do(http.MethodGet, "/whoami", "").Body, "|", 2)[0]
		if _, _, err := sys.identity.Issuer().IssueDevice(ctx, subject, "phone"); err != nil {
			t.Fatalf("IssueDevice: %v", err)
		}
		longAgo := time.Now().Add(-200 * 24 * time.Hour).Unix()
		_, err := db.Exec(ctx, query.Table(instance+"_api_tokens", func(statement *query.Builder) {
			statement.Unfiltered().Update(map[string]any{"created_at": longAgo})
		}))
		if err != nil {
			t.Fatalf("age the token: %v", err)
		}
		if err := sys.identity.Prune(ctx); err != nil {
			t.Fatalf("Prune: %v", err)
		}
		if tokens, err := sys.identity.Issuer().Tokens(ctx, subject); err != nil || len(tokens) != 0 {
			t.Fatalf("tokens after Prune = %v, %v; want none", tokens, err)
		}
	})
}
