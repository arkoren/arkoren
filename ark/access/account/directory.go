package account

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"

	"arkoren.dev/ark/access/internal/background"
	"arkoren.dev/ark/access/internal/storage"
)

// DefaultVerifyTTL is how long an email verification token stays valid when
// [Builder.VerifyTTL] is not set: 24 hours.
//
// Example:
//
//	accounts.VerifyTTL(account.DefaultVerifyTTL / 2)
const DefaultVerifyTTL = 24 * time.Hour

// maxEmailLength is the longest accepted address in bytes (RFC 5321).
const maxEmailLength = 254

// ErrInvalidConfig is wrapped by the errors of [NewDirectory] for an unusable
// configuration, such as a nil database or an invalid instance.
//
// Example:
//
//	_, err := account.NewDirectory(nil)
//	fmt.Println(errors.Is(err, account.ErrInvalidConfig)) // true
var ErrInvalidConfig = errors.New("account: invalid configuration")

// ErrNotMigrated is returned by [Directory.Ping] when the tables do not exist.
// Add [Directory.Migrations] to the application's migrator and run them.
//
// Example:
//
//	if errors.Is(directory.Ping(ctx), account.ErrNotMigrated) {
//		// run the migrator
//	}
var ErrNotMigrated = storage.ErrNotMigrated

// Account is the application-facing view of a user, carried as
// [auth.UserRecord.Value] and read with auth.UserFromContext[account.Account].
//
// Example:
//
//	user, ok := auth.UserFromContext[account.Account](r.Context())
//	if ok {
//		fmt.Println(user.Email, user.Verified())
//	}
type Account struct {
	// Subject is the user's identifier, a 32-character lowercase hex string.
	Subject string `json:"subject"`

	// Email is the lowercased, trimmed address.
	Email string `json:"email"`

	// VerifiedAt is when the user proved control of Email; zero means unverified.
	VerifiedAt time.Time `json:"verified_at,omitzero"`

	// CreatedAt is when the user was created.
	CreatedAt time.Time `json:"created_at"`
}

// Verified reports whether the user proved control of their email.
//
// Example:
//
//	if !user.Verified() {
//		// ask the user to verify
//	}
func (account Account) Verified() bool {
	return !account.VerifiedAt.IsZero()
}

// userRow is one row of the users table.
type userRow struct {
	ID         string `db:"id"`
	Email      string `db:"email"`
	Version    int64  `db:"version"`
	VerifiedAt int64  `db:"verified_at"`
	DisabledAt int64  `db:"disabled_at"`
	CreatedAt  int64  `db:"created_at"`
}

// Directory is the users table and its [auth.UserProvider]. It implements
// [auth.UserDriver], so the guard finds users through it, and offers the
// lookups the other packages need as plain functions of subjects and
// identifiers. It is safe for concurrent use.
//
// Create one with [NewDirectory], add [Directory.Migrations] to the
// application's migrator, and pass [Directory.Provider] to the guard. Build it
// once: a second directory over the same tables would own a second provider
// and forget hooks.
//
// Example:
//
//	directory, err := account.NewDirectory(db, store, ratelimit.TrustedProxyKey())
//	if err != nil {
//		return err
//	}
//	guard, err := auth.NewGuard(directory.Provider(), store)
type Directory struct {
	db        *database.Database
	instance  string
	users     string
	verifs    string
	events    *event.Bus
	deliver   func(context.Context, Message) error
	verifyTTL time.Duration
	provider  *auth.UserProvider
	store     *cache.Cache
	clientIP  func(*http.Request) string
	jobs      background.Group
}

// NewDirectory returns the directory over db. store counts the verification
// resends of [Directory.ResendHandler] and clientIP finds the client address
// they are keyed on; both are required, because an unlimited resend route lets
// one user flood a mailbox and the right address source depends on the
// deployment: behind a reverse proxy r.RemoteAddr is the proxy, so pass
// ratelimit.TrustedProxyKey with your proxies' prefixes, or with none when no
// proxy sits in front of the application. It does no I/O: the tables come
// from [Directory.Migrations], which the application's migrator runs, and
// [Directory.Ping] verifies they exist. Errors wrap [ErrInvalidConfig].
//
// The directory registers a forget hook on its provider that removes the
// user's pending verification, so forgetting a user in one transaction leaves
// no row of this package behind.
//
// Example:
//
//	directory, err := account.NewDirectory(db, store, ratelimit.TrustedProxyKey(), func(accounts *account.Builder) {
//		accounts.Events(events)
//	})
//	if err != nil {
//		return err
//	}
func NewDirectory(db *database.Database, store *cache.Cache, clientIP func(*http.Request) string, configure ...BuilderFunc) (*Directory, error) {
	if db == nil || store == nil || clientIP == nil {
		return nil, fmt.Errorf("%w: database, store and client address source are required", ErrInvalidConfig)
	}
	settings := Builder{}
	for _, apply := range configure {
		apply(&settings)
	}
	if err := settings.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	directory := &Directory{
		db:        db,
		instance:  settings.instance,
		users:     storage.Table(settings.instance, "users"),
		verifs:    storage.Table(settings.instance, "email_verifications"),
		events:    settings.events,
		deliver:   settings.deliver,
		verifyTTL: cmp.Or(settings.verifyTTL, DefaultVerifyTTL),
		store:     store,
		clientIP:  clientIP,
	}
	provider, err := auth.NewUserProvider(directory, func(users *auth.UserProviderBuilder) {
		users.Events(settings.events)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	provider.OnForget(directory.forget)
	directory.provider = provider

	return directory, nil
}

// Provider returns the [auth.UserProvider] of the directory, the one value the
// guard, the password verifier, the two-factor store, the token issuer and the
// link table share. It is built once by [NewDirectory].
//
// Example:
//
//	guard, err := auth.NewGuard(directory.Provider(), store)
func (directory *Directory) Provider() *auth.UserProvider {
	return directory.provider
}

// Migrations returns the migrations that create the "users" and
// "email_verifications" tables (prefixed "<instance>_" for a named instance) in
// the group "arkoren.dev:ark/access:account", plus ":<instance>". Each creates
// one table strictly, and its down step drops it. The application adds them
// to its migrator ahead of any migration that needs them; constructing or
// reading them never runs DDL.
//
// Example:
//
//	if err := migrator.Add(directory.Migrations()...); err != nil {
//		return err
//	}
func (directory *Directory) Migrations() []migrate.Definition {
	return storage.Group("account", directory.instance).Migrations(
		migrate.NewMigration("users-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(directory.users, func(table *schema.Table) {
				table.String("id", 255).Bytewise().Primary()
				table.String("email", 255).Bytewise().Unique()
				table.BigInteger("version").Default(0)
				table.BigInteger("verified_at").Default(0)
				table.BigInteger("disabled_at").Default(0)
				table.BigInteger("created_at")
			})))
			migration.Down(migrate.Schema(schema.Drop(directory.users)))
		}),
		migrate.NewMigration("email-verifications-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(directory.verifs, func(table *schema.Table) {
				table.String("subject", 255).Bytewise().Primary()
				table.String("token_hash", 64).Bytewise().Unique()
				table.BigInteger("expires_at")
			})))
			migration.Down(migrate.Schema(schema.Drop(directory.verifs)))
		}),
	)
}

// Ping verifies, read-only, that both tables exist. A missing table returns an
// error wrapping [ErrNotMigrated] that names Migrations. Use it as a readiness
// check.
//
// Example:
//
//	if err := directory.Ping(ctx); err != nil {
//		return err
//	}
func (directory *Directory) Ping(ctx context.Context) error {
	return storage.Ping(ctx, directory.db, directory.users, directory.verifs)
}

// Find returns the record of subject, which makes the directory an
// [auth.UserDriver]. The record's Version is the user's version, its Claims are
// "email" and "email_verified", and its Value is an [Account]. An invalid
// subject wraps [auth.ErrInvalidSubject], a missing user [auth.ErrUserNotFound]
// and a disabled one [auth.ErrUserDisabled].
//
// Example:
//
//	record, err := directory.Find(ctx, subject)
//	if err != nil {
//		return err
//	}
//	fmt.Println(record.Claims["email"])
func (directory *Directory) Find(ctx context.Context, subject string) (auth.UserRecord, error) {
	if err := auth.ValidateSubject(subject); err != nil {
		return auth.UserRecord{}, err
	}
	row, err := directory.row(ctx, "id", subject)
	if err != nil {
		return auth.UserRecord{Subject: subject}, err
	}
	if row.DisabledAt != 0 {
		return auth.UserRecord{Subject: subject}, fmt.Errorf("account: %w", auth.ErrUserDisabled)
	}

	return auth.UserRecord{
		Subject: subject,
		Version: strconv.FormatInt(row.Version, 10),
		Claims:  map[string]any{"email": row.Email, "email_verified": row.VerifiedAt != 0},
		Value: Account{
			Subject:    row.ID,
			Email:      row.Email,
			VerifiedAt: storage.Time(row.VerifiedAt),
			CreatedAt:  storage.Time(row.CreatedAt),
		},
	}, nil
}

// row loads the user whose column equals value, or an error wrapping
// [auth.ErrUserNotFound].
func (directory *Directory) row(ctx context.Context, column, value string) (userRow, error) {
	row, err := directory.db.Find[userRow](ctx, query.Table(directory.users, func(statement *query.Builder) {
		statement.Where(column, value).Limit(1)
	}))
	if errors.Is(err, database.ErrNoRows) {
		return userRow{}, fmt.Errorf("account: %w", auth.ErrUserNotFound)
	}

	return row, err
}

// normalizeEmail trims and lowercases email and requires a plain address
// ("ada@example.com", without a display name) of at most 254 bytes.
func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > maxEmailLength {
		return "", ErrInvalidEmail
	}

	return email, nil
}

// Create adds an unverified user with email and returns their subject. It
// never matches an existing user by email: an address in use returns
// [ErrEmailTaken], and an unusable one [ErrInvalidEmail]. Both are problems with
// status 409 and 422. It publishes [UserCreated] after the surrounding
// transaction commits.
//
// Example:
//
//	subject, err := directory.Create(ctx, "ada@example.com")
func (directory *Directory) Create(ctx context.Context, email string) (string, error) {
	return directory.create(ctx, email, false)
}

// CreateVerified is [Directory.Create] for an address an external identity
// provider already verified, such as a social login whose
// [auth.External.EmailVerified] is true: the user starts verified.
//
// Example:
//
//	subject, err := directory.CreateVerified(ctx, external.Email)
func (directory *Directory) CreateVerified(ctx context.Context, email string) (string, error) {
	return directory.create(ctx, email, true)
}

// create inserts the user. The unique email index decides a concurrent
// duplicate: the loser's insert does nothing and affects no row.
func (directory *Directory) create(ctx context.Context, email string, verified bool) (string, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return "", err
	}
	id, err := storage.NewID()
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	row := map[string]any{"id": id, "email": email, "version": 0, "verified_at": 0, "disabled_at": 0, "created_at": now}
	if verified {
		row["verified_at"] = now
	}
	affected, err := directory.db.Exec(ctx, query.Table(directory.users, func(statement *query.Builder) {
		statement.Insert(row)
		statement.OnConflict("email").DoNothing()
	}))
	if err != nil {
		return "", fmt.Errorf("account: create user: %w", err)
	}
	if affected == 0 {
		return "", ErrEmailTaken
	}
	directory.emit(ctx, UserCreated, id)

	return id, nil
}

// Lookup returns the subject and the stored address of the user with email, or
// an error wrapping [auth.ErrUserNotFound]. An unusable address is also not
// found, so a login attempt cannot tell the two apart. It is the Lookup
// function the password package needs when the directory is the user store.
//
// The returned address is the one stored with the user, which can differ from
// what the caller typed (letter case, or a character that lowercases to
// another such as "İ"). Mail about the account must go to it and never to the
// typed text, or an attacker could have a victim's reset link sent to a
// look-alike address.
//
// Example:
//
//	subject, address, err := directory.Lookup(ctx, "ada@example.com")
//	if errors.Is(err, auth.ErrUserNotFound) {
//		// unknown user
//	}
func (directory *Directory) Lookup(ctx context.Context, email string) (subject, address string, err error) {
	email, err = normalizeEmail(email)
	if err != nil {
		return "", "", fmt.Errorf("account: %w", auth.ErrUserNotFound)
	}
	row, err := directory.row(ctx, "email", email)
	if err != nil {
		return "", "", err
	}

	return row.ID, row.Email, nil
}

// Verified reports whether the user's email is verified, read from the table
// at call time, never from a cached claim. It is the function the facade gives
// the other packages' Verified settings. An unknown subject returns an error
// wrapping [auth.ErrUserNotFound].
//
// Example:
//
//	ok, err := directory.Verified(ctx, subject)
func (directory *Directory) Verified(ctx context.Context, subject string) (bool, error) {
	row, err := directory.row(ctx, "id", subject)
	if err != nil {
		return false, err
	}

	return row.VerifiedAt != 0, nil
}

// ProveEmail marks the user's email verified because the user just proved they
// control the mailbox, as a completed password reset does, and drops any
// pending verification token. It reports whether the user was unverified
// before: that is the signal that someone else may have registered the address
// first, so the caller must then remove everything an earlier holder could have
// attached (connections, two-factor, tokens). The update is atomic, so of two
// concurrent calls exactly one reports true. It publishes [EmailVerified] when
// it changed something and returns an error wrapping [auth.ErrUserNotFound] for
// an unknown subject.
//
// Example:
//
//	changed, err := directory.ProveEmail(ctx, subject)
//	if err != nil {
//		return err
//	}
//	if changed {
//		// purge what a squatter attached
//	}
func (directory *Directory) ProveEmail(ctx context.Context, subject string) (bool, error) {
	affected, err := directory.db.Exec(ctx, query.Table(directory.users, func(statement *query.Builder) {
		statement.Where("id", subject).Where("verified_at", 0).Update(map[string]any{"verified_at": time.Now().Unix()})
	}))
	if err != nil {
		return false, fmt.Errorf("account: prove email: %w", err)
	}
	if affected == 0 {
		_, err := directory.row(ctx, "id", subject)

		return false, err
	}
	if err := directory.forget(ctx, subject); err != nil {
		return false, err
	}
	directory.emit(ctx, EmailVerified, subject)

	return true, nil
}

// Rotate bumps the user's version, which ends every session, pending login and
// API token issued under the old one. A password reset calls it. It returns an
// error wrapping [auth.ErrUserNotFound] for an unknown subject.
//
// Example:
//
//	if err := directory.Rotate(ctx, subject); err != nil {
//		return err
//	}
func (directory *Directory) Rotate(ctx context.Context, subject string) error {
	affected, err := directory.db.Exec(ctx, query.Table(directory.users, func(statement *query.Builder) {
		statement.Where("id", subject).Increment("version", 1)
	}))
	if err != nil {
		return fmt.Errorf("account: rotate: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("account: %w", auth.ErrUserNotFound)
	}

	return nil
}

// Disable marks the user disabled: [Directory.Find] then fails with
// [auth.ErrUserDisabled], so every session, token and pending login of the
// user is rejected at the next request. [Directory.Enable] reverses it. An
// unknown subject returns an error wrapping [auth.ErrUserNotFound].
//
// Example:
//
//	err := directory.Disable(ctx, subject)
func (directory *Directory) Disable(ctx context.Context, subject string) error {
	return directory.setDisabled(ctx, subject, time.Now().Unix())
}

// Enable clears the disabled mark set by [Directory.Disable].
//
// Example:
//
//	err := directory.Enable(ctx, subject)
func (directory *Directory) Enable(ctx context.Context, subject string) error {
	return directory.setDisabled(ctx, subject, 0)
}

// setDisabled writes disabled_at and reports an unknown subject.
func (directory *Directory) setDisabled(ctx context.Context, subject string, at int64) error {
	if _, err := directory.row(ctx, "id", subject); err != nil {
		return err
	}
	_, err := directory.db.Exec(ctx, query.Table(directory.users, func(statement *query.Builder) {
		statement.Where("id", subject).Update(map[string]any{"disabled_at": at})
	}))
	if err != nil {
		return fmt.Errorf("account: set disabled: %w", err)
	}

	return nil
}

// Delete forgets the user and removes their row in one transaction: every forget
// hook registered on [Directory.Provider] (the other packages' rows, the guard's
// login records) runs first, and a failure of any of them leaves the user in
// place. It publishes [UserDeleted] after the commit. An unknown subject returns
// an error wrapping [auth.ErrUserNotFound].
//
// Example:
//
//	if err := directory.Delete(ctx, subject); err != nil {
//		return err
//	}
func (directory *Directory) Delete(ctx context.Context, subject string) error {
	err := directory.db.WithTransaction(ctx, func(ctx context.Context) error {
		if err := directory.provider.Forget(ctx, subject); err != nil {
			return err
		}
		affected, err := directory.db.Exec(ctx, query.Table(directory.users, func(statement *query.Builder) {
			statement.Where("id", subject).Delete()
		}))
		if err != nil {
			return fmt.Errorf("account: delete user: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("account: %w", auth.ErrUserNotFound)
		}

		return nil
	})
	if err != nil {
		return err
	}
	directory.emit(ctx, UserDeleted, subject)

	return nil
}

// forget removes the rows of this package that reference subject, in the
// caller's transaction. The user row itself is removed by [Directory.Delete].
func (directory *Directory) forget(ctx context.Context, subject string) error {
	_, err := directory.db.Exec(ctx, query.Table(directory.verifs, func(statement *query.Builder) {
		statement.Where("subject", subject).Delete()
	}))
	if err != nil {
		return fmt.Errorf("account: forget verification: %w", err)
	}

	return nil
}

// Wait blocks until the verification emails that [Directory.ResendHandler] sent
// in the background have been handed to [Builder.Deliver], or returns the error
// of ctx when it ends first. Call it during shutdown, after the server stopped
// accepting requests, so no message is lost; tests call it before asserting on
// what was delivered.
//
// Example:
//
//	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	defer cancel()
//	_ = directory.Wait(ctx)
func (directory *Directory) Wait(ctx context.Context) error {
	return directory.jobs.Wait(ctx)
}

// emit publishes topic with the subject once the surrounding transaction, if
// any, has committed.
func (directory *Directory) emit(ctx context.Context, topic event.Topic[UserEvent], subject string) {
	// A failing hook only loses a notification, never the committed change.
	_ = directory.db.AfterCommit(ctx, func(ctx context.Context) error {
		directory.events.Emit(ctx, topic, UserEvent{Subject: subject})

		return nil
	})
}
