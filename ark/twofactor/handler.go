package twofactor

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/totp"
)

// recoveryCodeLength is the number of random base32 characters in a recovery
// code (50 bits), shown as two groups of five separated by a hyphen.
const recoveryCodeLength = 10

// codeRequest is the JSON body of the handlers that check a code.
type codeRequest struct {
	// Code is the TOTP code, or the recovery code for [TwoFactor.Recover].
	Code string `json:"code"`
}

// enrollment is the JSON response of [TwoFactor.Enroll].
type enrollment struct {
	// Secret is the base32 secret for manual entry.
	Secret string `json:"secret"`

	// URI is the otpauth:// provisioning URI that authenticator apps read from a
	// QR code. It contains the secret.
	URI string `json:"uri"`
}

// confirmation is the JSON response of [TwoFactor.Confirm].
type confirmation struct {
	// RecoveryCodes are the plaintext single-use codes, shown only this once.
	RecoveryCodes []string `json:"recovery_codes"`
}

// secretRow is a stored secret.
type secretRow struct {
	// Secret is the encrypted TOTP key.
	Secret []byte `db:"secret"`

	// LastStep is the last accepted time step.
	LastStep int64 `db:"last_step"`
}

// recoveryRow is an unused recovery code.
type recoveryRow struct {
	// Slot is the code's position among the user's codes.
	Slot int `db:"slot"`

	// CodeHash is the password hash of the normalized code.
	CodeHash []byte `db:"code_hash"`
}

// Enroll starts enrollment for the authenticated user: it generates a secret,
// stores it encrypted and unconfirmed, and responds 200 with the base32 secret
// and the otpauth:// URI to render as a QR code. The user is not protected
// until [TwoFactor.Confirm] verifies a first code. Calling Enroll again before
// confirming replaces the unconfirmed secret; once confirmed it returns
// [ErrAlreadyEnrolled] until [TwoFactor.Disable] runs. Without an authenticated
// user it returns [auth.ErrUnauthenticated].
//
// The account label in the URI is the user's subject with colons replaced by
// hyphens. The response contains the secret: serve it over HTTPS with no-store
// caching, and protect the route with [auth.SessionIdentity.RequireFresh] so a
// stolen session cannot enroll its own authenticator.
//
// Example:
//
//	r.With(identity.RequireFresh(10*time.Minute)).Post("/two-factor/enroll", factors.Enroll())
func (twofactor *TwoFactor) Enroll() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()
		subject, err := twofactor.directory.Subject(ctx)

		if err != nil {
			return err
		}

		secret, err := twofactor.config.scheme.NewSecret()

		if err != nil {
			return err
		}

		sealed, err := twofactor.cipher.EncryptRaw(secret)

		if err != nil {
			return fmt.Errorf("two-factor enroll: %w", err)
		}

		uri, err := twofactor.config.scheme.URI(twofactor.config.issuer, strings.ReplaceAll(subject, ":", "-"), secret)

		if err != nil {
			return err
		}

		err = twofactor.db.WithTransaction(ctx, func(ctx context.Context) error {
			confirmed, err := twofactor.Enrolled(ctx, subject)

			if err != nil {
				return err
			}

			if confirmed {
				return ErrAlreadyEnrolled
			}

			if _, err := twofactor.db.Exec(ctx, query.Table(twofactor.secrets, func(statement *query.Builder) {
				statement.Delete().Where("user_id", subject)
			})); err != nil {
				return err
			}

			_, err = twofactor.db.Exec(ctx, query.Table(twofactor.secrets, func(statement *query.Builder) {
				statement.Insert(map[string]any{"user_id": subject, "secret": sealed, "last_step": 0})
			}))

			return err
		})

		if err != nil {
			return fmt.Errorf("two-factor enroll: %w", err)
		}

		w.Header().Set("Cache-Control", "no-store")

		return response.JSON(w, http.StatusOK, enrollment{Secret: totp.EncodeSecret(secret), URI: uri})
	}
}

// Confirm finishes enrollment: it reads {"code": "..."} from the JSON body,
// verifies it against the unconfirmed secret of the authenticated user, marks
// the enrollment confirmed, replaces any earlier recovery codes with a fresh
// set and responds 200 with {"recovery_codes": [...]}. The plaintext codes are
// shown only in this response; only their hashes are kept. A wrong code returns
// [ErrInvalidCode]; no enrollment waiting returns [ErrNotEnrolling]. The code's
// time step is recorded so it cannot be replayed to log in.
//
// Confirm does not touch the session. To make the current session satisfy
// [auth.SessionIdentity.RequireMethods] for [auth.MethodOTP], call
// [auth.SessionIdentity.Confirm] afterwards in your own handler or wrap this one.
//
// Example:
//
//	web.Post("/two-factor/confirm", factors.Confirm())
func (twofactor *TwoFactor) Confirm() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()
		subject, err := twofactor.directory.Subject(ctx)

		if err != nil {
			return err
		}

		body, err := request.JSON[codeRequest](r)

		if err != nil {
			return err
		}

		row, found, err := twofactor.secret(ctx, subject, false)

		if err != nil {
			return err
		}

		if !found {
			return ErrNotEnrolling
		}

		step, err := twofactor.check(row, body.Code)

		if err != nil {
			return err
		}

		codes, hashes, err := twofactor.newRecoveryCodes()

		if err != nil {
			return err
		}

		err = twofactor.db.WithTransaction(ctx, func(ctx context.Context) error {
			updated, err := twofactor.db.Exec(ctx, query.Table(twofactor.secrets, func(statement *query.Builder) {
				statement.Update(map[string]any{"confirmed_at": time.Now().UTC(), "last_step": step}).
					Where("user_id", subject).WhereNull("confirmed_at")
			}))

			if err != nil {
				return err
			}

			if updated != 1 {
				return ErrNotEnrolling
			}

			if _, err := twofactor.db.Exec(ctx, query.Table(twofactor.recovery, func(statement *query.Builder) {
				statement.Delete().Where("user_id", subject)
			})); err != nil {
				return err
			}

			for slot, hashed := range hashes {
				if _, err := twofactor.db.Exec(ctx, query.Table(twofactor.recovery, func(statement *query.Builder) {
					statement.Insert(map[string]any{"user_id": subject, "slot": slot, "code_hash": hashed})
				})); err != nil {
					return err
				}
			}

			return nil
		})

		if err != nil {
			return fmt.Errorf("two-factor confirm: %w", err)
		}

		w.Header().Set("Cache-Control", "no-store")

		return response.JSON(w, http.StatusOK, confirmation{RecoveryCodes: codes})
	}
}

// Disable removes the authenticated user's enrollment, secret and recovery
// codes, and responds 204. It is idempotent. It checks no code: protect the
// route with [auth.SessionIdentity.RequireFresh] or
// [auth.SessionIdentity.RequireMethods] so only a user who just proved
// themselves can turn the second factor off. Without an authenticated user it
// returns [auth.ErrUnauthenticated].
//
// Disabling does not end other sessions; make the user type implement
// [auth.Versioned] if it should.
//
// Example:
//
//	r.With(identity.RequireMethods(auth.MethodOTP)).Delete("/two-factor", factors.Disable())
func (twofactor *TwoFactor) Disable() router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		subject, err := twofactor.directory.Subject(r.Context())

		if err != nil {
			return err
		}

		if err := twofactor.Forget(r.Context(), subject); err != nil {
			return err
		}

		return response.Status(w, http.StatusNoContent)
	}
}

// Verify returns the handler for the second step of a challenged login. It
// reads {"code": "..."} from the JSON body, loads the user waiting in the
// session through [auth.SessionIdentity.Pending], verifies the code against the
// user's confirmed secret and, on success, completes the login with
// [auth.MethodOTP] and responds 204. The session ID is rotated by
// [auth.SessionIdentity.Complete].
//
// A code is accepted once: its time step must be later than the last accepted
// one (RFC 6238 section 5.2), checked and recorded in one atomic UPDATE so two
// concurrent submissions of the same code cannot both succeed. A wrong code, a
// replayed code and a user without a confirmed enrollment all return the same
// [ErrInvalidCode]. Without a pending login it returns [auth.ErrUnauthenticated];
// an invalid identity returns its configuration error.
//
// The ark does not count failures. Rate-limit this route by session ID, since a
// pending login stays open until its challenge expires.
//
// Example:
//
//	web.Post("/two-factor/verify", factors.Verify(identity))
func (twofactor *TwoFactor) Verify[K any, U auth.Identifiable[K]](identity auth.SessionIdentity[K, U]) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()
		current, subject, err := twofactor.pending(r, identity)

		if err != nil {
			return err
		}

		body, err := request.JSON[codeRequest](r)

		if err != nil {
			return err
		}

		row, found, err := twofactor.secret(ctx, subject, true)

		if err != nil {
			return err
		}

		if !found {
			return ErrInvalidCode
		}

		step, err := twofactor.check(row, body.Code)

		if err != nil {
			return err
		}

		// The conditional update is the replay check: it matches only while the
		// stored step is older, so of two concurrent submissions one updates.
		advanced, err := twofactor.db.Exec(ctx, query.Table(twofactor.secrets, func(statement *query.Builder) {
			statement.Update(map[string]any{"last_step": step}).
				Where("user_id", subject).WhereNotNull("confirmed_at").Where("last_step", "<", step)
		}))

		if err != nil {
			return fmt.Errorf("two-factor verify: %w", err)
		}

		if advanced != 1 {
			return ErrInvalidCode
		}

		if err := identity.Complete(ctx, current, auth.MethodOTP); err != nil {
			return err
		}

		return response.Status(w, http.StatusNoContent)
	}
}

// Recover returns the handler that completes a challenged login with a
// recovery code instead of a TOTP code. It reads {"code": "..."} from the JSON
// body (case and hyphens are ignored), checks it against the user's unused
// recovery code hashes, spends the matching code and, on success, completes the
// login with [auth.MethodRecovery] and responds 204. Each code works once: the
// code is marked used by an atomic UPDATE, so concurrent submissions of one code
// cannot both succeed. An unknown, spent or malformed code returns
// [ErrInvalidCode], as does a user without recovery codes.
//
// Checking costs one password hash per unused code, so rate-limit this route by
// session ID. Recovery bypasses the authenticator; consider telling the user and
// requiring them to re-enroll.
//
// Example:
//
//	web.Post("/two-factor/recover", factors.Recover(identity))
func (twofactor *TwoFactor) Recover[K any, U auth.Identifiable[K]](identity auth.SessionIdentity[K, U]) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()
		current, subject, err := twofactor.pending(r, identity)

		if err != nil {
			return err
		}

		body, err := request.JSON[codeRequest](r)

		if err != nil {
			return err
		}

		attempt := []byte(strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(body.Code)))

		rows, err := twofactor.db.Select[recoveryRow](ctx, query.Table(twofactor.recovery, func(statement *query.Builder) {
			statement.Select("slot", "code_hash").Where("user_id", subject).WhereNull("used_at").OrderBy("slot")
		}))

		if err != nil {
			return fmt.Errorf("two-factor recover: %w", err)
		}

		for _, row := range rows.Items() {
			match, err := twofactor.hasher.CheckRaw(attempt, row.CodeHash)

			if err != nil {
				return fmt.Errorf("two-factor recover: %w", err)
			}

			if !match {
				continue
			}

			spent, err := twofactor.db.Exec(ctx, query.Table(twofactor.recovery, func(statement *query.Builder) {
				statement.Update(map[string]any{"used_at": time.Now().UTC()}).
					Where("user_id", subject).Where("slot", row.Slot).WhereNull("used_at")
			}))

			if err != nil {
				return fmt.Errorf("two-factor recover: %w", err)
			}

			if spent != 1 {
				return ErrInvalidCode
			}

			if err := identity.Complete(ctx, current, auth.MethodRecovery); err != nil {
				return err
			}

			return response.Status(w, http.StatusNoContent)
		}

		return ErrInvalidCode
	}
}

// pending resolves the session and the subject of the user waiting for a
// further factor. A request without a session is unauthenticated, like the
// OAuth handlers; an absent or expired pending login is
// [auth.ErrUnauthenticated] from [auth.SessionIdentity.Pending].
func (twofactor *TwoFactor) pending[K any, U auth.Identifiable[K]](r *http.Request, identity auth.SessionIdentity[K, U]) (*session.Session, string, error) {
	current, err := identity.Session(r.Context())

	if errors.Is(err, session.ErrNoSession) {
		return nil, "", auth.ErrUnauthenticated
	}

	if err != nil {
		return nil, "", err
	}

	user, err := identity.Pending(r.Context(), current)

	if err != nil {
		return nil, "", err
	}

	subject, err := identity.Subject(user)

	return current, subject, err
}

// secret loads the stored secret of subject: the confirmed one, or the
// unconfirmed one of a started enrollment. found is false when there is none.
func (twofactor *TwoFactor) secret(ctx context.Context, subject string, confirmed bool) (row secretRow, found bool, err error) {
	rows, err := twofactor.db.Select[secretRow](ctx, query.Table(twofactor.secrets, func(statement *query.Builder) {
		statement.Select("secret", "last_step").Where("user_id", subject)

		if confirmed {
			statement.WhereNotNull("confirmed_at")
		} else {
			statement.WhereNull("confirmed_at")
		}
	}))

	if err != nil {
		return row, false, fmt.Errorf("two-factor: load secret: %w", err)
	}

	row, found = rows.First()

	return row, found, nil
}

// check decrypts the stored secret and verifies code against it for the current
// time, returning the matched time step. A code that does not match is
// [ErrInvalidCode]; the caller decides whether the step is fresh.
func (twofactor *TwoFactor) check(row secretRow, code string) (int64, error) {
	secret, err := twofactor.cipher.DecryptRaw(row.Secret)

	if err != nil {
		return 0, fmt.Errorf("two-factor: decrypt secret: %w", err)
	}

	step, err := twofactor.config.scheme.Verify(secret, strings.TrimSpace(code), time.Now())

	if errors.Is(err, totp.ErrInvalidCode) {
		return 0, ErrInvalidCode
	}

	return step, err
}

// newRecoveryCodes generates the configured number of recovery codes and
// returns them in display form ("ABCDE-FGHIJ") together with the password
// hashes of their normalized form, in the same order. Hashing happens here,
// outside any transaction, because it is deliberately slow.
func (twofactor *TwoFactor) newRecoveryCodes() (codes []string, hashes [][]byte, err error) {
	for range twofactor.config.recoveryCodes {
		code := rand.Text()[:recoveryCodeLength]
		hashed, err := twofactor.hasher.HashRaw([]byte(code))

		if err != nil {
			return nil, nil, fmt.Errorf("two-factor: hash recovery code: %w", err)
		}

		codes = append(codes, code[:recoveryCodeLength/2]+"-"+code[recoveryCodeLength/2:])
		hashes = append(hashes, hashed)
	}

	return codes, hashes, nil
}
