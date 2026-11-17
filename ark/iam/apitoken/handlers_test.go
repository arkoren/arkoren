package apitoken_test

import (
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam/apitoken"
)

// TestExchangeEndsSessionLogin verifies the exchange trades a fresh login for a
// token and ends the session.
func TestExchangeEndsSessionLogin(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		device := sys.login(t, "1")
		reply := device.Post("/device-tokens", `{"device":"Pixel 9"}`)
		if reply.Status != http.StatusCreated {
			t.Fatalf("exchange = %d %s", reply.Status, reply.Body)
		}
		if after := device.Do(http.MethodGet, "/whoami", ""); after.Body != "anonymous" {
			t.Fatalf("whoami after exchange = %q, want the session ended", after.Body)
		}
		secret := between(reply.Body, `"secret":"`, `"`)
		if who := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", bearer(secret)); !strings.HasPrefix(who.Body, "1|") {
			t.Fatalf("whoami with the device token = %q", who.Body)
		}
	})
}

// TestExchangeRequiresSessionLogin verifies a token cannot be traded for a
// device token and an anonymous request is refused.
func TestExchangeRequiresSessionLogin(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		_, secret, _ := sys.issuer.Issue(ctx, "1", apitoken.Request{Name: "a"})
		if reply := sys.client.NewDevice().Do(http.MethodPost, "/device-tokens", `{"device":"x"}`, bearer(secret)); reply.Status != http.StatusUnauthorized {
			t.Fatalf("exchange with a token = %d, want 401", reply.Status)
		}
		if reply := sys.client.NewDevice().Post("/device-tokens", `{"device":"x"}`); reply.Status != http.StatusUnauthorized {
			t.Fatalf("anonymous exchange = %d, want 401", reply.Status)
		}
	})
}

// TestDeleteCurrentRevokesPresentedToken verifies a device logs out by revoking
// its own token and a session cannot use the route.
func TestDeleteCurrentRevokesPresentedToken(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		_, secret, _ := sys.issuer.IssueDevice(ctx, "1", "Pixel 9")
		_, other, _ := sys.issuer.IssueDevice(ctx, "1", "iPad")
		if reply := sys.login(t, "1").Do(http.MethodDelete, "/device-tokens/current", ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("session DELETE current = %d, want 401", reply.Status)
		}
		if reply := sys.client.NewDevice().Do(http.MethodDelete, "/device-tokens/current", "", bearer(secret)); reply.Status != http.StatusNoContent {
			t.Fatalf("DELETE current = %d", reply.Status)
		}
		if reply := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", bearer(secret)); reply.Status != http.StatusUnauthorized {
			t.Fatalf("revoked token whoami = %d", reply.Status)
		}
		if reply := sys.client.NewDevice().Do(http.MethodGet, "/whoami", "", bearer(other)); reply.Status != http.StatusOK {
			t.Fatalf("the other device's token whoami = %d", reply.Status)
		}
	})
}

// TestListAndDeleteOwnTokens verifies the list, delete and the 404 for
// another user's token.
func TestListAndDeleteOwnTokens(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance)
		first := sys.login(t, "1")
		second := sys.login(t, "2")
		id, _ := createToken(t, first, `{"name":"mine","scopes":["posts:read"]}`)
		if reply := first.Do(http.MethodGet, "/tokens", ""); !strings.Contains(reply.Body, `"name":"mine"`) || strings.Contains(reply.Body, "secret") {
			t.Fatalf("list = %d %s", reply.Status, reply.Body)
		}
		if reply := second.Do(http.MethodDelete, "/tokens/"+id, ""); reply.Status != http.StatusNotFound {
			t.Fatalf("deleting another user's token = %d, want 404", reply.Status)
		}
		if reply := first.Do(http.MethodDelete, "/tokens/"+id, ""); reply.Status != http.StatusNoContent {
			t.Fatalf("delete = %d", reply.Status)
		}
		if reply := first.Do(http.MethodDelete, "/tokens/"+id, ""); reply.Status != http.StatusNotFound {
			t.Fatalf("second delete = %d, want 404", reply.Status)
		}
	})
}

// TestHandlersRejectTokenIdentity verifies a token can neither list, create nor
// delete tokens, and is never fresh.
func TestHandlersRejectTokenIdentity(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance)
		token, secret, _ := sys.issuer.Issue(ctx, "1", apitoken.Request{Name: "a", Scopes: []string{"posts:write"}})
		header := bearer(secret)
		for _, route := range []struct{ method, path, body string }{
			{http.MethodGet, "/tokens", ""},
			{http.MethodPost, "/tokens", `{"name":"b"}`},
			{http.MethodDelete, "/tokens/" + token.ID, ""},
			{http.MethodGet, "/fresh", ""},
		} {
			if reply := sys.client.NewDevice().Do(route.method, route.path, route.body, header); reply.Status < 400 {
				t.Errorf("%s %s with a token = %d, want an error", route.method, route.path, reply.Status)
			}
		}
	})
}

// TestDisabledTokenKindHandlersPanic verifies the handlers of a disabled kind
// fail at composition and those of the enabled kind do not.
func TestDisabledTokenKindHandlersPanic(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		personalOnly := newSystem(t, db, instance+"_a", func(tokens *apitoken.Builder) { tokens.DeviceTokens(false) })
		deviceOnly := newSystem(t, db, instance+"_b", func(tokens *apitoken.Builder) { tokens.PersonalTokens(false) })
		personalOnly.issuer.CreateHandler(personalOnly.guard)
		deviceOnly.issuer.ExchangeHandler(deviceOnly.guard)
		deviceOnly.issuer.DeleteCurrentHandler()
		for name, build := range map[string]func(){
			"ExchangeHandler without device tokens":      func() { personalOnly.issuer.ExchangeHandler(personalOnly.guard) },
			"DeleteCurrentHandler without device tokens": func() { personalOnly.issuer.DeleteCurrentHandler() },
			"CreateHandler without personal tokens":      func() { deviceOnly.issuer.CreateHandler(deviceOnly.guard) },
		} {
			if !panics(build) {
				t.Errorf("%s did not panic", name)
			}
		}
	})
}

// panics reports whether f panics.
func panics(f func()) (panicked bool) {
	defer func() {
		panicked = recover() != nil
	}()
	f()

	return false
}
