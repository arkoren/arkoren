package apitoken_test

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"

	"arkoren.dev/ark/iam/apitoken"
	"arkoren.dev/ark/iam/internal/apptest"
)

// system is an issuer wired to a guard and a running server.
type system struct {
	users    *memoryauth.Driver
	provider *auth.UserProvider
	issuer   *apitoken.Issuer
	guard    *auth.Guard
	client   *apptest.Client
}

// newSystem migrates the issuer on db and serves its handlers. Users "1" and
// "2" exist; the scope catalog is posts:read and posts:write, granted to every
// user (apitoken.AnyScope) unless configure says otherwise.
func newSystem(t *testing.T, db *database.Database, instance string, configure ...apitoken.BuilderFunc) *system {
	t.Helper()

	return newSystemOn(t, db, instance, cache.New(memorycache.New()), configure...)
}

// newSystemOn is newSystem over store, which the issuer and the guard share.
func newSystemOn(t *testing.T, db *database.Database, instance string, store *cache.Cache, configure ...apitoken.BuilderFunc) *system {
	t.Helper()

	users := memoryauth.New()
	for _, subject := range []string{"1", "2"} {
		if err := users.Put(auth.UserRecord{Subject: subject, Version: "0"}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	provider, err := auth.NewUserProvider(users)
	if err != nil {
		t.Fatalf("NewUserProvider: %v", err)
	}
	issuer, err := apitoken.NewIssuer(provider, db, store, append([]apitoken.BuilderFunc{func(tokens *apitoken.Builder) {
		tokens.Instance(instance)
		tokens.Grants(apitoken.AnyScope, "posts:read", "posts:write")
	}}, configure...)...)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	migrateUp(t, db, issuer.Migrations()...)
	guard, err := auth.NewGuard(provider, store)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	client := apptest.Serve(t, store, []auth.Authenticator{guard.Authenticator(), issuer.Authenticator()}, func(r *router.Router) {
		r.Post("/dev-login/{subject}", func(w http.ResponseWriter, r *http.Request) error {
			outcome, err := guard.Begin(r, r.PathValue("subject"), auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
			if err != nil {
				return err
			}

			return outcome.Response(w)
		})
		r.Get("/tokens", issuer.ListHandler())
		if issuer.PersonalTokens() {
			r.Post("/tokens", issuer.CreateHandler(guard))
		}
		r.Delete("/tokens/{id}", issuer.DeleteHandler())
		if issuer.DeviceTokens() {
			r.Post("/device-tokens", issuer.ExchangeHandler(guard))
			r.Delete("/device-tokens/current", issuer.DeleteCurrentHandler())
		}

		r.Get("/whoami", func(w http.ResponseWriter, r *http.Request) error {
			identity, ok := auth.FromContext(r.Context())
			if !ok {
				_, err := fmt.Fprint(w, "anonymous")

				return err
			}
			_, err := fmt.Fprintf(w, "%s|%s|%d|%t", identity.Subject(), strings.Join(identity.Scopes(), ","), identity.Level(), identity.Session())

			return err
		})
		r.With(guard.RequireFresh(time.Minute)).Get("/fresh", func(http.ResponseWriter, *http.Request) error { return nil })
	})

	return &system{users: users, provider: provider, issuer: issuer, guard: guard, client: client}
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

// createToken creates a personal token through the HTTP route and returns its
// id and secret text.
func createToken(t *testing.T, client *apptest.Client, body string) (string, string) {
	t.Helper()

	reply := client.Post("/tokens", body)
	if reply.Status != http.StatusCreated {
		t.Fatalf("create token = %d %s", reply.Status, reply.Body)
	}
	created := apptest.JSON[struct {
		Token  apitoken.Token `json:"token"`
		Secret string         `json:"secret"`
	}](t, reply)

	return created.Token.ID, created.Secret
}

// bearer returns the Authorization header of a token text.
func bearer(secret string) string {
	return "Authorization: Bearer " + secret
}

// whoami renders the expected /whoami body.
func whoami(subject, scopes string, level int, session bool) string {
	return subject + "|" + scopes + "|" + strconv.Itoa(level) + "|" + strconv.FormatBool(session)
}
