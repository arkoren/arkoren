package federation_test

import (
	"context"
	"errors"
	"net/http"
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

// TestResolveUnknownWithoutSignUpIs403 verifies the no-account problem.
func TestResolveUnknownWithoutSignUpIs403(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance, false, false)
		reply := callback(sys.client, "nobody")
		if reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "federation:no-account") {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
	})
}

// TestResolveUnknownWithCreateButNoSignUpCreatesNothing verifies a Create
// function alone does not open sign-up: the login is refused and no user is
// created.
func TestResolveUnknownWithCreateButNoSignUpCreatesNothing(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance+"_x", false, false)
		created := false
		links, err := federation.NewLinks(sys.provider, db, func(links *federation.Builder) {
			links.Instance(instance + "_x").Create(func(context.Context, auth.External) (string, error) {
				created = true

				return "new", nil
			})
		})
		if err != nil {
			t.Fatalf("NewLinks: %v", err)
		}
		_, err = links.Resolve(testContext(t), auth.External{Issuer: google, Subject: "g1"})
		if !errors.Is(err, federation.ErrNoAccount) || created {
			t.Fatalf("Resolve = %v, created %v; want ErrNoAccount and no user", err, created)
		}
	})
}

// TestSignUpRequiresCreate verifies SignUp(true) without a Create function is
// an invalid configuration.
func TestSignUpRequiresCreate(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		sys := newSystem(t, db, instance+"_x", false, false)
		_, err := federation.NewLinks(sys.provider, db, func(links *federation.Builder) {
			links.Instance(instance).SignUp(true)
		})
		if !errors.Is(err, federation.ErrInvalidConfig) {
			t.Fatalf("NewLinks error = %v, want ErrInvalidConfig", err)
		}
	})
}

// TestResolveKnownIdentityLogsInWithoutSignUp verifies sign-up off does not
// affect an identity that is already connected.
func TestResolveKnownIdentityLogsInWithoutSignUp(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, false, false)
		if err := sys.links.Link(ctx, "1", auth.External{Issuer: google, Subject: "g1"}); err != nil {
			t.Fatalf("Link: %v", err)
		}
		subject, err := sys.links.Resolve(ctx, auth.External{Issuer: google, Subject: "g1"})
		if err != nil || subject != "1" {
			t.Fatalf("Resolve = %q, %v; want user 1", subject, err)
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
		reply := sys.client.Post("/callback", `{"issuer":"`+google+`","subject":"g1","email":"taken@example.com","email_verified":true}`)
		if reply.Status != http.StatusConflict {
			t.Fatalf("callback = %d %s", reply.Status, reply.Body)
		}
		if _, err := sys.links.Lookup(ctx, google, "g1"); !errors.Is(err, federation.ErrNotLinked) {
			t.Fatalf("Lookup after a failed create error = %v", err)
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
				subject, err := sys.links.Resolve(ctx, auth.External{Issuer: google, Subject: "race", Email: "race@example.com", EmailVerified: true})
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

// TestResolveRefusesSignUpWithoutProvenEmail verifies a first login whose
// identity has no verified email, or no email at all, creates nothing and
// answers 403 with the stable email-unverified problem.
func TestResolveRefusesSignUpWithoutProvenEmail(t *testing.T) {
	t.Parallel()

	eachDatabase(t, func(t *testing.T, db *database.Database, instance string) {
		ctx := testContext(t)
		sys := newSystem(t, db, instance, true, false)

		for _, body := range []string{
			`{"issuer":"` + google + `","subject":"g1","email":"ada@example.com","email_verified":false}`,
			`{"issuer":"` + google + `","subject":"g1","email_verified":true}`,
			`{"issuer":"` + google + `","subject":"g1"}`,
		} {
			reply := sys.client.NewDevice().Post("/callback", body)

			if reply.Status != http.StatusForbidden || !strings.Contains(reply.Body, "ark/access:federation:email-unverified") {
				t.Fatalf("callback %s = %d %s, want 403 email-unverified", body, reply.Status, reply.Body)
			}
		}

		if _, err := sys.links.Lookup(ctx, google, "g1"); !errors.Is(err, federation.ErrNotLinked) {
			t.Fatalf("Lookup after refused sign-ups = %v, want ErrNotLinked", err)
		}
	})
}
