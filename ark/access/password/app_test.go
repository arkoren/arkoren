package password_test

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"testing"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/http/link"
	"arkoren.dev/core/http/middleware/ratelimit"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/bcrypt/bcrypthash"
	"arkoren.dev/foundation/key"

	"arkoren.dev/ark/access/internal/apptest"
	"arkoren.dev/ark/access/password"
)

// testClientIP is the client address source of the tests: the X-Test-IP header
// when a test sets one to play several clients, else the host of r.RemoteAddr.
func testClientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Test-IP"); ip != "" {
		return ip
	}

	return ratelimit.TrustedProxyKey()(r)
}

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
func (users *fakeUsers) Lookup(_ context.Context, identifier string) (string, string, error) {
	users.mu.Lock()
	defer users.mu.Unlock()

	subject, ok := users.subjects[identifier]
	if !ok {
		return "", "", fmt.Errorf("fake: %w", auth.ErrUserNotFound)
	}

	return subject, identifier, nil
}

// Create is [password.Users.Create].
func (users *fakeUsers) Create(_ context.Context, identifier string) (string, error) {
	users.mu.Lock()
	defer users.mu.Unlock()

	if _, ok := users.subjects[identifier]; ok {
		return "", password.ErrIdentifierTaken
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
	sent     *[]password.ResetMessage

	// dispatched holds every request the forgot and register handlers handed to
	// the dispatch function, in order.
	dispatched *[]password.ResetRequest
}

// newSigner returns a link signer over a fresh key: relative when origin is
// empty, otherwise absolute under origin.
func newSigner(t *testing.T, origin string) *link.Signer {
	t.Helper()

	secret, err := key.GenerateShared()
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	signer, err := link.New(secret, func(signer *link.Builder) {
		signer.Origin(origin)
	})
	if err != nil {
		t.Fatalf("link.New: %v", err)
	}

	return signer
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

	return newConfiguredSystem(t, db, instance, nil, second...)
}

// newConfiguredSystem is [newSystem] with one more builder callback, applied
// after the harness's own, for a test that needs a hook.
func newConfiguredSystem(t *testing.T, db *database.Database, instance string, configure password.BuilderFunc, second ...auth.Credential) *system {
	t.Helper()

	return newSignedSystem(t, db, instance, newSigner(t, ""), configure, second...)
}

// newSignedSystem is [newConfiguredSystem] with the link signer chosen. Its
// dispatch function runs [password.Verifier.SendReset] at once, as a queue
// worker would later, so the tests stay synchronous.
func newSignedSystem(t *testing.T, db *database.Database, instance string, signer *link.Signer, configure password.BuilderFunc, second ...auth.Credential) *system {
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
	sent := &[]password.ResetMessage{}
	dispatched := &[]password.ResetRequest{}
	var sentMu sync.Mutex
	var verifier *password.Verifier
	verifier, err = password.NewVerifier(provider, db, store, testClientIP, hasher, password.Users{
		Lookup: users.Lookup,
		Create: users.Create,
		Rotate: users.Rotate,
	}, func(passwords *password.Builder) {
		passwords.Instance(instance)
		passwords.Registration(true)
		passwords.Taken(func(ctx context.Context, identifier string) error {
			return verifier.RequestReset(ctx, password.ResetRequest{Identifier: identifier, Notice: true})
		})
		passwords.Reset(signer, func(ctx context.Context, request password.ResetRequest) error {
			sentMu.Lock()
			*dispatched = append(*dispatched, request)
			sentMu.Unlock()

			return verifier.SendReset(ctx, request)
		}, func(_ context.Context, message password.ResetMessage) error {
			sentMu.Lock()
			defer sentMu.Unlock()

			*sent = append(*sent, message)

			return nil
		})
		if configure != nil {
			configure(passwords)
		}
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
		r.Post("/register", verifier.RegisterHandler())
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

	return &system{users: users, provider: provider, hasher: hasher, verifier: verifier, guard: guard, store: store, client: client, sent: sent, dispatched: dispatched}
}

// registerAda registers ada@example.com on the client, then logs in, and fails
// unless both succeed.
func (sys *system) registerAda(t *testing.T, client *apptest.Client) {
	t.Helper()

	reply := client.Post("/register", `{"email":"ada@example.com","password":"correct horse"}`)
	if reply.Status != http.StatusAccepted {
		t.Fatalf("register = %d %s", reply.Status, reply.Body)
	}
	if login := loginAda(client, "correct horse"); login.Status != http.StatusNoContent {
		t.Fatalf("login after registering = %d %s", login.Status, login.Body)
	}
}

// loginAda logs ada in on client and returns the reply.
func loginAda(client *apptest.Client, secret string) apptest.Reply {
	return client.Post("/login", `{"email":"ada@example.com","password":"`+secret+`"}`)
}

// postedValue returns what a front end posts for a delivered link: the fragment
// of href, decoded as location.hash is with decodeURIComponent.
func postedValue(t *testing.T, href string) string {
	t.Helper()

	parsed, err := url.Parse(href)
	if err != nil || parsed.Fragment == "" {
		t.Fatalf("link %q has no fragment: %v", href, err)
	}

	return parsed.Fragment
}
