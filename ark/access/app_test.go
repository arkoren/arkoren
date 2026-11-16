package access_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/link"
	"arkoren.dev/core/http/middleware/ratelimit"
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
	"arkoren.dev/ark/access/account"
	"arkoren.dev/ark/access/federation"
	"arkoren.dev/ark/access/internal/apptest"
	"arkoren.dev/ark/access/password"
)

// wire says which email features of the facade the harness configures.
type wire struct {
	// reset configures password.Builder.Reset where [registration] is used.
	reset bool

	// verify configures account.Builder.Verification, which the built-in users
	// need and the application's own users cannot have.
	verify bool
}

// both is the usual harness: reset and verification, as an application that
// sends every email would configure them.
var both = wire{reset: true, verify: true}

// outbox collects what the facade dispatches and delivers. Its dispatch
// functions call the matching Send function of the facade at once, as a queue
// worker would later, so the tests stay synchronous.
type outbox struct {
	mu                 sync.Mutex
	iam                *access.Access
	signer             *link.Signer
	wire               wire
	resets             []password.ResetMessage
	verifications      []account.VerifyMessage
	dispatchedResets   []password.ResetRequest
	dispatchedVerifies []account.VerifyRequest
}

// building serializes the construction of facades: [registration] is a plain
// function the tests pass to Builder.Password, so it learns the outbox of the
// facade being built from here. The callbacks run synchronously inside
// access.New, which holds the lock.
var (
	building    sync.Mutex
	buildingBox *outbox
)

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

// newBox returns an outbox with a relative signer.
func newBox(t *testing.T, w wire) *outbox {
	t.Helper()

	return &outbox{signer: newSigner(t, ""), wire: w}
}

// construct runs access.New for box while it is the one [registration] sees.
func construct(box *outbox, db *database.Database, store *cache.Cache, configure ...access.BuilderFunc) (*access.Access, error) {
	building.Lock()
	defer building.Unlock()

	buildingBox = box
	defer func() { buildingBox = nil }()

	iam, err := access.New(db, store, testClientIP, configure...)
	box.iam = iam

	return iam, err
}

// dispatchReset is the dispatch function of password.Builder.Reset.
func (box *outbox) dispatchReset(ctx context.Context, request password.ResetRequest) error {
	box.mu.Lock()
	box.dispatchedResets = append(box.dispatchedResets, request)
	box.mu.Unlock()

	return box.iam.SendReset(ctx, request)
}

// deliverReset is the deliver function of password.Builder.Reset.
func (box *outbox) deliverReset(_ context.Context, message password.ResetMessage) error {
	box.mu.Lock()
	defer box.mu.Unlock()

	box.resets = append(box.resets, message)

	return nil
}

// dispatchVerify is the dispatch function of account.Builder.Verification.
func (box *outbox) dispatchVerify(ctx context.Context, request account.VerifyRequest) error {
	box.mu.Lock()
	box.dispatchedVerifies = append(box.dispatchedVerifies, request)
	box.mu.Unlock()

	return box.iam.SendVerification(ctx, request)
}

// deliverVerify is the deliver function of account.Builder.Verification.
func (box *outbox) deliverVerify(_ context.Context, message account.VerifyMessage) error {
	box.mu.Lock()
	defer box.mu.Unlock()

	box.verifications = append(box.verifications, message)

	return nil
}

// lastReset returns the newest reset message that is not a registration
// notice, failing the test when there is none.
func (box *outbox) lastReset(t *testing.T) password.ResetMessage {
	t.Helper()

	return box.lastResetWhere(t, false)
}

// lastNotice returns the newest registration notice, failing the test when
// there is none.
func (box *outbox) lastNotice(t *testing.T) password.ResetMessage {
	t.Helper()

	return box.lastResetWhere(t, true)
}

// lastResetWhere returns the newest reset message whose Notice is notice.
func (box *outbox) lastResetWhere(t *testing.T, notice bool) password.ResetMessage {
	t.Helper()

	box.mu.Lock()
	defer box.mu.Unlock()

	for i := len(box.resets) - 1; i >= 0; i-- {
		if box.resets[i].Notice == notice {
			return box.resets[i]
		}
	}
	t.Fatalf("no reset message with notice=%t was delivered", notice)

	return password.ResetMessage{}
}

// lastVerify returns the newest verification message, failing the test when
// there is none.
func (box *outbox) lastVerify(t *testing.T) account.VerifyMessage {
	t.Helper()

	box.mu.Lock()
	defer box.mu.Unlock()

	if len(box.verifications) == 0 {
		t.Fatal("no verification message was delivered")
	}

	return box.verifications[len(box.verifications)-1]
}

// counts returns how many reset and verification messages were delivered.
func (box *outbox) counts() (resets, verifications int) {
	box.mu.Lock()
	defer box.mu.Unlock()

	return len(box.resets), len(box.verifications)
}

// registration enables POST /register, which is off by default, and the
// forgotten-password recovery of the outbox of the facade being built when its
// wiring asks for it.
func registration(passwords *password.Builder) {
	passwords.Registration(true)
	if buildingBox != nil && buildingBox.wire.reset {
		reset(passwords)
	}
}

// reset configures forgotten-password recovery on the outbox of the facade
// being built, whatever its wiring says.
func reset(passwords *password.Builder) {
	passwords.Reset(buildingBox.signer, buildingBox.dispatchReset, buildingBox.deliverReset)
}

// verification returns the callback that configures email verification of the
// built-in users on box.
func (box *outbox) verification() access.BuilderFunc {
	return func(iam *access.Builder) {
		iam.Directory(func(accounts *account.Builder) {
			accounts.Verification(box.signer, box.dispatchVerify, box.deliverVerify)
		})
	}
}

// signUp lets a first provider login create a user, which is off by default.
func signUp(links *federation.Builder) {
	links.SignUp(true)
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

// newApp composes a facade on db with reset and verification wired to an
// outbox, mounts it and serves it. Cookies are allowed over plain HTTP.
func newApp(t *testing.T, db *database.Database, instance string, configure ...access.BuilderFunc) *app {
	t.Helper()

	return newAppWith(t, db, instance, both, configure...)
}

// newAppWith is [newApp] with the email features chosen.
func newAppWith(t *testing.T, db *database.Database, instance string, w wire, configure ...access.BuilderFunc) *app {
	t.Helper()

	return newAppOn(t, db, instance, cache.New(memorycache.New()), w, configure...)
}

// newAppOn is [newAppWith] over the given cache, for tests that break part of
// it.
func newAppOn(t *testing.T, db *database.Database, instance string, store *cache.Cache, w wire, configure ...access.BuilderFunc) *app {
	t.Helper()

	box := newBox(t, w)
	iam, err := construct(box, db, store, append([]access.BuilderFunc{func(iam *access.Builder) {
		iam.Instance(instance)
		if w.verify {
			box.verification()(iam)
		}
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
	value, err := totp.New("Example").Generate(secret, time.Now().Add(offset))
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
	token := sys.box.lastVerify(t).Signed
	if reply := client.Post("/email/verify", `{"token":"`+token+`"}`); reply.Status != http.StatusNoContent {
		t.Fatalf("verify = %d %s", reply.Status, reply.Body)
	}
}

// register signs up email on client, which registration does not log in, then
// logs in, and fails unless both succeed.
func register(t *testing.T, client *apptest.Client, email string) apptest.Reply {
	t.Helper()

	reply := client.Post("/register", `{"email":"`+email+`","password":"correct horse"}`)
	if reply.Status != http.StatusAccepted {
		t.Fatalf("register = %d %s", reply.Status, reply.Body)
	}
	if login := client.Post("/login", `{"email":"`+email+`","password":"correct horse"}`); login.Status != http.StatusNoContent {
		t.Fatalf("login after registering = %d %s", login.Status, login.Body)
	}

	return reply
}

// testClientIP is the client address source of the tests: the X-Test-IP header
// when a test sets one to play several clients, else the host of r.RemoteAddr.
func testClientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Test-IP"); ip != "" {
		return ip
	}

	return ratelimit.TrustedProxyKey()(r)
}

// newFacade composes a facade over an in-memory database without serving it.
func newFacade(t *testing.T, configure ...access.BuilderFunc) (*access.Access, error) {
	t.Helper()

	return newFacadeWith(t, both, configure...)
}

// newFacadeWith is [newFacade] with the email features chosen.
func newFacadeWith(t *testing.T, w wire, configure ...access.BuilderFunc) (*access.Access, error) {
	t.Helper()

	box := newBox(t, w)
	if w.verify {
		configure = append([]access.BuilderFunc{box.verification()}, configure...)
	}

	return construct(box, memoryDatabase(t), cache.New(memorycache.New()), configure...)
}
