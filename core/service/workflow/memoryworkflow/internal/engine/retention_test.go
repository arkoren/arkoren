package engine_test

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/workflow"
)

// TestRetentionForgetsFinishedRun checks that a successful result is retained
// until its policy expires, then becomes unknown to subsequent status reads.
func TestRetentionForgetsFinishedRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workflow.New("short-lived", func(*workflow.Context, string) (string, error) {
			return "ok", nil
		}, func(definition *workflow.Builder) {
			definition.MaxDuration(time.Hour)
			definition.Retention(time.Minute)
		})

		run, err := setup(t, w).Start(t.Context(), w, "r", "")

		if err != nil {
			t.Fatal(err)
		}

		if _, err := run.Result(t.Context()); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Minute)
		synctest.Wait()

		if _, err := run.Status(t.Context()); !errors.Is(err, workflow.ErrNotFound) {
			t.Fatalf("Status error = %v; want ErrNotFound after the retention", err)
		}
	})
}
