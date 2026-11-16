package federation_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/problem"

	"arkoren.dev/ark/access/federation"
	"arkoren.dev/ark/access/internal/apptest"
)

// errEmailTaken is what the test create function returns for the address
// "taken@example.com", as a directory would for an address in use.
var errEmailTaken = problem.Conflict()

// federatedMethod is the method protocol modules record for an external login.
const federatedMethod auth.Method = "federated"

// google is the issuer of the test identities.
const google = "https://accounts.google.com"

// system is a link store wired to a guard and a running server.
type system struct {
	users    *memoryauth.Driver
	provider *auth.UserProvider
	links    *federation.Links
	guard    *auth.Guard
	client   *apptest.Client

	mu      sync.Mutex
	created []string
}

// create is the Builder.Create function of the tests: it adds a user.
func (sys *system) create(_ context.Context, external auth.External) (string, error) {
	if external.Email == "taken@example.com" {
		return "", errEmailTaken
	}
	sys.mu.Lock()
	defer sys.mu.Unlock()

	subject := "new" + strconv.Itoa(len(sys.created)+1)
	sys.created = append(sys.created, subject)

	return subject, sys.users.Put(auth.UserRecord{Subject: subject, Version: "0"})
}

// newSystem migrates the links on db and serves their handlers. Users "1" and
// "2" exist. withCreate enables creation of users for unknown identities and
// hasFallback is the answer of the Fallback function.
func newSystem(t *testing.T, db *database.Database, instance string, withCreate, hasFallback bool) *system {
	t.Helper()

	sys := &system{users: memoryauth.New()}
	for _, subject := range []string{"1", "2"} {
		if err := sys.users.Put(auth.UserRecord{Subject: subject, Version: "0"}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	var err error
	sys.provider, err = auth.NewUserProvider(sys.users)
	if err != nil {
		t.Fatalf("NewUserProvider: %v", err)
	}
	sys.links, err = federation.NewLinks(sys.provider, db, func(links *federation.Builder) {
		links.Instance(instance)
		if withCreate {
			links.SignUp(true).Create(sys.create)
		}
		links.Fallback(func(context.Context, string) (bool, error) { return hasFallback, nil })
	})
	if err != nil {
		t.Fatalf("NewLinks: %v", err)
	}
	migrateUp(t, db, sys.links.Migrations()...)
	store := cache.New(memorycache.New())
	sys.guard, err = auth.NewGuard(sys.provider, store)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	bearer := auth.Bearer(func(_ context.Context, token string) (auth.Identity, error) {
		return auth.NewIdentity("1"), nil
	})
	sys.client = apptest.Serve(t, store, []auth.Authenticator{sys.guard.Authenticator(), bearer}, func(r *router.Router) {
		r.Post("/dev-login/{subject}", func(w http.ResponseWriter, r *http.Request) error {
			outcome, err := sys.guard.Begin(r, r.PathValue("subject"), auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
			if err != nil {
				return err
			}

			return outcome.Response(w)
		})
		// The callback route stands in for a protocol module: it resolves the
		// verified identity with the request context and begins the login.
		r.Post("/callback", func(w http.ResponseWriter, r *http.Request) error {
			var body struct {
				Issuer        string `json:"issuer"`
				Subject       string `json:"subject"`
				Email         string `json:"email"`
				EmailVerified bool   `json:"email_verified"`
			}
			if err := json.UnmarshalRead(r.Body, &body); err != nil {
				return err
			}
			subject, err := sys.links.Resolve(r.Context(), auth.External{
				Issuer: body.Issuer, Subject: body.Subject, Email: body.Email, EmailVerified: body.EmailVerified,
			})
			if err != nil {
				return err
			}
			outcome, err := sys.guard.Begin(r, subject, auth.Factor{Method: federatedMethod, Kind: auth.KindFederated})
			if err != nil {
				return err
			}

			return outcome.Response(w)
		})
		r.Post("/connections/{provider}", sys.links.ConnectHandler(sys.guard, map[string]string{"google": "/auth/google"}))
		r.Get("/connections", sys.links.ListHandler())
		r.Delete("/connections/{id}", sys.links.DeleteHandler(sys.guard))
		r.Get("/me", func(w http.ResponseWriter, r *http.Request) error {
			identity, ok := auth.FromContext(r.Context())
			if !ok {
				_, err := fmt.Fprint(w, "anonymous")

				return err
			}
			_, err := fmt.Fprint(w, identity.Subject())

			return err
		})
	})

	return sys
}

// callback posts a verified Google identity to the callback route of client.
func callback(client *apptest.Client, subject string) apptest.Reply {
	return client.Post("/callback", `{"issuer":"`+google+`","subject":"`+subject+`"}`)
}

// login logs subject in on a fresh device and returns it.
func (sys *system) login(t *testing.T, subject string) *apptest.Client {
	t.Helper()

	device := sys.client.NewDevice()
	if reply := device.Post("/dev-login/"+subject, ""); reply.Status != http.StatusNoContent {
		t.Fatalf("dev login = %d %s", reply.Status, reply.Body)
	}

	return device
}
