package temporal

import (
	"testing"
)

// TestHostConstructionCallbacks verifies ordered, once-only edits and
// publication isolation from retained construction drafts.
func TestHostConstructionCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	var retained *Builder
	component := New(func(host *Builder) {
		calls++
		retained = host

		if host.TaskQueue("first") != host {
			t.Fatal("setting returned a different draft")
		}
	}, func(host *Builder) {
		if calls != 1 || host != retained {
			t.Fatal("callbacks did not share one ordered draft")
		}

		calls++

		host.TaskQueue("tasks")
	})

	retained.TaskQueue("changed")

	if calls != 2 || component.config.TaskQueue != "tasks" {
		t.Fatalf("published settings changed or callback replayed: calls=%d settings=%+v", calls, component.config)
	}
}

// TestHostEmptyConstructionCallbacks verifies that omitted policies and
// expanded nil and empty policy slices all preserve the same defaults.
func TestHostEmptyConstructionCallbacks(t *testing.T) {
	t.Parallel()

	var absent []func(*Builder)
	component := New(absent...)

	empty := []func(*Builder){}
	other := New(empty...)

	if component.config.TaskQueue != other.config.TaskQueue {
		t.Fatal("empty construction policies changed defaults")
	}
}

// TestHostNilConstructionCallback verifies that supplied nil callbacks are
// programmer errors, not silently ignored optional settings.
func TestHostNilConstructionCallback(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback did not panic")
		}
	}()

	_ = New(nil)
}
