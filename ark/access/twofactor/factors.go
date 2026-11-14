package twofactor

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
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

	"arkoren.dev/ark/access/internal/storage"
	"arkoren.dev/ark/access/internal/throttle"
)

const (
	// MethodRecovery names the recovery-code method in challenge and confirm
	// requests. RFC 8176 has no value for it, so it belongs to this package.
	MethodRecovery auth.Method = "recovery"

	// DefaultRecoveryCodes is how many recovery codes enabling two-factor creates.
	DefaultRecoveryCodes = 10

	// maxRecoveryCodes bounds [Builder.RecoveryCodes].
	maxRecoveryCodes = 100

	// activateFailures is how many wrong codes [Factors.Activate] accepts per
	// user within activateWindow.
	activateFailures = 10

	// activateWindow is the lifetime of the activation attempt counter.
	activateWindow = 15 * time.Minute
)

// recoveryEncoding writes recovery codes: base32 without padding, so 10 random
// bytes make 16 characters.
var recoveryEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Builder configures [NewFactors] before publication. Callbacks run directly
// once, in order, on one fresh draft; methods return the same draft.
//
// Example:
//
//	factors, err := twofactor.NewFactors(provider, db, store, cipher, "Example", func(factors *twofactor.Builder) {
//		factors.RecoveryCodes(8)
//	})
type Builder struct {
	instance string
	events   *event.Bus
	scheme   totp.Scheme
	codes    int
}

// BuilderFunc is the callback form of [Builder].
//
// Example:
//
//	var configure twofactor.BuilderFunc = func(factors *twofactor.Builder) {
//		factors.Instance("tenant_a")
//	}
//	factors, err := twofactor.NewFactors(provider, db, store, cipher, "Example", configure)
type BuilderFunc = func(*Builder)

// Instance selects a named instance: tables are prefixed "<instance>_" and the
// migration group gains ":<instance>". The name is 1 to 20 bytes of [a-z0-9_]
// starting with a letter; the default is the empty instance.
//
// Example:
//
//	factors.Instance("tenant_a")
func (factors *Builder) Instance(name string) *Builder {
	factors.instance = name

	return factors
}

// Events publishes [Enabled], [Disabled], [Regenerated] and [RecoveryUsed], and
// the core [auth.CredentialEnrolled] event, on bus. A nil bus publishes nothing.
//
// Example:
//
//	factors.Events(events)
func (factors *Builder) Events(bus *event.Bus) *Builder {
	factors.events = bus

	return factors
}

// Scheme replaces the TOTP parameters (hash, digits, period, window). The
// default is [totp.New]: SHA-1, 6 digits, 30 seconds, one step of drift, which
// every authenticator app supports.
//
// Example:
//
//	factors.Scheme(totp.New(func(scheme *totp.Builder) {
//		scheme.Digits(8)
//	}))
func (factors *Builder) Scheme(scheme totp.Scheme) *Builder {
	factors.scheme = scheme

	return factors
}

// RecoveryCodes sets how many recovery codes enabling two-factor creates, 1 to
// 100; zero selects [DefaultRecoveryCodes].
//
// Example:
//
//	factors.RecoveryCodes(8)
func (factors *Builder) RecoveryCodes(count int) *Builder {
	factors.codes = count

	return factors
}

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
//	factors, err := twofactor.NewFactors(provider, db, store, cipher, "Example")
type Factors struct {
	db       *database.Database
	store    *cache.Cache
	cipher   *crypto.Encrypter
	issuer   string
	instance string
	secrets  string
	codes    string
	events   *event.Bus
	scheme   totp.Scheme
	count    int
}

// NewFactors returns the two-factor store. cipher encrypts the secrets (use a
// key reserved for them), store counts wrong activation codes, and issuer is the
// service name authenticator apps show: it is required and may not contain a
// colon. It does no I/O; the tables come from [Factors.Migrations]. Errors wrap
// [ErrInvalidConfig].
//
// It registers a forget hook on provider that deletes the user's secret and
// codes in the caller's transaction.
//
// Example:
//
//	factors, err := twofactor.NewFactors(provider, db, store, cipher, "Example")
func NewFactors(provider *auth.UserProvider, db *database.Database, store *cache.Cache, cipher *crypto.Encrypter, issuer string, configure ...BuilderFunc) (*Factors, error) {
	if provider == nil || db == nil || store == nil || cipher == nil {
		return nil, fmt.Errorf("%w: provider, database, store and cipher are required", ErrInvalidConfig)
	}
	if issuer == "" || strings.Contains(issuer, ":") {
		return nil, fmt.Errorf("%w: issuer must be non-empty and contain no colon", ErrInvalidConfig)
	}
	settings := Builder{scheme: totp.New()}
	for _, apply := range configure {
		apply(&settings)
	}
	if err := storage.ValidInstance(settings.instance); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if err := settings.scheme.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if settings.codes < 0 || settings.codes > maxRecoveryCodes {
		return nil, fmt.Errorf("%w: recovery codes must be 1 to %d", ErrInvalidConfig, maxRecoveryCodes)
	}
	if settings.codes == 0 {
		settings.codes = DefaultRecoveryCodes
	}
	factors := &Factors{
		db:       db,
		store:    store,
		cipher:   cipher,
		issuer:   issuer,
		instance: settings.instance,
		secrets:  storage.Table(settings.instance, "totp_secrets"),
		codes:    storage.Table(settings.instance, "recovery_codes"),
		events:   settings.events,
		scheme:   settings.scheme,
		count:    settings.codes,
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

// unused counts the recovery codes of subject that are not spent.
func (factors *Factors) unused(ctx context.Context, subject string) (int, error) {
	counts, err := factors.db.Select[int](ctx, query.Table(factors.codes, func(statement *query.Builder) {
		statement.SelectRaw("COUNT(*)").Where("subject", subject).Where("used_at", 0)
	}))
	if err != nil {
		return 0, fmt.Errorf("twofactor: count recovery codes: %w", err)
	}

	return counts[0], nil
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

// Enroll starts enrolment: it stores a new pending secret for subject, replacing
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
	uri, err := factors.scheme.URI(factors.issuer, strings.ReplaceAll(label, ":", " "), secret)
	if err != nil {
		return Enrollment{}, fmt.Errorf("twofactor: provisioning uri: %w", err)
	}
	sealed, err := factors.seal(secret)
	if err != nil {
		return Enrollment{}, err
	}
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
			// Either a concurrent enrolment inserted the pending row first, or the
			// user has a confirmed authenticator. Replacing the pending row again
			// tells them apart.
			affected, err = replace()
		}
	}
	if err != nil {
		return Enrollment{}, fmt.Errorf("twofactor: store secret: %w", err)
	}
	if affected == 0 {
		return Enrollment{}, ErrAlreadyEnabled
	}

	return Enrollment{Secret: totp.EncodeSecret(secret), URI: uri}, nil
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

// Activate confirms a pending enrolment with its first code, enables the
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
	wait, err := throttle.Reserve(ctx, factors.store, key, activateFailures, activateWindow)
	if err != nil {
		return nil, err
	}
	if wait > 0 {
		return nil, throttle.Reject(ErrThrottled, wait)
	}
	var codes []string
	err = factors.db.WithTransaction(ctx, func(ctx context.Context) error {
		row, ok, err := factors.row(ctx, subject)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotEnrolling
		}
		if row.ConfirmedAt != 0 {
			return ErrAlreadyEnabled
		}
		if err := factors.check(ctx, subject, row, code); err != nil {
			return err
		}
		affected, err := factors.db.Exec(ctx, query.Table(factors.secrets, func(statement *query.Builder) {
			statement.Where("subject", subject).Where("confirmed_at", 0).Update(map[string]any{"confirmed_at": time.Now().Unix()})
		}))
		if err != nil {
			return fmt.Errorf("twofactor: confirm: %w", err)
		}
		if affected == 0 {
			return ErrAlreadyEnabled
		}
		codes, err = factors.replaceCodes(ctx, subject)

		return err
	})
	if err != nil {
		return nil, err
	}
	// Counters expire on their own; a failed cleanup only keeps a stale count.
	_ = factors.store.Delete(ctx, key)
	_ = factors.db.AfterCommit(ctx, func(ctx context.Context) error {
		factors.events.Emit(ctx, Enabled, UserEvent{Subject: subject})
		factors.events.Emit(ctx, auth.CredentialEnrolled, auth.CredentialEnrolledEvent{Subject: subject, Method: auth.MethodOTP})

		return nil
	})

	return codes, nil
}

// replaceCodes deletes the recovery codes of subject and inserts a new set, in
// the caller's transaction, and returns the codes formatted for display.
func (factors *Factors) replaceCodes(ctx context.Context, subject string) ([]string, error) {
	_, err := factors.db.Exec(ctx, query.Table(factors.codes, func(statement *query.Builder) {
		statement.Where("subject", subject).Delete()
	}))
	if err != nil {
		return nil, fmt.Errorf("twofactor: clear recovery codes: %w", err)
	}
	display := make([]string, factors.count)
	rows := make([]map[string]any, factors.count)
	for slot := range factors.count {
		var raw [10]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, fmt.Errorf("twofactor: generate recovery code: %w", err)
		}
		plain := recoveryEncoding.EncodeToString(raw[:])
		display[slot] = plain[0:4] + "-" + plain[4:8] + "-" + plain[8:12] + "-" + plain[12:16]
		rows[slot] = map[string]any{"subject": subject, "slot": slot, "code_hash": storage.Hash(plain), "used_at": 0}
	}
	if _, err := factors.db.Exec(ctx, query.Table(factors.codes, func(statement *query.Builder) {
		statement.InsertRows(rows...)
	})); err != nil {
		return nil, fmt.Errorf("twofactor: store recovery codes: %w", err)
	}

	return display, nil
}

// Recover spends a recovery code and returns the factor to record (method
// [MethodRecovery], kind possession). The case, dashes and spaces of the code
// do not matter. Every failure is [ErrInvalidCode]. Spending is one conditional
// update, so a code works once even under concurrency. It publishes
// [RecoveryUsed].
//
// Example:
//
//	factor, err := factors.Recover(ctx, subject, "ABCD-EFGH-IJKL-MNOP")
//	if errors.Is(err, twofactor.ErrInvalidCode) {
//		// wrong or spent code
//	}
//	_ = factor
func (factors *Factors) Recover(ctx context.Context, subject, code string) (auth.Factor, error) {
	normalized := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	affected, err := factors.db.Exec(ctx, query.Table(factors.codes, func(statement *query.Builder) {
		statement.Where("subject", subject).Where("code_hash", storage.Hash(normalized)).Where("used_at", 0).
			Update(map[string]any{"used_at": time.Now().Unix()})
	}))
	if err != nil {
		return auth.Factor{}, fmt.Errorf("twofactor: spend recovery code: %w", err)
	}
	if affected == 0 {
		return auth.Factor{}, ErrInvalidCode
	}
	factors.events.Emit(ctx, RecoveryUsed, UserEvent{Subject: subject})

	return auth.Factor{Method: MethodRecovery, Kind: auth.KindPossession}, nil
}

// Regenerate replaces the user's recovery codes and returns the new ones,
// formatted; the old ones stop working at once. A user without an enabled
// authenticator gets [ErrNotEnabled]. It publishes [Regenerated].
//
// Example:
//
//	codes, err := factors.Regenerate(ctx, subject)
func (factors *Factors) Regenerate(ctx context.Context, subject string) ([]string, error) {
	var codes []string
	err := factors.db.WithTransaction(ctx, func(ctx context.Context) error {
		enabled, err := factors.Enrolled(ctx, subject)
		if err != nil {
			return err
		}
		if !enabled {
			return ErrNotEnabled
		}
		codes, err = factors.replaceCodes(ctx, subject)

		return err
	})
	if err != nil {
		return nil, err
	}
	_ = factors.db.AfterCommit(ctx, func(ctx context.Context) error {
		factors.events.Emit(ctx, Regenerated, UserEvent{Subject: subject})

		return nil
	})

	return codes, nil
}

// Remove deletes the user's authenticator, pending enrolment and recovery
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
		_ = factors.db.AfterCommit(ctx, func(ctx context.Context) error {
			factors.events.Emit(ctx, Disabled, UserEvent{Subject: subject})

			return nil
		})
	}

	return nil
}

// codeBody is the input of the code credentials.
type codeBody struct {
	Code string `json:"code"`
}

// credential builds the credential of one method around verify.
func credential(method auth.Method, enrolled func(context.Context, string) (bool, error), verify func(context.Context, string, string) (auth.Factor, error)) auth.Credential {
	return auth.Credential{
		Method:   method,
		Kind:     auth.KindPossession,
		Enrolled: enrolled,
		Verify: func(r *http.Request, subject string, input jsontext.Value) (auth.Factor, error) {
			var body codeBody
			if err := json.Unmarshal(input, &body); err != nil {
				return auth.Factor{}, fmt.Errorf("twofactor: %w", auth.ErrInvalidCredential)
			}
			factor, err := verify(r.Context(), subject, body.Code)
			if errors.Is(err, ErrInvalidCode) {
				return auth.Factor{}, fmt.Errorf("twofactor: %w", auth.ErrInvalidCredential)
			}

			return factor, err
		},
	}
}

// Credential returns the [auth.Credential] of the authenticator: method "otp",
// kind possession, request body {"method": "otp", "code": "123456"}. Register it
// with the guard's SecondFactor registry to finish a parked login
// (POST /login/challenge) and with its StepUp registry to re-prove a logged-in
// user (POST /confirm). A wrong code wraps [auth.ErrInvalidCredential], which
// core counts as an attempt.
//
// Example:
//
//	policy.SecondFactor(factors.Credential(), factors.RecoveryCredential())
//	policy.StepUp(passwords.Credential(), factors.Credential())
func (factors *Factors) Credential() auth.Credential {
	return credential(auth.MethodOTP, factors.Enrolled, factors.Verify)
}

// RecoveryCredential returns the [auth.Credential] of the recovery codes:
// method [MethodRecovery], kind possession, request body
// {"method": "recovery", "code": "abcd-efgh-ijkl-mnop"}. It is enrolled while the
// user has an authenticator and an unspent code. Register it with the SecondFactor
// registry after [Factors.Credential]; it is not a step-up method, so a spent
// recovery code never counts as the user's fresh proof.
//
// Example:
//
//	policy.SecondFactor(factors.Credential(), factors.RecoveryCredential())
func (factors *Factors) RecoveryCredential() auth.Credential {
	return credential(MethodRecovery, func(ctx context.Context, subject string) (bool, error) {
		enabled, err := factors.Enrolled(ctx, subject)
		if err != nil || !enabled {
			return false, err
		}
		remaining, err := factors.unused(ctx, subject)

		return remaining > 0, err
	}, factors.Recover)
}
