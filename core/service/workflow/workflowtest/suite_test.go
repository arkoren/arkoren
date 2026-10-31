package workflowtest

import (
	"errors"
	"testing"

	"arkoren.dev/core/service/workflow"
)

// TestWithoutChildLeavesSpawnRequired keeps the capability declarations separate.
// A driver opting out of Child must still satisfy the ordinary Spawn cases.
func TestWithoutChildLeavesSpawnRequired(t *testing.T) {
	t.Parallel()

	var settings options
	WithoutChild()(&settings)

	if !settings.withoutChild || settings.withoutSpawn || settings.synctest {
		t.Fatalf("WithoutChild settings = %+v", settings)
	}
}

// TestWithoutSpawnLeavesChildRequired keeps the capability declarations separate.
// A driver opting out of Spawn must still satisfy Child success, errors, and joins.
func TestWithoutSpawnLeavesChildRequired(t *testing.T) {
	t.Parallel()

	var settings options
	WithoutSpawn()(&settings)

	if !settings.withoutSpawn || settings.withoutChild || settings.synctest {
		t.Fatalf("WithoutSpawn settings = %+v", settings)
	}
}

// TestUnsupportedResultAcceptsRejection checks the no-submission assertion with
// a fresh driver. The wrapped sentinel must be recognized without pointer equality.
func TestUnsupportedResultAcceptsRejection(t *testing.T) {
	t.Parallel()

	err := unsupportedResult(t.Context(), New(), "child", "child-id", errors.Join(errors.New("operation"), workflow.ErrUnsupported))

	if err != nil {
		t.Fatal(err)
	}
}

// TestUnsupportedResultRejectsSupportedOperation prevents a capability option
// from silently skipping functionality that the driver claims not to support.
func TestUnsupportedResultRejectsSupportedOperation(t *testing.T) {
	t.Parallel()

	if err := unsupportedResult(t.Context(), New(), "child", "child-id", nil); err == nil {
		t.Fatal("accepted a supported operation under an unsupported declaration")
	}
}

// TestUnsupportedResultRejectsSubmission catches a dishonest runtime that starts
// work and then returns ErrUnsupported. Checking only the parent error would pass.
func TestUnsupportedResultRejectsSubmission(t *testing.T) {
	t.Parallel()

	driver := New()

	if err := driver.Start(t.Context(), "child", "child-id", nil, workflow.StartOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := unsupportedResult(t.Context(), driver, "child", "child-id", workflow.ErrUnsupported); err == nil {
		t.Fatal("accepted submission followed by ErrUnsupported")
	}
}

// TestUnsupportedResultRejectsCompletedSubmission retains the no-work assertion
// after a fast child finishes. Checking only running work would hide its effects.
func TestUnsupportedResultRejectsCompletedSubmission(t *testing.T) {
	t.Parallel()

	driver := New()

	if err := driver.Start(t.Context(), "child", "child-id", nil, workflow.StartOptions{}); err != nil {
		t.Fatal(err)
	}

	driver.Complete("child", "child-id", "done")

	if err := unsupportedResult(t.Context(), driver, "child", "child-id", workflow.ErrUnsupported); err == nil {
		t.Fatal("accepted completed work followed by ErrUnsupported")
	}
}
