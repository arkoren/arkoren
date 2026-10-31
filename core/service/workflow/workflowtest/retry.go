package workflowtest

import (
	"context"
	"errors"
	"sync"

	"arkoren.dev/core/service/workflow"
)

var (
	// immediateEntryCalls counts completed workflow entries by unique input.
	// A whole-workflow retry must not masquerade as retrying the affected step.
	immediateEntryCalls sync.Map

	// immediateStepCalls counts only actual executions of the affected handler,
	// including an execution incorrectly performed before ErrUnsupported.
	immediateStepCalls sync.Map

	// immediateOnceCalls counts the supported single-attempt control even when
	// the engine opts out of the separate multi-attempt zero-backoff policy.
	immediateOnceCalls sync.Map

	// immediateEntry is a single-attempt control before the affected policy.
	// Normal orchestration replay reuses its result rather than executing it again.
	immediateEntry = workflow.NewStep("conf-immediate-entry", func(_ context.Context, in string) (string, error) {
		counter(&immediateEntryCalls, in).Add(1)

		return in, nil
	})

	// immediateStep deliberately registers positive default backoff. The flow
	// derives zero initial backoff per call, so drivers must honor completed policy.
	immediateStep = workflow.NewStep("conf-immediate-step", func(_ context.Context, in string) (string, error) {
		if counter(&immediateStepCalls, in).Add(1) < 3 {
			return "", errors.New("retry this step")
		}

		return in + "-ok", nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
	})

	// immediateFlow invokes the affected step once. It does not implement a
	// retry loop in orchestration or request whole-workflow retries from the engine.
	immediateFlow = workflow.New("conf-immediate-retry", func(ctx *workflow.Context, in string) (string, error) {
		if _, err := ctx.Step(immediateEntry, in); err != nil {
			return "", err
		}

		step := immediateStep.Derive(func(step *workflow.StepBuilder) {
			step.Backoff(0, 1, 0)
		})

		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})

	// immediateOnceStep registers multiple attempts, deliberately differing
	// from the completed single-attempt policy selected by immediateOnceFlow.
	immediateOnceStep = workflow.NewStep("conf-immediate-once-step", func(_ context.Context, in string) (string, error) {
		counter(&immediateOnceCalls, in).Add(1)

		return in + "-once", nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
	})

	// immediateOnceFlow proves the opt-out is scoped to completed multi-attempt
	// policies, not all zero delays or the registered step's default attempts.
	immediateOnceFlow = workflow.New("conf-immediate-once", func(ctx *workflow.Context, in string) (string, error) {
		step := immediateOnceStep.Derive(func(step *workflow.StepBuilder) {
			step.Retry(1)
			step.Backoff(0, 1, 0)
		})

		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(maxDuration)
	})
)

// testImmediateRetries checks the completed derived policy rather than the
// registered positive default. The entry control distinguishes step retries
// from restarting the workflow; unsupported engines must execute no affected
// handler, even if they report the expected sentinel after doing work.
func testImmediateRetries(s *suite) {
	run, err := s.client.Start(s.ctx, immediateFlow, s.id, s.id)

	if err != nil {
		s.t.Fatalf("Start: %v", err)
	}

	got, err := run.Result(s.ctx)

	if calls := counter(&immediateEntryCalls, s.id).Load(); calls != 1 {
		s.t.Errorf("workflow entry step ran %d times; want 1 (not whole-workflow retries)", calls)
	}

	if s.opts.withoutImmediateStepRetries {
		if !errors.Is(err, workflow.ErrUnsupported) {
			s.t.Errorf("Result error = %v; want ErrUnsupported", err)
		}

		if calls := counter(&immediateStepCalls, s.id).Load(); calls != 0 {
			s.t.Errorf("affected step ran %d times; want 0", calls)
		}

		return
	}

	if err != nil || got != s.id+"-ok" {
		s.t.Fatalf("Result = %q, %v; want %s-ok", got, err, s.id)
	}

	if calls := counter(&immediateStepCalls, s.id).Load(); calls != 3 {
		s.t.Errorf("affected step ran %d times; want 3", calls)
	}
}

// testImmediateSingleAttempt remains required even with the immediate-retry
// opt-out. It checks actual per-call attempts rather than registered defaults.
func testImmediateSingleAttempt(s *suite) {
	run, err := s.client.Start(s.ctx, immediateOnceFlow, s.id, s.id)

	if err != nil {
		s.t.Fatalf("Start: %v", err)
	}

	got, err := run.Result(s.ctx)

	if err != nil || got != s.id+"-once" {
		s.t.Fatalf("Result = %q, %v; want %s-once", got, err, s.id)
	}

	if calls := counter(&immediateOnceCalls, s.id).Load(); calls != 1 {
		s.t.Errorf("single-attempt step ran %d times; want 1", calls)
	}
}
