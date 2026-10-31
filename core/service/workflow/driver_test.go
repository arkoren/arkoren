package workflow_test

import (
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
)

// TestBackoffDoesNotOverflow checks that a large uncapped exponential delay
// clamps to a positive duration instead of wrapping through integer overflow.
func TestBackoffDoesNotOverflow(t *testing.T) {
	o := workflow.StepOptions{
		Initial: time.Hour,
		Factor:  1000,
	}

	if got := o.Backoff(50); got <= 0 {
		t.Fatalf("Backoff = %v; want a positive clamped delay", got)
	}
}
