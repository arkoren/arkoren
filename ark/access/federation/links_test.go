package federation_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/access/federation"
)

// TestResolveKnownLinkLogsIn verifies a connected identity logs its user in.
func TestResolveKnownLinkLogsIn(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, false)
		if err := sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"}); err != nil {
			t.Fatalf("Link: %v", err)
		}
		device := sys.client.NewDevice()
		if reply := callback(device, "g1"); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
		if reply := device.Do(http.MethodGet, "/me", ""); reply.Body != "1" {
			t.Fatalf("/me = %q", reply.Body)
		}
		listed, _ := sys.links.List(ctx, "1")
		if len(listed) != 1 || listed[0].LastUsedAt.IsZero() {
			t.Fatalf("links = %+v, want last use recorded", listed)
		}
	})
}

// TestResolveDeletedOrDisabledUserIs401 verifies a connection of a user who is
// gone or disabled does not log in.
func TestResolveDeletedOrDisabledUserIs401(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, false)
		_ = sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"})
		_ = sys.links.Link(ctx, "2", auth.External{Issuer: google, Subject: "g2"})
		_ = sys.users.Disable("1")
		sys.users.Delete("2")
		for _, subject := range []string{"g1", "g2"} {
			if reply := callback(sys.client.NewDevice(), subject); reply.Status != http.StatusUnauthorized {
				t.Errorf("callback for %s = %d, want 401", subject, reply.Status)
			}
		}
	})
}

// TestResolveUnknownCreatesUser verifies the first login creates and connects a
// user, and the second finds it.
func TestResolveUnknownCreatesUser(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, true, false)
		first := sys.client.NewDevice()
		if reply := callback(first, "g9"); reply.Status != http.StatusNoContent {
			t.Fatalf("first callback = %d %s", reply.Status, reply.Body)
		}
		if reply := first.Do(http.MethodGet, "/me", ""); reply.Body != "new1" {
			t.Fatalf("/me = %q, want new1", reply.Body)
		}
		second := sys.client.NewDevice()
		callback(second, "g9")
		if reply := second.Do(http.MethodGet, "/me", ""); reply.Body != "new1" || len(sys.created) != 1 {
			t.Fatalf("second login /me = %q with %d users created", reply.Body, len(sys.created))
		}
	})
}

// TestResolveUnknownWithoutCreateIs401 verifies the not-linked problem.
func TestResolveUnknownWithoutCreateIs401(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, false, false)
		reply := callback(sys.client, "nobody")
		if reply.Status != http.StatusUnauthorized || !strings.Contains(reply.Body, "not-linked") {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
	})
}

// TestResolveCreateErrorPassesThrough verifies an email conflict from Create
// reaches the client and creates no link.
func TestResolveCreateErrorPassesThrough(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, true, false)
		reply := sys.client.Post("/callback", `{"issuer":"`+google+`","subject":"g1","email":"taken@example.com"}`)
		if reply.Status != http.StatusConflict {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
		if _, err := sys.links.Lookup(ctx, google, "g1"); !errors.Is(err, federation.ErrNotLinked) {
			t.Fatalf("Lookup after a failed create error = %v", err)
		}
	})
}

// TestLookupNeverCreates verifies Lookup is read-only.
func TestLookupNeverCreates(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, true, false)
		if _, err := sys.links.Lookup(testContext(t), google, "g1"); !errors.Is(err, federation.ErrNotLinked) {
			t.Fatalf("Lookup error = %v", err)
		}
		if len(sys.created) != 0 {
			t.Fatalf("Lookup created users: %v", sys.created)
		}
	})
}

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

// TestLoggedInWithoutIntentDoesNotLink verifies a logged-in user who merely
// signs in with another identity does not get it linked.
func TestLoggedInWithoutIntentDoesNotLink(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, true, true)
		device := sys.login(t, "1")
		if reply := callback(device, "g1"); reply.Status != http.StatusNoContent {
			t.Fatalf("callback = %d", reply.Status)
		}
		if subject, _ := sys.links.Lookup(ctx, google, "g1"); subject == "1" {
			t.Fatal("the identity was linked to the logged-in user without a connect intent")
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

// TestOneConnectionPerIssuer verifies a user cannot hold two identities of one
// issuer.
func TestOneConnectionPerIssuer(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, true)
		if err := sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"}); err != nil {
			t.Fatalf("Link: %v", err)
		}
		if err := sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"}); err != nil {
			t.Fatalf("idempotent Link: %v", err)
		}
		if err := sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g2"}); !errors.Is(err, federation.ErrAlreadyLinked) {
			t.Fatalf("second identity error = %v, want ErrAlreadyLinked", err)
		}
		if err := sys.links.Link(ctx, "2", auth.External{Issuer: google, Subject: "g1"}); !errors.Is(err, federation.ErrLinkedElsewhere) {
			t.Fatalf("stolen identity error = %v, want ErrLinkedElsewhere", err)
		}
	})
}

// TestConcurrentFirstLoginOneLink verifies concurrent first logins of one
// identity agree on one user.
func TestConcurrentFirstLoginOneLink(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, true, false)
		var wg sync.WaitGroup
		subjects := make([]string, 4)
		for i := range subjects {
			wg.Go(func() {
				subject, err := sys.links.Resolve(ctx, auth.External{Issuer: google, Subject: "race"})
				if err != nil {
					t.Errorf("Resolve: %v", err)
				}
				subjects[i] = subject
			})
		}
		wg.Wait()
		for _, subject := range subjects[1:] {
			if subject != subjects[0] {
				t.Fatalf("subjects = %v, want one", subjects)
			}
		}
		if listed, _ := sys.links.List(ctx, subjects[0]); len(listed) != 1 {
			t.Fatalf("links = %+v, want one", listed)
		}
	})
}

// TestDeleteLastLink verifies the last connection needs a fallback.
func TestDeleteLastLink(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		for _, hasFallback := range []bool{false, true} {
			sys := newSystem(t, db, instance+map[bool]string{false: "_n", true: "_y"}[hasFallback], false, hasFallback)
			_ = sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"})
			listed, _ := sys.links.List(ctx, "1")
			err := sys.links.Unlink(ctx, "1", listed[0].ID)
			if hasFallback && err != nil {
				t.Errorf("Unlink with a fallback: %v", err)
			}
			if !hasFallback && !errors.Is(err, federation.ErrLastLink) {
				t.Errorf("Unlink without a fallback error = %v, want ErrLastLink", err)
			}
		}
	})
}

// TestConcurrentUnlinkKeepsOneWayIn verifies two concurrent unlinks of a
// user's two connections, with no other way to log in, cannot both succeed:
// each would see the other still present if the rows were not locked.
func TestConcurrentUnlinkKeepsOneWayIn(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, false)
		for round := range 10 {
			subject := "r" + strconv.Itoa(round)
			_ = sys.links.Link(ctx, subject, auth.External{Issuer: google, Subject: "g" + subject})
			_ = sys.links.Link(ctx, subject, auth.External{Issuer: "https://github.com", Subject: "h" + subject})
			listed, _ := sys.links.List(ctx, subject)
			errs := make([]error, len(listed))
			var wg sync.WaitGroup
			for i, link := range listed {
				wg.Go(func() {
					errs[i] = sys.links.Unlink(ctx, subject, link.ID)
				})
			}
			wg.Wait()
			succeeded := 0
			for _, err := range errs {
				if err == nil {
					succeeded++
				} else if !errors.Is(err, federation.ErrLastLink) {
					t.Fatalf("round %d: Unlink error = %v", round, err)
				}
			}
			if remaining, _ := sys.links.List(ctx, subject); succeeded != 1 || len(remaining) != 1 {
				t.Fatalf("round %d: %d unlinks succeeded and %d connections remain, want 1 and 1", round, succeeded, len(remaining))
			}
		}
	})
}

// TestUnlinkAllRemovesEveryConnection verifies the purge a reset uses.
func TestUnlinkAllRemovesEveryConnection(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, false)
		_ = sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"})
		_ = sys.links.Link(ctx, "1", auth.External{Issuer: "https://github.com", Subject: "h1"})
		_ = sys.links.Link(ctx, "2", auth.External{Issuer: google, Subject: "g2"})
		if err := sys.links.UnlinkAll(ctx, "1"); err != nil {
			t.Fatalf("UnlinkAll: %v", err)
		}
		if err := sys.links.UnlinkAll(ctx, "1"); err != nil {
			t.Fatalf("second UnlinkAll: %v", err)
		}
		if listed, _ := sys.links.List(ctx, "1"); len(listed) != 0 {
			t.Fatalf("connections = %+v, want none", listed)
		}
		if listed, _ := sys.links.List(ctx, "2"); len(listed) != 1 {
			t.Fatalf("another user's connections = %+v, want one", listed)
		}
	})
}

// TestDeleteWithTwoLinksKeepsOne verifies a user with two connections can drop
// one with no fallback.
func TestDeleteWithTwoLinksKeepsOne(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, false)
		_ = sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"})
		_ = sys.links.Link(ctx, "1", auth.External{Issuer: "https://github.com", Subject: "h1"})
		listed, _ := sys.links.List(ctx, "1")
		if err := sys.links.Unlink(ctx, "1", listed[0].ID); err != nil {
			t.Fatalf("Unlink: %v", err)
		}
		if err := sys.links.Unlink(ctx, "1", listed[0].ID); !errors.Is(err, federation.ErrNotFound) {
			t.Fatalf("second Unlink error = %v, want ErrNotFound", err)
		}
		if err := sys.links.Unlink(ctx, "2", listed[1].ID); !errors.Is(err, federation.ErrNotFound) {
			t.Fatalf("another user's Unlink error = %v, want ErrNotFound", err)
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

// TestForgetHookDeletesLinks verifies forgetting a user removes the links.
func TestForgetHookDeletesLinks(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, true)
		_ = sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"})
		errRollback := errors.New("roll back")
		err := db.WithTransaction(ctx, func(ctx context.Context) error {
			if err := sys.provider.Forget(ctx, "1"); err != nil {
				return err
			}

			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("transaction error = %v", err)
		}
		if _, err := sys.links.Lookup(ctx, google, "g1"); err != nil {
			t.Fatalf("a rolled-back forget deleted the link: %v", err)
		}
		if err := sys.provider.Forget(ctx, "1"); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		if _, err := sys.links.Lookup(ctx, google, "g1"); !errors.Is(err, federation.ErrNotLinked) {
			t.Fatalf("Lookup after Forget error = %v", err)
		}
	})
}

// TestTwoInstancesSeparateTables verifies two instances keep separate links.
func TestTwoInstancesSeparateTables(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		first := newSystem(t, db, instance+"_a", false, true)
		second := newSystem(t, db, instance+"_b", false, true)
		_ = first.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"})
		if _, err := second.links.Lookup(ctx, google, "g1"); !errors.Is(err, federation.ErrNotLinked) {
			t.Fatalf("the second instance sees the first's link: %v", err)
		}
	})
}

// TestPingNamesMigrations verifies Ping reports a missing table.
func TestPingNamesMigrations(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance+"_x", false, true)
		links, err := federation.NewLinks(sys.provider, db, func(links *federation.Builder) {
			links.Instance(instance)
		})
		if err != nil {
			t.Fatalf("NewLinks: %v", err)
		}
		if err := links.Ping(ctx); !errors.Is(err, federation.ErrNotMigrated) {
			t.Fatalf("Ping error = %v, want ErrNotMigrated", err)
		}
		migrateUp(t, db, links.Migrations()...)
		if err := links.Ping(ctx); err != nil {
			t.Fatalf("Ping after migrations: %v", err)
		}
	})
}

// TestNewLinksRejectsBadConfig verifies the configuration errors.
func TestNewLinksRejectsBadConfig(t *testing.T) {
	t.Parallel()

	if _, err := federation.NewLinks(nil, nil); !errors.Is(err, federation.ErrInvalidConfig) {
		t.Fatalf("nil dependencies error = %v", err)
	}
}
