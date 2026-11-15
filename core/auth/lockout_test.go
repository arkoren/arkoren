package auth_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
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

// TestLockoutCaps verifies the lockout never exceeds 24 hours: a 16 hour window
// would double to 32 hours.
func TestLockoutCaps(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, locked := lockoutGuard(t, 16*time.Hour)

		for level, want := range []time.Duration{16 * time.Hour, 24 * time.Hour} {
			started := time.Now()
			exhaust(t, guard)

			if got := (*locked)[level].Until.Sub(started); got != want {
				t.Fatalf("lockout %d lasts %v, want %v", level, got, want)
			}

			time.Sleep(want)
		}
	})
}

// TestLockoutUnderTheLimit verifies an attacker who spends one attempt of two per
// window is still locked, because the 24 hour budget adds the failures up.
func TestLockoutUnderTheLimit(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, locked := lockoutGuard(t, lockoutWindow)

		for range 2 {
			r := sessionRequest(newSession())
			begin(t, guard, r, "7")
			wrongCode(t, guard, r)

			if len(*locked) == 0 {
				time.Sleep(lockoutWindow + time.Second)
			}
		}

		if len(*locked) != 1 || (*locked)[0].Subject != "7" {
			t.Fatalf("events = %+v, want one lockout of subject 7", *locked)
		}

		r := sessionRequest(newSession())
		begin(t, guard, r, "7")

		if err := challenge(guard, r, goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("correct code during the lockout = %v", err)
		}
	})
}

// TestLockoutBlocksConfirm verifies a lockout also refuses step-up.
func TestLockoutBlocksConfirm(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, _ := newGuard(t, withStepUp(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
			policy.ChallengeTTL(lockoutWindow).MaxAttempts(2)
		})

		r := sessionRequest(newSession())
		begin(t, guard, r, "7")

		for range 2 {
			if err := confirm(guard, r, badOTP); !errors.Is(err, auth.ErrInvalidCredential) {
				t.Fatalf("wrong code = %v", err)
			}
		}

		if err := confirm(guard, r, goodOTP); !errors.Is(err, auth.ErrInvalidCredential) {
			t.Fatalf("correct code during the lockout = %v", err)
		}

		time.Sleep(lockoutWindow)

		if err := confirm(guard, r, goodOTP); err != nil {
			t.Fatalf("correct code after the lockout = %v", err)
		}
	})
}

// failingAdd is a cache driver whose Add fails, which is how a lockout is
// stored.
type failingAdd struct {
	cache.Driver
}

// Add always fails.
func (failingAdd) Add(context.Context, string, []byte, time.Duration) (bool, error) {
	return false, errors.New("cache down")
}

// TestLockoutCacheFailureKeepsWrongCodeError verifies a cache error while
// locking neither becomes a server error nor skips the wrong-code cleanup: the
// pending login is still discarded and no event is published.
func TestLockoutCacheFailureKeepsWrongCodeError(t *testing.T) {
	t.Parallel()

	provider, _ := newDirectory(t)
	bus := event.New()
	published := 0

	if _, err := bus.Listen(auth.SecondFactorLocked, func(context.Context, auth.SecondFactorLockedEvent) error {
		published++

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	guard, err := auth.NewGuard(provider, cache.New(failingAdd{memorycache.New()}), withChecks(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(2).Events(bus)
	})

	if err != nil {
		t.Fatal(err)
	}

	r := sessionRequest(newSession())
	begin(t, guard, r, "7")
	wrongCode(t, guard, r)
	wrongCode(t, guard, r)

	if _, err := guard.Pending(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("pending after the last wrong code = %v", err)
	}

	if published != 0 {
		t.Fatalf("events = %d, want none", published)
	}
}

// TestLockoutWithoutBus verifies the lockout holds when no bus is set.
func TestLockoutWithoutBus(t *testing.T) {
	t.Parallel()

	guard, _ := newGuard(t, withChecks(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
		policy.MaxAttempts(2)
	})

	exhaust(t, guard)

	r := sessionRequest(newSession())
	begin(t, guard, r, "7")

	if err := challenge(guard, r, goodOTP); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("correct code during the lockout = %v", err)
	}
}

// TestUnlock verifies Unlock ends the lockout at once and forgets the
// escalation, so the next lockout lasts one window again.
func TestUnlock(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		guard, locked := lockoutGuard(t, lockoutWindow)

		exhaust(t, guard)

		if err := guard.Unlock(t.Context(), "7"); err != nil {
			t.Fatal(err)
		}

		if err := guard.Unlock(t.Context(), "never-locked"); err != nil {
			t.Fatalf("unlock of an unlocked subject: %v", err)
		}

		r := sessionRequest(newSession())
		begin(t, guard, r, "7")

		if err := challenge(guard, r, goodOTP); err != nil {
			t.Fatalf("correct code after Unlock = %v", err)
		}

		started := time.Now()
		exhaust(t, guard)

		if len(*locked) != 2 || (*locked)[1].Until.Sub(started) != lockoutWindow {
			t.Fatalf("events = %+v, want a second lockout of one window", *locked)
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

// TestStepUpTyposDoNotEscalate verifies five step-ups in a day, each with one
// typo before the right code, cause no lockout: a wrong step-up code does not
// count toward the budget unless it uses up the window.
func TestStepUpTyposDoNotEscalate(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		bus := event.New()
		published := 0

		if _, err := bus.Listen(auth.SecondFactorLocked, func(context.Context, auth.SecondFactorLockedEvent) error {
			published++

			return nil
		}); err != nil {
			t.Fatal(err)
		}

		guard, _ := newGuard(t, withStepUp(secondFactor(true, nil)), func(policy *auth.GuardBuilder) {
			policy.ChallengeTTL(lockoutWindow).MaxAttempts(2).Events(bus)
		})
		r := sessionRequest(newSession())
		begin(t, guard, r, "7")

		for range 5 {
			if err := confirm(guard, r, badOTP); !errors.Is(err, auth.ErrInvalidCredential) {
				t.Fatalf("typo = %v", err)
			}

			if err := confirm(guard, r, goodOTP); err != nil {
				t.Fatalf("right code after a typo = %v", err)
			}

			time.Sleep(2 * lockoutWindow)
		}

		if published != 0 {
			t.Fatalf("lockouts = %d, want none", published)
		}
	})
}
