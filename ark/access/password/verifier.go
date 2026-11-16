package password

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
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
	if err := settings.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
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
		resetTTL:   cmp.Or(settings.resetTTL, DefaultResetTTL),
		policy:     settings.policy,
		clientIP:   settings.clientIP,
		registered: settings.registered,
		changed:    settings.changed,
	}
	if verifier.policy == nil {
		verifier.policy = defaultPolicy
	}
	if verifier.clientIP == nil {
		verifier.clientIP = throttle.RemoteHost
	}
	provider.OnForget(verifier.forget)

	return verifier, nil
}

// defaultPolicy requires [MinLength] to [MaxLength] bytes.
func defaultPolicy(_ context.Context, password string) error {
	if len(password) < MinLength || len(password) > MaxLength {
		return fmt.Errorf("%w: use %d to %d bytes", ErrInvalidPassword, MinLength, MaxLength)
	}

	return nil
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
				table.String("address", 255).Bytewise()
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

// emit publishes topic with the subject once the surrounding transaction, if
// any, has committed.
func (verifier *Verifier) emit(ctx context.Context, topic event.Topic[UserEvent], subject string) {
	// A failing hook only loses a notification, never the committed change.
	_ = verifier.db.AfterCommit(ctx, func(ctx context.Context) error {
		verifier.events.Emit(ctx, topic, UserEvent{Subject: subject})

		return nil
	})
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
