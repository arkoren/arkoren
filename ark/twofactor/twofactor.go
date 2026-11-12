package twofactor

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/hash"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/totp"
)

// maxInstanceLength is the longest instance name in bytes. With the longest
// table suffix ("_twofactor_recovery_codes") it keeps every generated
// identifier under the 63-byte PostgreSQL limit.
const maxInstanceLength = 30

// maxRecoveryCodes bounds [Builder.RecoveryCodes]: each code costs one password
// hash to issue and up to one per attempt to check.
const maxRecoveryCodes = 100

// TwoFactor is the second-factor ark: TOTP enrollment, verification with replay
// rejection, single-use recovery codes, and the cleanup that keeps its tables
// free of deleted users. Create it with [New]. It is safe for concurrent use,
// borrows the database, cipher and hasher, and starts no goroutine. The
// application owns the lifecycle of all three and runs [TwoFactor.Migrations]
// before use; the ark never runs DDL.
//
// Rows are keyed by the user's subject, the text form of the identity's user ID
// (see [auth.SessionIdentity.Subject]), stored as a varchar(255) compared
// bytewise.
//
// Example:
//
//	factors, err := twofactor.New(identity, db, cipher, hasher, func(ark *twofactor.Builder) {
//		ark.Issuer("Example")
//	})
//	if err != nil {
//		return err
//	}
//	if err := migrator.Add(factors.Migrations()...); err != nil {
//		return err
//	}
//	web.Post("/two-factor/verify", factors.Verify(identity))
type TwoFactor struct {
	// db runs every statement. The application owns it.
	db *database.Database

	// cipher encrypts TOTP secrets at rest.
	cipher *crypto.Encrypter

	// hasher hashes recovery codes.
	hasher *hash.Hasher

	// directory resolves the current request's subject and whether a subject
	// still exists, without knowing the application's user type.
	directory auth.Directory

	// config holds the validated settings.
	config settings

	// secrets is the name of the secrets table, with the instance prefix.
	secrets string

	// recovery is the name of the recovery codes table, with the instance prefix.
	recovery string
}

// New creates the ark for the users of identity, storing its tables through db.
// Secrets are encrypted with cipher (use a key reserved for this purpose) and
// recovery codes are hashed with hasher (a deliberately slow password hash such
// as bcrypt or Argon2, since recovery codes carry far less entropy than a
// key). It does no I/O: the tables come from [TwoFactor.Migrations], which the
// application runs, and [TwoFactor.Ping] verifies them.
//
// Only the identity's [auth.Directory] is kept, so the ark never sees the
// application's user type. [Builder.Issuer] is required. Callbacks run once in
// order on one fresh draft before validation; nil entries panic, expanded
// nil/empty slices omit edits, and retained drafts are detached.
//
// New returns an error wrapping [ErrInvalidConfig] for a nil database, cipher
// or hasher, an identity that is invalid or whose user ID type is not
// supported by [auth.SessionIdentity.Directory], a database dialect other than
// PostgreSQL, MySQL or SQLite, or an invalid setting.
//
// Example:
//
//	factors, err := twofactor.New(identity, db, cipher, hasher, func(ark *twofactor.Builder) {
//		ark.Issuer("Example")
//		ark.Instance("admin")
//	})
//	if err != nil {
//		return err
//	}
//	_ = factors
func New[K any, U auth.Identifiable[K]](identity auth.SessionIdentity[K, U], db *database.Database, cipher *crypto.Encrypter, hasher *hash.Hasher, configure ...BuilderFunc) (*TwoFactor, error) {
	ark := &Builder{config: settings{scheme: totp.New()}}

	for _, configure := range configure {
		configure(ark)
	}

	config := ark.config
	directory, err := identity.Directory()

	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	case db == nil:
		return nil, fmt.Errorf("%w: database is nil", ErrInvalidConfig)
	case cipher == nil:
		return nil, fmt.Errorf("%w: cipher is nil", ErrInvalidConfig)
	case hasher == nil:
		return nil, fmt.Errorf("%w: hasher is nil", ErrInvalidConfig)
	case db.Dialect() != query.Postgres && db.Dialect() != query.MySQL && db.Dialect() != query.SQLite:
		return nil, fmt.Errorf("%w: the database must use the PostgreSQL, MySQL or SQLite dialect", ErrInvalidConfig)
	case config.issuer == "" || strings.Contains(config.issuer, ":"):
		return nil, fmt.Errorf("%w: Builder.Issuer is required and must not contain a colon", ErrInvalidConfig)
	case config.scheme.Err() != nil:
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, config.scheme.Err())
	case config.recoveryCodes < 0 || config.recoveryCodes > maxRecoveryCodes:
		return nil, fmt.Errorf("%w: recovery codes must be between 0 and %d", ErrInvalidConfig, maxRecoveryCodes)
	}

	if config.recoveryCodes == 0 {
		config.recoveryCodes = defaultRecoveryCodes
	}

	prefix := ""

	if config.instance != "" {
		if len(config.instance) > maxInstanceLength ||
			strings.Trim(config.instance, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" ||
			config.instance[0] >= '0' && config.instance[0] <= '9' {
			return nil, fmt.Errorf("%w: instance %q must be at most %d bytes of [a-z0-9_] and not start with a digit",
				ErrInvalidConfig, config.instance, maxInstanceLength)
		}

		prefix = config.instance + "_"
	}

	return &TwoFactor{
		db:        db,
		cipher:    cipher,
		hasher:    hasher,
		directory: directory,
		config:    config,
		secrets:   prefix + "twofactor_secrets",
		recovery:  prefix + "twofactor_recovery_codes",
	}, nil
}

// Ping verifies the connection and that both tables exist, read-only. When a
// table is missing it returns an error wrapping [ErrNotFound] that names
// [TwoFactor.Migrations] and the missing tables. Use it as a readiness check;
// it never creates tables. Database errors, including cancellation, are
// returned wrapped without [ErrNotFound].
//
// Example:
//
//	app.Check("twofactor", factors.Ping)
func (twofactor *TwoFactor) Ping(ctx context.Context) error {
	var catalog query.RawStatement

	switch twofactor.db.Dialect() {
	case query.Postgres:
		catalog = query.Raw("SELECT table_name AS name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name IN (?, ?)", twofactor.secrets, twofactor.recovery)
	case query.MySQL:
		catalog = query.Raw("SELECT table_name AS name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN (?, ?)", twofactor.secrets, twofactor.recovery)
	default:
		catalog = query.Raw("SELECT name FROM sqlite_master WHERE type = 'table' AND name IN (?, ?)", twofactor.secrets, twofactor.recovery)
	}

	present, err := twofactor.db.Select[string](ctx, catalog)

	if err != nil {
		return fmt.Errorf("two-factor ping: %w", err)
	}

	var missing []string

	for _, name := range []string{twofactor.secrets, twofactor.recovery} {
		if !slices.Contains(present.Items(), name) {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("two-factor: %w: table %s does not exist; add and run TwoFactor.Migrations() with the migrator",
			ErrNotFound, strings.Join(missing, ", "))
	}

	return nil
}

// Enrolled reports whether subject has a confirmed enrollment. Use it after the
// first factor to choose between [auth.SessionIdentity.Login] (no second factor
// set up) and [auth.SessionIdentity.Challenge] (second factor required).
// Obtain the subject from [auth.SessionIdentity.Subject]. An enrollment that
// was started but not confirmed does not count.
//
// Example:
//
//	subject, err := identity.Subject(user)
//	if err != nil {
//		return err
//	}
//	enrolled, err := factors.Enrolled(r.Context(), subject)
//	if err != nil {
//		return err
//	}
//	if enrolled {
//		return identity.Challenge(current, user, auth.MethodPassword)
//	}
//	return identity.Login(current, user, auth.MethodPassword)
func (twofactor *TwoFactor) Enrolled(ctx context.Context, subject string) (bool, error) {
	rows, err := twofactor.db.Select[string](ctx, query.Table(twofactor.secrets, func(statement *query.Builder) {
		statement.Select("user_id").Where("user_id", subject).WhereNotNull("confirmed_at")
	}))

	return rows.IsNotEmpty(), err
}

// Forget removes everything the ark stores for subject: the secret and every
// recovery code. It is idempotent, so forgetting an unknown subject succeeds.
// It runs through the ark's database, so inside [database.Database.WithTransaction]
// it joins the caller's transaction and rolls back with it; call it in the same
// transaction that deletes the user.
//
// The ark keeps no external state, so there is nothing to defer: unlike an ark
// that must cancel something at a provider, Forget completes all of its work
// in the transaction. [TwoFactor.Sweep] repairs a missed call.
//
// Example:
//
//	err := db.WithTransaction(ctx, func(ctx context.Context) error {
//		subject, err := identity.Subject(user)
//		if err != nil {
//			return err
//		}
//		if err := factors.Forget(ctx, subject); err != nil {
//			return err
//		}
//		return users.Delete(ctx, user.ID)
//	})
func (twofactor *TwoFactor) Forget(ctx context.Context, subject string) error {
	return twofactor.db.WithTransaction(ctx, func(ctx context.Context) error {
		for _, table := range []string{twofactor.secrets, twofactor.recovery} {
			_, err := twofactor.db.Exec(ctx, query.Table(table, func(statement *query.Builder) {
				statement.Delete().Where("user_id", subject)
			}))

			if err != nil {
				return fmt.Errorf("two-factor forget: %w", err)
			}
		}

		return nil
	})
}
