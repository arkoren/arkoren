package queue_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"arkoren.dev/core/service/queue"
)

// ExamplePermanent distinguishes an explicit terminal outcome from success.
// Applications must check operation errors before wrapping them permanently.
func ExamplePermanent() {
	fmt.Println(queue.Permanent(nil) == nil)
	fmt.Println(errors.Is(queue.Permanent(errors.New("unsupported template")), queue.ErrPermanent))
	// Output:
	// false
	// true
}

// TestRejectPreservesCauseAndType verifies rejection outcomes retain their cause and type.
func TestRejectPreservesCauseAndType(t *testing.T) {
	t.Parallel()

	cause := errors.New("invalid recipient")

	err := queue.Reject(cause)

	_, ok := errors.AsType[*queue.RejectedError](err)

	if !ok || !errors.Is(err, cause) {
		t.Fatalf("rejected error = %v", err)
	}
}

// TestPermanentMatchesErrPermanent verifies failure outcomes match the sentinel and retain their cause.
func TestPermanentMatchesErrPermanent(t *testing.T) {
	t.Parallel()

	cause := errors.New("invalid template")

	err := queue.Permanent(cause)

	if !errors.Is(err, queue.ErrPermanent) || !errors.Is(err, cause) {
		t.Fatalf("permanent error = %v", err)
	}
}

// TestRetryAfterPreservesCauseTypeAndDelay verifies delayed retries preserve outcome details.
func TestRetryAfterPreservesCauseTypeAndDelay(t *testing.T) {
	t.Parallel()

	cause := errors.New("rate limited")

	err := queue.RetryAfter(cause, time.Minute)

	delayed, ok := errors.AsType[*queue.RetryAfterError](err)

	if !ok || delayed.RetryAfter() != time.Minute || !errors.Is(err, cause) {
		t.Fatalf("retry-after error = %v", err)
	}
}
