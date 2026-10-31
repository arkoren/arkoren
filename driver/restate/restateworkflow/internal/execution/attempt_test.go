package execution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	restatesdk "github.com/restatedev/sdk-go"

	"arkoren.dev/core/service/workflow"
)

// stepHandler wraps fn as a step.
func stepHandler(fn func(context.Context, []byte) ([]byte, error)) workflow.StepHandler {
	return workflow.StepHandler{
		Name:   "s",
		Handle: fn,
	}
}

// TestAttemptSuccess verifies that a successful step attempt returns its opaque
// payload unchanged.
func TestAttemptSuccess(t *testing.T) {
	t.Parallel()

	out, err := attempt(t.Context(), stepHandler(func(context.Context, []byte) ([]byte, error) {
		return []byte("ok"), nil
	}), nil, workflow.StepOptions{})

	if err != nil || string(out) != "ok" {
		t.Fatalf("attempt = %q, %v", out, err)
	}
}

// TestAttemptRetryable verifies that ordinary step failures remain retryable at
// the native run boundary.
func TestAttemptRetryable(t *testing.T) {
	t.Parallel()

	_, err := attempt(t.Context(), stepHandler(func(context.Context, []byte) ([]byte, error) {
		return nil, errors.New("flaky")
	}), nil, workflow.StepOptions{})

	if err == nil || restatesdk.IsTerminalError(err) || !strings.Contains(err.Error(), "flaky") {
		t.Fatalf("attempt error = %v", err)
	}
}

// TestAttemptTerminal verifies that permanent step failures become native
// terminal failures rather than being retried.
func TestAttemptTerminal(t *testing.T) {
	t.Parallel()

	_, err := attempt(t.Context(), stepHandler(func(context.Context, []byte) ([]byte, error) {
		return nil, workflow.Permanent(errors.New("stop"))
	}), nil, workflow.StepOptions{})

	if !restatesdk.IsTerminalError(err) {
		t.Fatalf("attempt error = %v; want terminal", err)
	}
}

// TestAttemptTimeout verifies that a step context deadline becomes the encoded
// workflow timeout failure.
func TestAttemptTimeout(t *testing.T) {
	t.Parallel()

	_, err := attempt(t.Context(), stepHandler(func(ctx context.Context, _ []byte) ([]byte, error) {
		<-ctx.Done()

		return nil, ctx.Err()
	}), nil, workflow.StepOptions{
		Timeout: 10 * time.Millisecond,
	})

	if err == nil || !errors.Is(workflow.DecodeError([]byte(err.Error())), workflow.ErrTimeout) {
		t.Fatalf("attempt error = %v; want timeout kind", err)
	}
}

// TestAttemptCallerCanceled verifies that caller cancellation is not
// misclassified as a step timeout.
func TestAttemptCallerCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := attempt(ctx, stepHandler(func(ctx context.Context, _ []byte) ([]byte, error) {
		return nil, context.Cause(ctx)
	}), nil, workflow.StepOptions{
		Timeout: time.Hour,
	})

	if err == nil || errors.Is(workflow.DecodeError([]byte(err.Error())), workflow.ErrTimeout) {
		t.Fatalf("attempt error = %v; want a non-timeout failure", err)
	}
}
