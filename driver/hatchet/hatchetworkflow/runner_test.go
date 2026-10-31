package hatchetworkflow_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
	hatchethost "arkoren.dev/driver/hatchet"
	"arkoren.dev/driver/hatchet/hatchetworkflow"
)

// TestNewRunnerRegistersOnHost verifies that runner construction registers
// workflow and step handlers before startup and exposes its paired driver.
func TestNewRunnerRegistersOnHost(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("s", func(context.Context, string) (string, error) {
		return "", nil
	})

	flow := workflow.New("w", func(*workflow.Context, string) (string, error) {
		return "", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(1)
	})

	registry, err := workflow.Handlers(flow, step)

	if err != nil {
		t.Fatalf("Handlers() error = %v", err)
	}

	host := hatchethost.New()

	runner, err := hatchetworkflow.NewRunner(host, registry)

	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	if runner.Driver() == nil {
		t.Fatal("Driver() = nil")
	}

	if _, err := hatchetworkflow.NewRunner(host, nil); err == nil {
		t.Fatal("NewRunner(nil registry) error = nil")
	}
}

// TestNewRunnerRejectsUnlimitedStepTimeout preserves the core policy's meaning:
// an unlimited step cannot silently acquire an arbitrary native 24-hour limit.
func TestNewRunnerRejectsUnlimitedStepTimeout(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("unlimited", func(context.Context, string) (string, error) {
		return "done", nil
	}, func(step *workflow.StepBuilder) {
		step.Timeout(0)
	})

	registry, err := workflow.Handlers(step)

	if err != nil {
		t.Fatal(err)
	}

	_, err = hatchetworkflow.NewRunner(hatchethost.New(), registry)

	if !errors.Is(err, workflow.ErrUnsupported) {
		t.Fatalf("NewRunner = %v; want ErrUnsupported for unlimited native timeout", err)
	}
}

// TestNewRunnerRejectsOverflowingWorkflowDuration keeps a valid core duration
// from wrapping into a one-second native timeout when the margin is added.
func TestNewRunnerRejectsOverflowingWorkflowDuration(t *testing.T) {
	t.Parallel()

	flow := workflow.New("long", func(*workflow.Context, string) (string, error) {
		return "done", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Duration(math.MaxInt64))
	})

	registry, err := workflow.Handlers(flow)

	if err != nil {
		t.Fatal(err)
	}

	_, err = hatchetworkflow.NewRunner(hatchethost.New(), registry)

	if !errors.Is(err, workflow.ErrUnsupported) {
		t.Fatalf("NewRunner = %v; want ErrUnsupported for native duration overflow", err)
	}
}

// TestNewRunnerRejectsHostNoWorker verifies that a client-only host cannot
// accept a workflow runner registration.
func TestNewRunnerRejectsHostNoWorker(t *testing.T) {
	t.Parallel()

	registry, err := workflow.Handlers()

	if err != nil {
		t.Fatalf("Handlers() error = %v", err)
	}

	if _, err := hatchetworkflow.NewRunner(hatchethost.New(func(host *hatchethost.Builder) {
		host.NoWorker(true)
	}), registry); err == nil {
		t.Fatal("NewRunner() error = nil")
	}
}
