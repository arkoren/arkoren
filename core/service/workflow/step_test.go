package workflow_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
)

// TestRegistryNormalizesInfiniteBackoffFactor keeps accepted step policies
// finite and deterministic rather than overflowing or multiplying zero by Inf.
func TestRegistryNormalizesInfiniteBackoffFactor(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("retry", func(_ context.Context, input string) (string, error) {
		return input, nil
	}, func(step *workflow.StepBuilder) {
		step.Backoff(time.Second, math.Inf(1), 0)
	})

	registry, err := workflow.Handlers(step)

	if err != nil {
		t.Fatal(err)
	}

	handler, ok := registry.Step("retry")

	if !ok {
		t.Fatal("step was not registered")
	}

	if got := handler.Options.Backoff(2); got != time.Second {
		t.Fatalf("nonfinite factor did not select constant backoff: %s", got)
	}
}

// TestStepCallbacksAndDeriveDetachDrafts verifies inherited policies and errors
// are copied rather than reset, and neither retained draft can mutate a handle.
func TestStepCallbacksAndDeriveDetachDrafts(t *testing.T) {
	t.Parallel()

	var originalDraft, derivedDraft *workflow.StepBuilder
	calls := 0
	base := workflow.NewStep("draft", noopStep, func(step *workflow.StepBuilder) {
		originalDraft = step
		calls++

		if step.Retry(4) != step {
			t.Fatal("Retry returned a different draft")
		}

		if step.Backoff(2*time.Second, 3, time.Minute) != step {
			t.Fatal("Backoff returned a different draft")
		}
	}, func(step *workflow.StepBuilder) {
		if step != originalDraft || calls != 1 {
			t.Fatal("callbacks did not share one ordered draft")
		}

		calls++

		if step.Timeout(0) != step {
			t.Fatal("Timeout returned a different draft")
		}
	})

	derived := base.Derive(func(step *workflow.StepBuilder) {
		derivedDraft = step
		step.Retry(2)
	}, func(step *workflow.StepBuilder) {
		if step != derivedDraft || step == originalDraft {
			t.Fatal("Derive did not use its own shared draft")
		}
	})
	originalDraft.Retry(9)
	derivedDraft.Timeout(time.Hour)

	registry, err := workflow.Handlers(derived)

	if err != nil {
		t.Fatal(err)
	}

	handler, _ := registry.Step("draft")
	want := workflow.StepOptions{
		Attempts: 2,
		Initial:  2 * time.Second,
		Factor:   3,
		Max:      time.Minute,
	}

	if calls != 2 || handler.Options != want {
		t.Fatalf("calls = %d; derived options = %+v", calls, handler.Options)
	}

	baseRegistry, err := workflow.Handlers(base.Derive())

	if err != nil {
		t.Fatal(err)
	}

	baseHandler, _ := baseRegistry.Step("draft")

	if baseHandler.Options.Attempts != 4 || baseHandler.Options.Timeout != 0 {
		t.Fatalf("base changed: %+v", baseHandler.Options)
	}
}

// TestStepBackoffNormalizesNaNAndKeepsZeroCap preserves normalization at the
// builder boundary, including the explicit unlimited-cap and zero-delay values.
func TestStepBackoffNormalizesNaNAndKeepsZeroCap(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("backoff", noopStep, func(step *workflow.StepBuilder) {
		step.Backoff(0, math.NaN(), 0)
	})

	registry, err := workflow.Handlers(step)

	if err != nil {
		t.Fatal(err)
	}

	handler, _ := registry.Step("backoff")
	want := workflow.StepOptions{
		Attempts: 1,
		Factor:   1,
		Timeout:  time.Minute,
	}

	if handler.Options != want {
		t.Fatalf("backoff options = %+v", handler.Options)
	}
}

// TestStepBackoffRejectsNegativeCap reports invalid backoff construction before
// any runtime work, rather than treating a negative cap as unlimited.
func TestStepBackoffRejectsNegativeCap(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("backoff", noopStep, func(step *workflow.StepBuilder) {
		step.Backoff(time.Second, 2, -time.Second)
	})

	if _, err := workflow.Handlers(step); !errors.Is(err, workflow.ErrInvalidOptions) {
		t.Fatalf("negative cap = %v", err)
	}
}

// TestStepNilCallbackPanics requires NewStep to invoke supplied callbacks
// directly, rather than filtering nil entries or recovering application panics.
func TestStepNilCallbackPanics(t *testing.T) {
	t.Parallel()

	callbackMustPanic(t, func() {
		workflow.NewStep("step", noopStep, nil)
	})
}

// TestStepDeriveNilCallbackPanics applies the same callback contract to Derive.
func TestStepDeriveNilCallbackPanics(t *testing.T) {
	t.Parallel()

	callbackMustPanic(t, func() {
		workflow.NewStep("step", noopStep).Derive(nil)
	})
}

// TestRegisteredDerivedStepSnapshotsFunction proves deriving preserves the
// typed body and registration detaches it from later pointer assignment.
func TestRegisteredDerivedStepSnapshotsFunction(t *testing.T) {
	t.Parallel()

	base := workflow.NewStep("snapshot", func(_ context.Context, input string) (string, error) {
		return "original:" + input, nil
	})

	derived := base.Derive(func(step *workflow.StepBuilder) {
		*base = *workflow.NewStep("base-replacement", noopStep)
		step.Retry(3)
	})

	registry, err := workflow.Handlers(derived)

	if err != nil {
		t.Fatal(err)
	}

	*derived = *workflow.NewStep("derived-replacement", noopStep)
	handler, _ := registry.Step("snapshot")

	input, err := workflow.EncodeValue(nil, "input")

	if err != nil {
		t.Fatal(err)
	}

	output, err := handler.Handle(t.Context(), input)

	if err != nil {
		t.Fatal(err)
	}

	var result string

	if err := workflow.DecodeValue(nil, output, &result); err != nil {
		t.Fatal(err)
	}

	if result != "original:input" || handler.Options.Attempts != 3 {
		t.Fatalf("registered step changed: %q, %+v", result, handler.Options)
	}
}

// TestDeriveReturnsCopy checks that local step policy edits publish a distinct
// handle while preserving the base registration and shared step identity.
func TestDeriveReturnsCopy(t *testing.T) {
	base := workflow.NewStep("copy", noopStep)
	derived := base.Derive(func(step *workflow.StepBuilder) {
		step.Retry(3)
	})

	if base == derived {
		t.Fatal("setter must return a copy")
	}

	registry, err := workflow.Handlers(base)

	if err != nil {
		t.Fatalf("Handlers: %v", err)
	}

	if handler, _ := registry.Step("copy"); handler.Options.Attempts != 1 {
		t.Fatalf("base attempts = %d; the setter mutated the receiver", handler.Options.Attempts)
	}

	if derived.Name() != "copy" {
		t.Fatalf("derived name = %q", derived.Name())
	}
}

// TestStepOptions checks publication and retry-delay calculation for a custom
// step policy, including normalization of zero attempts to one execution.
func TestStepOptions(t *testing.T) {
	step := workflow.NewStep("opts", noopStep, func(step *workflow.StepBuilder) {
		step.Retry(4)
		step.Backoff(time.Second, 3, 10*time.Second)
		step.Timeout(5 * time.Second)
	})

	registry, _ := workflow.Handlers(step)
	options := registry.Steps()[0].Options

	if options.Attempts != 4 || options.Initial != time.Second || options.Factor != 3 ||
		options.Max != 10*time.Second || options.Timeout != 5*time.Second {
		t.Fatalf("options = %+v", options)
	}

	want := []time.Duration{time.Second, 3 * time.Second, 9 * time.Second, 10 * time.Second}

	for n, delay := range want {
		if got := options.Backoff(n + 1); got != delay {
			t.Errorf("Backoff(%d) = %v; want %v", n+1, got, delay)
		}
	}

	retry := workflow.NewStep("zero", noopStep, func(step *workflow.StepBuilder) {
		step.Retry(0)
	})

	registry, _ = workflow.Handlers(retry)

	if got := registry.Steps()[0].Options.Attempts; got != 1 {
		t.Fatalf("Retry(0) attempts = %d; want 1", got)
	}
}
