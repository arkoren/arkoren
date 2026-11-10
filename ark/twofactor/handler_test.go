package twofactor_test

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arkoren.dev/ark/twofactor"
	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/http/session"
	"arkoren.dev/foundation/query"
	"arkoren.dev/foundation/totp"
)

// enrolled starts and confirms an enrollment for user and returns the raw
// secret and the plaintext recovery codes.
func enrolled(t *testing.T, factors *twofactor.TwoFactor, user account) ([]byte, []string) {
	t.Helper()

	secret := enroll(t, factors, user)
	code, err := totp.New().Generate(secret, time.Now())

	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()

	if err := factors.Confirm()(recorder, loggedIn(user, codeBody(code))); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	var confirmed struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &confirmed); err != nil {
		t.Fatal(err)
	}

	return secret, confirmed.RecoveryCodes
}

// enroll starts an enrollment for user and returns the raw secret.
func enroll(t *testing.T, factors *twofactor.TwoFactor, user account) []byte {
	t.Helper()

	recorder := httptest.NewRecorder()

	if err := factors.Enroll()(recorder, loggedIn(user, "")); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	var started struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(started.URI, "otpauth://totp/Example:") || !strings.Contains(started.URI, "secret="+started.Secret) {
		t.Fatalf("uri = %q, want an otpauth URI for the issuer carrying the secret", started.URI)
	}

	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("enrollment response is cacheable")
	}

	secret, err := totp.DecodeSecret(started.Secret)

	if err != nil {
		t.Fatal(err)
	}

	return secret
}

// codeAt returns the TOTP code for the time step containing at.
func codeAt(t *testing.T, secret []byte, at time.Time) string {
	t.Helper()

	code, err := totp.New().Generate(secret, at)

	if err != nil {
		t.Fatal(err)
	}

	return code
}

// TestEnrollConfirmVerifyCompletesChallengedLogin runs the whole flow: enroll,
// confirm (which issues recovery codes), challenge, verify. The session must be
// anonymous until Verify and then carry the password and OTP factors.
func TestEnrollConfirmVerifyCompletesChallengedLogin(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		env.users.add(1)

		factors := newFactors(t, env, func(ark *twofactor.Builder) {
			ark.RecoveryCodes(3)
		})
		user := account{ID: 1}

		enrolledBefore, err := factors.Enrolled(t.Context(), "1")

		if err != nil || enrolledBefore {
			t.Fatalf("Enrolled before = %v, %v; want false", enrolledBefore, err)
		}

		secret, codes := enrolled(t, factors, user)

		if len(codes) != 3 {
			t.Fatalf("recovery codes = %d, want 3", len(codes))
		}

		if enrolledAfter, err := factors.Enrolled(t.Context(), "1"); err != nil || !enrolledAfter {
			t.Fatalf("Enrolled after = %v, %v; want true", enrolledAfter, err)
		}

		// Confirm spent the current step, so log in with the next one.
		r, current := challenged(t, env, user, codeBody(codeAt(t, secret, time.Now().Add(30*time.Second))))

		if _, err := env.identity.Authenticator()(r); !errors.Is(err, auth.ErrNoCredentials) {
			t.Fatalf("pending login authenticated: %v", err)
		}

		recorder := httptest.NewRecorder()

		if err := factors.Verify(env.identity)(recorder, r); err != nil {
			t.Fatalf("Verify: %v", err)
		}

		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", recorder.Code)
		}

		if got, err := env.identity.Authenticator()(r); err != nil || got.ID != 1 {
			t.Fatalf("authenticated = %v, %v; want user 1", got, err)
		}

		state, err := env.identity.State(current)

		if err != nil || len(state.Factors) != 2 || state.Factors[0].Method != auth.MethodPassword || state.Factors[1].Method != auth.MethodOTP {
			t.Fatalf("state = %+v, %v; want password then otp", state, err)
		}
	})
}

// TestVerifyRejectsReplayedCode submits one code for two challenged logins. The
// second must fail even though the code is still inside the verification window.
// A later step's code is then accepted and an earlier step's code rejected.
func TestVerifyRejectsReplayedCode(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		env.users.add(1)

		factors := newFactors(t, env)
		user := account{ID: 1}
		secret, _ := enrolled(t, factors, user)

		// Confirm recorded the current step, so the same step is already spent.
		r, _ := challenged(t, env, user, codeBody(codeAt(t, secret, time.Now())))

		if err := factors.Verify(env.identity)(httptest.NewRecorder(), r); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("code used at confirmation = %v, want ErrInvalidCode", err)
		}

		next := time.Now().Add(30 * time.Second)
		r, _ = challenged(t, env, user, codeBody(codeAt(t, secret, next)))

		if err := factors.Verify(env.identity)(httptest.NewRecorder(), r); err != nil {
			t.Fatalf("later step: %v", err)
		}

		r, _ = challenged(t, env, user, codeBody(codeAt(t, secret, next)))

		if err := factors.Verify(env.identity)(httptest.NewRecorder(), r); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("replayed code = %v, want ErrInvalidCode", err)
		}

		r, _ = challenged(t, env, user, codeBody(codeAt(t, secret, time.Now())))

		if err := factors.Verify(env.identity)(httptest.NewRecorder(), r); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("earlier step = %v, want ErrInvalidCode", err)
		}
	})
}

// TestVerifyFailuresAreIndistinguishable compares a wrong code with a user who
// never enrolled. Both must return ErrInvalidCode and leave the login pending.
func TestVerifyFailuresAreIndistinguishable(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		env.users.add(1, 2)

		factors := newFactors(t, env)
		enrolled(t, factors, account{ID: 1})

		for _, user := range []account{{ID: 1}, {ID: 2}} {
			r, current := challenged(t, env, user, codeBody("000000"))

			if err := factors.Verify(env.identity)(httptest.NewRecorder(), r); !errors.Is(err, twofactor.ErrInvalidCode) {
				t.Fatalf("user %d: err = %v, want ErrInvalidCode", user.ID, err)
			}

			if _, err := env.identity.Pending(t.Context(), current); err != nil {
				t.Fatalf("user %d: login no longer pending: %v", user.ID, err)
			}
		}
	})
}

// TestVerifyWithoutPendingLoginIsUnauthenticated sends a code with no pending
// login, and with no session at all.
func TestVerifyWithoutPendingLoginIsUnauthenticated(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		factors := newUnmigratedFactors(t, env)
		current := session.New(time.Now().Add(time.Hour), map[string]any{})
		withSession := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(codeBody("000000")))
		withSession = withSession.WithContext(session.NewContext(withSession.Context(), current))
		withoutSession := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(codeBody("000000")))

		for name, r := range map[string]*http.Request{"empty session": withSession, "no session": withoutSession} {
			if err := factors.Verify(env.identity)(httptest.NewRecorder(), r); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Errorf("%s: err = %v, want ErrUnauthenticated", name, err)
			}
		}
	})
}

// TestConfirmWithWrongCodeLeavesEnrollmentUnconfirmed checks that a bad first
// code neither confirms the enrollment nor issues recovery codes, and that
// confirming without enrolling is ErrNotEnrolling.
func TestConfirmWithWrongCodeLeavesEnrollmentUnconfirmed(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		env.users.add(1)

		factors := newFactors(t, env)
		user := account{ID: 1}

		if err := factors.Confirm()(httptest.NewRecorder(), loggedIn(user, codeBody("000000"))); !errors.Is(err, twofactor.ErrNotEnrolling) {
			t.Fatalf("Confirm before Enroll = %v, want ErrNotEnrolling", err)
		}

		enroll(t, factors, user)

		if err := factors.Confirm()(httptest.NewRecorder(), loggedIn(user, codeBody("000000"))); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("Confirm with wrong code = %v, want ErrInvalidCode", err)
		}

		if enrolled, err := factors.Enrolled(t.Context(), "1"); err != nil || enrolled {
			t.Fatalf("Enrolled = %v, %v; want false", enrolled, err)
		}
	})
}

// TestEnrollReplacesUnconfirmedSecretButNotConfirmedOne checks re-enrollment
// before confirmation (the first secret stops working) and the refusal after it.
func TestEnrollReplacesUnconfirmedSecretButNotConfirmedOne(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		env.users.add(1)

		factors := newFactors(t, env)
		user := account{ID: 1}
		first := enroll(t, factors, user)
		second := enroll(t, factors, user)

		if err := factors.Confirm()(httptest.NewRecorder(), loggedIn(user, codeBody(codeAt(t, first, time.Now())))); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("Confirm with replaced secret = %v, want ErrInvalidCode", err)
		}

		if err := factors.Confirm()(httptest.NewRecorder(), loggedIn(user, codeBody(codeAt(t, second, time.Now())))); err != nil {
			t.Fatalf("Confirm with current secret: %v", err)
		}

		if err := factors.Enroll()(httptest.NewRecorder(), loggedIn(user, "")); !errors.Is(err, twofactor.ErrAlreadyEnrolled) {
			t.Fatalf("Enroll after confirmation = %v, want ErrAlreadyEnrolled", err)
		}
	})
}

// TestEnrollWithoutUserIsUnauthenticated checks the handlers that need the
// authenticated user.
func TestEnrollWithoutUserIsUnauthenticated(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		factors := newUnmigratedFactors(t, env)

		for name, handler := range map[string]func(*httptest.ResponseRecorder, *http.Request) error{
			"Enroll":  func(w *httptest.ResponseRecorder, r *http.Request) error { return factors.Enroll()(w, r) },
			"Confirm": func(w *httptest.ResponseRecorder, r *http.Request) error { return factors.Confirm()(w, r) },
			"Disable": func(w *httptest.ResponseRecorder, r *http.Request) error { return factors.Disable()(w, r) },
		} {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(codeBody("000000")))

			if err := handler(httptest.NewRecorder(), r); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Errorf("%s: err = %v, want ErrUnauthenticated", name, err)
			}
		}
	})
}

// TestRecoverSpendsCodeOnce completes a challenged login with a recovery code,
// written lowercase without the hyphen, and then submits the same code again.
func TestRecoverSpendsCodeOnce(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		env.users.add(1)

		factors := newFactors(t, env)
		user := account{ID: 1}
		_, codes := enrolled(t, factors, user)
		typed := strings.ToLower(strings.ReplaceAll(codes[0], "-", ""))
		r, current := challenged(t, env, user, codeBody(typed))

		recorder := httptest.NewRecorder()

		if err := factors.Recover(env.identity)(recorder, r); err != nil || recorder.Code != http.StatusNoContent {
			t.Fatalf("Recover = %v, status %d; want 204", err, recorder.Code)
		}

		state, err := env.identity.State(current)

		if err != nil || state.Factors[len(state.Factors)-1].Method != auth.MethodRecovery {
			t.Fatalf("state = %+v, %v; want a recovery factor", state, err)
		}

		r, _ = challenged(t, env, user, codeBody(codes[0]))

		if err := factors.Recover(env.identity)(httptest.NewRecorder(), r); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("spent code = %v, want ErrInvalidCode", err)
		}

		r, _ = challenged(t, env, user, codeBody(codes[1]))

		if err := factors.Recover(env.identity)(httptest.NewRecorder(), r); err != nil {
			t.Fatalf("another code: %v", err)
		}
	})
}

// TestRecoverRejectsUnknownCode covers a wrong code and a user with no codes.
func TestRecoverRejectsUnknownCode(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		env.users.add(1, 2)

		factors := newFactors(t, env)
		enrolled(t, factors, account{ID: 1})

		for _, user := range []account{{ID: 1}, {ID: 2}} {
			r, _ := challenged(t, env, user, codeBody("AAAAA-AAAAA"))

			if err := factors.Recover(env.identity)(httptest.NewRecorder(), r); !errors.Is(err, twofactor.ErrInvalidCode) {
				t.Errorf("user %d: err = %v, want ErrInvalidCode", user.ID, err)
			}
		}
	})
}

// TestDisableRemovesEnrollmentAndRecoveryCodes disables twice, which must be
// idempotent, and checks that a challenged login can no longer verify.
func TestDisableRemovesEnrollmentAndRecoveryCodes(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		env.users.add(1)

		factors := newFactors(t, env)
		user := account{ID: 1}
		secret, codes := enrolled(t, factors, user)

		for range 2 {
			recorder := httptest.NewRecorder()

			if err := factors.Disable()(recorder, loggedIn(user, "")); err != nil || recorder.Code != http.StatusNoContent {
				t.Fatalf("Disable = %v, status %d; want 204", err, recorder.Code)
			}
		}

		if enrolled, err := factors.Enrolled(t.Context(), "1"); err != nil || enrolled {
			t.Fatalf("Enrolled = %v, %v; want false", enrolled, err)
		}

		r, _ := challenged(t, env, user, codeBody(codeAt(t, secret, time.Now().Add(30*time.Second))))

		if err := factors.Verify(env.identity)(httptest.NewRecorder(), r); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("Verify after Disable = %v, want ErrInvalidCode", err)
		}

		r, _ = challenged(t, env, user, codeBody(codes[0]))

		if err := factors.Recover(env.identity)(httptest.NewRecorder(), r); !errors.Is(err, twofactor.ErrInvalidCode) {
			t.Fatalf("Recover after Disable = %v, want ErrInvalidCode", err)
		}
	})
}

// TestStoredSecretIsEncrypted reads the secrets table directly: the stored bytes
// must not contain the raw secret.
func TestStoredSecretIsEncrypted(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		env.users.add(1)

		instance := uniqueInstance()
		factors := newFactors(t, env, withInstance(instance))
		secret := enroll(t, factors, account{ID: 1})
		rows, err := env.db.Select[[]byte](t.Context(), query.Raw("SELECT secret FROM "+instance+"_twofactor_secrets"))

		if err != nil || rows.Len() != 1 {
			t.Fatalf("rows = %d, %v; want 1", rows.Len(), err)
		}

		stored, _ := rows.First()

		if strings.Contains(string(stored), string(secret)) {
			t.Fatal("secret stored in plaintext")
		}
	})
}
