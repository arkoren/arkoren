package password_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/password"
)

// TestAuthenticateFailuresLookAlike verifies unknown, wrong, hashless and
// disabled users all return the one error.
func TestAuthenticateFailuresLookAlike(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		subject, _ := sys.users.Create(ctx, "ada@example.com")
		_, _ = sys.users.Create(ctx, "bob@example.com")
		if err := sys.verifier.Set(ctx, subject, "correct horse"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		for name, attempt := range map[string][2]string{
			"unknown":  {"nobody@example.com", "correct horse"},
			"wrong":    {"ada@example.com", "wrong horse!"},
			"hashless": {"bob@example.com", "correct horse"},
		} {
			if _, err := sys.verifier.Authenticate(ctx, attempt[0], attempt[1]); !errors.Is(err, password.ErrInvalidCredentials) {
				t.Errorf("%s error = %v, want ErrInvalidCredentials", name, err)
			}
		}
		if got, err := sys.verifier.Authenticate(ctx, "ada@example.com", "correct horse"); err != nil || got != subject {
			t.Fatalf("Authenticate = %q, %v", got, err)
		}
		if err := sys.users.driver.Disable(subject); err != nil {
			t.Fatalf("Disable: %v", err)
		}
		if _, err := sys.verifier.Authenticate(ctx, "ada@example.com", "correct horse"); !errors.Is(err, password.ErrInvalidCredentials) {
			t.Errorf("disabled error = %v, want ErrInvalidCredentials", err)
		}
	})
}

// TestSetEnforcesPolicy verifies the default policy and that a stored hash
// round-trips.
func TestSetEnforcesPolicy(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		for _, weak := range []string{"", "short", strings.Repeat("x", 73)} {
			if err := sys.verifier.Set(ctx, "1", weak); !errors.Is(err, password.ErrInvalidPassword) {
				t.Errorf("Set(%d bytes) error = %v, want ErrInvalidPassword", len(weak), err)
			}
		}
		if err := sys.verifier.Set(ctx, "1", "first password"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := sys.verifier.Set(ctx, "1", "second password"); err != nil {
			t.Fatalf("second Set: %v", err)
		}
		if err := sys.verifier.Check(ctx, "1", "first password"); !errors.Is(err, password.ErrInvalidCredentials) {
			t.Errorf("old password still works: %v", err)
		}
		if err := sys.verifier.Check(ctx, "1", "second password"); err != nil {
			t.Errorf("new password rejected: %v", err)
		}
	})
}

// TestLoginLogsIn verifies the 204 login and the 422 for a wrong password.
func TestLoginLogsIn(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		device := sys.client.NewDevice()
		if reply := loginAda(device, "wrong horse!"); reply.Status != http.StatusUnprocessableEntity {
			t.Fatalf("wrong password = %d %s", reply.Status, reply.Body)
		}
		if reply := loginAda(device, "correct horse"); reply.Status != http.StatusNoContent {
			t.Fatalf("login = %d %s", reply.Status, reply.Body)
		}
		if reply := device.Do(http.MethodGet, "/me", ""); reply.Body != "1" {
			t.Fatalf("/me = %d %q", reply.Status, reply.Body)
		}
	})
}

// TestLoginThrottlesPerIdentifier verifies the eleventh attempt is 429 with
// Retry-After and that the identifier's counter clears on success.
func TestLoginThrottlesPerIdentifier(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		device := sys.client.NewDevice()
		for range 10 {
			if reply := loginAda(device, "wrong horse!"); reply.Status != http.StatusUnprocessableEntity {
				t.Fatalf("failed attempt = %d", reply.Status)
			}
		}
		reply := loginAda(device, "correct horse")
		if reply.Status != http.StatusTooManyRequests || reply.Header.Get("Retry-After") == "" {
			t.Fatalf("throttled login = %d, Retry-After %q", reply.Status, reply.Header.Get("Retry-After"))
		}
	})
}

// TestLoginSuccessClearsIdentifierCounter verifies successful logins never
// exhaust the limit.
func TestLoginSuccessClearsIdentifierCounter(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		device := sys.client.NewDevice()
		for range 15 {
			if reply := loginAda(device, "correct horse"); reply.Status != http.StatusNoContent {
				t.Fatalf("login = %d %s", reply.Status, reply.Body)
			}
		}
	})
}

// TestLoginPendingReturns202 verifies a user with a second factor gets the
// challenge body and finishes the login through the core route.
func TestLoginPendingReturns202(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		enrolled := map[string]bool{}
		sys := newSystem(t, db, instance, secondFactor(enrolled))
		sys.registerAda(t, sys.client)
		enrolled["1"] = true
		device := sys.client.NewDevice()
		reply := loginAda(device, "correct horse")
		if reply.Status != http.StatusAccepted || !strings.Contains(reply.Body, `"otp"`) {
			t.Fatalf("login = %d %s", reply.Status, reply.Body)
		}
		if reply := device.Do(http.MethodGet, "/me", ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("pending login /me = %d", reply.Status)
		}
		if reply := device.Post("/login/challenge", `{"method":"otp","code":"ok"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("challenge = %d %s", reply.Status, reply.Body)
		}
		if reply := device.Do(http.MethodGet, "/me", ""); reply.Body != "1" {
			t.Fatalf("/me = %d %q", reply.Status, reply.Body)
		}
	})
}

// TestRegisterDuplicatePassesCreateError verifies the user store's error
// reaches the client and no second hash is stored.
func TestRegisterDuplicatePassesCreateError(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		reply := sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"another horse"}`)
		if reply.Status != http.StatusConflict {
			t.Fatalf("duplicate register = %d %s", reply.Status, reply.Body)
		}
		if reply := sys.client.NewDevice().Post("/register", `{"email":"bob@example.com","password":"x"}`); reply.Status != http.StatusUnprocessableEntity {
			t.Fatalf("weak register = %d %s", reply.Status, reply.Body)
		}
		if _, err := sys.users.Lookup(testContext(t), "bob@example.com"); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("a rejected password still created a user: %v", err)
		}
	})
}

// TestChangePasswordKeepsCallerEndsOthers verifies a change keeps the session
// that made it, ends the other device and requires the current password.
func TestChangePasswordKeepsCallerEndsOthers(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		other := sys.client.NewDevice()
		if reply := loginAda(other, "correct horse"); reply.Status != http.StatusNoContent {
			t.Fatalf("second login = %d", reply.Status)
		}
		wrong := sys.client.Do(http.MethodPut, "/password", `{"current_password":"nope nope","password":"brand new horse"}`)
		if wrong.Status != http.StatusUnprocessableEntity {
			t.Fatalf("wrong current password = %d %s", wrong.Status, wrong.Body)
		}
		change := sys.client.Do(http.MethodPut, "/password", `{"current_password":"correct horse","password":"brand new horse"}`)
		if change.Status != http.StatusNoContent {
			t.Fatalf("change = %d %s", change.Status, change.Body)
		}
		if reply := sys.client.Do(http.MethodGet, "/me", ""); reply.Body != "1" {
			t.Fatalf("caller /me = %d", reply.Status)
		}
		if reply := other.Do(http.MethodGet, "/me", ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("other device /me = %d, want 401", reply.Status)
		}
		if len(sys.users.rotated) != 0 {
			t.Fatalf("a change rotated the user: %v", sys.users.rotated)
		}
		if reply := loginAda(sys.client.NewDevice(), "brand new horse"); reply.Status != http.StatusNoContent {
			t.Fatalf("login with the new password = %d", reply.Status)
		}
	})
}

// TestChangePasswordLeavesLoginThrottleAlone verifies a password change does
// not refund attempts it never counted against the client address.
func TestChangePasswordLeavesLoginThrottleAlone(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		other := sys.client.NewDevice()
		for range 3 {
			loginAda(other, "wrong horse!")
		}
		key := cache.NewKey[int64]("access:" + instance + ":password:ip:127.0.0.1")
		before, err := sys.store.Get(ctx, key)
		if err != nil || before != 3 {
			t.Fatalf("client counter = %d, %v; want 3", before, err)
		}
		change := sys.client.Do(http.MethodPut, "/password", `{"current_password":"correct horse","password":"brand new horse"}`)
		if change.Status != http.StatusNoContent {
			t.Fatalf("change = %d %s", change.Status, change.Body)
		}
		if after, _ := sys.store.Get(ctx, key); after != before {
			t.Fatalf("client counter after a change = %d, want %d", after, before)
		}
	})
}

// TestChangePasswordFirstPasswordWithoutCurrent verifies a user without a
// password sets the first one without proving a current one.
func TestChangePasswordFirstPasswordWithoutCurrent(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		subject, _ := sys.users.Create(ctx, "ada@example.com")
		if reply := sys.client.Post("/dev-login/"+subject, ""); reply.Status != http.StatusNoContent {
			t.Fatalf("dev login = %d %s", reply.Status, reply.Body)
		}
		change := sys.client.Do(http.MethodPut, "/password", `{"password":"first horse ok"}`)
		if change.Status != http.StatusNoContent {
			t.Fatalf("first password = %d %s", change.Status, change.Body)
		}
		if reply := loginAda(sys.client.NewDevice(), "first horse ok"); reply.Status != http.StatusNoContent {
			t.Fatalf("login with the first password = %d", reply.Status)
		}
	})
}

// TestChangePasswordRequiresSession verifies an anonymous request is rejected.
func TestChangePasswordRequiresSession(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		reply := sys.client.Do(http.MethodPut, "/password", `{"password":"brand new horse"}`)
		if reply.Status != http.StatusUnauthorized {
			t.Fatalf("anonymous change = %d", reply.Status)
		}
	})
}

// TestResetFlowEndsEveryLogin verifies forgot, reset, the rotation and that
// the token is single use.
func TestResetFlowEndsEveryLogin(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		if reply := sys.client.Post("/password/forgot", `{"email":"ada@example.com"}`); reply.Status != http.StatusAccepted {
			t.Fatalf("forgot = %d", reply.Status)
		}
		if len(*sys.sent) != 1 {
			t.Fatalf("sent %d messages, want 1", len(*sys.sent))
		}
		token := (*sys.sent)[0].Token
		if reply := sys.client.Post("/password/reset", `{"token":"`+token+`","password":"short"}`); reply.Status != http.StatusUnprocessableEntity {
			t.Fatalf("weak reset = %d", reply.Status)
		}
		reset := sys.client.NewDevice().Post("/password/reset", `{"token":"`+token+`","password":"reset horse ok"}`)
		if reset.Status != http.StatusNoContent {
			t.Fatalf("reset = %d %s", reset.Status, reset.Body)
		}
		if len(sys.users.rotated) != 1 {
			t.Fatalf("rotated = %v, want one rotation", sys.users.rotated)
		}
		if reply := sys.client.Do(http.MethodGet, "/me", ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("old session /me = %d, want 401", reply.Status)
		}
		if reply := sys.client.NewDevice().Post("/password/reset", `{"token":"`+token+`","password":"reset horse ok"}`); reply.Status != http.StatusUnprocessableEntity {
			t.Fatalf("token reuse = %d", reply.Status)
		}
		if reply := loginAda(sys.client.NewDevice(), "reset horse ok"); reply.Status != http.StatusNoContent {
			t.Fatalf("login after reset = %d", reply.Status)
		}
	})
}

// TestResetDoesNotLogIn verifies the reset response sets no login.
func TestResetDoesNotLogIn(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		_ = sys.client.Post("/password/forgot", `{"email":"ada@example.com"}`)
		device := sys.client.NewDevice()
		device.Post("/password/reset", `{"token":"`+(*sys.sent)[0].Token+`","password":"reset horse ok"}`)
		if reply := device.Do(http.MethodGet, "/me", ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("/me after reset = %d, want 401", reply.Status)
		}
	})
}

// TestForgotUnknownIdentifierIs202 verifies nothing is sent and the response
// does not differ.
func TestForgotUnknownIdentifierIs202(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		if reply := sys.client.Post("/password/forgot", `{"email":"nobody@example.com"}`); reply.Status != http.StatusAccepted {
			t.Fatalf("forgot = %d", reply.Status)
		}
		if len(*sys.sent) != 0 {
			t.Fatalf("sent %d messages", len(*sys.sent))
		}
	})
}

// TestResetTokenExpiredAndReplaced verifies expiry and replacement.
func TestResetTokenExpiredAndReplaced(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, "ada@example.com")
		_ = sys.verifier.SendReset(ctx, "ada@example.com")
		first, second := (*sys.sent)[0].Token, (*sys.sent)[1].Token
		if _, err := sys.verifier.Reset(ctx, first, "reset horse ok"); !errors.Is(err, password.ErrInvalidToken) {
			t.Fatalf("replaced token error = %v", err)
		}
		past := time.Now().Add(-time.Hour).Unix()
		_, err := db.Exec(ctx, query.Table(instance+"_password_resets", func(statement *query.Builder) {
			statement.Unfiltered().Update(map[string]any{"expires_at": past})
		}))
		if err != nil {
			t.Fatalf("expire: %v", err)
		}
		if _, err := sys.verifier.Reset(ctx, second, "reset horse ok"); !errors.Is(err, password.ErrInvalidToken) {
			t.Fatalf("expired token error = %v", err)
		}
	})
}

// TestConfirmWithPasswordCredential verifies the credential works through the
// core confirm route and a wrong password is core's 403.
func TestConfirmWithPasswordCredential(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		if reply := sys.client.Post("/confirm", `{"method":"pwd","password":"wrong horse!"}`); reply.Status != http.StatusForbidden {
			t.Fatalf("wrong confirm = %d %s", reply.Status, reply.Body)
		}
		if reply := sys.client.Post("/confirm", `{"method":"pwd","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("confirm = %d %s", reply.Status, reply.Body)
		}
	})
}

// TestConcurrentResetOneWins verifies two requests with one token do not both
// succeed.
func TestConcurrentResetOneWins(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, "ada@example.com")
		token := (*sys.sent)[0].Token
		results := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := sys.verifier.Reset(ctx, token, "reset horse ok")
				results <- err
			}()
		}
		succeeded := 0
		for range 2 {
			if err := <-results; err == nil {
				succeeded++
			}
		}
		if succeeded != 1 {
			t.Fatalf("%d resets succeeded, want 1", succeeded)
		}
	})
}

// TestForgetHookDeletesRows verifies forgetting a user removes the hash and the
// pending reset.
func TestForgetHookDeletesRows(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		_ = sys.verifier.SendReset(ctx, "ada@example.com")
		if err := sys.provider.Forget(ctx, "1"); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		if has, err := sys.verifier.Has(ctx, "1"); err != nil || has {
			t.Fatalf("Has after forget = %v, %v", has, err)
		}
		rows, err := db.Select[int](ctx, query.Table(instance+"_password_resets", func(statement *query.Builder) {
			statement.SelectRaw("COUNT(*)")
		}))
		if err != nil || rows[0] != 0 {
			t.Fatalf("resets left = %v, %v", rows, err)
		}
	})
}

// TestTwoInstancesSeparateTables verifies two verifiers over one database keep
// separate hashes.
func TestTwoInstancesSeparateTables(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		first := newSystem(t, db, instance+"_a")
		second := newSystem(t, db, instance+"_b")
		if err := first.verifier.Set(ctx, "1", "first password"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if has, _ := second.verifier.Has(ctx, "1"); has {
			t.Fatal("the second instance sees the first instance's hash")
		}
	})
}

// TestPingNamesMigrations verifies Ping reports missing tables.
func TestPingNamesMigrations(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		users := newFakeUsers()
		provider, _ := auth.NewUserProvider(users.driver)
		verifier, err := password.NewVerifier(provider, db, newSystem(t, db, instance+"_x").store, newSystem(t, db, instance+"_y").hasher, password.Users{
			Lookup: users.Lookup,
			Rotate: users.Rotate,
		}, func(passwords *password.Builder) {
			passwords.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		if err := verifier.Ping(ctx); !errors.Is(err, password.ErrNotMigrated) {
			t.Fatalf("Ping error = %v, want ErrNotMigrated", err)
		}
		migrateUp(t, db, verifier.Migrations()...)
		if err := verifier.Ping(ctx); err != nil {
			t.Fatalf("Ping after migrations: %v", err)
		}
	})
}

// TestRegisterHandlerWithoutCreatePanics verifies the composition-time panic.
func TestRegisterHandlerWithoutCreatePanics(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		users := newFakeUsers()
		provider, _ := auth.NewUserProvider(users.driver)
		verifier, err := password.NewVerifier(provider, db, sys.store, sys.hasher, password.Users{
			Lookup: users.Lookup,
			Rotate: users.Rotate,
		})
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		defer func() {
			if recover() == nil {
				t.Fatal("RegisterHandler did not panic")
			}
		}()
		verifier.RegisterHandler(sys.guard)
	})
}

// TestNewVerifierRejectsBadConfig verifies the configuration errors.
func TestNewVerifierRejectsBadConfig(t *testing.T) {
	t.Parallel()

	if _, err := password.NewVerifier(nil, nil, nil, nil, password.Users{}); !errors.Is(err, password.ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
}
