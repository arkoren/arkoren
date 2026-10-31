package workflow_test

import (
	"errors"
	"testing"

	"arkoren.dev/core/service/workflow"
)

// TestPermanent checks nil preservation and wrapping that retains the
// original error while adding the permanent-failure classification.
func TestPermanent(t *testing.T) {
	if workflow.Permanent(nil) != nil {
		t.Fatal("Permanent(nil) must be nil")
	}

	base := errors.New("base")

	err := workflow.Permanent(base)

	if !errors.Is(err, base) || !errors.Is(err, workflow.ErrPermanent) || err.Error() != "base" {
		t.Fatalf("Permanent = %v", err)
	}

	if errors.Is(base, workflow.ErrPermanent) {
		t.Fatal("plain error is not permanent")
	}
}
