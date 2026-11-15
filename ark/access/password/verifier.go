package password

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/core/service/hash"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"

	"arkoren.dev/ark/access/internal/background"
	"arkoren.dev/ark/access/internal/storage"
	"arkoren.dev/ark/access/internal/throttle"
)

const (
	// DefaultResetTTL is how long a password reset token stays valid when
	// [Builder.ResetTTL] is not set.
	//
	// Example:
	//
	//	passwords.ResetTTL(password.DefaultResetTTL / 2)
	DefaultResetTTL = time.Hour

	// MinLength is the shortest password the default policy accepts, in bytes.
	//
	// Example:
	//
	//	fmt.Println(password.MinLength) // 8
	MinLength = 8

	// MaxLength is the longest password the default policy accepts, in bytes.
	// bcrypt hashers reject more than 72 bytes, so the default never admits a
	// password the common hashers cannot take.
	//
	// Example:
	//
	//	fmt.Println(password.MaxLength) // 72
	MaxLength = 72

	// maxFailures is how many failed logins one identifier may have from one
	// client address within throttleWindow. The counter is keyed on both, so an
	// attacker elsewhere cannot lock the real user out by failing ten times.
	maxFailures = 10

	// maxIdentifierFailures is how many failed logins one identifier may have
	// from all addresses together within throttleWindow: a looser cap that
	// still stops a distributed guess against one account, five times the
	// per-address limit.
	maxIdentifierFailures = 50

	// maxClientFailures is how many failed logins per client address are
	// allowed within throttleWindow: ten times the identifier limit, so one
	// attacker cannot lock out an office behind a shared address by guessing
	// ten times, while a credential-stuffing run is still stopped.
	maxClientFailures = 100

	// maxForgot is how many reset requests one identifier, as typed, may make
	// within throttleWindow.
	maxForgot = 5

	// maxClientForgot is how many reset requests one client address may make
	// within throttleWindow, across identifiers.
	maxClientForgot = 30

	// maxRegistrations is how many registrations one identifier, as typed, may
	// attempt within throttleWindow.
	maxRegistrations = 5

	// maxClientRegistrations is how many registrations one client address may
	// attempt within throttleWindow.
	maxClientRegistrations = 30

	// maxLiveResets is how many unexpired reset tokens a user can hold. A new
	// request beyond it replaces the oldest, so a repeat request does not kill
	// the link the user is about to click, and an attacker cannot make the
	// table grow.
	maxLiveResets = 3

	// throttleWindow is the lifetime of an attempt counter.
	throttleWindow = 15 * time.Minute
)

// ErrNotMigrated is returned by [Verifier.Ping] when the tables do not exist.
// Add [Verifier.Migrations] to the application's migrator and run them.
//
// Example:
//
//	if errors.Is(verifier.Ping(ctx), password.ErrNotMigrated) {
//		// run the migrator
//	}
var ErrNotMigrated = storage.ErrNotMigrated

// Users connects the verifier to the application's user store. The functions
// speak identifiers (what the user types at login, such as an email) and
// subjects (what the rest of the system uses). The facade fills them from the
// built-in directory or from the application's own users; the package never
// imports either.
//
// Example:
//
//	users := password.Users{
//		Lookup: directory.Lookup,
//		Create: directory.Create,
//		Rotate: directory.Rotate,
//	}
type Users struct {
	// Lookup returns the subject of the user with identifier and the address
	// to write to, as stored with the user (with the directory, the stored
	// email, which can differ from what was typed). An unknown user must
	// return an error wrapping [auth.ErrUserNotFound]; any other error is an
	// infrastructure failure. It must not reveal more than that. The address
	// must not be empty: a reset email is sent only to it, never to the typed
	// identifier, so a request cannot redirect a victim's link.
	Lookup func(ctx context.Context, identifier string) (subject, address string, err error)

	// Create adds a user with identifier and returns the subject. It is used by
	// registration and may be nil for an application that registers users
	// elsewhere, in which case [Verifier.RegisterHandler] panics. Its errors
	// (a taken identifier, an invalid one) pass through to the client. It must
	// fail on an existing identifier and never return the existing user.
	Create func(ctx context.Context, identifier string) (subject string, err error)

	// Rotate bumps the user's [auth.UserRecord.Version], which ends every
	// session, pending login and API token of the user. A reset calls it.
	Rotate func(ctx context.Context, subject string) error

	// ProveEmail is optional. [Verifier.Reset] calls it in its transaction,
	// after the new password is stored, because a completed reset proves the
	// user controls the mailbox the link went to. An implementation marks the
	// address verified and, when it was not verified before, removes what
	// someone who registered the address first could have attached
	// (connections, two-factor, tokens). An error rolls the reset back.
	ProveEmail func(ctx context.Context, subject string) error
}

// Message is what [Builder.Deliver] receives to write a reset email. The token
// is a credential until it is used or expires; never log it.
//
// Example:
//
//	link := message.Link("https://app.example.com/reset-password")
type Message struct {
	// Identifier is the address to write to, as returned by [Users.Lookup]: the
	// one stored with the user, never the text of the request.
	Identifier string

	// Token is the single-use secret that [Verifier.Reset] accepts.
	Token string

	// ExpiresAt is when the token stops working.
	ExpiresAt time.Time
}

// Link returns base with the token as its "token" query parameter, escaped.
//
// Example:
//
//	message := password.Message{Token: "abc"}
//	fmt.Println(message.Link("https://app.example.com/reset")) // https://app.example.com/reset?token=abc
func (message Message) Link(base string) string {
	return base + "?" + url.Values{"token": {message.Token}}.Encode()
}

// Builder configures [NewVerifier] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft.
//
// Example:
//
//	verifier, err := password.NewVerifier(provider, db, store, hasher, users, func(passwords *password.Builder) {
//		passwords.Deliver(send).ResetTTL(30 * time.Minute)
//	})
type Builder struct {
	instance   string
	events     *event.Bus
	deliver    func(context.Context, Message) error
	resetTTL   time.Duration
	policy     func(context.Context, string) error
	clientIP   func(*http.Request) string
	registered func(context.Context, string, string) error
	changed    func(context.Context, string) error
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure password.BuilderFunc = func(passwords *password.Builder) {
//		passwords.Instance("tenant_a")
//	}
//	verifier, err := password.NewVerifier(provider, db, store, hasher, users, configure)
type BuilderFunc = func(*Builder)

// Instance selects a named instance: tables are prefixed "<instance>_" and the
// migration group gains ":<instance>". The name is 1 to 20 bytes of [a-z0-9_]
// starting with a letter; the default is the empty instance.
//
// Example:
//
//	passwords.Instance("tenant_a")
func (passwords *Builder) Instance(name string) *Builder {
	passwords.instance = name

	return passwords
}

// Events publishes [Changed] and [WasReset] on bus. A nil bus publishes nothing.
//
// Example:
//
//	passwords.Events(events)
func (passwords *Builder) Events(bus *event.Bus) *Builder {
	passwords.events = bus

	return passwords
}

// Deliver sets the function that sends reset links. Without it
// [Verifier.SendReset] returns [ErrNoDelivery].
//
// Example:
//
//	passwords.Deliver(func(ctx context.Context, message password.Message) error {
//		return mailer.Send(ctx, message.Identifier, message.Link("https://app.example.com/reset"))
//	})
func (passwords *Builder) Deliver(deliver func(ctx context.Context, message Message) error) *Builder {
	passwords.deliver = deliver

	return passwords
}

// ResetTTL sets how long a reset token is valid; zero selects
// [DefaultResetTTL].
//
// Example:
//
//	passwords.ResetTTL(30 * time.Minute)
func (passwords *Builder) ResetTTL(ttl time.Duration) *Builder {
	passwords.resetTTL = ttl

	return passwords
}

// Policy replaces the password policy, which by default requires [MinLength] to
// [MaxLength] bytes. check returns an error wrapping [ErrInvalidPassword] to
// reject a password; any other error is an infrastructure failure.
//
// Example:
//
//	passwords.Policy(func(ctx context.Context, password string) error {
//		if len(password) < 12 {
//			return fmt.Errorf("too short: %w", passwordpkg.ErrInvalidPassword)
//		}
//		return nil
//	})
func (passwords *Builder) Policy(check func(ctx context.Context, password string) error) *Builder {
	passwords.policy = check

	return passwords
}

// ClientIP sets how the client address of the login throttle is found. The
// default is the host of r.RemoteAddr, which behind a reverse proxy is the
// proxy: pass the same function given to the guard's ClientIP, such as
// ratelimit.TrustedProxyKey.
//
// Example:
//
//	passwords.ClientIP(ratelimit.TrustedProxyKey(netip.MustParsePrefix("10.0.0.0/8")))
func (passwords *Builder) ClientIP(find func(*http.Request) string) *Builder {
	passwords.clientIP = find

	return passwords
}

// Registered sets a function that runs after [Verifier.RegisterHandler]
// committed a new user and before it logs them in, for work that must follow a
// registration such as sending an email verification. An error is logged and
// does not fail the request: the user is registered either way, and a mail
// outage must not turn into a failed signup.
//
// Example:
//
//	passwords.Registered(func(ctx context.Context, subject, identifier string) error {
//		return directory.SendVerification(ctx, subject)
//	})
func (passwords *Builder) Registered(hook func(ctx context.Context, subject, identifier string) error) *Builder {
	passwords.registered = hook

	return passwords
}

// AfterChange sets a function that runs in [Verifier.ChangeHandler] after the new
// password is stored and before the caller's other logins end, for what must
// not survive a password change, such as the user's device tokens. An error
// fails the request; the password has changed by then, so the client may retry.
//
// Example:
//
//	passwords.AfterChange(issuer.RevokeDevices)
func (passwords *Builder) AfterChange(hook func(ctx context.Context, subject string) error) *Builder {
	passwords.changed = hook

	return passwords
}

// Verifier owns the password hashes and reset tokens. It is safe for
// concurrent use. Create it with [NewVerifier].
//
// Example:
//
//	verifier, err := password.NewVerifier(provider, db, store, hasher, users)
//	if err != nil {
//		return err
//	}
type Verifier struct {
	provider   *auth.UserProvider
	db         *database.Database
	store      *cache.Cache
	hasher     *hash.Hasher
	users      Users
	instance   string
	hashes     string
	resets     string
	events     *event.Bus
	deliver    func(context.Context, Message) error
	resetTTL   time.Duration
	policy     func(context.Context, string) error
	clientIP   func(*http.Request) string
	registered func(context.Context, string, string) error
	changed    func(context.Context, string) error
	jobs       background.Group

	dummyOnce sync.Once
	dummy     []byte
}

// NewVerifier returns a verifier. provider finds users (to refuse disabled ones
// and to read the version), db holds the tables, store counts login attempts,
// hasher hashes passwords and users connects the user store. It does no I/O:
// the tables come from [Verifier.Migrations]. Errors wrap [ErrInvalidConfig].
//
// It registers a forget hook on provider that deletes the user's hash and
// reset token in the caller's transaction.
//
// Example:
//
//	verifier, err := password.NewVerifier(provider, db, store, hasher, password.Users{
//		Lookup: directory.Lookup,
//		Rotate: directory.Rotate,
//	})
func NewVerifier(provider *auth.UserProvider, db *database.Database, store *cache.Cache, hasher *hash.Hasher, users Users, configure ...BuilderFunc) (*Verifier, error) {
	switch {
	case provider == nil, db == nil, store == nil, hasher == nil:
		return nil, fmt.Errorf("%w: provider, database, store and hasher are required", ErrInvalidConfig)
	case users.Lookup == nil, users.Rotate == nil:
		return nil, fmt.Errorf("%w: Users.Lookup and Users.Rotate are required", ErrInvalidConfig)
	}
	settings := Builder{}
	for _, apply := range configure {
		apply(&settings)
	}
	if err := storage.ValidInstance(settings.instance); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if settings.resetTTL < 0 {
		return nil, fmt.Errorf("%w: negative reset lifetime", ErrInvalidConfig)
	}
	verifier := &Verifier{
		provider:   provider,
		db:         db,
		store:      store,
		hasher:     hasher,
		users:      users,
		instance:   settings.instance,
		hashes:     storage.Table(settings.instance, "password_hashes"),
		resets:     storage.Table(settings.instance, "password_resets"),
		events:     settings.events,
		deliver:    settings.deliver,
		resetTTL:   cmpOr(settings.resetTTL, DefaultResetTTL),
		policy:     settings.policy,
		clientIP:   settings.clientIP,
		registered: settings.registered,
		changed:    settings.changed,
	}
	if verifier.policy == nil {
		verifier.policy = defaultPolicy
	}
	if verifier.clientIP == nil {
		verifier.clientIP = remoteHost
	}
	provider.OnForget(verifier.forget)

	return verifier, nil
}

// cmpOr returns value, or fallback when value is zero.
func cmpOr(value, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}

	return value
}

// defaultPolicy requires [MinLength] to [MaxLength] bytes.
func defaultPolicy(_ context.Context, password string) error {
	if len(password) < MinLength || len(password) > MaxLength {
		return fmt.Errorf("%w: use %d to %d bytes", ErrInvalidPassword, MinLength, MaxLength)
	}

	return nil
}

// remoteHost is the default client address: the host part of r.RemoteAddr.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

// Migrations returns the migrations that create the "password_hashes" and
// "password_resets" tables (prefixed "<instance>_" for a named instance) in the
// group "arkoren.dev:ark/access:password", plus ":<instance>". Each creates one
// table strictly and its down step drops it. The application adds them to its
// migrator; constructing or reading them never runs DDL.
//
// Example:
//
//	if err := migrator.Add(verifier.Migrations()...); err != nil {
//		return err
//	}
func (verifier *Verifier) Migrations() []migrate.Definition {
	return storage.Group("password", verifier.instance).Migrations(
		migrate.NewMigration("hashes-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(verifier.hashes, func(table *schema.Table) {
				table.String("subject", 255).Bytewise().Primary()
				table.Text("hash")
				table.BigInteger("changed_at")
			})))
			migration.Down(migrate.Schema(schema.Drop(verifier.hashes)))
		}),
		migrate.NewMigration("resets-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(verifier.resets, func(table *schema.Table) {
				table.String("token_hash", 64).Bytewise().Primary()
				table.String("subject", 255).Bytewise()
				table.BigInteger("expires_at")
				table.BigInteger("created_at")
				table.Index("subject")
			})))
			migration.Down(migrate.Schema(schema.Drop(verifier.resets)))
		}),
	)
}

// Ping verifies, read-only, that both tables exist. A missing table returns an
// error wrapping [ErrNotMigrated]. Use it as a readiness check.
//
// Example:
//
//	if err := verifier.Ping(ctx); err != nil {
//		return err
//	}
func (verifier *Verifier) Ping(ctx context.Context) error {
	return storage.Ping(ctx, verifier.db, verifier.hashes, verifier.resets)
}

// forget deletes the rows of subject in the caller's transaction.
func (verifier *Verifier) forget(ctx context.Context, subject string) error {
	for _, table := range []string{verifier.hashes, verifier.resets} {
		_, err := verifier.db.Exec(ctx, query.Table(table, func(statement *query.Builder) {
			statement.Where("subject", subject).Delete()
		}))
		if err != nil {
			return fmt.Errorf("password: forget %s: %w", table, err)
		}
	}

	return nil
}

// credentialBody is the input of the password credential and of the login and
// confirm requests.
type credentialBody struct {
	Password string `json:"password"`
}

// Credential returns the [auth.Credential] that lets a user re-prove
// themselves with their password: method "pwd", kind knowledge. Register it
// with the guard's StepUp registry so the shared confirm route (POST /confirm
// with {"method": "pwd", "password": "..."}) accepts it. It is enrolled for
// users who have a password; a wrong password wraps
// [auth.ErrInvalidCredential], which core counts as an attempt.
//
// Example:
//
//	guard, err := auth.NewGuard(provider, store, func(policy *auth.GuardBuilder) {
//		policy.StepUp(verifier.Credential())
//	})
func (verifier *Verifier) Credential() auth.Credential {
	return auth.Credential{
		Method:   auth.MethodPassword,
		Kind:     auth.KindKnowledge,
		Enrolled: verifier.Has,
		Verify: func(r *http.Request, subject string, input jsontext.Value) (auth.Factor, error) {
			var body credentialBody
			if err := json.Unmarshal(input, &body); err != nil {
				return auth.Factor{}, fmt.Errorf("password: %w", auth.ErrInvalidCredential)
			}
			if err := verifier.Check(r.Context(), subject, body.Password); err != nil {
				if errors.Is(err, ErrInvalidCredentials) {
					return auth.Factor{}, fmt.Errorf("password: %w", auth.ErrInvalidCredential)
				}

				return auth.Factor{}, err
			}

			return auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}, nil
		},
	}
}

// load returns the stored hash of subject, or false when the user has none.
func (verifier *Verifier) load(ctx context.Context, subject string) ([]byte, bool, error) {
	row, err := verifier.db.Find[struct {
		Hash string `db:"hash"`
	}](ctx, query.Table(verifier.hashes, func(statement *query.Builder) {
		statement.Select("hash").Where("subject", subject).Limit(1)
	}))
	if errors.Is(err, database.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("password: load hash: %w", err)
	}
	stored, err := base64.StdEncoding.DecodeString(row.Hash)
	if err != nil {
		return nil, false, fmt.Errorf("password: stored hash is corrupt: %w", err)
	}

	return stored, true, nil
}

// Has reports whether subject has a password. It is the Enrolled function of
// [Verifier.Credential] and the fallback check that stops a user from removing
// their last way to log in.
//
// Example:
//
//	has, err := verifier.Has(ctx, subject)
func (verifier *Verifier) Has(ctx context.Context, subject string) (bool, error) {
	_, ok, err := verifier.load(ctx, subject)

	return ok, err
}

// Check verifies password against the stored hash of subject and returns
// [ErrInvalidCredentials] for a wrong password or a user without one, doing the
// same work for both. A successful check upgrades the stored hash when the
// hasher says it is outdated.
//
// Example:
//
//	if err := verifier.Check(ctx, subject, "current password"); err != nil {
//		return err
//	}
func (verifier *Verifier) Check(ctx context.Context, subject, password string) error {
	stored, ok, err := verifier.load(ctx, subject)
	if err != nil {
		return err
	}
	if !ok {
		verifier.burn(password)

		return ErrInvalidCredentials
	}
	match, err := verifier.hasher.Check(password, stored)
	if err != nil {
		return fmt.Errorf("password: check: %w", err)
	}
	if !match {
		return ErrInvalidCredentials
	}
	if verifier.hasher.NeedsRehash(stored) {
		// Upgrading is an optimization: the login already succeeded and the
		// next one retries it.
		_ = verifier.save(ctx, subject, password)
	}

	return nil
}

// burn spends the time of one hash comparison so an unknown user or a user
// without a password takes as long as a wrong password.
func (verifier *Verifier) burn(password string) {
	verifier.dummyOnce.Do(func() {
		var seed [16]byte
		_, _ = rand.Read(seed[:])
		verifier.dummy, _ = verifier.hasher.Hash(base64.StdEncoding.EncodeToString(seed[:]))
	})
	if verifier.dummy != nil {
		// The result is meaningless: only the elapsed time matters.
		_, _ = verifier.hasher.Check(password, verifier.dummy)
	}
}

// Authenticate checks identifier and password and returns the subject. Every
// failure (unknown user, wrong password, no password, disabled user) is
// [ErrInvalidCredentials], and unknown users cost one dummy hash. It does not
// log anyone in and does not throttle; [Verifier.LoginHandler] adds both. A
// failure of the user store or the database is returned as is.
//
// Example:
//
//	subject, err := verifier.Authenticate(ctx, "ada@example.com", "correct horse")
//	if errors.Is(err, password.ErrInvalidCredentials) {
//		// wrong email or password
//	}
func (verifier *Verifier) Authenticate(ctx context.Context, identifier, password string) (string, error) {
	subject, _, err := verifier.users.Lookup(ctx, identifier)
	if errors.Is(err, auth.ErrUserNotFound) {
		verifier.burn(password)

		return "", ErrInvalidCredentials
	}
	if err != nil {
		return "", err
	}
	if err := verifier.Check(ctx, subject, password); err != nil {
		return "", err
	}
	if _, err := verifier.provider.Find(ctx, subject); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) || errors.Is(err, auth.ErrUserDisabled) {
			return "", ErrInvalidCredentials
		}

		return "", err
	}

	return subject, nil
}

// Set replaces the password of subject, creating it when there is none, after
// checking the policy ([ErrInvalidPassword]). It does not end any login: the
// handlers decide that, and [Verifier.Reset] rotates the user.
//
// Example:
//
//	if err := verifier.Set(ctx, subject, "a new long password"); err != nil {
//		return err
//	}
func (verifier *Verifier) Set(ctx context.Context, subject, password string) error {
	if err := verifier.policy(ctx, password); err != nil {
		return err
	}

	return verifier.save(ctx, subject, password)
}

// save hashes password and upserts it. The update goes first because the row
// usually exists; a missing row is inserted, and a concurrent insert that wins
// the race is followed by one more update.
func (verifier *Verifier) save(ctx context.Context, subject, password string) error {
	hashed, err := verifier.hasher.Hash(password)
	if err != nil {
		return fmt.Errorf("password: hash: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(hashed)
	now := time.Now().Unix()
	update := func() (int64, error) {
		return verifier.db.Exec(ctx, query.Table(verifier.hashes, func(statement *query.Builder) {
			statement.Where("subject", subject).Update(map[string]any{"hash": encoded, "changed_at": now})
		}))
	}
	affected, err := update()
	if err == nil && affected == 0 {
		affected, err = verifier.db.Exec(ctx, query.Table(verifier.hashes, func(statement *query.Builder) {
			statement.Insert(map[string]any{"subject": subject, "hash": encoded, "changed_at": now})
			statement.OnConflict("subject").DoNothing()
		}))
		if err == nil && affected == 0 {
			_, err = update()
		}
	}
	if err != nil {
		return fmt.Errorf("password: store hash: %w", err)
	}

	return nil
}

// SendReset starts a password reset for identifier: it creates a single-use
// token and hands it to [Builder.Deliver] with the address that [Users.Lookup]
// returned for the user, never the text of identifier. A user keeps up to three
// live tokens: asking again does not invalidate a link already sent, and the
// oldest is dropped only beyond three. An unknown or disabled user gets nothing
// and the call returns nil, so the caller cannot learn which identifiers
// exist. It returns [ErrNoDelivery] without a delivery function, and an error
// when [Users.Lookup] returns no address. It does not throttle:
// [Verifier.ForgotHandler] does, and runs this in the background.
//
// Example:
//
//	if err := verifier.SendReset(ctx, "ada@example.com"); err != nil {
//		return err
//	}
func (verifier *Verifier) SendReset(ctx context.Context, identifier string) error {
	if verifier.deliver == nil {
		return ErrNoDelivery
	}
	token := rand.Text()
	subject, address, err := verifier.users.Lookup(ctx, identifier)
	if errors.Is(err, auth.ErrUserNotFound) {
		// Spend the hashing time of the known path so timing stays level.
		_ = storage.Hash(token)

		return nil
	}
	if err != nil {
		return err
	}
	if address == "" {
		return errors.New("password: Users.Lookup returned no address")
	}
	if _, err := verifier.provider.Find(ctx, subject); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) || errors.Is(err, auth.ErrUserDisabled) {
			return nil
		}

		return err
	}
	expiresAt := time.Now().Add(verifier.resetTTL)
	err = verifier.db.WithTransaction(ctx, func(ctx context.Context) error {
		// Expired tokens are dead weight; of the live ones keep the newest
		// maxLiveResets-1 so the new token makes at most maxLiveResets.
		_, err := verifier.db.Exec(ctx, query.Table(verifier.resets, func(statement *query.Builder) {
			statement.Where("subject", subject).Where("expires_at", "<=", time.Now().Unix()).Delete()
		}))
		if err != nil {
			return err
		}
		live, err := verifier.db.Select[struct {
			TokenHash string `db:"token_hash"`
		}](ctx, query.Table(verifier.resets, func(statement *query.Builder) {
			statement.Select("token_hash").Where("subject", subject).OrderBy("created_at")
		}))
		if err != nil {
			return err
		}
		if excess := len(live) - (maxLiveResets - 1); excess > 0 {
			stale := make([]string, 0, excess)
			for _, row := range live[:excess] {
				stale = append(stale, row.TokenHash)
			}
			_, err = verifier.db.Exec(ctx, query.Table(verifier.resets, func(statement *query.Builder) {
				statement.WhereIn("token_hash", stale...).Delete()
			}))
			if err != nil {
				return err
			}
		}
		_, err = verifier.db.Exec(ctx, query.Table(verifier.resets, func(statement *query.Builder) {
			statement.Insert(map[string]any{
				"subject":    subject,
				"token_hash": storage.Hash(token),
				"expires_at": expiresAt.Unix(),
				"created_at": time.Now().UnixNano(),
			})
		}))

		return err
	})
	if err != nil {
		return fmt.Errorf("password: store reset: %w", err)
	}

	return verifier.deliver(ctx, Message{Identifier: address, Token: token, ExpiresAt: expiresAt})
}

// Reset consumes a reset token (and the user's other live ones), sets the
// password, calls [Users.ProveEmail] when set and then [Users.Rotate], in one
// transaction, and returns the subject. The link only reached someone who
// controls the mailbox, so the reset is the proof of ownership an unverified
// registration lacks. Rotation ends every session,
// pending login and API token of the user; call [auth.Guard.RevokeAll] to
// remove their login records as well, as [Verifier.ResetHandler] does. Reset
// never logs the user in, so it cannot bypass a second factor. An unknown,
// expired, used or replaced token is [ErrInvalidToken]; a password that fails
// the policy is [ErrInvalidPassword] and leaves the token usable.
//
// Example:
//
//	subject, err := verifier.Reset(ctx, token, "a new long password")
//	if errors.Is(err, password.ErrInvalidToken) {
//		// ask for a new link
//	}
func (verifier *Verifier) Reset(ctx context.Context, token, password string) (string, error) {
	if err := verifier.policy(ctx, password); err != nil {
		return "", err
	}
	var subject string
	hashed := storage.Hash(token)
	err := verifier.db.WithTransaction(ctx, func(ctx context.Context) error {
		pending, err := verifier.db.Find[struct {
			Subject string `db:"subject"`
		}](ctx, query.Table(verifier.resets, func(statement *query.Builder) {
			statement.Select("subject").Where("token_hash", hashed).Where("expires_at", ">", time.Now().Unix()).Limit(1)
		}))
		if errors.Is(err, database.ErrNoRows) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		// The delete is the single-use gate: of two concurrent requests with the
		// same token only one removes the row.
		affected, err := verifier.db.Exec(ctx, query.Table(verifier.resets, func(statement *query.Builder) {
			statement.Where("token_hash", hashed).Delete()
		}))
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrInvalidToken
		}
		subject = pending.Subject
		// The user's other live tokens die with this one: the password they
		// were meant to reset no longer exists.
		_, err = verifier.db.Exec(ctx, query.Table(verifier.resets, func(statement *query.Builder) {
			statement.Where("subject", subject).Delete()
		}))
		if err != nil {
			return err
		}
		if err := verifier.save(ctx, subject, password); err != nil {
			return err
		}
		if verifier.users.ProveEmail != nil {
			if err := verifier.users.ProveEmail(ctx, subject); err != nil {
				return err
			}
		}

		return verifier.users.Rotate(ctx, subject)
	})
	if errors.Is(err, ErrInvalidToken) {
		return "", ErrInvalidToken
	}
	if err != nil {
		return "", fmt.Errorf("password: reset: %w", err)
	}
	verifier.emit(ctx, WasReset, subject)

	return subject, nil
}

// emit publishes topic with the subject once the surrounding transaction, if
// any, has committed.
func (verifier *Verifier) emit(ctx context.Context, topic event.Topic[UserEvent], subject string) {
	// A failing hook only loses a notification, never the committed change.
	_ = verifier.db.AfterCommit(ctx, func(ctx context.Context) error {
		verifier.events.Emit(ctx, topic, UserEvent{Subject: subject})

		return nil
	})
}

// attempt reserves one attempt on key and fails with [ErrThrottled] and a
// Retry-After header when the counter exceeds limit.
func (verifier *Verifier) attempt(ctx context.Context, key cache.Key[int64], limit int64) error {
	return throttle.Attempt(ctx, verifier.store, key, limit, throttleWindow, ErrThrottled)
}

// refund gives back one attempt reserved on key, for a login that succeeded:
// only failures count against the limits.
func (verifier *Verifier) refund(ctx context.Context, key cache.Key[int64]) {
	_, _ = verifier.store.Decrement(ctx, key, 1)
}

// Wait blocks until the reset emails that [Verifier.ForgotHandler] sent in the
// background have been handed to [Builder.Deliver], or returns the error of ctx
// when it ends first. Call it during shutdown, after the server stopped
// accepting requests, so no message is lost; tests call it before asserting on
// what was delivered.
//
// Example:
//
//	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	defer cancel()
//	_ = verifier.Wait(ctx)
func (verifier *Verifier) Wait(ctx context.Context) error {
	return verifier.jobs.Wait(ctx)
}

// clear forgets the attempts counted on key after a success. Counters expire on
// their own, so a failed cleanup only keeps a stale count.
func (verifier *Verifier) clear(ctx context.Context, key cache.Key[int64]) {
	_ = verifier.store.Delete(ctx, key)
}

// identifierKey is the counter key of one identifier, lowercased and hashed so
// the key has a fixed shape whatever the user typed.
func (verifier *Verifier) identifierKey(identifier string) cache.Key[int64] {
	return cache.NewKey[int64]("access:" + verifier.instance + ":password:id:" + storage.Hash(strings.ToLower(strings.TrimSpace(identifier))))
}

// clientKey is the counter key of the client address of r.
func (verifier *Verifier) clientKey(r *http.Request) cache.Key[int64] {
	return cache.NewKey[int64]("access:" + verifier.instance + ":password:ip:" + verifier.clientIP(r))
}

// pairKey is the counter key of one identifier from one client address, the
// login lockout: an attacker's failures lock only the attacker's own address
// out of the account.
func (verifier *Verifier) pairKey(identifier string, r *http.Request) cache.Key[int64] {
	return cache.NewKey[int64]("access:" + verifier.instance + ":password:pair:" + storage.Hash(strings.ToLower(strings.TrimSpace(identifier))) + ":" + verifier.clientIP(r))
}

// throttleAction counts one request of action (such as "forgot") on the
// identifier as typed and on the client address of r, before any lookup, so the
// answer cannot reveal whether the identifier belongs to a user.
func (verifier *Verifier) throttleAction(r *http.Request, action, identifier string, perIdentifier, perClient int64) error {
	prefix := "access:" + verifier.instance + ":password:" + action
	byIdentifier := cache.NewKey[int64](prefix + ":id:" + storage.Hash(strings.ToLower(strings.TrimSpace(identifier))))
	if err := verifier.attempt(r.Context(), byIdentifier, perIdentifier); err != nil {
		return err
	}

	return verifier.attempt(r.Context(), cache.NewKey[int64](prefix+":ip:"+verifier.clientIP(r)), perClient)
}
