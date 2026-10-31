package engine_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/workflow"
)

// TestDerivedStepOverridesRegisteredPolicy runs both a derived and base step
// against one registration, proving per-call retry settings reach the runtime
// without changing the registered handler's default policy.
func TestDerivedStepOverridesRegisteredPolicy(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		failure := workflow.NewError("declined", "declined")
		charge := workflow.NewStep("charge", func(context.Context, string) (string, error) {
			calls.Add(1)

			return "", failure
		}, func(step *workflow.StepBuilder) {
			step.Retry(3)
			step.Backoff(0, 1, 0)
		})

		singleAttempt := charge.Derive(func(step *workflow.StepBuilder) {
			step.Retry(1)
		})

		checkout := workflow.New("checkout", func(workflowContext *workflow.Context, input string) (string, error) {
			if _, err := workflowContext.Step(singleAttempt, input); !errors.Is(err, failure) {
				return "", errors.New("derived step did not return its failure")
			}

			return workflowContext.Step(charge, input)
		}, func(definition *workflow.Builder) {
			definition.MaxDuration(time.Minute)
		})

		run, err := setup(t, checkout, charge).Start(t.Context(), checkout, "override", "")

		if err != nil {
			t.Fatal(err)
		}

		if _, err := run.Result(t.Context()); !errors.Is(err, failure) {
			t.Fatalf("Result = %v", err)
		}

		if calls.Load() != 4 {
			t.Fatalf("executions = %d; want one derived and three base attempts", calls.Load())
		}
	})
}

// TestBackoffThenFailure checks the actual attempt timestamps and final
// failure after the configured exponential retry policy is exhausted.
func TestBackoffThenFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts []time.Time

		step := workflow.NewStep("always-fails", func(context.Context, string) (string, error) {
			attempts = append(attempts, time.Now())

			return "", errors.New("nope")
		}, func(step *workflow.StepBuilder) {
			step.Retry(3)
			step.Backoff(time.Minute, 2, time.Hour)
			step.Timeout(0)
		})

		w := workflow.New("retrier", func(c *workflow.Context, in string) (string, error) {
			return c.Step(step, in)
		}, func(definition *workflow.Builder) {
			definition.MaxDuration(24 * time.Hour)
		})

		run, err := setup(t, w, step).Start(t.Context(), w, "r", "")

		if err != nil {
			t.Fatal(err)
		}

		if _, err := run.Result(t.Context()); err == nil || errors.Is(err, workflow.ErrTimeout) {
			t.Fatalf("Result error = %v; want step failure", err)
		}

		if len(attempts) != 3 || attempts[1].Sub(attempts[0]) != time.Minute || attempts[2].Sub(attempts[1]) != 2*time.Minute {
			t.Fatalf("attempts at %v; want 3 spaced 1m and 2m", attempts)
		}
	})
}
