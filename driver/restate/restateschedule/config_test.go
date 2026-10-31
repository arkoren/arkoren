package restateschedule

import (
	"testing"

	host "arkoren.dev/driver/restate"
)

// TestSchedulerConstructionCallbacks verifies ordered, once-only edits and
// publication isolation from retained construction drafts.
func TestSchedulerConstructionCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	var retained *Builder
	component, err := New(host.New(), func(scheduler *Builder) {
		calls++
		retained = scheduler

		if scheduler.Prefix("first") != scheduler {
			t.Fatal("setting returned a different draft")
		}
	}, func(scheduler *Builder) {
		if calls != 1 || scheduler != retained {
			t.Fatal("callbacks did not share one ordered draft")
		}

		calls++
		scheduler.Prefix("application")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Prefix("changed")

	if calls != 2 || component.config.Prefix != "application" {
		t.Fatalf("published settings changed or callback replayed: calls=%d settings=%+v", calls, component.config)
	}
}

// TestSchedulerEmptyConstructionCallbacks verifies that omitted policies and
// expanded nil and empty policy slices all preserve the same defaults.
func TestSchedulerEmptyConstructionCallbacks(t *testing.T) {
	t.Parallel()

	var absent []func(*Builder)
	component, err := New(host.New(), absent...)

	if err != nil {
		t.Fatal(err)
	}

	empty := []func(*Builder){}
	other, err := New(host.New(), empty...)

	if err != nil {
		t.Fatal(err)
	}

	if component.config.Prefix != other.config.Prefix {
		t.Fatal("empty construction policies changed defaults")
	}
}

// TestSchedulerNilConstructionCallback verifies that supplied nil callbacks are
// programmer errors, not silently ignored optional settings.
func TestSchedulerNilConstructionCallback(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback did not panic")
		}
	}()

	_, _ = New(host.New(), nil)
}
