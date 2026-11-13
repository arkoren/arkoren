package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/router"
)

// guarded runs middleware around a handler for a request that carries identity
// (none for a nil pointer) and reports whether the handler was reached and the
// middleware error.
func guarded(middleware router.Middleware, identity *auth.Identity) (reached bool, err error) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	if identity != nil {
		r = r.WithContext(auth.NewContext(r.Context(), *identity))
	}

	err = middleware(func(http.ResponseWriter, *http.Request) error {
		reached = true

		return nil
	})(httptest.NewRecorder(), r)

	return reached, err
}

// withFactors returns an identity for subject "7" with factors.
func withFactors(factors ...auth.Factor) *auth.Identity {
	identity := auth.NewIdentity("7", func(principal *auth.IdentityBuilder) {
		principal.Factors(factors...)
	})

	return &identity
}

// admitted fails the test unless middleware admits identity.
func admitted(t *testing.T, middleware router.Middleware, identity *auth.Identity) {
	t.Helper()

	if reached, err := guarded(middleware, identity); err != nil || !reached {
		t.Fatalf("reached = %v, err = %v", reached, err)
	}
}

// stepUpRequired fails the test unless middleware answers identity with
// ErrStepUpRequired without reaching the handler.
func stepUpRequired(t *testing.T, middleware router.Middleware, identity *auth.Identity) {
	t.Helper()

	if reached, err := guarded(middleware, identity); !errors.Is(err, auth.ErrStepUpRequired) || reached {
		t.Fatalf("reached = %v, err = %v", reached, err)
	}
}

// TestRequireLevelSingle verifies one factor, or none for LevelNone, passes and
// that LevelSingle needs a factor.
func TestRequireLevelSingle(t *testing.T) {
	t.Parallel()

	admitted(t, auth.RequireLevel(auth.LevelSingle), withFactors(password))
	admitted(t, auth.RequireLevel(auth.LevelNone), withFactors())
	stepUpRequired(t, auth.RequireLevel(auth.LevelSingle), withFactors())
}

// TestRequireLevelMulti verifies only two distinct kinds reach LevelMulti.
func TestRequireLevelMulti(t *testing.T) {
	t.Parallel()

	middleware := auth.RequireLevel(auth.LevelMulti)

	admitted(t, middleware, withFactors(password, otp))
	stepUpRequired(t, middleware, withFactors(password))
	stepUpRequired(t, middleware, withFactors(password, auth.Factor{Method: auth.MethodRecovery, Kind: auth.KindKnowledge}))
	stepUpRequired(t, middleware, withFactors(password, federated))
}

// TestRequireLevelRejectsAnonymous verifies a request without identity is
// ErrUnauthenticated, not a step-up.
func TestRequireLevelRejectsAnonymous(t *testing.T) {
	t.Parallel()

	for _, middleware := range []router.Middleware{auth.RequireLevel(auth.LevelSingle), auth.RequireFresh(time.Minute)} {
		if reached, err := guarded(middleware, nil); !errors.Is(err, auth.ErrUnauthenticated) || reached {
			t.Fatalf("reached = %v, err = %v", reached, err)
		}
	}
}

// TestRequireLevelWithBearerIdentity verifies an identity from a token
// authenticator passes only when it carries the factors.
func TestRequireLevelWithBearerIdentity(t *testing.T) {
	t.Parallel()

	authenticate := auth.Bearer(func(_ context.Context, token string) (auth.Identity, error) {
		return auth.NewIdentity("7", func(principal *auth.IdentityBuilder) {
			if token == "strong" {
				principal.Factors(password, otp)
			}
		}), nil
	})
	protected := auth.Middleware(authenticate)(auth.RequireLevel(auth.LevelMulti)(func(http.ResponseWriter, *http.Request) error {
		return nil
	}))

	if err := protected(httptest.NewRecorder(), bearerRequest("Bearer strong")); err != nil {
		t.Fatalf("token with factors: %v", err)
	}

	if err := protected(httptest.NewRecorder(), bearerRequest("Bearer weak")); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("token without factors: %v", err)
	}
}

// TestRequireFreshWindow verifies the age window: the edge is fresh, one
// nanosecond later is not, and the freshest of several factors counts.
func TestRequireFreshWindow(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		at := func(offset time.Duration) auth.Factor {
			return auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge, At: time.Now().Add(offset)}
		}
		middleware := auth.RequireFresh(time.Minute)

		admitted(t, middleware, withFactors(at(0)))
		admitted(t, middleware, withFactors(at(-time.Minute)))
		admitted(t, middleware, withFactors(at(-time.Hour), at(-time.Second)))
		stepUpRequired(t, middleware, withFactors(at(-time.Minute-time.Nanosecond)))
		stepUpRequired(t, middleware, withFactors())
	})
}

// TestRequireFreshRejectsFutureFactor verifies a factor dated in the future is
// not fresh.
func TestRequireFreshRejectsFutureFactor(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		future := auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge, At: time.Now().Add(time.Nanosecond)}

		stepUpRequired(t, auth.RequireFresh(time.Hour), withFactors(future))
	})
}

// TestRequireFreshRejectsUnknownTime verifies a factor without a time is not
// fresh, however long the window.
func TestRequireFreshRejectsUnknownTime(t *testing.T) {
	t.Parallel()

	stepUpRequired(t, auth.RequireFresh(1000*time.Hour), withFactors(password))
}

// TestRequireFreshZeroAgeAdmitsNothing verifies a non-positive age admits no
// request, even one with a factor from this instant.
func TestRequireFreshZeroAgeAdmitsNothing(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		now := auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge, At: time.Now()}

		stepUpRequired(t, auth.RequireFresh(0), withFactors(now))
		stepUpRequired(t, auth.RequireFresh(-time.Minute), withFactors(now))
	})
}

// TestRequireFreshWithBearerIdentity verifies factor times on a token identity
// count like those of a session.
func TestRequireFreshWithBearerIdentity(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		fresh := withFactors(auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession, At: time.Now()})

		admitted(t, auth.RequireFresh(time.Minute), fresh)
		stepUpRequired(t, auth.RequireFresh(time.Minute), withFactors())
	})
}

// TestStepUpRunsCallbacksOnce verifies the middleware builders run a caller's
// callbacks once, at construction.
func TestStepUpRunsCallbacksOnce(t *testing.T) {
	t.Parallel()

	calls := 0
	configure := func(policy *auth.MiddlewareBuilder) {
		calls++
	}

	auth.RequireLevel(auth.LevelSingle, configure)
	auth.RequireFresh(time.Minute, configure)

	if calls != 2 {
		t.Fatalf("callbacks ran %d times for 2 middleware", calls)
	}
}

// TestStepUpProblem verifies the stable problem identity and status.
func TestStepUpProblem(t *testing.T) {
	t.Parallel()

	if auth.ErrStepUpRequired.Type() != "tag:arkoren.dev,2026-11:core:auth:step-up-required" || auth.ErrStepUpRequired.Status() != http.StatusForbidden {
		t.Fatalf("problem = %q, %d", auth.ErrStepUpRequired.Type(), auth.ErrStepUpRequired.Status())
	}
}

// TestConfirmSatisfiesFreshness verifies the login flow end to end: a stale
// session fails RequireFresh until Confirm records a new factor.
func TestConfirmSatisfiesFreshness(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		login, _ := newLogin(t)
		r := sessionRequest(newSession())
		begin(t, login, r, "7")

		time.Sleep(time.Hour)

		check := func() error {
			identity, err := login.Authenticator()(r)

			if err != nil {
				return err
			}

			_, err = guarded(auth.RequireFresh(time.Minute), &identity)

			return err
		}

		if err := check(); !errors.Is(err, auth.ErrStepUpRequired) {
			t.Fatalf("stale session = %v", err)
		}

		if err := login.Confirm(r, password); err != nil {
			t.Fatal(err)
		}

		if err := check(); err != nil {
			t.Fatalf("confirmed session = %v", err)
		}
	})
}
