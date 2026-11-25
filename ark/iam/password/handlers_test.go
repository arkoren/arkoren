package password_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam/internal/apptest"
	"arkoren.dev/ark/iam/password"
)

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

// TestRegisterWeakPasswordCreatesNothing verifies a password the policy rejects
// is 422 and creates no user.
func TestRegisterWeakPasswordCreatesNothing(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		if reply := sys.client.NewDevice().Post("/register", `{"email":"bob@example.com","password":"x"}`); reply.Status != http.StatusUnprocessableEntity {
			t.Fatalf("weak register = %d %s", reply.Status, reply.Body)
		}
		if _, _, err := sys.users.Lookup(testContext(t), "bob@example.com"); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("a rejected password still created a user: %v", err)
		}
	})
}

// TestRegisterAnswersTheSameForNewAndTaken verifies a new and a taken identifier
// get the same 202 with the same (empty) body, neither logs the client in, and
// the taken one changes nothing.
func TestRegisterAnswersTheSameForNewAndTaken(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		fresh := sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"correct horse"}`)
		taken := sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"another horse"}`)
		if fresh.Status != http.StatusAccepted || taken.Status != http.StatusAccepted || fresh.Body != taken.Body {
			t.Fatalf("new = %d %q, taken = %d %q, want the same 202", fresh.Status, fresh.Body, taken.Status, taken.Body)
		}
		device := sys.client.NewDevice()
		device.Post("/register", `{"email":"ada@example.com","password":"another horse"}`)
		if reply := device.Do(http.MethodGet, "/me", ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("/me after registering = %d, want 401: registering logs nobody in", reply.Status)
		}
		if reply := loginAda(sys.client.NewDevice(), "another horse"); reply.Status != http.StatusUnprocessableEntity {
			t.Fatalf("login with the taken registration's password = %d, want 422: nothing was changed", reply.Status)
		}
		if reply := loginAda(sys.client.NewDevice(), "correct horse"); reply.Status != http.StatusNoContent {
			t.Fatalf("login with the original password = %d", reply.Status)
		}
	})
}

// TestRegisterTakenNotifiesTheOwner verifies the owner of a taken identifier
// gets one reset message marked as a registration notice, and a new identifier
// dispatches nothing.
func TestRegisterTakenNotifiesTheOwner(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"correct horse"}`)
		if len(*sys.dispatched) != 0 || len(*sys.sent) != 0 {
			t.Fatalf("a new registration dispatched %d and sent %d, want none", len(*sys.dispatched), len(*sys.sent))
		}
		sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"another horse"}`)
		if len(*sys.dispatched) != 1 || (*sys.dispatched)[0] != (password.ResetRequest{Identifier: "ada@example.com", Notice: true}) {
			t.Fatalf("dispatched = %+v, want one notice for ada", *sys.dispatched)
		}
		if len(*sys.sent) != 1 {
			t.Fatalf("messages after the taken registration = %d, want 1", len(*sys.sent))
		}
		if message := (*sys.sent)[0]; !message.Notice || message.Address != "ada@example.com" || message.Signed == "" {
			t.Fatalf("message = %+v, want a registration notice for ada", message)
		}
	})
}

// TestRegisterTakenDispatchFailureStaysAccepted verifies a failing dispatch on
// the taken path is logged and the answer is still the 202 of a new identifier.
func TestRegisterTakenDispatchFailureStaysAccepted(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newConfiguredSystem(t, db, instance, func(passwords *password.Builder) {
			passwords.Queue(failingQueue())
		})
		sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"correct horse"}`)
		reply := sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"another horse"}`)
		if reply.Status != http.StatusAccepted {
			t.Fatalf("taken registration with a failing dispatch = %d %s, want 202", reply.Status, reply.Body)
		}
	})
}

// TestRegisterTakenRunsTheTakenHook verifies the Taken hook runs for a taken
// identifier only.
func TestRegisterTakenRunsTheTakenHook(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		var taken []string
		sys := newConfiguredSystem(t, db, instance, func(passwords *password.Builder) {
			passwords.Taken(func(_ context.Context, identifier string) error {
				taken = append(taken, identifier)

				return nil
			})
		})
		sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"correct horse"}`)
		if len(taken) != 0 {
			t.Fatalf("Taken ran for a new identifier: %v", taken)
		}
		sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"another horse"}`)
		if len(taken) != 1 || taken[0] != "ada@example.com" {
			t.Fatalf("taken = %v, want [ada@example.com]", taken)
		}
	})
}

// TestRegisterNoticesAreThrottled verifies the registration throttle also caps
// the notices: five registrations per identifier in the window (the first made
// ada), and every one after that is 429 and sends nothing.
func TestRegisterNoticesAreThrottled(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		before := len(*sys.sent)
		for i := range 6 {
			reply := sys.client.NewDevice().Post("/register", `{"email":"ada@example.com","password":"another horse"}`)
			want := http.StatusAccepted
			if i >= 4 {
				want = http.StatusTooManyRequests
			}
			if reply.Status != want {
				t.Fatalf("registration #%d = %d %s, want %d", i+1, reply.Status, reply.Body, want)
			}
		}
		if got := len(*sys.sent) - before; got != 4 {
			t.Fatalf("notices = %d, want 4: registerAda used one of the five registrations", got)
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
		token := (*sys.sent)[0].Signed
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
		device.Post("/password/reset", `{"token":"`+(*sys.sent)[0].Signed+`","password":"reset horse ok"}`)
		if reply := device.Do(http.MethodGet, "/me", ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("/me after reset = %d, want 401", reply.Status)
		}
	})
}

// TestForgotUnknownIdentifierIs202 verifies the request is dispatched exactly as
// typed even for an identifier nobody owns, the answer does not differ from a
// known one, and the worker sends nothing.
func TestForgotUnknownIdentifierIs202(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		known := sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`)
		unknown := sys.client.NewDevice().Post("/password/forgot", `{"email":"Nobody@Example.com"}`)
		if unknown.Status != http.StatusAccepted || known.Status != unknown.Status || known.Body != unknown.Body {
			t.Fatalf("known = %d %q, unknown = %d %q, want the same 202", known.Status, known.Body, unknown.Status, unknown.Body)
		}
		want := []password.ResetRequest{{Identifier: "ada@example.com"}, {Identifier: "Nobody@Example.com"}}
		if len(*sys.dispatched) != 2 || (*sys.dispatched)[0] != want[0] || (*sys.dispatched)[1] != want[1] {
			t.Fatalf("dispatched = %+v, want %+v", *sys.dispatched, want)
		}
		if len(*sys.sent) != 1 {
			t.Fatalf("sent %d messages, want 1: only ada exists", len(*sys.sent))
		}
	})
}

// TestForgotDispatchErrorIs500 verifies work that could not be stored is an
// error, not a silent 202, so the client tries again.
func TestForgotDispatchErrorIs500(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newConfiguredSystem(t, db, instance, func(passwords *password.Builder) {
			passwords.Queue(failingQueue())
		})
		if reply := sys.client.Post("/password/forgot", `{"email":"ada@example.com"}`); reply.Status != http.StatusInternalServerError {
			t.Fatalf("forgot with a failing dispatch = %d %s, want 500", reply.Status, reply.Body)
		}
	})
}

// TestForgotIsThrottledBeforeDispatch verifies the sixth request for one
// identifier is 429 and never reaches the dispatch function.
func TestForgotIsThrottledBeforeDispatch(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		for range 5 {
			sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`)
		}
		if reply := sys.client.NewDevice().Post("/password/forgot", `{"email":"ada@example.com"}`); reply.Status != http.StatusTooManyRequests {
			t.Fatalf("sixth forgot = %d, want 429", reply.Status)
		}
		if len(*sys.dispatched) != 5 {
			t.Fatalf("dispatched %d requests, want 5", len(*sys.dispatched))
		}
	})
}

// TestChangeEndsOtherLoginsEvenWhenTheHookFails verifies a failing AfterChange
// hook is returned to the client only after the other logins ended, and that a
// change ends the user's unused reset links.
func TestChangeEndsOtherLoginsEvenWhenTheHookFails(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newConfiguredSystem(t, db, instance, func(passwords *password.Builder) {
			passwords.AfterChange(func(context.Context, string) error { return errors.New("hook down") })
		})
		sys.registerAda(t, sys.client)
		other := sys.client.NewDevice()
		if reply := loginAda(other, "correct horse"); reply.Status != http.StatusNoContent {
			t.Fatalf("other login = %d", reply.Status)
		}
		if err := sys.verifier.SendReset(ctx, password.ResetRequest{Identifier: "ada@example.com"}); err != nil {
			t.Fatalf("SendReset: %v", err)
		}
		reply := sys.client.Do(http.MethodPut, "/password", `{"current_password":"correct horse","password":"changed horse ok"}`)
		if reply.Status != http.StatusInternalServerError {
			t.Fatalf("change with a failing hook = %d %s, want 500", reply.Status, reply.Body)
		}
		if reply := other.Do(http.MethodGet, "/me", ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("the other login after a failed hook = %d, want it ended", reply.Status)
		}
		if _, err := sys.verifier.Reset(ctx, (*sys.sent)[0].Signed, "reset horse ok"); !errors.Is(err, password.ErrInvalidToken) {
			t.Fatalf("a reset link survived the change: %v", err)
		}
	})
}

// TestResetHandlersWithoutResetPanic verifies the forgot and reset handlers fail
// at composition without Builder.Reset.
func TestResetHandlersWithoutResetPanic(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		users := newFakeUsers()
		provider, _ := auth.NewUserProvider(users.driver)
		verifier, err := password.NewVerifier(provider, db, sys.store, testClientIP, sys.hasher, password.Users{
			Lookup: users.Lookup,
			Rotate: users.Rotate,
		})
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		if verifier.ResetEnabled() {
			t.Fatal("ResetEnabled() is true by default")
		}
		for name, build := range map[string]func(){
			"ForgotHandler": func() { verifier.ForgotHandler() },
			"ResetHandler":  func() { verifier.ResetHandler(sys.guard) },
		} {
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("%s did not panic without Reset", name)
					}
				}()
				build()
			}()
		}
		if err := verifier.SendReset(testContext(t), password.ResetRequest{Identifier: "x"}); !errors.Is(err, password.ErrInvalidConfig) {
			t.Fatalf("SendReset error = %v, want ErrInvalidConfig", err)
		}
	})
}

// TestRegisterHandlerWithoutRegistrationPanics verifies registration is off by
// default and the handler fails at composition then.
func TestRegisterHandlerWithoutRegistrationPanics(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		users := newFakeUsers()
		provider, _ := auth.NewUserProvider(users.driver)
		verifier, err := password.NewVerifier(provider, db, sys.store, testClientIP, sys.hasher, password.Users{
			Lookup: users.Lookup,
			Create: users.Create,
			Rotate: users.Rotate,
		})
		if err != nil {
			t.Fatalf("NewVerifier: %v", err)
		}
		if verifier.Registration() {
			t.Fatal("Registration() is true by default")
		}
		defer func() {
			if recover() == nil {
				t.Fatal("RegisterHandler did not panic")
			}
		}()
		verifier.RegisterHandler()
	})
}

// TestRegistrationRequiresCreate verifies Registration(true) without
// Users.Create is an invalid configuration.
func TestRegistrationWithoutCreateIsIgnored(t *testing.T) {
	warnings := apptest.CaptureWarnings(t)

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		users := newFakeUsers()
		provider, _ := auth.NewUserProvider(users.driver)
		verifier, err := password.NewVerifier(provider, db, sys.store, testClientIP, sys.hasher, password.Users{
			Lookup: users.Lookup,
			Rotate: users.Rotate,
		}, func(passwords *password.Builder) {
			passwords.Registration(true)
		})
		if err != nil {
			t.Fatalf("NewVerifier error = %v, want the setting ignored", err)
		}
		if verifier.Registration() {
			t.Fatal("Registration() = true, want it ignored without Users.Create")
		}
		if got := warnings.Settings(); !slices.Contains(got, "password.Builder.Registration") {
			t.Fatalf("warnings = %v, want one naming password.Builder.Registration", got)
		}
	})
}

// TestChangePasswordCounterIsIndependentOfLoginCounter verifies the attempts
// counted against a user when they change their password do not share a counter
// with logins whose typed identifier happens to look like the subject: ten
// failed logins for "subject:<id>" from ten addresses must not throttle the
// owner's change.
func TestChangePasswordCounterIsIndependentOfLoginCounter(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		sys.registerAda(t, sys.client)
		subject := sys.client.Do(http.MethodGet, "/me", "").Body
		other := sys.client.NewDevice()
		for i := range 10 {
			ip := "X-Test-IP: 198.51.100." + strconv.Itoa(i+1)
			body := `{"email":"subject:` + subject + `","password":"wrong horse!"}`
			if reply := other.Do(http.MethodPost, "/login", body, ip); reply.Status == http.StatusTooManyRequests {
				t.Fatalf("login %d = 429 before the identifier limit", i)
			}
		}
		change := sys.client.Do(http.MethodPut, "/password", `{"current_password":"correct horse","password":"brand new horse"}`)
		if change.Status != http.StatusNoContent {
			t.Fatalf("change after ten failed logins for subject:%s = %d %s, want 204", subject, change.Status, change.Body)
		}
	})
}

// TestLoginRejectsMistypedMembersWithValidationProblem verifies a body whose
// members have the wrong JSON type is parsed through the handler's schema and
// answered with the request package's 422 validation problem, never with a
// plain error body.
func TestLoginRejectsMistypedMembersWithValidationProblem(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)

		reply := sys.client.Post("/login", `{"email":5,"password":"correct horse"}`)

		if reply.Status != http.StatusUnprocessableEntity {
			t.Fatalf("mistyped email = %d %s, want 422", reply.Status, reply.Body)
		}

		if !strings.Contains(reply.Body, "validation-failed") {
			t.Fatalf("body = %s, want the validation-failed problem", reply.Body)
		}
	})
}
