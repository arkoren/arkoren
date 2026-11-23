package iam_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/foundation/totp"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/ark/iam/password"
	"arkoren.dev/ark/iam/twofactor"
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration, func(passwords *password.Builder) {
				passwords.AfterChange(func(context.Context, string) error {
					ran = true

					return nil
				})
			})
			identity.Tokens()
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		_, personal, err := sys.identity.Issuer().Issue(testContext(t), mustSubject(t, sys), apitoken.Request{Name: "ci"})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if reply := sys.client.Do(http.MethodPut, "/password", `{"current_password":"correct horse","password":"changed horse ok"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("change = %d %s", reply.Status, reply.Body)
		}
		if !ran {
			t.Fatal("the application's AfterChange hook did not run")
		}
		if got := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", "Authorization: Bearer "+personal).Status; got != http.StatusUnauthorized {
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t), func(factors *twofactor.Builder) {
				factors.OnEnable(func(context.Context, string) error {
					ran = true

					return nil
				})
			})
			identity.Tokens()
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		_, personal, err := sys.identity.Issuer().Issue(testContext(t), mustSubject(t, sys), apitoken.Request{Name: "ci"})
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
		if got := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", "Authorization: Bearer "+personal).Status; got != http.StatusUnauthorized {
			t.Fatalf("personal token after enabling two-factor = %d, want 401", got)
		}
	})
}

// TestAppVerifiedKeepsTwoFactorGate verifies an unverified user still cannot
// start enrolling when the application sets a Verified check that admits all.
func TestAppVerifiedKeepsTwoFactorGate(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.TwoFactor(totp.New("Example"), cipher(t), func(factors *twofactor.Builder) {
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration)
			identity.Tokens(func(tokens *apitoken.Builder) {
				tokens.Verified(allVerified)
			})
		})
		register(t, sys.client, "ada@example.com")
		if reply := sys.client.Post("/tokens", `{"name":"ci"}`); reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "account:email-unverified") {
			t.Fatalf("token by an unverified user = %d %s, want 403 email-unverified", reply.Status, reply.Body)
		}
	})
}

// TestProviderCreateKeepsTheFacadesSignUp verifies a first login still creates
// a verified user when the provider's Create function declines.
func TestProviderCreateKeepsTheFacadesSignUp(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Provider(fakeProvider{name: "fake"}, signUp, func(login *iam.ProviderBuilder) {
				login.Create(func(context.Context, auth.External) (string, error) { return "", nil })
			})
		})
		device := sys.client.NewDevice()
		if reply := device.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d %s, want 204", reply.Status, reply.Body)
		}
		subject, _, err := sys.identity.Directory().Lookup(testContext(t), "ada@example.com")
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if verified, _ := sys.identity.Directory().Verified(testContext(t), subject); !verified {
			t.Fatal("the user the facade created from a verified identity is not verified")
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration, func(passwords *password.Builder) {
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
		if got := sys.box.dispatchedVerifies(t); len(got) != 1 || got[0].Subject != mustSubject(t, sys) {
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
		sys := newApp(t, db, instance, func(identity *iam.Builder) {
			identity.Password(hasher(), registration, func(passwords *password.Builder) {
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
		if got := sys.box.dispatchedResets(t); len(got) != 1 || !got[0].Notice {
			t.Fatalf("resets dispatched = %+v, want one notice for the owner", got)
		}
	})
}
