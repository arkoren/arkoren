package execution

import (
	"errors"
	"testing"

	restatesdk "github.com/restatedev/sdk-go"

	"arkoren.dev/core/service/workflow"
)

// TestFailureOf verifies that cancellation and application error envelopes
// survive conversion to native terminal failures.
func TestFailureOf(t *testing.T) {
	t.Parallel()

	canceled := failureOf(workflow.ErrCanceled)

	terminal := restatesdk.AsTerminalError(canceled)

	if terminal == nil || terminal.Code() != 409 {
		t.Fatalf("canceled failure = %v", canceled)
	}

	other := restatesdk.AsTerminalError(failureOf(workflow.NewError("custom", "boom")))

	if other == nil || other.Code() != 500 {
		t.Fatalf("other failure = %v", other)
	}

	if err := workflow.DecodeError([]byte(other.Message())); !errors.Is(err, workflow.NewError("custom", "")) {
		t.Fatalf("message %q does not decode to its kind", other.Message())
	}
}
