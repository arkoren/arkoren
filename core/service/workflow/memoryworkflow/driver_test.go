package memoryworkflow_test

import (
	"context"
	"testing"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/memoryworkflow"
	"arkoren.dev/core/service/workflow/workflowtest"
)

// TestConformance runs the complete driver contract with fresh memory drivers
// and virtual time so retries, delays, and waits need no wall-clock pauses.
func TestConformance(t *testing.T) {
	workflowtest.Run(t, func(t *testing.T) workflow.Driver {
		driver := memoryworkflow.New(workflowtest.Registry(t))
		t.Cleanup(func() {
			_ = driver.Stop(context.Background())
		})

		return driver
	}, workflowtest.WithSynctest())
}
