package hatchetworkflow

import (
	"errors"
	"testing"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"arkoren.dev/core/service/workflow"
)

// TestRunKeyEncodingSeparatesComponents covers punctuation and empty namespace
// boundaries that delimiter joining would conflate. The literal fixes the
// versioned wire format independently of its callers.
func TestRunKeyEncodingSeparatesComponents(t *testing.T) {
	t.Parallel()

	driver := newDriver(driverSettings(func(driver *DriverBuilder) {
		driver.Prefix("app")
	}))
	driver.namespace = "prod_"

	if got := driver.runKey("order", "o-1"); got != "arkoren.workflow.v2.cHJvZF8.YXBw.b3JkZXI.by0x" {
		t.Fatalf("runKey = %q", got)
	}

	other := newDriver(driverSettings(func(driver *DriverBuilder) {
		driver.Prefix("app:order")
	}))

	if driver.runKey("order", "x:y") == other.runKey("x", "y") {
		t.Fatal("delimiter-containing components collided")
	}

	other.namespace = "prod_"

	if driver.runKey("order", "x:y") == other.runKey("x", "y") {
		t.Fatal("same-namespace delimiter-containing components collided")
	}

	other = newDriver(driverSettings(func(driver *DriverBuilder) {
		driver.Prefix("prod_app")
	}))

	if driver.runKey("order", "o-1") == other.runKey("order", "o-1") {
		t.Fatal("namespace/prefix boundary collided")
	}
}

// TestDiscoveryRejectsForeignWorkflow deliberately ignores the requested
// backend filter. A physical run's real workflow must be checked by the driver
// before even cancellation can retain or use its ID.
func TestDiscoveryRejectsForeignWorkflow(t *testing.T) {
	t.Parallel()

	f := &fake{}
	driver := newTestDriver(f, func(driver *DriverBuilder) {
		driver.LookupGrace(-1)
	})
	driver.namespace = "staging_"
	f.record("production_"+driver.workflowName("order"), "foreign",
		wire{
			ID:  "o-1",
			Key: driver.runKey("order", "o-1"),
		})
	f.listed = f.runs

	if err := driver.Cancel(t.Context(), "order", "o-1"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Cancel() = %v", err)
	}

	if _, ok := driver.cached(driver.runKey("order", "o-1")); ok || len(f.canceled) != 0 {
		t.Fatal("foreign execution was cached or canceled")
	}
}

// TestDiscoveryRejectsForeignInput proves the workflow-name check alone is not
// enough: even a correctly named row cannot supply another logical identity.
func TestDiscoveryRejectsForeignInput(t *testing.T) {
	t.Parallel()

	f := &fake{}
	driver := newTestDriver(f, func(driver *DriverBuilder) {
		driver.LookupGrace(-1)
	})
	f.record(driver.workflowName("order"), "foreign", wire{
		ID:  "o-2",
		Key: driver.runKey("order", "o-1"),
	})
	f.listed = f.runs

	if _, err := driver.Result(t.Context(), "order", "o-1"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Result() = %v", err)
	}

	if _, ok := driver.cached(driver.runKey("order", "o-1")); ok {
		t.Fatal("foreign logical ID was cached")
	}
}

// TestCollisionRejectsForeignExecution exercises the SDK AlreadyStarted seam:
// its physical ID is not proof that the current namespace owns the run.
func TestCollisionRejectsForeignExecution(t *testing.T) {
	t.Parallel()

	f := &fake{
		triggerErr: &hatchet.IdempotencyCollisionError{
			ExistingRunExternalId: "foreign",
		},
	}
	driver := newTestDriver(f, func(driver *DriverBuilder) {
		driver.LookupGrace(-1)
	})
	driver.namespace = "staging_"
	f.record("production_"+driver.workflowName("order"), "foreign",
		wire{
			ID:  "o-1",
			Key: driver.runKey("order", "o-1"),
		})
	f.listed = f.runs

	err := driver.Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{})

	if !errors.Is(err, workflow.ErrNotFound) || errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("Start() = %v; foreign collision must not prove AlreadyStarted", err)
	}

	if _, ok := driver.cached(driver.runKey("order", "o-1")); ok {
		t.Fatal("foreign collision was cached")
	}

	if err := driver.Signal(t.Context(), "order", "o-1", "go", nil); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Signal() = %v", err)
	}

	if len(f.pushed) != 0 {
		t.Fatal("foreign execution admitted a signal")
	}
}

// TestStartRejectsForeignTriggerResponse covers a successful backend response
// as well as collisions. No returned ID is retained until its identity agrees.
func TestStartRejectsForeignTriggerResponse(t *testing.T) {
	t.Parallel()

	f := &fake{
		namespace: "production_",
	}
	driver := newTestDriver(f, func(driver *DriverBuilder) {
		driver.LookupGrace(-1)
	})
	driver.namespace = "staging_"

	if err := driver.Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{}); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Start() = %v", err)
	}

	if _, ok := driver.cached(driver.runKey("order", "o-1")); ok {
		t.Fatal("foreign trigger response was cached")
	}
}

// TestLegacyRunsFailClosed supplies ambiguous old-format runs even though the
// backend is asked for a new-format key. Neither empty nor configured namespaces
// may discover, read, cancel, or signal them through a compatibility fallback.
func TestLegacyRunsFailClosed(t *testing.T) {
	t.Parallel()

	f := &fake{}
	driver := newTestDriver(f, func(driver *DriverBuilder) {
		driver.LookupGrace(-1)
	})
	f.record(driver.workflowName("order"), "legacy-a", wire{
		ID:  "o-1",
		Key: "arkoren:order:o-1",
	})
	f.record("staging_"+driver.workflowName("order"), "legacy-b", wire{
		ID:  "o-1",
		Key: "arkoren:order:o-1",
	})
	f.listed = f.runs

	for _, namespace := range []string{"", "staging_"} {
		driver.namespace = namespace

		if _, err := driver.Status(t.Context(), "order", "o-1"); !errors.Is(err, workflow.ErrNotFound) {
			t.Fatalf("Status(%q) = %v", namespace, err)
		}

		if _, err := driver.Result(t.Context(), "order", "o-1"); !errors.Is(err, workflow.ErrNotFound) {
			t.Fatalf("Result(%q) = %v", namespace, err)
		}

		if err := driver.Cancel(t.Context(), "order", "o-1"); !errors.Is(err, workflow.ErrNotFound) {
			t.Fatalf("Cancel(%q) = %v", namespace, err)
		}

		if err := driver.Signal(t.Context(), "order", "o-1", "go", nil); !errors.Is(err, workflow.ErrNotFound) {
			t.Fatalf("Signal(%q) = %v", namespace, err)
		}
	}

	if len(f.canceled) != 0 || len(f.pushed) != 0 || len(driver.runs) != 0 {
		t.Fatal("legacy runs were cached or mutated")
	}
}

// TestRuntimeRejectsLegacyKeys verifies the worker rejects old wire identities
// before accessing its durable context or invoking user code after an upgrade.
func TestRuntimeRejectsLegacyKeys(t *testing.T) {
	t.Parallel()

	runner := &Runner{
		driver: newDriver(driverSettings()),
	}

	for _, key := range []string{"", "arkoren:order:o-1", "foreign"} {
		_, err := newRuntime(runner, nil, workflow.WorkflowHandler{
			Name: "order",
		}, wire{
			ID:  "o-1",
			Key: key,
		})

		if !errors.Is(err, workflow.ErrNotFound) {
			t.Fatalf("newRuntime(%q) = %v", key, err)
		}
	}
}

// TestSignalMarkerRejectsForeignNamespace checks the second idempotency domain,
// where a marker collision or untrusted event must not expose another value.
func TestSignalMarkerRejectsForeignNamespace(t *testing.T) {
	t.Parallel()

	f := &fake{}
	driver := newTestDriver(f, func(driver *DriverBuilder) {
		driver.LookupGrace(-1)
	})
	driver.namespace = "staging_"
	key := driver.runKey("order", "o-1") + "/go"
	f.record("production_"+driver.signalWorkflow(), "foreign", wire{
		Key:  key,
		Data: "secret",
	})
	f.listed = f.runs

	if _, err := driver.firstValue(t.Context(), "foreign", key, "physical"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("firstValue() = %v", err)
	}
}

// TestDiscoverySelectsOnlyExpectedWorkflow ensures a newer row belonging to
// another namespace cannot hide the older, correct execution when filters fail.
func TestDiscoverySelectsOnlyExpectedWorkflow(t *testing.T) {
	t.Parallel()

	f := &fake{}
	driver := newTestDriver(f, func(driver *DriverBuilder) {
		driver.LookupGrace(-1)
	})
	key := driver.runKey("order", "o-1")
	f.record(driver.workflowName("order"), "correct", wire{
		ID:  "o-1",
		Key: key,
	})
	f.record("staging_"+driver.workflowName("order"), "foreign", wire{
		ID:  "o-1",
		Key: key,
	})
	f.details_["correct"] = runDetail{
		Input:  f.details_["correct"].Input,
		Status: statusRunning,
	}
	f.runs[0].Created = time.Now().Add(-time.Minute)
	f.listed = f.runs

	if got, err := driver.Status(t.Context(), "order", "o-1"); err != nil || got != workflow.Running {
		t.Fatalf("Status() = %v, %v", got, err)
	}

	if entry, _ := driver.cached(key); entry.runID != "correct" {
		t.Fatalf("cached ID = %q", entry.runID)
	}
}
