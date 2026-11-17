package twofactor_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/database"
	"arkoren.dev/driver/aes/aescrypto"
	"arkoren.dev/foundation/key"
	"arkoren.dev/foundation/totp"

	"arkoren.dev/ark/iam/internal/apptest"
	"arkoren.dev/ark/iam/twofactor"
)

// bearerSubject is the token the test bearer authenticator accepts; it yields
// an identity that is not a browser session.
const bearerSubject = "bearer-token"

// system is a factors store wired to a guard and a running server.
type system struct {
	provider *auth.UserProvider
	factors  *twofactor.Factors
	guard    *auth.Guard
	client   *apptest.Client
	events   *event.Bus
	enrolled []auth.CredentialEnrolledEvent
}

// newCipher returns an AES encrypter over a fresh key.
func newCipher(t *testing.T, previous ...crypto.Driver) (*crypto.Encrypter, crypto.Driver) {
	t.Helper()

	secret, err := key.GenerateShared()
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	driver, err := aescrypto.New(secret)
	if err != nil {
		t.Fatalf("aescrypto.New: %v", err)
	}
	encrypter, err := crypto.New(driver, func(encrypter *crypto.Builder) {
		encrypter.Previous(previous...)
	})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}

	return encrypter, driver
}

// newSystem migrates the factors on db and serves their handlers. Users "1" and
// "2" exist.
func newSystem(t *testing.T, db *database.Database, instance string, cipher *crypto.Encrypter) *system {
	t.Helper()

	users := memoryauth.New()
	for _, subject := range []string{"1", "2"} {
		if err := users.Put(auth.UserRecord{Subject: subject, Version: "0", Claims: map[string]any{"email": subject + "@example.com"}}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	provider, err := auth.NewUserProvider(users)
	if err != nil {
		t.Fatalf("NewUserProvider: %v", err)
	}
	store := cache.New(memorycache.New())
	if cipher == nil {
		cipher, _ = newCipher(t)
	}
	sys := &system{provider: provider, events: event.New()}
	if _, err := sys.events.Listen(auth.CredentialEnrolled, func(_ context.Context, enrolled auth.CredentialEnrolledEvent) error {
		sys.enrolled = append(sys.enrolled, enrolled)

		return nil
	}); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	sys.factors, err = twofactor.NewFactors(provider, db, store, totp.New("Example"), cipher, func(factors *twofactor.Builder) {
		factors.Instance(instance)
		factors.Events(sys.events)
		factors.RecoveryCodes(3)
	})
	if err != nil {
		t.Fatalf("NewFactors: %v", err)
	}
	migrateUp(t, db, sys.factors.Migrations()...)
	sys.guard, err = auth.NewGuard(provider, store, func(policy *auth.GuardBuilder) {
		policy.SecondFactor(sys.factors.Credential(), sys.factors.RecoveryCredential())
		policy.StepUp(sys.factors.Credential())
	})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	bearer := auth.Bearer(func(_ context.Context, token string) (auth.Identity, error) {
		if token != bearerSubject {
			return auth.Identity{}, auth.ErrNoCredentials
		}

		return auth.NewIdentity("1"), nil
	})
	sys.client = apptest.Serve(t, store, []auth.Authenticator{sys.guard.Authenticator(), bearer}, func(r *router.Router) {
		r.Post("/dev-login/{subject}", func(w http.ResponseWriter, r *http.Request) error {
			factor := auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}
			outcome, err := sys.guard.Begin(r, r.PathValue("subject"), factor)
			if err != nil {
				return err
			}

			return outcome.Response(w)
		})
		r.Post("/login/challenge", sys.guard.ChallengeHandler())
		r.Post("/confirm", sys.guard.ConfirmHandler())
		r.Post("/two-factor/setup", sys.factors.SetupHandler(sys.guard))
		r.Post("/two-factor", sys.factors.EnableHandler(sys.guard))
		r.Delete("/two-factor", sys.factors.DisableHandler(sys.guard))
		r.Post("/two-factor/recovery-codes", sys.factors.RecoveryCodesHandler(sys.guard))
		r.Get("/level", func(w http.ResponseWriter, r *http.Request) error {
			identity, ok := auth.FromContext(r.Context())
			if !ok {
				return auth.ErrUnauthenticated
			}
			_, err := fmt.Fprint(w, strconv.Itoa(int(identity.Level())))

			return err
		})
	})

	return sys
}

// currentCode returns the TOTP code of a base32 secret at the current time.
func currentCode(t *testing.T, encoded string) string {
	t.Helper()

	secret, err := totp.DecodeSecret(encoded)
	if err != nil {
		t.Fatalf("DecodeSecret: %v", err)
	}
	code, err := totp.New("Example").Generate(secret, time.Now())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	return code
}

// enable runs setup and enable for subject through the HTTP routes on client,
// logged in as that subject, and returns the secret and the recovery codes.
func (sys *system) enable(t *testing.T, client *apptest.Client, subject string) (string, []string) {
	t.Helper()

	if reply := client.Post("/dev-login/"+subject, ""); reply.Status != http.StatusNoContent {
		t.Fatalf("dev login = %d %s", reply.Status, reply.Body)
	}
	setup := client.Post("/two-factor/setup", "")
	if setup.Status != http.StatusOK {
		t.Fatalf("setup = %d %s", setup.Status, setup.Body)
	}
	enrollment := apptest.JSON[twofactor.Enrollment](t, setup)
	enable := client.Post("/two-factor", `{"code":"`+currentCode(t, enrollment.Secret)+`"}`)
	if enable.Status != http.StatusOK {
		t.Fatalf("enable = %d %s", enable.Status, enable.Body)
	}
	codes := apptest.JSON[struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}](t, enable)

	return enrollment.Secret, codes.RecoveryCodes
}

// cacheOf returns a fresh in-memory cache.
func cacheOf(t *testing.T) *cache.Cache {
	t.Helper()

	return cache.New(memorycache.New())
}
