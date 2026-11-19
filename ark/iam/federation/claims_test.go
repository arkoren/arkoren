package federation_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	sqlite "arkoren.dev/driver/sqlite/sqlitedatabase"
	"arkoren.dev/foundation/query/schema"

	"arkoren.dev/ark/iam/federation"
	"arkoren.dev/ark/iam/internal/apptest"
	"arkoren.dev/ark/iam/internal/storage"
)

// claimsSystem is a system with sign-up on whose resolver keeps names.
func claimsSystem(t *testing.T, db *database.Database, instance string, names ...string) *system {
	t.Helper()

	sys := newSystem(t, db, instance, true, true)
	resolve, err := sys.links.Resolver(func(resolver *federation.ResolverBuilder) {
		resolver.KeepClaims(names...)
	})
	if err != nil {
		t.Fatalf("Resolver: %v", err)
	}
	sys.resolve = resolve

	return sys
}

// callbackClaims posts a verified Google identity that carries the JSON object
// claims to the callback route of client.
func callbackClaims(client *apptest.Client, subject, claims string) apptest.Reply {
	return client.Post("/callback", `{"issuer":"`+google+`","subject":"`+subject+`","email":"`+subject+`@example.com","email_verified":true,"claims":`+claims+`}`)
}

// claimsOf returns the kept claims of the only connection of subject.
func claimsOf(t *testing.T, sys *system, subject string) map[string]any {
	t.Helper()

	listed, err := sys.links.List(testContext(t), subject)
	if err != nil || len(listed) != 1 {
		t.Fatalf("List(%s) = %+v, %v; want one connection", subject, listed, err)
	}

	return listed[0].Claims
}

// TestKeepClaimsSignUpStoresOnlyTheAllowlist verifies a first login stores the
// named claims and nothing else.
func TestKeepClaimsSignUpStoresOnlyTheAllowlist(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := claimsSystem(t, db, instance, "name", "locale")
		if reply := callbackClaims(sys.client.NewDevice(), "g9", `{"name":"Ada","picture":"https://p.example/ada.png","locale":"en"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
		want := map[string]any{"name": "Ada", "locale": "en"}
		if got := claimsOf(t, sys, "new1"); !reflect.DeepEqual(got, want) {
			t.Fatalf("claims = %v, want %v", got, want)
		}
	})
}

// TestKeepClaimsNothingByDefault verifies the default resolver and Links.Resolve
// keep no claims, whatever the provider sends.
func TestKeepClaimsNothingByDefault(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, true, true)
		if reply := callbackClaims(sys.client.NewDevice(), "g9", `{"name":"Ada"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
		if got := claimsOf(t, sys, "new1"); got != nil {
			t.Fatalf("claims = %v, want none", got)
		}
	})
}

// TestKeepClaimsRefusesEmptyAndProtocolNames verifies an empty name and each
// protocol claim make Resolver fail with ErrInvalidConfig.
func TestKeepClaimsRefusesEmptyAndProtocolNames(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, true, true)
		for _, name := range []string{"", "iss", "sub", "aud", "exp", "iat", "nbf", "nonce", "at_hash", "auth_time", "scope", "azp", "acr", "amr"} {
			_, err := sys.links.Resolver(func(resolver *federation.ResolverBuilder) {
				resolver.KeepClaims("name", name)
			})
			if !errors.Is(err, federation.ErrInvalidConfig) {
				t.Errorf("KeepClaims(%q) = %v, want ErrInvalidConfig", name, err)
			}
		}
	})
}

// TestKeepClaimsDropsStructuredValues verifies scalars and lists of strings are
// kept, objects, nested lists and mixed lists are dropped, and each dropped
// claim logs one warning naming it.
func TestKeepClaimsDropsStructuredValues(t *testing.T) {
	warnings := apptest.CaptureWarnings(t)

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := claimsSystem(t, db, instance, "name", "age", "admin", "groups", "address", "mixed")
		claims := `{"name":"Ada","age":36,"admin":true,"groups":["a","b"],"address":{"city":"London"},"mixed":["a",1]}`
		if reply := callbackClaims(sys.client.NewDevice(), "g9", claims); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
		want := map[string]any{"name": "Ada", "age": 36.0, "admin": true, "groups": []any{"a", "b"}}
		if got := claimsOf(t, sys, "new1"); !reflect.DeepEqual(got, want) {
			t.Fatalf("claims = %v, want %v", got, want)
		}
	})
	dropped := warnings.Attrs("claim")
	slices.Sort(dropped)
	// Each database the scenario runs on logs the same two claims once.
	if got := slices.Compact(dropped); !slices.Equal(got, []string{"address", "mixed"}) {
		t.Fatalf("warned claims = %v, want address and mixed", got)
	}
}

// TestKeepClaimsOverTheSizeCapAreSkipped verifies a login whose merged claims
// would exceed 8 KiB leaves the stored claims as they were and still succeeds.
func TestKeepClaimsOverTheSizeCapAreSkipped(t *testing.T) {
	warnings := apptest.CaptureWarnings(t)

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := claimsSystem(t, db, instance, "name", "bio")
		if reply := callbackClaims(sys.client.NewDevice(), "g9", `{"name":"Ada"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("first callback = %d %s", reply.Status, reply.Body)
		}
		big := strings.Repeat("x", 9<<10)
		if reply := callbackClaims(sys.client.NewDevice(), "g9", `{"name":"Grace","bio":"`+big+`"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("second callback = %d %s", reply.Status, reply.Body)
		}
		want := map[string]any{"name": "Ada"}
		if got := claimsOf(t, sys, "new1"); !reflect.DeepEqual(got, want) {
			t.Fatalf("claims = %v, want %v untouched", got, want)
		}
	})
	if len(warnings.Attrs("reason")) == 0 {
		t.Fatal("no warning for the skipped update")
	}
}

// TestKeepClaimsLoginMergesAndKeepsMissing verifies the Apple case: a later
// login without the name keeps it, a login that carries a claim overwrites it,
// and a claim the provider stops sending stays.
func TestKeepClaimsLoginMergesAndKeepsMissing(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := claimsSystem(t, db, instance, "name", "locale")
		steps := []struct {
			sent string
			want map[string]any
		}{
			{`{"name":"Ada","locale":"en"}`, map[string]any{"name": "Ada", "locale": "en"}},
			{`{"locale":"es"}`, map[string]any{"name": "Ada", "locale": "es"}},
			{`{}`, map[string]any{"name": "Ada", "locale": "es"}},
			{`{"name":"Ada L"}`, map[string]any{"name": "Ada L", "locale": "es"}},
		}
		for _, step := range steps {
			if reply := callbackClaims(sys.client.NewDevice(), "g9", step.sent); reply.Status != http.StatusNoContent {
				t.Fatalf("callback %s = %d %s", step.sent, reply.Status, reply.Body)
			}
			if got := claimsOf(t, sys, "new1"); !reflect.DeepEqual(got, step.want) {
				t.Fatalf("after %s claims = %v, want %v", step.sent, got, step.want)
			}
		}
	})
}

// TestKeepClaimsConnectStoresClaims verifies the connect flow stores the claims
// of the identity it links, and the public Link keeps none.
func TestKeepClaimsConnectStoresClaims(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := claimsSystem(t, db, instance, "name")
		device := sys.login(t, "1")
		if connect := device.Post("/connections/google", ""); connect.Status != http.StatusSeeOther {
			t.Fatalf("connect = %d", connect.Status)
		}
		if reply := callbackClaims(device, "g1", `{"name":"Ada","picture":"x"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
		if got, want := claimsOf(t, sys, "1"), map[string]any{"name": "Ada"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("claims = %v, want %v", got, want)
		}
		if err := sys.links.Link(ctx, "2", auth.External{Issuer: google, Subject: "g2", Claims: map[string]any{"name": "Grace"}}); err != nil {
			t.Fatalf("Link: %v", err)
		}
		if got := claimsOf(t, sys, "2"); got != nil {
			t.Fatalf("Link kept claims %v, want none", got)
		}
	})
}

// TestKeepClaimsTakeOverSignUpStoresClaims verifies the connection made by an
// account takeover holds the kept claims.
func TestKeepClaimsTakeOverSignUpStoresClaims(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, false, false)
		links, err := federation.NewLinks(sys.provider, db, func(links *federation.Builder) {
			links.Instance(instance + "_t").TakeOver(func(context.Context, auth.External) (string, error) { return "1", nil })
		})
		if err != nil {
			t.Fatalf("NewLinks: %v", err)
		}
		migrateUp(t, db, links.Migrations()...)
		resolve, err := links.Resolver(func(resolver *federation.ResolverBuilder) { resolver.KeepClaims("name") })
		if err != nil {
			t.Fatalf("Resolver: %v", err)
		}
		external := auth.External{Issuer: google, Subject: "g1", Email: "a@example.com", EmailVerified: true, Claims: map[string]any{"name": "Ada"}}
		if subject, err := resolve(testContext(t), external); err != nil || subject != "1" {
			t.Fatalf("resolve = %q, %v; want the taken-over user", subject, err)
		}
		listed, err := links.List(testContext(t), "1")
		if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0].Claims, map[string]any{"name": "Ada"}) {
			t.Fatalf("List = %+v, %v; want the kept name", listed, err)
		}
	})
}

// TestKeepClaimsConcurrentFirstLoginsStoreOneRow verifies concurrent first
// logins of one identity leave one connection that holds the claims.
func TestKeepClaimsConcurrentFirstLoginsStoreOneRow(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := claimsSystem(t, db, instance, "name")
		var wg sync.WaitGroup
		subjects := make([]string, 4)
		for i := range subjects {
			wg.Go(func() {
				external := auth.External{Issuer: google, Subject: "race", Email: "race@example.com", EmailVerified: true, Claims: map[string]any{"name": "Ada"}}
				subject, err := sys.resolve(testContext(t), external)
				if err != nil {
					t.Errorf("resolve: %v", err)
				}
				subjects[i] = subject
			})
		}
		wg.Wait()
		listed, err := sys.links.List(testContext(t), subjects[0])
		if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0].Claims, map[string]any{"name": "Ada"}) {
			t.Fatalf("List = %+v, %v; want one connection with the claims", listed, err)
		}
		for _, loser := range subjects[1:] {
			if loser != subjects[0] {
				t.Fatalf("subjects = %v, want one", subjects)
			}
		}
	})
}

// TestKeepClaimsFailingUpdateDoesNotFailLogin verifies a login still succeeds
// when the connection row cannot take the update, here because the claims
// column is gone. It runs on SQLite only: PostgreSQL rejects the cached plan of
// a SELECT * after a column drop, which fails the lookup before the update.
func TestKeepClaimsFailingUpdateDoesNotFailLogin(t *testing.T) {
	t.Parallel()

	driver, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	start(t, driver)
	db := database.New(driver)
	instance := newInstance(t)
	sys := claimsSystem(t, db, instance, "name")
	if reply := callbackClaims(sys.client.NewDevice(), "g9", `{"name":"Ada"}`); reply.Status != http.StatusNoContent {
		t.Fatalf("first callback = %d %s", reply.Status, reply.Body)
	}
	drop := schema.Alter(storage.Table(instance, "federation_links"), func(table *schema.Table) { table.DropColumn("claims") })
	if err := migrate.Schema(drop)(testContext(t), db); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if reply := callbackClaims(sys.client.NewDevice(), "g9", `{"name":"Grace"}`); reply.Status != http.StatusNoContent {
		t.Fatalf("second callback = %d %s, want the login to succeed", reply.Status, reply.Body)
	}
}

// TestKeepClaimsGoWithTheConnection verifies unlinking, UnlinkAll and the
// forget hook remove the claims with the row: a fresh connection starts empty.
func TestKeepClaimsGoWithTheConnection(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := claimsSystem(t, db, instance, "name")
		external := auth.External{Issuer: google, Subject: "g1", Email: "a@example.com", EmailVerified: true, Claims: map[string]any{"name": "Ada"}}
		signIn := func() {
			t.Helper()

			if _, err := sys.resolve(ctx, external); err != nil {
				t.Fatalf("resolve: %v", err)
			}
		}
		signIn()
		listed, _ := sys.links.List(ctx, "new1")
		if len(listed) != 1 || listed[0].Claims == nil {
			t.Fatalf("List = %+v, want the claims", listed)
		}
		if err := sys.links.Unlink(ctx, "new1", listed[0].ID); err != nil {
			t.Fatalf("Unlink: %v", err)
		}
		if err := sys.links.Link(ctx, "new1", auth.External{Issuer: google, Subject: "g1"}); err != nil {
			t.Fatalf("Link: %v", err)
		}
		if got := claimsOf(t, sys, "new1"); got != nil {
			t.Fatalf("claims after Unlink = %v, want none", got)
		}
		if err := sys.links.UnlinkAll(ctx, "new1"); err != nil {
			t.Fatalf("UnlinkAll: %v", err)
		}
		if listed, _ := sys.links.List(ctx, "new1"); len(listed) != 0 {
			t.Fatalf("List after UnlinkAll = %+v, want none", listed)
		}
		signIn()
		if err := sys.provider.Forget(ctx, "new1"); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		if listed, _ := sys.links.List(ctx, "new1"); len(listed) != 0 {
			t.Fatalf("List after Forget = %+v, want none", listed)
		}
	})
}

// TestConnectionsShowClaimsOnlyWhenKept verifies GET /connections includes
// "claims" for a connection that holds some and omits it otherwise.
func TestConnectionsShowClaimsOnlyWhenKept(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := claimsSystem(t, db, instance, "name")
		if reply := callbackClaims(sys.client.NewDevice(), "g9", `{"name":"Ada"}`); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
		with := sys.login(t, "new1").Do(http.MethodGet, "/connections", "")
		if with.Status != http.StatusOK || !strings.Contains(with.Body, `"claims":{"name":"Ada"}`) {
			t.Fatalf("GET /connections = %d %s, want the kept claims", with.Status, with.Body)
		}
		if err := sys.links.Link(testContext(t), "2", auth.External{Issuer: google, Subject: "g2"}); err != nil {
			t.Fatalf("Link: %v", err)
		}
		without := sys.login(t, "2").Do(http.MethodGet, "/connections", "")
		if without.Status != http.StatusOK || strings.Contains(without.Body, "claims") {
			t.Fatalf("GET /connections = %d %s, want no claims member", without.Status, without.Body)
		}
	})
}
