package workflow_test

import (
	"testing"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/workflowtest"
)

// TestRunCancelPublishesRunCanceled checks that the Run handle retains its
// client's event bus and publishes the canceled workflow/run identity pair.
func TestRunCancelPublishesRunCanceled(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := collect(t, bus, workflow.RunCanceled)
	w := workflow.New("evt-cancel", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	client := workflow.NewClient(workflowtest.New(), func(client *workflow.ClientBuilder) {
		client.Events(bus)
	})

	run, err := client.Start(t.Context(), w, "run-1", "in")

	if err != nil {
		t.Fatal(err)
	}

	if err := run.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 1 || (*got)[0] != (workflow.RunEvent{
		Workflow: "evt-cancel",
		RunID:    "run-1",
	}) {
		t.Fatalf("RunCanceled = %+v", *got)
	}
}

// TestRunWithoutDriver checks a handle obtained without another Start call:
// it can query and cancel the already known driver-side run.
func TestRunWithoutDriver(t *testing.T) {
	driver := workflowtest.New()
	w := workflow.New("rw", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	client := workflow.NewClient(driver)

	if _, err := client.Start(t.Context(), w, "x", "in"); err != nil {
		t.Fatal(err)
	}

	run := client.Run(w, "x")

	if status, err := run.Status(t.Context()); err != nil || status != workflow.Running {
		t.Fatalf("Status = %v, %v", status, err)
	}

	if err := run.Cancel(t.Context()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}
