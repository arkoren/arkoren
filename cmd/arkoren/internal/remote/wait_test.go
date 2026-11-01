package remote

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestExpiredWaitReportsBudgetBeforeContextTimerFires checks timeout wording.
// An expired stored deadline is sufficient even without a fired context timer.
func TestExpiredWaitReportsBudgetBeforeContextTimerFires(t *testing.T) {
	t.Parallel()

	budget := &WaitBudget{
		Timeout:  time.Second,
		Deadline: time.Now().Add(-time.Millisecond),
	}

	err := WaitError(context.Background(), budget, "waiting for life:ready", context.DeadlineExceeded)

	if err == nil || !strings.Contains(err.Error(), "timed out after 1s waiting for life:ready") {
		t.Fatalf("error = %v", err)
	}
}
