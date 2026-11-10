package twofactor_test

import (
	"errors"
	"strconv"
	"testing"

	"arkoren.dev/ark/twofactor"
	"arkoren.dev/core/http/auth"
	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/memoryschedule"
	"arkoren.dev/foundation/query"
)

// runSweep adds the sweep entry of factors to a scheduler and runs it now.
func runSweep(t *testing.T, factors *twofactor.TwoFactor) error {
	t.Helper()

	scheduler := schedule.New(memoryschedule.New())
	entry := factors.Sweep(schedule.UTC().Hour(3))

	if err := scheduler.Add(entry); err != nil {
		t.Fatal(err)
	}

	return scheduler.RunNow(t.Context(), entry.Name())
}

// TestSweepForgetsOnlyDeletedUsers enrolls an existing, a deleted and a
// suspended user. The sweep must forget the deleted one, keep the suspended
// one's rows, and report the suspended user's loader error.
func TestSweepForgetsOnlyDeletedUsers(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		env.users.add(1, 2, 3)

		factors := newFactors(t, env)

		for id := int64(1); id <= 3; id++ {
			enrolled(t, factors, account{ID: id})
		}

		env.users.remove(2)
		env.users.suspend(3)

		if err := runSweep(t, factors); !errors.Is(err, auth.ErrNoCredentials) || errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("sweep = %v, want only the suspended user's ErrNoCredentials", err)
		}

		for subject, want := range map[string]bool{"1": true, "2": false, "3": true} {
			if enrolled, err := factors.Enrolled(t.Context(), subject); err != nil || enrolled != want {
				t.Errorf("Enrolled(%s) = %v, %v; want %v", subject, enrolled, err, want)
			}
		}
	})
}

// TestSweepPagesThroughEverySubject stores more subjects than one page holds,
// all belonging to deleted users, and expects the sweep to forget every one.
func TestSweepPagesThroughEverySubject(t *testing.T) {
	forEachDialect(t, func(t *testing.T, env environment) {
		instance := uniqueInstance()
		factors := newFactors(t, env, withInstance(instance))

		for id := range 250 {
			insertSecret(t, env, instance, strconv.Itoa(id+1))
		}

		if err := runSweep(t, factors); err != nil {
			t.Fatalf("sweep: %v", err)
		}

		remaining, err := env.db.Select[string](t.Context(), query.Table(instance+"_twofactor_secrets", func(statement *query.Builder) {
			statement.Select("user_id")
		}))

		if err != nil || remaining.Len() != 0 {
			t.Fatalf("remaining = %d, %v; want 0", remaining.Len(), err)
		}
	})
}

// TestSweepEntryNamesAreValidAndDistinctPerInstance checks that instances
// containing underscores still yield valid, non-colliding entry names.
func TestSweepEntryNamesAreValidAndDistinctPerInstance(t *testing.T) {
	t.Parallel()

	env := newEnvironment(t, "sqlite")
	seen := map[string]bool{}

	for instance, want := range map[string]string{
		"":       "twofactor-sweep",
		"admin":  "twofactor-admin-sweep",
		"a_b":    "twofactor-a-b-sweep",
		"a_b_c1": "twofactor-a-b-c1-sweep",
	} {
		factors := newUnmigratedFactors(t, env, withInstance(instance))
		entry := factors.Sweep(schedule.UTC().Hour(3))

		if entry.Name() != want || entry.Err() != nil || seen[entry.Name()] {
			t.Errorf("instance %q: name = %q, err = %v; want %q", instance, entry.Name(), entry.Err(), want)
		}

		seen[entry.Name()] = true
	}

	if err := newUnmigratedFactors(t, env).Sweep(schedule.UTC()).Err(); err == nil {
		t.Error("a sweep without a schedule is valid")
	}
}
