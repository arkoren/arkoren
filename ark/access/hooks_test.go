package access_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/foundation/totp"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access"
	"arkoren.dev/ark/access/apitoken"
	"arkoren.dev/ark/access/federation"
	"arkoren.dev/ark/access/password"
	"arkoren.dev/ark/access/twofactor"
)

// The hooks the facade sets on the packages (token revocation, the verified-email
// gate, the takeover, user creation, the fallback check, the registration
// notices) add up with the application's callbacks instead of being replaced by
// them. Each test sets the same hook from an application callback, in a way
// that would have dropped the facade's behavior when setters replaced, and
// checks the behavior survived.

// allVerified is a Verified setting that admits everyone, the one an
// application callback could use to lift the gate of the facade.
func allVerified(context.Context, string) (bool, error) { return true, nil }

// TestAppAfterChangeKeepsTokenRevocation verifies a password change still
// revokes the tokens when the application sets its own AfterChange hook.
func TestAppAfterChangeKeepsTokenRevocation(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		var ran bool
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration, func(passwords *password.Builder) {
				passwords.AfterChange(func(context.Context, string) error {
					ran = true

					return nil
				})
			})
			iam.Tokens()
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		_, personal, err := sys.iam.Issuer().Issue(testContext(t), mustSubject(t, sys), apitoken.Request{Name: "ci"})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if reply := sys.client.Do(http.MethodPut, "/password", `{"current_password":"correct horse","password":"changed horse ok"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("change = %d %s", reply.Status, reply.Body)
		}
		if !ran {
			t.Fatal("the application's AfterChange hook did not run")
		}
		if got := sys.client.NewDevice().Do(http.MethodGet, "/me", "", "Authorization: Bearer "+personal).Status; got != http.StatusUnauthorized {
			t.Fatalf("personal token after a password change = %d, want 401", got)
		}
	})
}

// TestAppOnEnableKeepsTokenRevocation verifies enabling two-factor still
// revokes the tokens when the application sets its own OnEnable hook.
func TestAppOnEnableKeepsTokenRevocation(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		var ran bool
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.TwoFactor(totp.New("Example"), cipher(t), func(factors *twofactor.Builder) {
				factors.OnEnable(func(context.Context, string) error {
					ran = true

					return nil
				})
			})
			iam.Tokens()
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		_, personal, err := sys.iam.Issuer().Issue(testContext(t), mustSubject(t, sys), apitoken.Request{Name: "ci"})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		setup := sys.client.Post("/two-factor/setup", "")
		if reply := sys.client.Post("/two-factor", `{"code":"`+code(t, secretOf(setup.Body), 0)+`"}`); reply.Status != http.StatusOK {
			t.Fatalf("enable = %d %s", reply.Status, reply.Body)
		}
		if !ran {
			t.Fatal("the application's OnEnable hook did not run")
		}
		if got := sys.client.NewDevice().Do(http.MethodGet, "/me", "", "Authorization: Bearer "+personal).Status; got != http.StatusUnauthorized {
			t.Fatalf("personal token after enabling two-factor = %d, want 401", got)
		}
	})
}

// TestAppVerifiedKeepsTwoFactorGate verifies an unverified user still cannot
// start enrolling when the application sets a Verified check that admits all.
func TestAppVerifiedKeepsTwoFactorGate(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.TwoFactor(totp.New("Example"), cipher(t), func(factors *twofactor.Builder) {
				factors.Verified(allVerified)
			})
		})
		register(t, sys.client, "ada@example.com")
		if reply := sys.client.Post("/two-factor/setup", ""); reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "account:email-unverified") {
			t.Fatalf("setup by an unverified user = %d %s, want 403 email-unverified", reply.Status, reply.Body)
		}
	})
}

// TestAppVerifiedKeepsTokenGate verifies an unverified user still cannot create
// a token when the application sets a Verified check that admits all.
func TestAppVerifiedKeepsTokenGate(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Tokens(func(tokens *apitoken.Builder) {
				tokens.Verified(allVerified)
			})
		})
		register(t, sys.client, "ada@example.com")
		if reply := sys.client.Post("/tokens", `{"name":"ci"}`); reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "account:email-unverified") {
			t.Fatalf("token by an unverified user = %d %s, want 403 email-unverified", reply.Status, reply.Body)
		}
	})
}

// TestAppVerifiedKeepsConnectGate verifies an unverified user still cannot
// connect a provider when the application sets a Verified check that admits all.
func TestAppVerifiedKeepsConnectGate(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Provider(fakeProvider{name: "fake"}).Federation(func(links *federation.Builder) {
				links.Verified(allVerified)
			})
		})
		register(t, sys.client, "ada@example.com")
		if reply := sys.client.Post("/connections/fake", ""); reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "account:email-unverified") {
			t.Fatalf("connect by an unverified user = %d %s, want 403 email-unverified", reply.Status, reply.Body)
		}
	})
}

// TestAppTakeOverKeepsTheFacadesTakeover verifies a provider login whose email
// is verified still takes over the unverified account when the application sets
// a TakeOver function that declines.
func TestAppTakeOverKeepsTheFacadesTakeover(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Provider(fakeProvider{name: "fake"}).Federation(func(links *federation.Builder) {
				links.TakeOver(func(context.Context, auth.External) (string, error) { return "", nil })
			})
		})
		register(t, sys.client, "ada@example.com")
		squatted := strings.SplitN(sys.client.Do(http.MethodGet, "/me", "").Body, "|", 2)[0]
		owner := sys.client.NewDevice()
		if reply := owner.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("owner callback = %d %s, want 204", reply.Status, reply.Body)
		}
		if me := owner.Do(http.MethodGet, "/me", ""); !strings.HasPrefix(me.Body, squatted+"|") {
			t.Fatalf("owner /me = %q, want the registered user %s", me.Body, squatted)
		}
		if reply := sys.client.NewDevice().Post("/login", login); reply.Status == http.StatusNoContent {
			t.Fatalf("the squatter's password still logs in: %d", reply.Status)
		}
	})
}

// TestAppCreateKeepsTheFacadesSignUp verifies a first login still creates a
// verified user when the application sets a Create function that declines.
func TestAppCreateKeepsTheFacadesSignUp(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Provider(fakeProvider{name: "fake"}).Federation(signUp, func(links *federation.Builder) {
				links.Create(func(context.Context, auth.External) (string, error) { return "", nil })
			})
		})
		device := sys.client.NewDevice()
		if reply := device.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d %s, want 204", reply.Status, reply.Body)
		}
		subject, _, err := sys.iam.Directory().Lookup(testContext(t), "ada@example.com")
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if verified, _ := sys.iam.Directory().Verified(testContext(t), subject); !verified {
			t.Fatal("the user the facade created from a verified identity is not verified")
		}
	})
}

// TestAppFallbackKeepsThePasswordFallback verifies a user with a password can
// still remove their last connection when the application sets a Fallback that
// says no.
func TestAppFallbackKeepsThePasswordFallback(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Provider(fakeProvider{name: "fake"}).Federation(func(links *federation.Builder) {
				links.Fallback(func(context.Context, string) (bool, error) { return false, nil })
			})
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		if err := sys.iam.Links().Link(testContext(t), mustSubject(t, sys), auth.External{Issuer: "https://fake.example", Subject: "g1"}); err != nil {
			t.Fatalf("Link: %v", err)
		}
		id := between(sys.client.Do(http.MethodGet, "/connections", "").Body, `"id":"`, `"`)
		if reply := sys.client.Do(http.MethodDelete, "/connections/"+id, ""); reply.Status != http.StatusNoContent {
			t.Fatalf("disconnect the last connection of a user with a password = %d %s, want 204", reply.Status, reply.Body)
		}
	})
}

// TestAppRegisteredKeepsTheVerificationRequest verifies a registration still
// dispatches the verification email when the application sets a Registered
// hook.
func TestAppRegisteredKeepsTheVerificationRequest(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		var ran bool
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration, func(passwords *password.Builder) {
				passwords.Registered(func(context.Context, string, string) error {
					ran = true

					return nil
				})
			})
		})
		if reply := sys.client.Post("/register", `{"email":"ada@example.com","password":"correct horse"}`); reply.Status != http.StatusAccepted {
			t.Fatalf("register = %d %s", reply.Status, reply.Body)
		}
		if !ran {
			t.Fatal("the application's Registered hook did not run")
		}
		if got := sys.box.dispatchedVerifies; len(got) != 1 || got[0].Subject != mustSubject(t, sys) {
			t.Fatalf("verifications dispatched = %+v, want one for the new user", got)
		}
	})
}

// TestAppTakenKeepsTheOwnerNotice verifies registering a taken address still
// dispatches the reset notice to its owner when the application sets a Taken
// hook.
func TestAppTakenKeepsTheOwnerNotice(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		var ran bool
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration, func(passwords *password.Builder) {
				passwords.Taken(func(context.Context, string) error {
					ran = true

					return nil
				})
			})
		})
		registerBoth(sys)
		if !ran {
			t.Fatal("the application's Taken hook did not run")
		}
		if got := sys.box.dispatchedResets; len(got) != 1 || !got[0].Notice {
			t.Fatalf("resets dispatched = %+v, want one notice for the owner", got)
		}
	})
}
