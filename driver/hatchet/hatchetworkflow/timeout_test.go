package hatchetworkflow

import (
	"testing"
	"time"
)

// requireSeconds verifies wholeSeconds and ceilSeconds round in to the given values.
func requireSeconds(t *testing.T, in, want time.Duration, secs int) {
	t.Helper()

	if got := wholeSeconds(in); got != want {
		t.Errorf("wholeSeconds(%v) = %v; want %v", in, got, want)
	}

	if got := ceilSeconds(in); got != secs {
		t.Errorf("ceilSeconds(%v) = %d; want %d", in, got, secs)
	}
}

// TestTimeoutsZero verifies that zero timeout settings retain the engine's
// minimum duration and zero rounded count.
func TestTimeoutsZero(t *testing.T) {
	t.Parallel()

	requireSeconds(t, 0, time.Second, 0)
}

// TestTimeoutsSubSecond verifies that positive subsecond timeouts round up to
// the native whole-second minimum.
func TestTimeoutsSubSecond(t *testing.T) {
	t.Parallel()

	requireSeconds(t, 50*time.Millisecond, time.Second, 1)
}

// TestTimeoutsOneSecond verifies that an exact whole-second timeout is not
// rounded up again.
func TestTimeoutsOneSecond(t *testing.T) {
	t.Parallel()

	requireSeconds(t, time.Second, time.Second, 1)
}

// TestTimeoutsRoundUp verifies that fractional seconds are rounded upward
// rather than shortening the execution allowance.
func TestTimeoutsRoundUp(t *testing.T) {
	t.Parallel()

	requireSeconds(t, 1500*time.Millisecond, 2*time.Second, 2)
}
