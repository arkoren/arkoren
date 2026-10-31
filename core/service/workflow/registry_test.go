package workflow_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
)

// TestWorkflowSnapshotOperations checks native filtering while preserving
// lexical order and detached workflow policy records.
func TestWorkflowSnapshotOperations(t *testing.T) {
	t.Parallel()

	orchestrate := func(*workflow.Context, string) (string, error) {
		return "", nil
	}

	registry, err := workflow.Handlers(
		workflow.New("zeta", orchestrate, func(definition *workflow.Builder) {
			definition.MaxDuration(time.Minute)
		}),
		workflow.New("alpha", orchestrate, func(definition *workflow.Builder) {
			definition.MaxDuration(time.Minute)
		}),
	)

	if err != nil {
		t.Fatal(err)
	}

	workflows := registry.Workflows().Filter(func(_ int, handler workflow.WorkflowHandler) bool {
		return handler.Options.MaxDuration == time.Minute
	})

	if workflows.Len() != 2 || workflows[0].Name != "alpha" {
		t.Fatalf("workflows = %+v", workflows)
	}

	workflows[0].Options.MaxDuration = time.Hour

	if registry.Workflows()[0].Options.MaxDuration != time.Minute {
		t.Fatal("snapshot changed workflow policy")
	}
}

// TestStepSnapshotOperations checks grouping of detached name-ordered records
// without replacing the handler functions shared with the registry.
func TestStepSnapshotOperations(t *testing.T) {
	t.Parallel()

	perform := func(context.Context, string) (string, error) {
		return "", nil
	}

	registry, err := workflow.Handlers(
		workflow.NewStep("step-z", perform),
		workflow.NewStep("step-a", perform),
	)

	if err != nil {
		t.Fatal(err)
	}

	steps := registry.Steps()
	grouped := steps.GroupBy(func(_ int, handler workflow.StepHandler) int {
		return handler.Options.Attempts
	})

	if grouped[1].Len() != 2 || steps[0].Name != "step-a" {
		t.Fatalf("steps = %+v", steps)
	}

	handler, _ := registry.Step("step-a")

	if reflect.ValueOf(steps[0].Handle).Pointer() != reflect.ValueOf(handler.Handle).Pointer() {
		t.Fatal("snapshot replaced handler function")
	}

	steps[0].Name = "changed"

	if registry.Steps()[0].Name != "step-a" {
		t.Fatal("snapshot changed registered step")
	}
}

// TestRegistryEmptySnapshotsRemainNil protects the original nil representation
// when neither kind of definition has been registered.
func TestRegistryEmptySnapshotsRemainNil(t *testing.T) {
	t.Parallel()

	registry, err := workflow.Handlers()

	if err != nil {
		t.Fatal(err)
	}

	if registry.Workflows() != nil || registry.Steps() != nil {
		t.Fatal("empty registry inventories must remain nil")
	}
}

// TestRegistryRejectsZeroStep prevents an unnamed nil handler from entering
// the registry through a declaration that bypassed NewStep.
func TestRegistryRejectsZeroStep(t *testing.T) {
	t.Parallel()

	var step workflow.Step[string, string]

	if _, err := workflow.Handlers(&step); !errors.Is(err, workflow.ErrInvalidName) {
		t.Fatalf("zero step registration: %v", err)
	}
}

// TestRegistryRejectsZeroWorkflowBeforeMissingDuration reports absent identity
// rather than suggesting that a zero declaration only needs a worker timeout.
func TestRegistryRejectsZeroWorkflowBeforeMissingDuration(t *testing.T) {
	t.Parallel()

	var definition workflow.Workflow[string, string]

	if _, err := workflow.Handlers(&definition); !errors.Is(err, workflow.ErrInvalidName) {
		t.Fatalf("zero workflow registration: %v", err)
	}
}

// TestHandlersValidation checks published handler defaults and validation of
// missing workflow duration and name clashes within one registry namespace.
func TestHandlersValidation(t *testing.T) {
	good := workflow.New("good", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
		definition.Description("d")
		definition.Retention(time.Hour)
	})

	step := workflow.NewStep("step-a", noopStep)

	registry, err := workflow.Handlers(good, step)

	if err != nil {
		t.Fatalf("Handlers: %v", err)
	}

	workflows := registry.Workflows()

	if len(workflows) != 1 || workflows[0].Options.MaxDuration != time.Minute ||
		workflows[0].Options.Description != "d" || workflows[0].Options.Retention != time.Hour {
		t.Fatalf("Workflows = %+v", workflows)
	}

	steps := registry.Steps()

	if len(steps) != 1 || steps[0].Options.Attempts != 1 || steps[0].Options.Timeout != time.Minute {
		t.Fatalf("Steps = %+v", steps)
	}

	if _, err := workflow.Handlers(workflow.New("no-max", noopWorkflow)); !errors.Is(err, workflow.ErrMissingMaxDuration) {
		t.Fatalf("missing MaxDuration error = %v", err)
	}

	dup := workflow.New("good", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	if _, err := workflow.Handlers(good, dup); !errors.Is(err, workflow.ErrDuplicateName) {
		t.Fatalf("duplicate error = %v", err)
	}

	clash := workflow.NewStep("good", noopStep)

	if _, err := workflow.Handlers(good, clash); !errors.Is(err, workflow.ErrDuplicateName) {
		t.Fatalf("workflow/step clash error = %v", err)
	}
}

// TestRegistrySorted checks deterministic name ordering for both workflow and
// step listings regardless of definition registration order.
func TestRegistrySorted(t *testing.T) {
	registry, err := workflow.Handlers(
		workflow.New("zeta", noopWorkflow, func(definition *workflow.Builder) {
			definition.MaxDuration(time.Second)
		}),
		workflow.New("alpha", noopWorkflow, func(definition *workflow.Builder) {
			definition.MaxDuration(time.Second)
		}),
		workflow.NewStep("s2", noopStep),
		workflow.NewStep("s1", noopStep),
	)

	if err != nil {
		t.Fatal(err)
	}

	got := registry.Workflows()

	if got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("Workflows order = %v, %v", got[0].Name, got[1].Name)
	}

	steps := registry.Steps()

	if steps[0].Name != "s1" || steps[1].Name != "s2" {
		t.Fatalf("Steps order = %v, %v", steps[0].Name, steps[1].Name)
	}
}

// TestHandlersRejectsTypedNil checks nil definition handles hidden in an
// interface and requires the normal missing-function declaration error.
func TestHandlersRejectsTypedNil(t *testing.T) {
	var nilWorkflow *workflow.Workflow[string, string]

	if _, err := workflow.Handlers(nilWorkflow); !errors.Is(err, workflow.ErrNilFunc) {
		t.Fatalf("Handlers error = %v; want ErrNilFunc", err)
	}
}

// TestHandlersRejectsNegativeStepTimeout checks that invalid step execution
// policy remains a registration error rather than reaching the runtime.
func TestHandlersRejectsNegativeStepTimeout(t *testing.T) {
	step := workflow.NewStep("neg", noopStep, func(step *workflow.StepBuilder) {
		step.Timeout(-time.Second)
	})

	if _, err := workflow.Handlers(step); !errors.Is(err, workflow.ErrInvalidOptions) {
		t.Fatalf("Handlers error = %v; want ErrInvalidOptions", err)
	}
}
