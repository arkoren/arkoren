package access_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access"
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
			iam.Provider(fakeProvider{name: "fake"})
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

// TestProviderUnverifiedEmailCreatesUnverifiedUser verifies EmailVerified
// decides whether the new user's email counts as verified.
func TestProviderUnverifiedEmailCreatesUnverifiedUser(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Provider(fakeProvider{name: "fake"})
		})
		device := sys.client.NewDevice()
		device.Do(http.MethodGet, callbackPath("fake", "g1", "ada@example.com", false), "")
		subject := strings.SplitN(device.Do(http.MethodGet, "/me", "").Body, "|", 2)[0]
		record, _ := sys.iam.UserProvider().Find(testContext(t), subject)
		if record.Claims["email_verified"] != false {
			t.Fatalf("claims = %v, want an unverified email", record.Claims)
		}
	})
}

// TestProviderNeverLinksByEmail verifies a provider identity with the email of
// an existing user is a conflict, not a login to that user.
func TestProviderNeverLinksByEmail(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
			iam.Provider(fakeProvider{name: "fake"})
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
			iam.Password(hasher())
			iam.Provider(fakeProvider{name: "fake"})
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
			iam.Provider(fakeProvider{name: "fake"})
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
			iam.Password(hasher())
			iam.Provider(fakeProvider{name: "fake"})
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
		iam.Provider(fakeProvider{name: "web"}, nativeProvider{fakeProvider{name: "phone"}})
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
		iam.Password(hasher())
	})
	if _, err := sys.iam.Resolve(context.Background(), auth.External{Issuer: "x", Subject: "y"}); err == nil {
		t.Fatal("Resolve without providers succeeded")
	}
}
