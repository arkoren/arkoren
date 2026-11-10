package twofactor_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/ark/twofactor"
	"arkoren.dev/core/http/auth"
	"arkoren.dev/foundation/query"
)

// structUser has an identifier type that cannot become a subject.
type structUser struct {
	// ID is a struct, which Directory rejects.
	ID struct{ Part int }
}

// AuthID reports the unsupported identifier.
func (user structUser) AuthID() struct{ Part int } {
	return user.ID
}

// TestNewRejectsInvalidArguments passes each invalid argument or setting in
// turn. All must fail with ErrInvalidConfig before any I/O.
func TestNewRejectsInvalidArguments(t *testing.T) {
	t.Parallel()

	env := newEnvironment(t, "sqlite")
	structIdentity := auth.NewSessionIdentity(func(context.Context, struct{ Part int }) (structUser, error) {
		return structUser{}, nil
	})
	issuer := func(ark *twofactor.Builder) { ark.Issuer("Example") }
	cases := map[string]func() (*twofactor.TwoFactor, error){
		"nil database": func() (*twofactor.TwoFactor, error) {
			return twofactor.New(env.identity, nil, env.cipher, env.hasher, issuer)
		},
		"nil cipher": func() (*twofactor.TwoFactor, error) {
			return twofactor.New(env.identity, env.db, nil, env.hasher, issuer)
		},
		"nil hasher": func() (*twofactor.TwoFactor, error) {
			return twofactor.New(env.identity, env.db, env.cipher, nil, issuer)
		},
		"missing issuer": func() (*twofactor.TwoFactor, error) {
			return twofactor.New(env.identity, env.db, env.cipher, env.hasher)
		},
		"issuer with colon": func() (*twofactor.TwoFactor, error) {
			return twofactor.New(env.identity, env.db, env.cipher, env.hasher, func(ark *twofactor.Builder) { ark.Issuer("A:B") })
		},
		"unsupported identifier type": func() (*twofactor.TwoFactor, error) {
			return twofactor.New(structIdentity, env.db, env.cipher, env.hasher, issuer)
		},
		"uppercase instance": func() (*twofactor.TwoFactor, error) {
			return twofactor.New(env.identity, env.db, env.cipher, env.hasher, issuer, withInstance("Admin"))
		},
		"digit-leading instance": func() (*twofactor.TwoFactor, error) {
			return twofactor.New(env.identity, env.db, env.cipher, env.hasher, issuer, withInstance("0admin"))
		},
		"long instance": func() (*twofactor.TwoFactor, error) {
			return twofactor.New(env.identity, env.db, env.cipher, env.hasher, issuer, withInstance(strings.Repeat("a", 31)))
		},
		"negative recovery codes": func() (*twofactor.TwoFactor, error) {
			return twofactor.New(env.identity, env.db, env.cipher, env.hasher, issuer, func(ark *twofactor.Builder) { ark.RecoveryCodes(-1) })
		},
		"too many recovery codes": func() (*twofactor.TwoFactor, error) {
			return twofactor.New(env.identity, env.db, env.cipher, env.hasher, issuer, func(ark *twofactor.Builder) { ark.RecoveryCodes(101) })
		},
	}

	for name, construct := range cases {
		if _, err := construct(); !errors.Is(err, twofactor.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
}

// TestEnrolledIgnoresUnconfirmedEnrollments checks that Enrolled is false for an
// unknown subject and for a started but unconfirmed enrollment.
func TestEnrolledIgnoresUnconfirmedEnrollments(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		factors := newFactors(t, env)

		enroll(t, factors, account{ID: 5})

		for _, subject := range []string{"5", "6"} {
			if enrolled, err := factors.Enrolled(t.Context(), subject); err != nil || enrolled {
				t.Errorf("Enrolled(%q) = %v, %v; want false", subject, enrolled, err)
			}
		}
	})
}

// TestSubjectsAreComparedBytewise stores "abc" and "ABC" side by side and
// forgets one. MySQL's default collation would treat them as the same key.
func TestSubjectsAreComparedBytewise(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		instance := uniqueInstance()
		factors := newFactors(t, env, withInstance(instance))

		for _, subject := range []string{"abc", "ABC"} {
			insertSecret(t, env, instance, subject)
		}

		if err := factors.Forget(t.Context(), "ABC"); err != nil {
			t.Fatal(err)
		}

		remaining, err := env.db.Select[string](t.Context(), query.Table(instance+"_twofactor_secrets", func(statement *query.Builder) {
			statement.Select("user_id")
		}))

		if err != nil || remaining.Len() != 1 || remaining.Items()[0] != "abc" {
			t.Fatalf("remaining = %v, %v; want only abc", remaining.Items(), err)
		}
	})
}

// insertSecret stores a placeholder secret row for subject in instance's table.
func insertSecret(t *testing.T, env environment, instance, subject string) {
	t.Helper()

	_, err := env.db.Exec(t.Context(), query.Table(instance+"_twofactor_secrets", func(statement *query.Builder) {
		statement.Insert(map[string]any{"user_id": subject, "secret": []byte{1}, "last_step": 0})
	}))

	if err != nil {
		t.Fatalf("insert %q: %v", subject, err)
	}
}

// TestForgetJoinsTransactionAndIsIdempotent forgets inside a transaction that
// rolls back, which must keep the rows, then forgets for real, twice.
func TestForgetJoinsTransactionAndIsIdempotent(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		env.users.add(1)

		factors := newFactors(t, env)
		enrolled(t, factors, account{ID: 1})

		rollback := errors.New("rollback")
		err := env.db.WithTransaction(t.Context(), func(ctx context.Context) error {
			if err := factors.Forget(ctx, "1"); err != nil {
				return err
			}

			return rollback
		})

		if !errors.Is(err, rollback) {
			t.Fatalf("transaction = %v, want the rollback error", err)
		}

		if enrolled, err := factors.Enrolled(t.Context(), "1"); err != nil || !enrolled {
			t.Fatalf("Enrolled after rollback = %v, %v; want true", enrolled, err)
		}

		for range 2 {
			if err := factors.Forget(t.Context(), "1"); err != nil {
				t.Fatal(err)
			}
		}

		if enrolled, err := factors.Enrolled(t.Context(), "1"); err != nil || enrolled {
			t.Fatalf("Enrolled after Forget = %v, %v; want false", enrolled, err)
		}
	})
}
