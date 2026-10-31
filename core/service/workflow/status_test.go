package workflow_test

import (
	"encoding/json/v2"
	"testing"

	"arkoren.dev/core/service/workflow"
)

// TestStatusJSONUsesStrings prevents storage formats from depending on enum
// declaration order. Empty remains invalid rather than identifying a run.
func TestStatusJSONUsesStrings(t *testing.T) {
	t.Parallel()

	statuses := []workflow.Status{"", workflow.Running, workflow.Completed, workflow.Failed, workflow.Canceled}

	data, err := json.Marshal(statuses)

	if err != nil {
		t.Fatal(err)
	}

	if string(data) != `["","running","completed","failed","canceled"]` {
		t.Fatalf("statuses JSON = %s", data)
	}

	var decoded []workflow.Status

	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded[0] != "" || decoded[1] != workflow.Running {
		t.Fatalf("decoded statuses = %v", decoded)
	}
}

// TestStatusEmptyStringRemainsInvalid ensures an absent persisted state is not
// accidentally treated as a running or completed workflow.
func TestStatusEmptyStringRemainsInvalid(t *testing.T) {
	t.Parallel()

	var status workflow.Status

	if status.String() != "" {
		t.Fatalf("zero Status.String() = %q", status.String())
	}
}

// TestStatusString checks the stable text for an active run. Other terminal
// states and the unknown-value fallback have separate assertions below.
func TestStatusString(t *testing.T) {
	if got := workflow.Running.String(); got != "running" {
		t.Fatalf("Running.String() = %q; want running", got)
	}
}

// TestStatusStringCompleted checks the text for a successful terminal run,
// keeping its expected representation independent of other status cases.
func TestStatusStringCompleted(t *testing.T) {
	t.Parallel()

	if got := workflow.Completed.String(); got != "completed" {
		t.Fatalf("Completed.String() = %q; want completed", got)
	}
}

// TestStatusStringFailed checks the text for an unsuccessful terminal run.
// It protects the stable representation used in diagnostics and inspection.
func TestStatusStringFailed(t *testing.T) {
	t.Parallel()

	if got := workflow.Failed.String(); got != "failed" {
		t.Fatalf("Failed.String() = %q; want failed", got)
	}
}

// TestStatusStringCanceled checks that cancellation has its own terminal text
// rather than reusing the generic execution-failure representation.
func TestStatusStringCanceled(t *testing.T) {
	t.Parallel()

	if got := workflow.Canceled.String(); got != "canceled" {
		t.Fatalf("Canceled.String() = %q; want canceled", got)
	}
}

// TestStatusStringUnknown checks that future stored states remain inspectable
// without mapping them to a known terminal state.
func TestStatusStringUnknown(t *testing.T) {
	t.Parallel()

	if got := workflow.Status("paused").String(); got != "paused" {
		t.Fatalf("Status.String() = %q; want paused", got)
	}
}
