package access_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/hash"
	"arkoren.dev/driver/aes/aescrypto"
	"arkoren.dev/driver/bcrypt/bcrypthash"
	"arkoren.dev/foundation/key"
	"arkoren.dev/foundation/totp"

	"arkoren.dev/ark/access"
	"arkoren.dev/ark/access/internal/apptest"
)

// outbox collects the messages the facade delivers.
type outbox struct {
	mu       sync.Mutex
	messages []access.Message
}

// send is the Builder.Deliver function of the tests.
func (box *outbox) send(_ context.Context, message access.Message) error {
	box.mu.Lock()
	defer box.mu.Unlock()

	box.messages = append(box.messages, message)

	return nil
}

// all returns a copy of every message delivered so far.
func (box *outbox) all() []access.Message {
	box.mu.Lock()
	defer box.mu.Unlock()

	return append([]access.Message(nil), box.messages...)
}

// last returns the newest message of kind, failing the test when there is none.
func (box *outbox) last(t *testing.T, kind access.Kind) access.Message {
	t.Helper()

	box.mu.Lock()
	defer box.mu.Unlock()

	for i := len(box.messages) - 1; i >= 0; i-- {
		if box.messages[i].Kind == kind {
			return box.messages[i]
		}
	}
	t.Fatalf("no %s message was delivered", kind)

	return access.Message{}
}

// hasher returns a fast bcrypt hasher for tests.
func hasher() *hash.Hasher {
	return hash.New(bcrypthash.New(func(driver *bcrypthash.Builder) {
		driver.Cost(4)
	}))
}

// cipher returns an AES encrypter over a fresh key.
func cipher(t *testing.T) *crypto.Encrypter {
	t.Helper()

	secret, err := key.GenerateShared()
	if err != nil {
		t.Fatalf("GenerateShared: %v", err)
	}
	driver, err := aescrypto.New(secret)
	if err != nil {
		t.Fatalf("aescrypto.New: %v", err)
	}
	encrypter, err := crypto.New(driver)
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}

	return encrypter
}

// app is a composed facade behind a running server.
type app struct {
	iam    *access.Access
	box    *outbox
	client *apptest.Client
	router *router.Router
}

// newApp composes a facade on db with a delivery outbox, mounts it and serves
// it. Cookies are allowed over plain HTTP.
func newApp(t *testing.T, db *database.Database, instance string, configure ...access.BuilderFunc) *app {
	t.Helper()

	box := &outbox{}
	iam, err := access.New(db, cache.New(memorycache.New()), append([]access.BuilderFunc{func(iam *access.Builder) {
		iam.Instance(instance)
		iam.Deliver(box.send)
		iam.Session(func(cookies *session.Builder) {
			cookies.AllowInsecure(true)
		})
	}}, configure...)...)
	if err != nil {
		t.Fatalf("access.New: %v", err)
	}
	migrateUp(t, db, iam.Migrations()...)
	if err := iam.Ping(testContext(t)); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	r := router.New()
	web := r.With(iam.Middleware())
	iam.Mount(web)
	web.Get("/me", func(w http.ResponseWriter, r *http.Request) error {
		identity, ok := auth.FromContext(r.Context())
		if !ok {
			_, err := fmt.Fprint(w, "anonymous")

			return err
		}
		_, err := fmt.Fprintf(w, "%s|%d|%t", identity.Subject(), identity.Level(), identity.Session())

		return err
	})

	return &app{iam: iam, box: box, client: apptest.ServeRouter(t, r), router: r}
}

// code returns the TOTP code of a base32 secret at a step offset from now.
func code(t *testing.T, encoded string, offset time.Duration) string {
	t.Helper()

	secret, err := totp.DecodeSecret(encoded)
	if err != nil {
		t.Fatalf("DecodeSecret: %v", err)
	}
	value, err := totp.New().Generate(secret, time.Now().Add(offset))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	return value
}

// registerVerified signs email up on client and verifies it with the link the
// outbox received, as a user who clicked it would.
func registerVerified(t *testing.T, sys *app, client *apptest.Client, email string) {
	t.Helper()

	register(t, client, email)
	token := sys.box.last(t, access.KindVerify).Token
	if reply := client.Post("/email/verify", `{"token":"`+token+`"}`); reply.Status != http.StatusNoContent {
		t.Fatalf("verify = %d %s", reply.Status, reply.Body)
	}
}

// settle waits for the emails the handlers send in the background.
func (sys *app) settle(t *testing.T) {
	t.Helper()

	if err := sys.iam.Wait(testContext(t)); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// register signs ada up on client and fails unless the reply is 201.
func register(t *testing.T, client *apptest.Client, email string) apptest.Reply {
	t.Helper()

	reply := client.Post("/register", `{"email":"`+email+`","password":"correct horse"}`)
	if reply.Status != http.StatusCreated {
		t.Fatalf("register = %d %s", reply.Status, reply.Body)
	}

	return reply
}

// newFacade composes a facade over an in-memory database without serving it.
func newFacade(t *testing.T, configure ...access.BuilderFunc) (*access.Access, error) {
	t.Helper()

	return access.New(memoryDatabase(t), cache.New(memorycache.New()), configure...)
}
