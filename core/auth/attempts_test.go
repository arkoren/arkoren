package auth_test

import (
	"errors"
	"maps"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/auth/memoryauth"
	"arkoren.dev/core/http/session"
)

// wrongCode runs one failed second-factor attempt the way a handler does: the
// pending check that counts it, then Fail.
func wrongCode(t *testing.T, guard *auth.Guard, r *http.Request) {
	t.Helper()

	if _, err := guard.Pending(r); err != nil {
		t.Fatalf("pending: %v", err)
	}

	if err := guard.Fail(r); err != nil {
		t.Fatalf("fail: %v", err)
	}
}

// TestReBeginDoesNotResetAttempts verifies the attempt count belongs to the
// user: a new pending login in the same or another session continues it, and
// the lockout holds in every session.
func TestReBeginDoesNotResetAttempts(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(2)
	})
	r := sessionRequest(newSession())

	for range 2 {
		begin(t, guard, r, "7")
		wrongCode(t, guard, r)
	}

	if _, err := guard.Pending(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending after the limit = %v", err)
	}

	other := sessionRequest(newSession())
	begin(t, guard, other, "7")

	if _, err := guard.Pending(other); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending in a new session after the limit = %v", err)
	}

	begin(t, guard, other, "7")

	if err := guard.Complete(other, otp); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("complete in a new session after the limit = %v", err)
	}
}

// TestCompleteClearsAttempts verifies a successful second factor resets the
// user's failure count.
func TestCompleteClearsAttempts(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(2)
	})

	for range 3 {
		r := sessionRequest(newSession())
		begin(t, guard, r, "7")
		wrongCode(t, guard, r)

		if err := guard.Complete(r, otp); err != nil {
			t.Fatalf("complete: %v", err)
		}
	}
}

// TestConcurrentFailuresLockOut verifies requests that fork one pending session
// share the user's failure count: after MaxAttempts failures across the forks no
// session can read the pending login or complete it. Run with -race.
func TestConcurrentFailuresLockOut(t *testing.T) {
	t.Parallel()

	const maxAttempts = 3

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(maxAttempts)
	})
	origin := newSession()
	begin(t, guard, sessionRequest(origin), "7")

	fork := func() *session.Session {
		return session.New(origin.ExpiresAt(), maps.Clone(origin.All()))
	}

	var group sync.WaitGroup

	for range 2 * maxAttempts {
		current := fork()

		group.Go(func() {
			r := sessionRequest(current)

			if _, err := guard.Pending(r); err != nil {
				return
			}

			if err := guard.Fail(r); err != nil {
				t.Errorf("fail: %v", err)
			}
		})
	}

	group.Wait()

	if _, err := guard.Pending(sessionRequest(fork())); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending after concurrent failures = %v", err)
	}

	if err := guard.Complete(sessionRequest(fork()), otp); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("complete after concurrent failures = %v", err)
	}
}

// TestFederatedFactorFreshnessFollowsItsTime verifies a login stamped with an
// old authentication time does not satisfy RequireFresh, while one stamped
// recently does.
func TestFederatedFactorFreshnessFollowsItsTime(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t)
	fresh := auth.RequireFresh(10 * time.Minute)

	for _, test := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"silent re-login with old auth_time", time.Now().Add(-2 * time.Hour), false},
		{"fresh auth_time", time.Now().Add(-time.Minute), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := newSession()

			if _, err := guard.Begin(sessionRequest(current), "7", auth.Factor{Method: auth.MethodFederated, Kind: auth.KindFederated, At: test.at}); err != nil {
				t.Fatal(err)
			}

			identity, err := authenticate(guard, current)

			if err != nil {
				t.Fatal(err)
			}

			if test.want {
				admitted(t, fresh, &identity)
			} else {
				stepUpRequired(t, fresh, &identity)
			}
		})
	}
}

// TestStaleSessionCannotManageDevices verifies a session whose user was disabled
// or whose credential version changed can neither list nor revoke devices, and
// that RevokeOthers leaves the other devices alone.
func TestStaleSessionCannotManageDevices(t *testing.T) {
	t.Parallel()

	for name, invalidate := range map[string]func(*memoryauth.Driver) error{
		"version bump": func(driver *memoryauth.Driver) error {
			return driver.Put(auth.UserRecord{Subject: "7", Version: "v2", Value: user{"u"}})
		},
		"disabled": func(driver *memoryauth.Driver) error { return driver.Disable("7") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			guard, driver := newGuard(t)
			_, stale := device(t, guard, "7", "Firefox")
			device(t, guard, "7", "Safari")

			if err := invalidate(driver); err != nil {
				t.Fatal(err)
			}

			if _, err := guard.Logins(stale); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("Logins: %v", err)
			}

			if err := guard.RevokeOthers(stale); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("RevokeOthers: %v", err)
			}

			if got := len(loginsOf(t, guard, "7")); got != 2 {
				t.Fatalf("records = %d, want 2", got)
			}
		})
	}
}

// TestConcurrentPendingBoundsCodeChecks verifies a burst of requests forking one
// pending session passes Pending, the point where a code check is allowed, at
// most MaxAttempts times. Reading the counter before the check and counting
// only afterwards would let every request through. Run with -race.
func TestConcurrentPendingBoundsCodeChecks(t *testing.T) {
	t.Parallel()

	const maxAttempts = 5

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(maxAttempts)
	})
	origin := newSession()
	begin(t, guard, sessionRequest(origin), "7")

	sessions := make([]*session.Session, 200)

	for i := range sessions {
		sessions[i] = session.New(origin.ExpiresAt(), maps.Clone(origin.All()))
	}

	if got := countPending(guard, sessions); got != maxAttempts {
		t.Fatalf("code checks allowed from one pending session = %d, want %d", got, maxAttempts)
	}
}

// TestConcurrentPendingAcrossSessionsBoundsCodeChecks verifies the limit is per
// user: many independent pending sessions of one subject share MaxAttempts code
// checks. Run with -race.
func TestConcurrentPendingAcrossSessionsBoundsCodeChecks(t *testing.T) {
	t.Parallel()

	const maxAttempts = 5

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(maxAttempts)
	})
	sessions := make([]*session.Session, 200)

	for i := range sessions {
		sessions[i] = newSession()
		begin(t, guard, sessionRequest(sessions[i]), "7")
	}

	if got := countPending(guard, sessions); got != maxAttempts {
		t.Fatalf("code checks allowed across pending sessions = %d, want %d", got, maxAttempts)
	}
}

// countPending calls Pending once per session concurrently and returns how many
// calls were allowed to check a code.
func countPending(guard *auth.Guard, sessions []*session.Session) int {
	var (
		group   sync.WaitGroup
		allowed atomic.Int64
	)

	for _, current := range sessions {
		group.Go(func() {
			if _, err := guard.Pending(sessionRequest(current)); err == nil {
				allowed.Add(1)
			}
		})
	}

	group.Wait()

	return int(allowed.Load())
}
