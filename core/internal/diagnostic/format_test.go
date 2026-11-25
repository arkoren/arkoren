package diagnostic_test

import (
	"testing"
	"time"

	"arkoren.dev/core/internal/diagnostic"
)

// TestMillisPreservesMachinePrecision keeps fractional millisecond values for
// programs; people see them in readable units from the CLI.
func TestMillisPreservesMachinePrecision(t *testing.T) {
	t.Parallel()

	if diagnostic.Millis(182123456*time.Nanosecond) != 182.123456 {
		t.Fatal("machine duration rounded")
	}
}
