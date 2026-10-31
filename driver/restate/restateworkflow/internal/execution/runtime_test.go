package execution

import (
	"errors"
	"testing"

	"arkoren.dev/core/service/workflow"
)

// TestRuntimeGuard verifies that the runtime preserves SDK panics for replay
// and prevents operations after the execution has stopped.
func TestRuntimeGuard(t *testing.T) {
	t.Parallel()

	rt := &execution{
		done: make(chan struct{}),
	}

	if err := rt.guard(func() error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if rt.Repanic("x") {
		t.Fatal("Repanic before any SDK panic")
	}

	func() {
		defer func() {
			if recovered := recover(); recovered != "suspend" {
				t.Fatalf("recovered = %v; guard must raise the panic again", recovered)
			}
		}()

		_ = rt.guard(func() error {
			panic("suspend")
		}) //nolint:errcheck // panics
	}()

	if !rt.Repanic("suspend") {
		t.Fatal("Repanic = false after an SDK panic")
	}

	_ = rt.stop(workflow.ErrCanceled) //nolint:errcheck // checked through Err

	ran := false

	if err := rt.guard(func() error {
		ran = true

		return nil
	}); !errors.Is(err, workflow.ErrCanceled) || ran {
		t.Fatalf("guard after stop ran = %v, err = %v", ran, err)
	}
}

// TestRuntimeStopKeepsFirstError verifies that the first execution failure
// remains the reported cause after later stop attempts.
func TestRuntimeStopKeepsFirstError(t *testing.T) {
	t.Parallel()

	rt := &execution{
		done: make(chan struct{}),
	}

	first := rt.stop(workflow.ErrCanceled)
	second := rt.stop(workflow.ErrTimeout)

	if !errors.Is(first, workflow.ErrCanceled) || !errors.Is(second, workflow.ErrCanceled) || !errors.Is(rt.Err(), workflow.ErrCanceled) {
		t.Fatalf("errors = %v, %v, %v", first, second, rt.Err())
	}
}
