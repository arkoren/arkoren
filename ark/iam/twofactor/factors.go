package twofactor

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/crypto"
	"arkoren.dev/core/service/database"
	"arkoren.dev/core/service/database/migrate"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/query/schema"
	"arkoren.dev/foundation/totp"

	"arkoren.dev/ark/iam/internal/storage"
	"arkoren.dev/ark/iam/internal/throttle"
)

const (
	// MethodRecovery names the recovery-code method in challenge and confirm
	// requests. RFC 8176 has no value for it, so it belongs to this package.
	//
	// Example:
	//
	//	body := `{"method": "recovery", "code": "ABCD-EFGH-IJKL-MNOP"}`
	//	_ = twofactor.MethodRecovery // the value of "method" in body
	MethodRecovery auth.Method = "recovery"

	// DefaultRecoveryCodes is how many recovery codes enabling two-factor
	// creates when [Builder.RecoveryCodes] is not set.
	//
	// Example:
	//
	//	factors.RecoveryCodes(twofactor.DefaultRecoveryCodes)
	DefaultRecoveryCodes = 10

	// DefaultStepUpWindow is how recent the second factor must be for the
	// handlers that weaken two-factor when [Builder.StepUpWindow] is not set:
	// 15 minutes, core's default freshness window.
	//
	// Example:
	//
	//	factors.StepUpWindow(twofactor.DefaultStepUpWindow / 3)
	DefaultStepUpWindow = 15 * time.Minute

	// maxRecoveryCodes bounds [Builder.RecoveryCodes].
	maxRecoveryCodes = 100

	// activateFailures is how many wrong codes [Factors.Activate] accepts per
	// user within activateWindow.
	activateFailures = 10

	// activateWindow is the lifetime of the activation attempt counter.
	activateWindow = 15 * time.Minute
)

// Enrollment is what setup returns: the secret for manual entry and the
// otpauth:// URI for a QR code. Both contain the secret; show them only to the
// enrolling user and never log them.
//
// Example:
//
//	enrollment, err := factors.Enroll(ctx, subject, "ada@example.com")
//	fmt.Println(enrollment.URI)
type Enrollment struct {
	// Secret is the base32 secret, for typing into an authenticator.
	Secret string `json:"secret"`

	// URI is the otpauth:// provisioning URI to render as a QR code.
	URI string `json:"uri"`
}

// Status describes a user's two-factor state for the application's own
// account page.
//
// Example:
//
//	status, err := factors.Status(ctx, subject)
//	fmt.Println(status.Enabled, status.RecoveryCodes)
type Status struct {
	// Enabled reports whether the user has a confirmed authenticator.
	Enabled bool `json:"enabled"`

	// EnabledAt is when the authenticator was confirmed.
	EnabledAt time.Time `json:"enabled_at,omitzero"`

	// RecoveryCodes is how many recovery codes are still unused.
	RecoveryCodes int `json:"recovery_codes"`
}

// Factors owns the TOTP secrets and recovery codes. It is safe for concurrent
// use. Create it with [NewFactors].
//
// Example:
//
//	scheme := totp.New("Acme") // the name authenticator apps show for this account
//	factors, err := twofactor.NewFactors(provider, db, store, scheme, cipher)
type Factors struct {
	db       *database.Database
	store    *cache.Cache
	cipher   *crypto.Encrypter
	instance string
	secrets  string
	codes    string
	events   *event.Bus
	scheme   totp.Scheme
	count    int
	window   time.Duration
	verified func(context.Context, string) (bool, error)
	enabled  func(context.Context, string) error
	extra    []auth.Credential
}

// NewFactors returns the two-factor store. scheme holds the TOTP parameters and
// the issuer, the service name authenticator apps show (see [totp.New]); an
// invalid scheme is [ErrInvalidConfig] wrapping [totp.Scheme.Err]. cipher
// encrypts the secrets (use a key reserved for them) and store counts wrong
// activation codes. It does no I/O; the tables come from [Factors.Migrations]. Errors wrap
// [ErrInvalidConfig].
//
// It registers a forget hook on provider that deletes the user's secret and
// codes in the caller's transaction.
//
// Example:
//
//	scheme := totp.New("Acme") // the name authenticator apps show for this account
//	factors, err := twofactor.NewFactors(provider, db, store, scheme, cipher)
func NewFactors(provider *auth.UserProvider, db *database.Database, store *cache.Cache, scheme totp.Scheme, cipher *crypto.Encrypter, configure ...BuilderFunc) (*Factors, error) {
	if provider == nil || db == nil || store == nil || cipher == nil {
		return nil, fmt.Errorf("%w: provider, database, store and cipher are required", ErrInvalidConfig)
	}
	if err := scheme.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	settings := Builder{}
	for _, apply := range configure {
		apply(&settings)
	}
	if err := settings.validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	factors := &Factors{
		db:       db,
		store:    store,
		cipher:   cipher,
		instance: settings.instance,
		secrets:  storage.Table(settings.instance, "totp_secrets"),
		codes:    storage.Table(settings.instance, "recovery_codes"),
		events:   settings.events,
		scheme:   scheme,
		count:    cmp.Or(settings.codes, DefaultRecoveryCodes),
		window:   cmp.Or(settings.window, DefaultStepUpWindow),
		verified: settings.verified,
		enabled:  settings.enabled,
		extra:    slices.Clone(settings.extra),
	}
	provider.OnForget(factors.forget)

	return factors, nil
}

// Migrations returns the migrations that create the "totp_secrets" and
// "recovery_codes" tables (prefixed "<instance>_" for a named instance) in the
// group "arkoren.dev:ark/access:twofactor", plus ":<instance>". Each creates one
// table strictly and its down step drops it. The application adds them to its
// migrator; constructing or reading them never runs DDL.
//
// Example:
//
//	if err := migrator.Add(factors.Migrations()...); err != nil {
//		return err
//	}
func (factors *Factors) Migrations() []migrate.Definition {
	return storage.Group("twofactor", factors.instance).Migrations(
		migrate.NewMigration("secrets-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(factors.secrets, func(table *schema.Table) {
				table.String("subject", 255).Bytewise().Primary()
				table.Text("secret")
				table.BigInteger("last_step").Default(0)
				table.BigInteger("confirmed_at").Default(0)
			})))
			migration.Down(migrate.Schema(schema.Drop(factors.secrets)))
		}),
		migrate.NewMigration("recovery-codes-create-table", func(migration *migrate.MigrationBuilder) {
			migration.Up(migrate.Schema(schema.Create(factors.codes, func(table *schema.Table) {
				table.String("subject", 255).Bytewise()
				table.SmallInteger("slot")
				table.String("code_hash", 64).Bytewise()
				table.BigInteger("used_at").Default(0)
				table.Primary("subject", "slot")
			})))
			migration.Down(migrate.Schema(schema.Drop(factors.codes)))
		}),
	)
}

// Ping verifies, read-only, that both tables exist. A missing table returns an
// error wrapping [ErrNotMigrated]. Use it as a readiness check.
//
// Example:
//
//	if err := factors.Ping(ctx); err != nil {
//		return err
//	}
func (factors *Factors) Ping(ctx context.Context) error {
	return storage.Ping(ctx, factors.db, factors.secrets, factors.codes)
}

// forget deletes the rows of subject in the caller's transaction.
func (factors *Factors) forget(ctx context.Context, subject string) error {
	for _, table := range []string{factors.secrets, factors.codes} {
		_, err := factors.db.Exec(ctx, query.Table(table, func(statement *query.Builder) {
			statement.Where("subject", subject).Delete()
		}))
		if err != nil {
			return fmt.Errorf("twofactor: forget %s: %w", table, err)
		}
	}

	return nil
}

// secretRow is one row of the secrets table.
type secretRow struct {
	Secret      string `db:"secret"`
	LastStep    int64  `db:"last_step"`
	ConfirmedAt int64  `db:"confirmed_at"`
}

// row loads the secret row of subject, or false when there is none.
func (factors *Factors) row(ctx context.Context, subject string) (secretRow, bool, error) {
	row, err := factors.db.Find[secretRow](ctx, query.Table(factors.secrets, func(statement *query.Builder) {
		statement.Select("secret", "last_step", "confirmed_at").Where("subject", subject).Limit(1)
	}))
	if errors.Is(err, database.ErrNoRows) {
		return secretRow{}, false, nil
	}
	if err != nil {
		return secretRow{}, false, fmt.Errorf("twofactor: load secret: %w", err)
	}

	return row, true, nil
}

// Enrolled reports whether subject has a confirmed authenticator. It is the
// Enrolled function of [Factors.Credential].
//
// Example:
//
//	enabled, err := factors.Enrolled(ctx, subject)
func (factors *Factors) Enrolled(ctx context.Context, subject string) (bool, error) {
	row, ok, err := factors.row(ctx, subject)

	return ok && row.ConfirmedAt != 0, err
}

// Status returns the two-factor state of subject for display.
//
// Example:
//
//	status, err := factors.Status(ctx, subject)
//	if err != nil {
//		return err
//	}
//	fmt.Println(status.Enabled)
func (factors *Factors) Status(ctx context.Context, subject string) (Status, error) {
	row, ok, err := factors.row(ctx, subject)
	if err != nil || !ok || row.ConfirmedAt == 0 {
		return Status{}, err
	}
	remaining, err := factors.unused(ctx, subject)
	if err != nil {
		return Status{}, err
	}

	return Status{Enabled: true, EnabledAt: storage.Time(row.ConfirmedAt), RecoveryCodes: remaining}, nil
}

// Enroll starts enrollment: it stores a new pending secret for subject, replacing
// an earlier unconfirmed one, and returns it with a provisioning URI. label is
// what the authenticator app shows (usually the email); a colon in it is
// replaced by a space, because the URI label cannot carry one. A user who
// already has a confirmed authenticator gets [ErrAlreadyEnabled].
//
// Example:
//
//	enrollment, err := factors.Enroll(ctx, subject, "ada@example.com")
//	if err != nil {
//		return err
//	}
//	fmt.Println(enrollment.URI) // render as a QR code
func (factors *Factors) Enroll(ctx context.Context, subject, label string) (Enrollment, error) {
	secret, err := factors.scheme.NewSecret()
	if err != nil {
		return Enrollment{}, fmt.Errorf("twofactor: new secret: %w", err)
	}
	uri, err := factors.scheme.URI(strings.ReplaceAll(label, ":", " "), secret)
	if err != nil {
		return Enrollment{}, fmt.Errorf("twofactor: provisioning uri: %w", err)
	}
	sealed, err := factors.seal(secret)
	if err != nil {
		return Enrollment{}, err
	}
	if err := factors.storePending(ctx, subject, sealed); err != nil {
		return Enrollment{}, err
	}

	return Enrollment{Secret: totp.EncodeSecret(secret), URI: uri}, nil
}

// storePending stores sealed as the pending secret of subject, replacing an
// earlier pending one. A user with a confirmed authenticator gets
// [ErrAlreadyEnabled]. The update goes first because a pending row usually
// exists after a first setup.
func (factors *Factors) storePending(ctx context.Context, subject, sealed string) error {
	replace := func() (int64, error) {
		return factors.db.Exec(ctx, query.Table(factors.secrets, func(statement *query.Builder) {
			statement.Where("subject", subject).Where("confirmed_at", 0).Update(map[string]any{"secret": sealed, "last_step": 0})
		}))
	}
	affected, err := replace()
	if err == nil && affected == 0 {
		affected, err = factors.db.Exec(ctx, query.Table(factors.secrets, func(statement *query.Builder) {
			statement.Insert(map[string]any{"subject": subject, "secret": sealed, "last_step": 0, "confirmed_at": 0})
			statement.OnConflict("subject").DoNothing()
		}))
		if err == nil && affected == 0 {
			// Either a concurrent enrollment inserted the pending row first, or the
			// user has a confirmed authenticator. Replacing the pending row again
			// tells them apart.
			affected, err = replace()
		}
	}
	if err != nil {
		return fmt.Errorf("twofactor: store secret: %w", err)
	}
	if affected == 0 {
		return ErrAlreadyEnabled
	}

	return nil
}

// seal encrypts a secret for storage.
func (factors *Factors) seal(secret []byte) (string, error) {
	sealed, err := factors.cipher.EncryptRaw(secret)
	if err != nil {
		return "", fmt.Errorf("twofactor: encrypt secret: %w", err)
	}

	return base64.StdEncoding.EncodeToString(sealed), nil
}

// open decrypts a stored secret and reports whether a previous key was used.
func (factors *Factors) open(stored string) ([]byte, bool, error) {
	sealed, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return nil, false, fmt.Errorf("twofactor: stored secret is corrupt: %w", err)
	}
	secret, previous, err := factors.cipher.DecryptRawWithRotation(sealed)
	if err != nil {
		return nil, false, fmt.Errorf("twofactor: decrypt secret: %w", err)
	}

	return secret, previous, nil
}

// check verifies a TOTP code against the secret row of subject and spends its
// time step. It returns [ErrInvalidCode] for a wrong code or a replay. Spending
// is one conditional update, so of two concurrent identical codes one wins.
func (factors *Factors) check(ctx context.Context, subject string, row secretRow, code string) error {
	secret, previous, err := factors.open(row.Secret)
	if err != nil {
		return err
	}
	step, err := factors.scheme.Verify(secret, code, time.Now())
	if errors.Is(err, totp.ErrInvalidCode) {
		return ErrInvalidCode
	}
	if err != nil {
		return fmt.Errorf("twofactor: verify code: %w", err)
	}
	values := map[string]any{"last_step": step}
	if previous {
		// The secret was sealed with a rotated-out key: seal it with the current
		// one in the same write that spends the step.
		resealed, err := factors.seal(secret)
		if err != nil {
			return err
		}
		values["secret"] = resealed
	}
	affected, err := factors.db.Exec(ctx, query.Table(factors.secrets, func(statement *query.Builder) {
		statement.Where("subject", subject).Where("last_step", "<", step).Update(values)
	}))
	if err != nil {
		return fmt.Errorf("twofactor: spend step: %w", err)
	}
	if affected == 0 {
		return ErrInvalidCode
	}

	return nil
}

// Verify checks a TOTP code of a confirmed authenticator and returns the factor
// to record (method "otp", kind possession). Every failure is
// [ErrInvalidCode]. A code is accepted once. It does not count failures: core
// counts them when the guard's challenge and confirm handlers call it through
// [Factors.Credential]; code that calls Verify directly needs its own limit.
//
// Example:
//
//	factor, err := factors.Verify(ctx, subject, "123456")
//	if errors.Is(err, twofactor.ErrInvalidCode) {
//		// wrong code
//	}
//	_ = factor
func (factors *Factors) Verify(ctx context.Context, subject, code string) (auth.Factor, error) {
	row, ok, err := factors.row(ctx, subject)
	if err != nil {
		return auth.Factor{}, err
	}
	if !ok || row.ConfirmedAt == 0 {
		return auth.Factor{}, ErrInvalidCode
	}
	if err := factors.check(ctx, subject, row, code); err != nil {
		return auth.Factor{}, err
	}

	return auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession}, nil
}

// Activate confirms a pending enrollment with its first code, enables the
// authenticator and creates the recovery codes, which it returns formatted
// xxxx-xxxx-xxxx-xxxx and never again. A wrong code is [ErrInvalidCode]; wrong
// codes are limited to 10 per 15 minutes per user ([ErrThrottled]). A user who
// never ran [Factors.Enroll] gets [ErrNotEnrolling] and one who is already
// enabled [ErrAlreadyEnabled]. It publishes [Enabled] and [auth.CredentialEnrolled]
// after the surrounding transaction commits.
//
// Example:
//
//	codes, err := factors.Activate(ctx, subject, "123456")
//	if err != nil {
//		return err
//	}
//	fmt.Println(codes)
func (factors *Factors) Activate(ctx context.Context, subject, code string) ([]string, error) {
	key := cache.NewKey[int64]("access:" + factors.instance + ":twofactor:activate:" + storage.Hash(subject))
	if err := throttle.Attempt(ctx, factors.store, key, activateFailures, activateWindow, ErrThrottled); err != nil {
		return nil, err
	}
	var codes []string
	err := factors.db.WithTransaction(ctx, func(ctx context.Context) error {
		var err error
		codes, err = factors.confirm(ctx, subject, code)

		return err
	})
	if err != nil {
		return nil, err
	}
	// Counters expire on their own; a failed cleanup only keeps a stale count.
	_ = factors.store.Delete(ctx, key)
	factors.afterCommit(ctx, func(ctx context.Context) {
		factors.events.Emit(ctx, Enabled, UserEvent{Subject: subject})
		factors.events.Emit(ctx, auth.CredentialEnrolled, auth.CredentialEnrolledEvent{Subject: subject, Method: auth.MethodOTP})
	})

	return codes, nil
}

// confirm checks the first code of the pending enrollment of subject, marks it
// confirmed and creates the recovery codes, in the caller's transaction.
func (factors *Factors) confirm(ctx context.Context, subject, code string) ([]string, error) {
	row, ok, err := factors.row(ctx, subject)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotEnrolling
	}
	if row.ConfirmedAt != 0 {
		return nil, ErrAlreadyEnabled
	}
	if err := factors.check(ctx, subject, row, code); err != nil {
		return nil, err
	}
	affected, err := factors.db.Exec(ctx, query.Table(factors.secrets, func(statement *query.Builder) {
		statement.Where("subject", subject).Where("confirmed_at", 0).Update(map[string]any{"confirmed_at": time.Now().Unix()})
	}))
	if err != nil {
		return nil, fmt.Errorf("twofactor: confirm: %w", err)
	}
	if affected == 0 {
		return nil, ErrAlreadyEnabled
	}

	return factors.replaceCodes(ctx, subject)
}

// Remove deletes the user's authenticator, pending enrollment and recovery
// codes. It is idempotent, so it also serves an administrator's "reset
// two-factor". It publishes [Disabled] when something was enabled.
//
// Example:
//
//	if err := factors.Remove(ctx, subject); err != nil {
//		return err
//	}
func (factors *Factors) Remove(ctx context.Context, subject string) error {
	enabled, err := factors.Enrolled(ctx, subject)
	if err != nil {
		return err
	}
	err = factors.db.WithTransaction(ctx, func(ctx context.Context) error {
		return factors.forget(ctx, subject)
	})
	if err != nil {
		return err
	}
	if enabled {
		factors.afterCommit(ctx, func(ctx context.Context) {
			factors.events.Emit(ctx, Disabled, UserEvent{Subject: subject})
		})
	}

	return nil
}

// afterCommit runs publish once the surrounding transaction, if any, has
// committed.
func (factors *Factors) afterCommit(ctx context.Context, publish func(context.Context)) {
	// A failing hook only loses a notification, never the committed change.
	_ = factors.db.AfterCommit(ctx, func(ctx context.Context) error {
		publish(ctx)

		return nil
	})
}
