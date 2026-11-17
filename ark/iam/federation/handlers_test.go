package federation_test

import (
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"
)

// TestConnectLinksCurrentUser verifies the connect flow links the identity to
// the logged-in user, keeps their session and is single use.
func TestConnectLinksCurrentUser(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, true, true)
		device := sys.login(t, "1")
		connect := device.Post("/connections/google", "")
		if connect.Status != http.StatusSeeOther || connect.Header.Get("Location") != "/auth/google" {
			t.Fatalf("connect = %d location %q", connect.Status, connect.Header.Get("Location"))
		}
		if reply := callback(device, "g1"); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
		if subject, err := sys.links.Lookup(ctx, google, "g1"); err != nil || subject != "1" {
			t.Fatalf("Lookup = %q, %v; want the logged-in user", subject, err)
		}
		if reply := device.Do(http.MethodGet, "/me", ""); reply.Body != "1" {
			t.Fatalf("/me = %q, want the session kept", reply.Body)
		}
		// The intent is spent: another unknown identity creates a new user.
		callback(device, "g2")
		if subject, _ := sys.links.Lookup(ctx, google, "g2"); subject == "1" {
			t.Fatal("a second callback reused the spent connect intent")
		}
	})
}

// TestConnectLinkedElsewhereConflicts verifies connecting an identity that
// belongs to another user is 409 and still spends the intent.
func TestConnectLinkedElsewhereConflicts(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, true)
		_ = sys.links.Link(ctx, "2", auth.External{Issuer: google, Subject: "g1"})
		device := sys.login(t, "1")
		device.Post("/connections/google", "")
		if reply := callback(device, "g1"); reply.Status != http.StatusConflict {
			t.Fatalf("callback = %d %s, want 409", reply.Status, reply.Body)
		}
		if reply := callback(device, "g1"); reply.Status != http.StatusNoContent || device.Do(http.MethodGet, "/me", "").Body != "2" {
			t.Fatalf("after the intent was spent the identity logs in as its owner: %d", reply.Status)
		}
	})
}

// TestConnectUnknownProvider404 verifies the connect handler answers 404.
func TestConnectUnknownProvider404(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, false, true)
		if reply := sys.login(t, "1").Post("/connections/nope", ""); reply.Status != http.StatusNotFound {
			t.Fatalf("connect = %d", reply.Status)
		}
	})
}

// TestHandlersListAndDelete verifies the HTTP routes.
func TestHandlersListAndDelete(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, true)
		_ = sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"})
		device := sys.login(t, "1")
		list := device.Do(http.MethodGet, "/connections", "")
		if list.Status != http.StatusOK || !strings.Contains(list.Body, google) || strings.Contains(list.Body, "g1") {
			t.Fatalf("list = %d %s", list.Status, list.Body)
		}
		listed, _ := sys.links.List(ctx, "1")
		if reply := device.Do(http.MethodDelete, "/connections/"+listed[0].ID, ""); reply.Status != http.StatusNoContent {
			t.Fatalf("delete = %d %s", reply.Status, reply.Body)
		}
	})
}

// TestHandlersRejectBearerIdentity verifies a token identity cannot manage
// connections.
func TestHandlersRejectBearerIdentity(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, false, true)
		header := "Authorization: Bearer anything"
		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/connections"},
			{http.MethodPost, "/connections/google"},
			{http.MethodDelete, "/connections/x"},
		} {
			if reply := sys.client.Do(route.method, route.path, "", header); reply.Status != http.StatusUnauthorized {
				t.Errorf("%s %s with a bearer token = %d, want 401", route.method, route.path, reply.Status)
			}
		}
	})
}
