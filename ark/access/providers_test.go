package access_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access"
	"arkoren.dev/ark/access/federation"
)

// federatedMethod is the method protocol modules record for an external login.
const federatedMethod auth.Method = "federated"

// fakeProvider is an access.Provider whose "provider" answers the callback
// straight from the query: sub, email and verified.
type fakeProvider struct {
	name string
}

// Name is [access.Provider.Name].
func (provider fakeProvider) Name() string {
	return provider.name
}

// LoginHandler is [access.Provider.LoginHandler].
func (provider fakeProvider) LoginHandler() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		return response.Redirect(w, http.StatusFound, "/idp/"+provider.name)
	}
}

// CallbackHandler is [access.Provider.CallbackHandler]: it resolves the
// identity named by the query and begins the login, as a protocol module would.
func (provider fakeProvider) CallbackHandler(guard *auth.Guard, resolve auth.Resolver) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		query := r.URL.Query()
		subject, err := resolve(r.Context(), auth.External{
			Issuer:        "https://" + provider.name + ".example",
			Subject:       query.Get("sub"),
			Email:         query.Get("email"),
			EmailVerified: query.Get("verified") == "true",
		})
		if err != nil {
			return err
		}
		outcome, err := guard.Begin(r, subject, auth.Factor{Method: federatedMethod, Kind: auth.KindFederated})
		if err != nil {
			return err
		}

		return outcome.Response(w)
	}
}

// nativeProvider adds the native capability to fakeProvider.
type nativeProvider struct {
	fakeProvider
}

// NonceHandler is [access.NativeProvider.NonceHandler].
func (nativeProvider) NonceHandler() router.Handler {
	return func(w http.ResponseWriter, _ *http.Request) error {
		return response.String(w, http.StatusOK, "nonce")
	}
}

// NativeHandler is [access.NativeProvider.NativeHandler].
func (provider nativeProvider) NativeHandler(guard *auth.Guard, resolve auth.Resolver) router.Handler {
	return provider.CallbackHandler(guard, resolve)
}

// callbackPath returns the callback of the fake provider for an identity.
func callbackPath(name, subject, email string, verified bool) string {
	path := "/auth/" + name + "/callback?sub=" + subject + "&email=" + email
	if verified {
		path += "&verified=true"
	}

	return path
}

// TestProviderFirstLoginCreatesVerifiedUser verifies a verified provider email
// creates a verified user and the second login finds the same one.
func TestProviderFirstLoginCreatesVerifiedUser(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Provider(fakeProvider{name: "fake"}, signUp)
		})
		first := sys.client.NewDevice()
		if reply := first.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
		subject := strings.SplitN(first.Do(http.MethodGet, "/me", "").Body, "|", 2)[0]
		record, err := sys.iam.UserProvider().Find(testContext(t), subject)
		if err != nil || record.Claims["email_verified"] != true || record.Claims["email"] != "ada@example.com" {
			t.Fatalf("record = %+v, %v", record, err)
		}
		second := sys.client.NewDevice()
		second.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), "")
		if reply := second.Do(http.MethodGet, "/me", ""); !strings.HasPrefix(reply.Body, subject+"|") {
			t.Fatalf("second login /me = %q, want user %s", reply.Body, subject)
		}
	})
}

// TestProviderFirstLoginWithoutSignUpIsRefused verifies sign-up is off by
// default: an unknown identity gets 403 no-account and no user is created.
func TestProviderFirstLoginWithoutSignUpIsRefused(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Provider(fakeProvider{name: "fake"})
		})
		device := sys.client.NewDevice()
		reply := device.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), "")
		if reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "federation:no-account") {
			t.Fatalf("callback = %d %s, want 403 no-account", reply.Status, reply.Body)
		}
		if _, _, err := sys.iam.Directory().Lookup(testContext(t), "ada@example.com"); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("Lookup after a refused login = %v, want no user", err)
		}
	})
}

// TestConnectWithoutSignUpStillWorks verifies a logged-in user connects a
// provider and then logs in with it while sign-up is off.
func TestConnectWithoutSignUpStillWorks(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Provider(fakeProvider{name: "fake"})
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		sys.client.Post("/connections/fake", "")
		if reply := sys.client.Do(http.MethodGet, callbackPath("fake", "g1", "", false), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("connecting callback = %d %s", reply.Status, reply.Body)
		}
		subject := strings.SplitN(sys.client.Do(http.MethodGet, "/me", "").Body, "|", 2)[0]
		other := sys.client.NewDevice()
		if reply := other.Do(http.MethodGet, callbackPath("fake", "g1", "", false), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("login with the connected identity = %d %s", reply.Status, reply.Body)
		}
		if me := other.Do(http.MethodGet, "/me", ""); !strings.HasPrefix(me.Body, subject+"|") {
			t.Fatalf("/me = %q, want user %s", me.Body, subject)
		}
	})
}

// TestProviderUnverifiedEmailCreatesNoUser verifies a first login whose
// provider did not verify the email creates no user and is refused with the
// stable email-unverified problem.
func TestProviderUnverifiedEmailCreatesNoUser(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Provider(fakeProvider{name: "fake"}, signUp)
		})
		reply := sys.client.NewDevice().Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", false), "")
		if reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "ark/access:federation:email-unverified") {
			t.Fatalf("callback = %d %s, want 403 email-unverified", reply.Status, reply.Body)
		}
		if _, _, err := sys.iam.Directory().Lookup(testContext(t), "ada@example.com"); !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatalf("Lookup after the refused sign-up = %v, want ErrUserNotFound", err)
		}
	})
}

// TestProviderNeverLinksByEmail verifies a provider identity with the email of
// an existing user is a conflict, not a login to that user.
func TestProviderNeverLinksByEmail(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Provider(fakeProvider{name: "fake"}, signUp)
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		device := sys.client.NewDevice()
		reply := device.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), "")
		if reply.Status != http.StatusConflict {
			t.Fatalf("callback = %d %s, want 409", reply.Status, reply.Body)
		}
		if me := device.Do(http.MethodGet, "/me", ""); me.Body != "anonymous" {
			t.Fatalf("the provider login reached the existing account: %q", me.Body)
		}
	})
}

// TestConnectFlowLinksAndListsAndDisconnects verifies the connect intent, the
// listing and the last-way-in rule with and without a password.
func TestConnectFlowLinksAndListsAndDisconnects(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Provider(fakeProvider{name: "fake"}, signUp)
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		connect := sys.client.Post("/connections/fake", "")
		if connect.Status != http.StatusSeeOther || connect.Header.Get("Location") != "/auth/fake" {
			t.Fatalf("connect = %d %q", connect.Status, connect.Header.Get("Location"))
		}
		if reply := sys.client.Do(http.MethodGet, callbackPath("fake", "g1", "", false), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
		other := sys.client.NewDevice()
		other.Do(http.MethodGet, callbackPath("fake", "g1", "", false), "")
		if a, b := sys.client.Do(http.MethodGet, "/me", "").Body, other.Do(http.MethodGet, "/me", "").Body; strings.SplitN(a, "|", 2)[0] != strings.SplitN(b, "|", 2)[0] {
			t.Fatalf("the provider login reached another user: %q vs %q", a, b)
		}
		list := sys.client.Do(http.MethodGet, "/connections", "")
		if list.Status != http.StatusOK || !strings.Contains(list.Body, "https://fake.example") {
			t.Fatalf("list = %d %s", list.Status, list.Body)
		}
		id := between(list.Body, `"id":"`, `"`)
		// The user has a password, so the last connection may go.
		if reply := sys.client.Do(http.MethodDelete, "/connections/"+id, ""); reply.Status != http.StatusNoContent {
			t.Fatalf("disconnect = %d %s", reply.Status, reply.Body)
		}
	})
}

// TestDisconnectLastWayInConflicts verifies a social-only user cannot remove
// their only connection.
func TestDisconnectLastWayInConflicts(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Provider(fakeProvider{name: "fake"}, signUp)
		})
		device := sys.client.NewDevice()
		device.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), "")
		id := between(device.Do(http.MethodGet, "/connections", "").Body, `"id":"`, `"`)
		if reply := device.Do(http.MethodDelete, "/connections/"+id, ""); reply.Status != http.StatusConflict {
			t.Fatalf("disconnect = %d %s, want 409", reply.Status, reply.Body)
		}
	})
}

// TestSilentFederatedReloginNotFreshForUserWithPassword verifies a provider
// login does not make a user who has a password fresh, while a federated-only
// user is fresh right after logging in.
func TestSilentFederatedReloginNotFreshForUserWithPassword(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Provider(fakeProvider{name: "fake"}, signUp)
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		sys.client.Post("/connections/fake", "")
		sys.client.Do(http.MethodGet, callbackPath("fake", "g1", "", false), "")

		silent := sys.client.NewDevice()
		silent.Do(http.MethodGet, callbackPath("fake", "g1", "", false), "")
		if reply := silent.Do(http.MethodDelete, "/account", ""); reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, `"pwd"`) {
			t.Fatalf("delete after a silent provider login = %d %s, want 403 listing pwd", reply.Status, reply.Body)
		}
		if reply := silent.Post("/confirm", `{"method":"pwd","password":"correct horse"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("confirm = %d", reply.Status)
		}
		if reply := silent.Do(http.MethodDelete, "/account", ""); reply.Status != http.StatusNoContent {
			t.Fatalf("delete after confirming = %d %s", reply.Status, reply.Body)
		}

		socialOnly := sys.client.NewDevice()
		socialOnly.Do(http.MethodGet, callbackPath("fake", "g2", "bob@example.com", true), "")
		if reply := socialOnly.Do(http.MethodDelete, "/account", ""); reply.Status != http.StatusNoContent {
			t.Fatalf("a federated-only user right after login = %d %s, want fresh", reply.Status, reply.Body)
		}
	})
}

// TestNativeRoutesOnlyForNativeProviders verifies the nonce and native routes.
func TestNativeRoutesOnlyForNativeProviders(t *testing.T) {
	t.Parallel()

	db := memoryDatabase(t)
	sys := newApp(t, db, newInstance(t), func(iam *access.Builder) {
		iam.Provider(fakeProvider{name: "web"}).Provider(nativeProvider{fakeProvider{name: "phone"}})
	})
	for _, route := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/auth/web", true},
		{http.MethodGet, "/auth/web/callback", true},
		{http.MethodPost, "/auth/web/callback", true},
		{http.MethodGet, "/auth/web/nonce", false},
		{http.MethodPost, "/auth/web/native", false},
		{http.MethodGet, "/auth/phone/nonce", true},
		{http.MethodPost, "/auth/phone/native", true},
		{http.MethodGet, "/connections", true},
		{http.MethodPost, "/connections/phone", true},
		{http.MethodDelete, "/connections/abc", true},
	} {
		if got := sys.router.Has(route.method, route.path); got != route.want {
			t.Errorf("%s %s mounted = %v, want %v", route.method, route.path, got, route.want)
		}
	}
}

// Compile-time check that Resolve is an auth.Resolver.
var _ auth.Resolver = (*access.Access)(nil).Resolve

// TestResolveWithoutProvidersIsNotLinked verifies the guard rail.
func TestResolveWithoutProvidersIsNotLinked(t *testing.T) {
	t.Parallel()

	sys := newApp(t, memoryDatabase(t), "", func(iam *access.Builder) {
		iam.Password(hasher(), registration)
	})
	if _, err := sys.iam.Resolve(context.Background(), auth.External{Issuer: "x", Subject: "y"}); err == nil {
		t.Fatal("Resolve without providers succeeded")
	}
}

// TestNewRejectsDuplicateProviderNames verifies two providers cannot share a
// mounted path.
func TestNewRejectsDuplicateProviderNames(t *testing.T) {
	t.Parallel()

	_, err := newFacade(t, func(iam *access.Builder) {
		iam.Provider(fakeProvider{name: "oauth"}).Provider(fakeProvider{name: "oauth"})
	})
	if !errors.Is(err, access.ErrInvalidConfig) || !strings.Contains(err.Error(), "oauth") {
		t.Fatalf("error = %v, want a duplicate-name rejection", err)
	}
}

// TestProviderNamesMustBeOnePathSegment verifies New refuses a provider name
// that is not one path segment, because the name is mounted as "/auth/<name>".
func TestProviderNamesMustBeOnePathSegment(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"a/b", "{x}", "..", "a b", "a?b"} {
		_, err := newFacade(t, func(iam *access.Builder) { iam.Provider(fakeProvider{name: name}) })
		if !errors.Is(err, access.ErrInvalidConfig) {
			t.Errorf("provider name %q: New error = %v, want ErrInvalidConfig", name, err)
		}
	}
}

// TestVerifiedProviderEmailTakesOverUnverifiedAccount verifies the owner of an
// address a provider verified takes over the account someone registered with
// it and never verified, even with sign-up off: the squatter's password and
// session stop working, and the owner is logged in to the same user.
func TestVerifiedProviderEmailTakesOverUnverifiedAccount(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Provider(fakeProvider{name: "fake"})
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
		if verified, err := sys.iam.Directory().Verified(testContext(t), squatted); err != nil || !verified {
			t.Fatalf("Verified after the takeover = %v, %v", verified, err)
		}
		if me := sys.client.Do(http.MethodGet, "/me", ""); me.Body != "anonymous" {
			t.Fatalf("the squatter's session survived: %q", me.Body)
		}
		if reply := sys.client.NewDevice().Post("/login", login); reply.Status == http.StatusNoContent {
			t.Fatalf("the squatter's password still logs in: %d", reply.Status)
		}
	})
}

// TestUnverifiedProviderEmailDoesNotTakeOver verifies a provider that does not
// vouch for the address gets nothing: no login, and the registered user keeps
// their password and stays unverified.
func TestUnverifiedProviderEmailDoesNotTakeOver(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Provider(fakeProvider{name: "fake"})
		})
		register(t, sys.client, "ada@example.com")
		subject := strings.SplitN(sys.client.Do(http.MethodGet, "/me", "").Body, "|", 2)[0]

		device := sys.client.NewDevice()
		if reply := device.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", false), ""); reply.Status != http.StatusForbidden {
			t.Fatalf("callback = %d %s, want 403 no-account", reply.Status, reply.Body)
		}
		if verified, _ := sys.iam.Directory().Verified(testContext(t), subject); verified {
			t.Fatal("an unverified provider email verified the account")
		}
		if reply := sys.client.NewDevice().Post("/login", login); reply.Status != http.StatusNoContent {
			t.Fatalf("the registered password stopped working: %d", reply.Status)
		}
	})
}

// TestTakeOverEndsTheRegistrantsLogins verifies the takeover deletes the login
// records of the user, not only their sessions' validity: after the owner logs
// in, the user has exactly one login, the owner's.
func TestTakeOverEndsTheRegistrantsLogins(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Provider(fakeProvider{name: "fake"})
		})
		register(t, sys.client, "ada@example.com")
		owner := sys.client.NewDevice()
		if reply := owner.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("owner callback = %d %s", reply.Status, reply.Body)
		}
		if got := strings.Count(owner.Do(http.MethodGet, "/sessions", "").Body, `"id"`); got != 1 {
			t.Fatalf("logins of the user after the takeover = %d, want only the owner's", got)
		}
	})
}

// TestTakeOverPublishesAccountClaimed verifies the takeover publishes
// federation.AccountClaimed with the claimed subject, once, and that a plain
// first login does not.
func TestTakeOverPublishesAccountClaimed(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		var claimed []string
		bus := event.New()
		if _, err := bus.Listen(federation.AccountClaimed, func(_ context.Context, claim federation.UserEvent) error {
			claimed = append(claimed, claim.Subject)

			return nil
		}); err != nil {
			t.Fatalf("Listen: %v", err)
		}
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Events(bus)
			iam.Password(hasher(), registration)
			iam.Provider(fakeProvider{name: "fake"}, signUp)
		})
		sys.client.NewDevice().Do(http.MethodGet, callbackPath("fake", "fresh", "new@example.com", true), "")
		if len(claimed) != 0 {
			t.Fatalf("AccountClaimed after a first login = %v, want none", claimed)
		}
		register(t, sys.client, "ada@example.com")
		squatted := strings.SplitN(sys.client.Do(http.MethodGet, "/me", "").Body, "|", 2)[0]
		sys.client.NewDevice().Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), "")
		if len(claimed) != 1 || claimed[0] != squatted {
			t.Fatalf("AccountClaimed = %v, want [%s]", claimed, squatted)
		}
	})
}

// TestTakeOverNeverReenablesADisabledUser verifies a disabled unverified user
// stays disabled and untouched: the login is refused and the takeover rolls
// back.
func TestTakeOverNeverReenablesADisabledUser(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher(), registration)
			iam.Provider(fakeProvider{name: "fake"})
		})
		register(t, sys.client, "ada@example.com")
		subject := strings.SplitN(sys.client.Do(http.MethodGet, "/me", "").Body, "|", 2)[0]
		if err := sys.iam.Directory().Disable(testContext(t), subject); err != nil {
			t.Fatalf("Disable: %v", err)
		}
		device := sys.client.NewDevice()
		if reply := device.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("callback = %d %s, want 401", reply.Status, reply.Body)
		}
		if verified, _ := sys.iam.Directory().Verified(testContext(t), subject); verified {
			t.Fatal("the refused takeover verified the disabled user")
		}
		if has, _ := sys.iam.Verifier().Has(testContext(t), subject); !has {
			t.Fatal("the refused takeover deleted the disabled user's password")
		}
	})
}

// TestInvitedUserIsClaimedByTheirFirstProviderLogin verifies the invite-only
// flow: a user the application created without a password signs in with a
// provider that verified their email, with sign-up off, and keeps signing in
// through the connection.
func TestInvitedUserIsClaimedByTheirFirstProviderLogin(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Provider(fakeProvider{name: "fake"})
		})
		invited, err := sys.iam.Directory().Create(testContext(t), "ada@example.com")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		first := sys.client.NewDevice()
		if reply := first.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", true), ""); reply.Status != http.StatusNoContent {
			t.Fatalf("first callback = %d %s, want 204", reply.Status, reply.Body)
		}
		if me := first.Do(http.MethodGet, "/me", ""); !strings.HasPrefix(me.Body, invited+"|") {
			t.Fatalf("/me = %q, want the invited user %s", me.Body, invited)
		}
		later := sys.client.NewDevice()
		later.Do(http.MethodGet, callbackPath("fake", "g1", "", false), "")
		if me := later.Do(http.MethodGet, "/me", ""); !strings.HasPrefix(me.Body, invited+"|") {
			t.Fatalf("second login /me = %q, want the invited user %s", me.Body, invited)
		}
	})
}
