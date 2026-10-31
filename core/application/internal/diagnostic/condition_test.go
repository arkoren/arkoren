package diagnostic

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/application/internal/health"
	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/validate"
)

// TestFailFastWaitsForPendingHealth keeps an uncompleted first pass distinct
// from a completed failure, even when the caller selects fail-fast waiting.
func TestFailFastWaitsForPendingHealth(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		monitor := health.New(time.Second, time.Second, func(context.Context, []health.Result, []health.Result) {})
		monitor.Add("database", func(context.Context) error {
			t.Fatal("passive condition ran a check")

			return nil
		})

		owner := &Owner{State: func() string { return "running" }, Health: monitor}

		start := time.Now()

		result, err := awaitHealthCondition(t.Context(), owner,
			healthConditionInput{Wait: true, FailFast: true, Timeout: validate.Duration(time.Second)},
			func() (HealthyView, bool) {
				return owner.HealthyView(), false
			})

		failure, ok := mcp.ToolFailureInfo(err)

		if !ok || failure.Code != "condition_timeout" || result.Status != "pending" || time.Since(start) != time.Second {
			t.Fatalf("result=%+v error=%v elapsed=%v", result, err, time.Since(start))
		}
	})
}
