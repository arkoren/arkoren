package engine_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/memoryworkflow"
)

// setup builds a driver for defs inside the current synctest bubble.
func setup(t *testing.T, defs ...workflow.Definition) *workflow.Client {
	t.Helper()

	registry, err := workflow.Handlers(defs...)

	if err != nil {
		t.Fatal(err)
	}

	driver := memoryworkflow.New(registry)
	t.Cleanup(func() {
		_ = driver.Stop(context.Background())
	})

	return workflow.NewClient(driver)
}

// TestSleepTakesWorkflowTime checks that the workflow clock advances by the
// requested sleep duration when execution resumes.
func TestSleepTakesWorkflowTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workflow.New("sleeper", func(c *workflow.Context, _ string) (int64, error) {
			started := c.Now()

			err := c.Sleep(time.Hour)

			return int64(c.Now().Sub(started)), err
		}, func(definition *workflow.Builder) {
			definition.MaxDuration(24 * time.Hour)
		})

		run, err := setup(t, w).Start(t.Context(), w, "r", "")

		if err != nil {
			t.Fatal(err)
		}

		got, err := run.Result(t.Context())

		if err != nil || time.Duration(got) != time.Hour {
			t.Fatalf("Result = %v, %v; want 1h", got, err)
		}
	})
}
