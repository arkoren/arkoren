package password_test

import (
	"context"
	"encoding/json/jsontext"
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
	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/bcrypt/bcrypthash"
	"arkoren.dev/foundation/problem"

	"arkoren.dev/ark/access/internal/apptest"
	"arkoren.dev/ark/access/password"
)

// errTaken is what the fake user store returns for a duplicate identifier.
var errTaken = problem.Conflict()

// fakeUsers is an in-memory user store: the application's own users, as far as
// the password package can tell.
type fakeUsers struct {
	mu       sync.Mutex
	driver   *memoryauth.Driver
	subjects map[string]string
	versions map[string]int
	rotated  []string
}

// newFakeUsers returns an empty store.
func newFakeUsers() *fakeUsers {
	return &fakeUsers{driver: memoryauth.New(), subjects: map[string]string{}, versions: map[string]int{}}
}

// Lookup is [password.Users.Lookup].
func (users *fakeUsers) Lookup(_ context.Context, identifier string) (string, error) {
	users.mu.Lock()
	defer users.mu.Unlock()

	subject, ok := users.subjects[identifier]
	if !ok {
		return "", fmt.Errorf("fake: %w", auth.ErrUserNotFound)
	}

	return subject, nil
}

// Create is [password.Users.Create].
func (users *fakeUsers) Create(_ context.Context, identifier string) (string, error) {
	users.mu.Lock()
	defer users.mu.Unlock()

	if _, ok := users.subjects[identifier]; ok {
		return "", errTaken
	}
	subject := strconv.Itoa(len(users.subjects) + 1)
	users.subjects[identifier] = subject

	return subject, users.driver.Put(auth.UserRecord{Subject: subject, Version: "0"})
}

// Rotate is [password.Users.Rotate]: it bumps the version of the record.
func (users *fakeUsers) Rotate(_ context.Context, subject string) error {
	users.mu.Lock()
	defer users.mu.Unlock()

	users.versions[subject]++
	users.rotated = append(users.rotated, subject)

	return users.driver.Put(auth.UserRecord{Subject: subject, Version: strconv.Itoa(users.versions[subject])})
}

// system is a verifier wired to a guard and a running server.
type system struct {
	users    *fakeUsers
	provider *auth.UserProvider
	hasher   *hash.Hasher
	verifier *password.Verifier
	guard    *auth.Guard
	store    *cache.Cache
	client   *apptest.Client
	sent     *[]password.Message
}

// secondFactor is a SecondFactor credential that accepts the code "ok" for the
// users in enrolled.
func secondFactor(enrolled map[string]bool) auth.Credential {
	return auth.Credential{
		Method: auth.MethodOTP,
		Kind:   auth.KindPossession,
		Enrolled: func(_ context.Context, subject string) (bool, error) {
			return enrolled[subject], nil
		},
		Verify: func(_ *http.Request, _ string, input jsontext.Value) (auth.Factor, error) {
			if string(input) != `{"code":"ok"}` {
				return auth.Factor{}, auth.ErrInvalidCredential
			}

			return auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession}, nil
		},
	}
}

// newSystem migrates the verifier on db and serves its handlers.
func newSystem(t *testing.T, db *database.Database, instance string, second ...auth.Credential) *system {
	t.Helper()

	users := newFakeUsers()
	provider, err := auth.NewUserProvider(users.driver)
	if err != nil {
		t.Fatalf("NewUserProvider: %v", err)
	}
	store := cache.New(memorycache.New())
	hasher := hash.New(bcrypthash.New(func(driver *bcrypthash.Builder) {
		driver.Cost(4)
	}))
	sent := &[]password.Message{}
	verifier, err := password.NewVerifier(provider, db, store, hasher, password.Users{
		Lookup: users.Lookup,
		Create: users.Create,
		Rotate: users.Rotate,
	}, func(passwords *password.Builder) {
		passwords.Instance(instance)
		passwords.Deliver(func(_ context.Context, message password.Message) error {
			*sent = append(*sent, message)

			return nil
		})
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	migrateUp(t, db, verifier.Migrations()...)
	guard, err := auth.NewGuard(provider, store, func(policy *auth.GuardBuilder) {
		policy.StepUp(verifier.Credential())
		policy.SecondFactor(second...)
	})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	client := apptest.Serve(t, store, []auth.Authenticator{guard.Authenticator()}, func(r *router.Router) {
		r.Post("/register", verifier.RegisterHandler(guard))
		r.Post("/login", verifier.LoginHandler(guard))
		r.Post("/login/challenge", guard.ChallengeHandler())
		r.Post("/confirm", guard.ConfirmHandler())
		r.Put("/password", verifier.ChangeHandler(guard))
		r.Post("/password/forgot", verifier.ForgotHandler())
		r.Post("/password/reset", verifier.ResetHandler(guard))
		r.Post("/dev-login/{subject}", func(w http.ResponseWriter, r *http.Request) error {
			factor := auth.Factor{Method: "federated", Kind: auth.KindFederated}
			outcome, err := guard.Begin(r, r.PathValue("subject"), factor)
			if err != nil {
				return err
			}

			return outcome.Response(w)
		})
		r.Get("/me", func(w http.ResponseWriter, r *http.Request) error {
			identity, ok := auth.FromContext(r.Context())
			if !ok {
				return auth.ErrUnauthenticated
			}
			_, werr := w.Write([]byte(identity.Subject()))

			return werr
		})
	})

	return &system{users: users, provider: provider, hasher: hasher, verifier: verifier, guard: guard, store: store, client: client, sent: sent}
}

// registerAda registers ada@example.com on the client and fails unless it is
// logged in.
func (sys *system) registerAda(t *testing.T, client *apptest.Client) {
	t.Helper()

	reply := client.Post("/register", `{"email":"ada@example.com","password":"correct horse"}`)
	if reply.Status != http.StatusCreated {
		t.Fatalf("register = %d %s", reply.Status, reply.Body)
	}
}

// loginAda logs ada in on client and returns the reply.
func loginAda(client *apptest.Client, secret string) apptest.Reply {
	return client.Post("/login", `{"email":"ada@example.com","password":"`+secret+`"}`)
}
