package auth_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
)

// lockoutWindow is the challenge window of the lockout tests.
const lockoutWindow = time.Minute

// lockoutGuard returns a guard allowing two attempts per window whose
// [auth.SecondFactorLocked] events are collected in the returned slice. Call it
// inside a synctest bubble so the cache and the clock are fake.
func lockoutGuard(t *testing.T, window time.Duration) (*auth.Guard, *[]auth.SecondFactorLockedEvent) {
	t.Helper()

	bus := event.New()
	locked := &[]auth.SecondFactorLockedEvent{}

	if _, err := bus.Listen(auth.SecondFactorLocked, func(_ context.Context, payload auth.SecondFactorLockedEvent) error {
		*locked = append(*locked, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.ChallengeTTL(window).MaxAttempts(2).Events(bus)
	})

	return guard, locked
}

// exhaust parks a login for subject "7" and spends both attempts with wrong
// codes.
func exhaust(t *testing.T, guard *auth.Guard) {
	t.Helper()

	r := sessionRequest(newSession())
	begin(t, guard, r, "7")
	wrongCode(t, guard, r)
	wrongCode(t, guard, r)
}

// TestLockoutEscalates verifies each lockout lasts twice the previous one, is
// published with the subject and its end, and refuses even a correct code until
// it ends.
func TestLockoutEscalates(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, locked := lockoutGuard(t, lockoutWindow)

		for level, want := range []time.Duration{lockoutWindow, 2 * lockoutWindow, 4 * lockoutWindow} {
			started := time.Now()
			exhaust(t, guard)

			if len(*locked) != level+1 {
				t.Fatalf("events after lockout %d = %d", level, len(*locked))
			}

			if got := (*locked)[level]; got.Subject != "7" || got.Until.Sub(started) != want {
				t.Fatalf("event %d = %+v, want subject 7 lasting %v", level, got, want)
			}

			r := sessionRequest(newSession())
			begin(t, guard, r, "7")
			time.Sleep(want - time.Second)

			if err := challenge(guard, r, goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("level %d: correct code during the lockout = %v", level, err)
			}

			time.Sleep(time.Second)
		}
	})
}

// TestLockoutCaps verifies the lockout never exceeds 24 hours.
func TestLockoutCaps(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, locked := lockoutGuard(t, 10*time.Hour)

		for level, want := range []time.Duration{10 * time.Hour, 20 * time.Hour, 24 * time.Hour, 24 * time.Hour} {
			started := time.Now()
			exhaust(t, guard)

			if got := (*locked)[level].Until.Sub(started); got != want {
				t.Fatalf("lockout %d lasts %v, want %v", level, got, want)
			}

			time.Sleep(want)
		}
	})
}

// TestLockoutResetsOnSuccess verifies a correct challenge forgets the
// escalation, so the next lockout lasts one window again.
func TestLockoutResetsOnSuccess(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, locked := lockoutGuard(t, lockoutWindow)

		exhaust(t, guard)
		time.Sleep(lockoutWindow)

		r := sessionRequest(newSession())
		begin(t, guard, r, "7")

		if err := challenge(guard, r, goodOTP); err != nil {
			t.Fatalf("challenge after the lockout: %v", err)
		}

		started := time.Now()
		exhaust(t, guard)

		if len(*locked) != 2 || (*locked)[1].Until.Sub(started) != lockoutWindow {
			t.Fatalf("events = %+v, want a second lockout of one window", *locked)
		}
	})
}

// TestLockoutLevelExpires verifies a day without a lockout forgets the
// escalation.
func TestLockoutLevelExpires(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, locked := lockoutGuard(t, lockoutWindow)

		exhaust(t, guard)
		time.Sleep(lockoutWindow + 24*time.Hour + time.Second)

		started := time.Now()
		exhaust(t, guard)

		if len(*locked) != 2 || (*locked)[1].Until.Sub(started) != lockoutWindow {
			t.Fatalf("events = %+v, want a second lockout of one window", *locked)
		}
	})
}
