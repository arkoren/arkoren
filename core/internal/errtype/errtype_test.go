package errtype_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"arkoren.dev/core/internal/errtype"
)

// errKnown contains sensitive text that classification must never expose.
// Tests register a stable class name instead of using its message.
var errKnown = errors.New("secret value 42")

// checkOf fails the test when errtype.Of classifies err as anything but want,
// using the package's errKnown sentinel as the registered sentinel.
func checkOf(t *testing.T, err error, want string) {
	t.Helper()

	known := errtype.Sentinel{Err: errKnown, Name: "pkg.ErrKnown"}

	if got := errtype.Of(err, known); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestOfNil checks the successful-operation classification.
// A nil error must not produce an error.type label.
func TestOfNil(t *testing.T) {
	t.Parallel()

	checkOf(t, nil, "")
}

// TestOfCanceled checks cancellation through an operation wrapper.
// Classification must retain the stable context sentinel name.
func TestOfCanceled(t *testing.T) {
	t.Parallel()

	checkOf(t, fmt.Errorf("op: %w", context.Canceled), "context.Canceled")
}

// TestOfDeadline checks the deadline sentinel's stable class name.
// It must remain distinct from ordinary cancellation.
func TestOfDeadline(t *testing.T) {
	t.Parallel()

	checkOf(t, context.DeadlineExceeded, "context.DeadlineExceeded")
}

// TestOfSentinel checks explicitly registered error classes through wrapping.
// The registered name must replace potentially sensitive error text.
func TestOfSentinel(t *testing.T) {
	t.Parallel()

	checkOf(t, fmt.Errorf("op: %w", errKnown), "pkg.ErrKnown")
}

// TestOfSingleJoined checks traversal through a one-error join.
// The leaf's concrete type must win over join and formatting wrappers.
func TestOfSingleJoined(t *testing.T) {
	t.Parallel()

	checkOf(t, errors.Join(fmt.Errorf("a: %w", io.ErrUnexpectedEOF)), "*errors.errorString")
}

// TestOfInnermost checks classification through multiple formatting wrappers.
// Only the innermost error type belongs in the resulting label.
func TestOfInnermost(t *testing.T) {
	t.Parallel()

	checkOf(t, fmt.Errorf("a: %w", fmt.Errorf("b: %w", io.ErrUnexpectedEOF)), "*errors.errorString")
}

// TestOfWrappedType checks fallback classification for an unregistered type.
// The qualified type name must not depend on its Error message.
func TestOfWrappedType(t *testing.T) {
	t.Parallel()

	checkOf(t, fmt.Errorf("a: %w", &net{}), "*errtype_test.net")
}

// net is an unregistered concrete error for fallback classification tests.
// Its message deliberately differs from the qualified type name.
type net struct{}

// Error supplies the message required by the error interface.
// Classification tests verify that this text is not used as the class name.
func (*net) Error() string {
	return "boom"
}
