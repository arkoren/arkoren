package hatchetworkflow

import (
	"testing"

	"arkoren.dev/core/service/workflow"
	host "arkoren.dev/driver/hatchet"
)

// TestDriverConstructionCallbacks verifies ordered, once-only edits and
// publication isolation from retained construction drafts.
func TestDriverConstructionCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	var retained *DriverBuilder

	component, err := NewDriver(host.New(), func(driver *DriverBuilder) {
		calls++
		retained = driver

		if driver.Prefix("first") != driver {
			t.Fatal("setting returned a different draft")
		}
	}, func(driver *DriverBuilder) {
		if calls != 1 || driver != retained {
			t.Fatal("callbacks did not share one ordered draft")
		}

		calls++
		driver.Prefix("application")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Prefix("changed")

	if calls != 2 || component.config.Prefix != "application" {
		t.Fatalf("published settings changed or callback replayed: calls=%d settings=%+v", calls, component.config)
	}
}

// TestDriverEmptyConstructionCallbacks verifies that omitted policies and
// expanded nil and empty policy slices all preserve the same defaults.
func TestDriverEmptyConstructionCallbacks(t *testing.T) {
	t.Parallel()

	var absent []func(*DriverBuilder)
	component, err := NewDriver(host.New(), absent...)

	if err != nil {
		t.Fatal(err)
	}

	empty := []func(*DriverBuilder){}
	other, err := NewDriver(host.New(), empty...)

	if err != nil {
		t.Fatal(err)
	}

	if component.config.Prefix != other.config.Prefix {
		t.Fatal("empty construction policies changed defaults")
	}
}

// TestDriverNilConstructionCallback verifies that supplied nil callbacks are
// programmer errors, not silently ignored optional settings.
func TestDriverNilConstructionCallback(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback did not panic")
		}
	}()

	_, _ = NewDriver(host.New(), nil)
}

// TestRunnerConstructionCallbacks verifies ordered, once-only edits and
// publication isolation from retained construction drafts.
func TestRunnerConstructionCallbacks(t *testing.T) {
	t.Parallel()

	registry, err := workflow.Handlers()

	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	var retained *RunnerBuilder

	component, err := NewRunner(host.New(), registry, func(runner *RunnerBuilder) {
		calls++
		retained = runner

		if runner.Prefix("first") != runner {
			t.Fatal("setting returned a different draft")
		}
	}, func(runner *RunnerBuilder) {
		if calls != 1 || runner != retained {
			t.Fatal("callbacks did not share one ordered draft")
		}

		calls++
		runner.Prefix("application")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Prefix("changed")

	if calls != 2 || component.config.Prefix != "application" {
		t.Fatalf("published settings changed or callback replayed: calls=%d settings=%+v", calls, component.config)
	}
}

// TestRunnerEmptyConstructionCallbacks verifies that omitted policies and
// expanded nil and empty policy slices all preserve the same defaults.
func TestRunnerEmptyConstructionCallbacks(t *testing.T) {
	t.Parallel()

	registry, err := workflow.Handlers()

	if err != nil {
		t.Fatal(err)
	}

	var absent []func(*RunnerBuilder)
	component, err := NewRunner(host.New(), registry, absent...)

	if err != nil {
		t.Fatal(err)
	}

	empty := []func(*RunnerBuilder){}
	other, err := NewRunner(host.New(), registry, empty...)

	if err != nil {
		t.Fatal(err)
	}

	if component.config.Prefix != other.config.Prefix {
		t.Fatal("empty construction policies changed defaults")
	}
}

// TestRunnerNilConstructionCallback verifies that supplied nil callbacks are
// programmer errors, not silently ignored optional settings.
func TestRunnerNilConstructionCallback(t *testing.T) {
	t.Parallel()

	registry, err := workflow.Handlers()

	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback did not panic")
		}
	}()

	_, _ = NewRunner(host.New(), registry, nil)
}
