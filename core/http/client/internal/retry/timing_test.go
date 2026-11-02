package retry

import (
	"math"
	"net/http"
	"testing"
	"time"
)

// TestRetryAfterLargestWholeSeconds preserves the largest decimal delay that
// can be multiplied into a duration without overflowing.
func TestRetryAfterLargestWholeSeconds(t *testing.T) {
	t.Parallel()

	wait, ok := retryAfter(&http.Response{Header: http.Header{"Retry-After": {"9223372036"}}})

	if !ok || wait != 9223372036*time.Second {
		t.Fatalf("retryAfter = %v, %v", wait, ok)
	}
}

// TestRetryAfterDurationOverflow saturates before multiplying seconds.
func TestRetryAfterDurationOverflow(t *testing.T) {
	t.Parallel()

	wait, ok := retryAfter(&http.Response{Header: http.Header{"Retry-After": {"9223372037"}}})

	if !ok || wait != time.Duration(math.MaxInt64) {
		t.Fatalf("retryAfter = %v, %v", wait, ok)
	}
}

// TestRetryAfterUnsignedMaximum prevents signed conversion from wrapping.
func TestRetryAfterUnsignedMaximum(t *testing.T) {
	t.Parallel()

	wait, ok := retryAfter(&http.Response{Header: http.Header{"Retry-After": {"18446744073709551615"}}})

	if !ok || wait != time.Duration(math.MaxInt64) {
		t.Fatalf("retryAfter = %v, %v", wait, ok)
	}
}
