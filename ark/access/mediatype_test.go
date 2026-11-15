package access_test

import (
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access"
)

// login is the body of ada's password login.
const login = `{"email":"ada@example.com","password":"correct horse"}`

// TestFormContentTypeIsRejected verifies a cross-site form, which can only send
// a form media type, never reaches a handler that decodes a body.
func TestFormContentTypeIsRejected(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) { iam.Password(hasher()) })
		register(t, sys.client, "ada@example.com")
		for _, contentType := range []string{"application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "text/plain"} {
			reply := sys.client.NewDevice().Do(http.MethodPost, "/login", login, "Content-Type: "+contentType)
			if reply.Status != http.StatusUnsupportedMediaType {
				t.Fatalf("login as %s = %d %s, want 415", contentType, reply.Status, reply.Body)
			}
		}
	})
}

// TestMissingContentTypeIsRejected verifies a request without a Content-Type is
// 415 too, so the check does not depend on the browser sending one.
func TestMissingContentTypeIsRejected(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) { iam.Password(hasher()) })
		register(t, sys.client, "ada@example.com")
		if reply := sys.client.NewDevice().Do(http.MethodPost, "/login", login, "Content-Type: "); reply.Status != http.StatusUnsupportedMediaType {
			t.Fatalf("login without a Content-Type = %d %s, want 415", reply.Status, reply.Body)
		}
	})
}

// TestJSONContentTypeAcceptsParameters verifies a charset parameter and a
// different case do not matter, because the media type is parsed.
func TestJSONContentTypeAcceptsParameters(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) { iam.Password(hasher()) })
		register(t, sys.client, "ada@example.com")
		for _, contentType := range []string{"application/json; charset=utf-8", "Application/JSON"} {
			if reply := sys.client.NewDevice().Do(http.MethodPost, "/login", login, "Content-Type: "+contentType); reply.Status != http.StatusNoContent {
				t.Fatalf("login as %s = %d %s, want 204", contentType, reply.Status, reply.Body)
			}
		}
	})
}

// TestJSONContentTypeIsNotAPrefixMatch verifies a lookalike media type is
// rejected.
func TestJSONContentTypeIsNotAPrefixMatch(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) { iam.Password(hasher()) })
		register(t, sys.client, "ada@example.com")
		if reply := sys.client.NewDevice().Do(http.MethodPost, "/login", login, "Content-Type: application/jsonp"); reply.Status != http.StatusUnsupportedMediaType {
			t.Fatalf("login as application/jsonp = %d, want 415", reply.Status)
		}
	})
}

// TestBodylessRoutesRequireJSONToo verifies POST routes that read no body
// (logout, recovery codes, token creation without a body) are 415 for a form,
// since a form can post to them as easily as to a route with a body.
func TestBodylessRoutesRequireJSONToo(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
			iam.TwoFactor(cipher(t), "Example")
			iam.Tokens()
		})
		registerVerified(t, sys, sys.client, "ada@example.com")
		for _, route := range []struct{ method, path string }{
			{http.MethodPost, "/logout"},
			{http.MethodPost, "/two-factor/setup"},
			{http.MethodPost, "/two-factor/recovery-codes"},
			{http.MethodPost, "/email/verify/resend"},
			{http.MethodPut, "/password"},
		} {
			reply := sys.client.Do(route.method, route.path, "", "Content-Type: application/x-www-form-urlencoded")
			if reply.Status != http.StatusUnsupportedMediaType {
				t.Fatalf("%s %s as a form = %d %s, want 415", route.method, route.path, reply.Status, reply.Body)
			}
		}
		if reply := sys.client.Do(http.MethodGet, "/me", ""); !strings.Contains(reply.Body, "|") {
			t.Fatalf("/me after the rejected logout = %q: the session ended", reply.Body)
		}
	})
}

// TestDeleteRoutesAreNotChecked verifies DELETE passes whatever the media type,
// because a form cannot send it.
func TestDeleteRoutesAreNotChecked(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) { iam.Password(hasher()) })
		registerVerified(t, sys, sys.client, "ada@example.com")
		if reply := sys.client.Do(http.MethodDelete, "/sessions", "", "Content-Type: application/x-www-form-urlencoded"); reply.Status != http.StatusNoContent {
			t.Fatalf("DELETE /sessions as a form = %d %s, want 204", reply.Status, reply.Body)
		}
	})
}

// TestProviderCallbackAcceptsAForm verifies the provider routes are exempt: an
// identity provider answers with a form_post and a redirect, never with JSON.
func TestProviderCallbackAcceptsAForm(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newApp(t, db, instance, func(iam *access.Builder) {
			iam.Password(hasher())
			iam.Provider(fakeProvider{name: "fake"})
		})
		path := callbackPath("fake", "form-user", "form@example.com", true)
		reply := sys.client.Do(http.MethodPost, path, "sub=x", "Content-Type: application/x-www-form-urlencoded")
		if reply.Status != http.StatusNoContent {
			t.Fatalf("POST %s as a form = %d %s, want 204: provider callbacks must stay exempt", path, reply.Status, reply.Body)
		}
	})
}
