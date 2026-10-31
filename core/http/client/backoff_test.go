package client_test

import (
	"fmt"
	"math"
	"testing"
	"time"

	"arkoren.dev/core/http/client"
)

// TestBackoff checks jitter bounds, saturation, and invalid-input normalization.
func TestBackoff(t *testing.T) {
	backoff := client.Backoff(100*time.Millisecond, 2, 300*time.Millisecond)

	for range 200 {
		for attempt, limit := range map[int]time.Duration{2: 100, 3: 200, 4: 300, 20: 300} {
			if delay := backoff(attempt); delay < 0 || delay >= limit*time.Millisecond {
				t.Fatalf("backoff(%d) = %v, want in [0, %dms)", attempt, delay, limit)
			}
		}
	}

	for _, invalid := range []func(int) time.Duration{
		client.Backoff(-1, 2, time.Second),
		client.Backoff(time.Second, -3, -1),
		client.Backoff(time.Second, 1e300, 0),
	} {
		if delay := invalid(1000); delay < 0 {
			t.Fatalf("invalid inputs gave negative delay %v", delay)
		}
	}
}

// ExampleBackoff_zeroInitial demonstrates disabling waits without allowing
// infinite growth to turn the zero policy into an invalid random bound.
func ExampleBackoff_zeroInitial() {
	backoff := client.Backoff(0, math.Inf(1), 0)
	fmt.Println(backoff(2048))
	// Output: 0s
}

// TestBackoffZeroInitialSurvivesOverflow keeps the documented zero-delay
// policy even when exponential growth cannot be represented by a float.
func TestBackoffZeroInitialSurvivesOverflow(t *testing.T) {
	t.Parallel()

	backoff := client.Backoff(0, 2, 0)

	for _, attempt := range []int{2, 2048, math.MaxInt} {
		if delay := backoff(attempt); delay != 0 {
			t.Fatalf("Backoff(%d) = %v, want zero", attempt, delay)
		}
	}
}

// TestBackoffZeroInitialSurvivesInfiniteFactor covers the same zero-delay
// contract when infinity is supplied directly rather than produced by growth.
func TestBackoffZeroInitialSurvivesInfiniteFactor(t *testing.T) {
	t.Parallel()

	backoff := client.Backoff(0, math.Inf(1), 0)

	for _, attempt := range []int{2, 3, math.MaxInt} {
		if delay := backoff(attempt); delay != 0 {
			t.Fatalf("Backoff(%d) = %v, want zero", attempt, delay)
		}
	}
}

// TestBackoffMinimumAttemptDoesNotOverflow treats attempts below two as the
// first retry. A one-nanosecond bound makes its jitter deterministically zero.
func TestBackoffMinimumAttemptDoesNotOverflow(t *testing.T) {
	t.Parallel()

	backoff := client.Backoff(time.Nanosecond, 2, 0)

	if delay := backoff(math.MinInt); delay != 0 {
		t.Fatalf("Backoff(MinInt) = %v, want zero", delay)
	}
}

// TestBackoffNonfiniteFactorsStayBounded preserves NaN/negative normalization
// and finite maximums, without depending on a random sample's exact value.
func TestBackoffNonfiniteFactorsStayBounded(t *testing.T) {
	t.Parallel()

	for _, factor := range []float64{math.NaN(), math.Inf(-1), math.Inf(1)} {
		backoff := client.Backoff(time.Nanosecond, factor, time.Nanosecond)

		if delay := backoff(math.MaxInt); delay != 0 {
			t.Fatalf("Backoff factor %v = %v, want zero", factor, delay)
		}
	}
}
