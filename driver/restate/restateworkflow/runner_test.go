package restateworkflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/restate"
)

// testRegistry builds a registry with two workflows and a step.
func testRegistry(t *testing.T) *workflow.Registry {
	t.Helper()

	one := workflow.New("one", func(*workflow.Context, string) (string, error) {
		return "", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
		definition.Retention(time.Hour)
		definition.Description("first")
	})
	two := workflow.New("two-2", func(*workflow.Context, string) (string, error) {
		return "", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	step := workflow.NewStep("a-step", func(context.Context, string) (string, error) {
		return "", nil
	})

	registry, err := workflow.Handlers(two, step, one)

	if err != nil {
		t.Fatal(err)
	}

	return registry
}

// TestNewRunner verifies that runner construction binds its service definitions
// before host startup and refuses registration afterwards.
func TestNewRunner(t *testing.T) {
	t.Parallel()

	host := restate.New(func(host *restate.Builder) {
		host.AllowInsecure(true)
	}, func(host *restate.Builder) {
		host.Addr("127.0.0.1:0")
	})

	runner, err := NewRunner(host, testRegistry(t), func(runner *RunnerBuilder) {
		runner.Prefix("acme")
	})

	if err != nil {
		t.Fatal(err)
	}

	want := []string{"AcmeWf_one", "AcmeWf_two-2"}

	if got := runner.Services(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Services = %v, want %v", got, want)
	}

	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	defer host.Stop(t.Context())

	if host.Addr() == nil {
		t.Fatal("host does not listen although services are bound")
	}

	if _, err := NewRunner(host, testRegistry(t)); !errors.Is(err, restate.ErrStarted) {
		t.Fatalf("NewRunner after Start = %v", err)
	}
}

// TestNewRunnerInvalid verifies that missing host, missing registry, and
// client-only hosts are rejected before service binding.
func TestNewRunnerInvalid(t *testing.T) {
	t.Parallel()

	if _, err := NewRunner(nil, testRegistry(t)); err == nil {
		t.Fatal("nil host accepted")
	}

	if _, err := NewRunner(restate.New(), nil); err == nil {
		t.Fatal("nil registry accepted")
	}

	if _, err := NewRunner(restate.New(func(host *restate.Builder) {
		host.ClientOnly(true)
	}), testRegistry(t)); !errors.Is(err, restate.ErrClientOnly) {
		t.Fatalf("NewRunner on a client-only host = %v", err)
	}
}

// TestDefinition verifies that generated native definitions expose Run and
// Signal handlers and preserve configured workflow retention.
func TestDefinition(t *testing.T) {
	t.Parallel()

	runner, err := NewRunner(restate.New(), testRegistry(t))

	if err != nil {
		t.Fatal(err)
	}

	handler, _ := runner.registry.Workflow("one")
	definition := runner.definition(handler)

	if definition.Name() != "ArkorenWf_one" {
		t.Fatalf("Name = %q", definition.Name())
	}

	if _, ok := definition.Handlers()[runHandler]; !ok {
		t.Fatal("no Run handler")
	}

	if _, ok := definition.Handlers()[signalHandler]; !ok {
		t.Fatal("no Signal handler")
	}

	options := definition.GetOptions()

	if options.WorkflowRetention == nil || *options.WorkflowRetention != time.Hour {
		t.Fatalf("WorkflowRetention = %v", options.WorkflowRetention)
	}

	other, _ := runner.registry.Workflow("two-2")

	if got := runner.definition(other).GetOptions().WorkflowRetention; got != nil {
		t.Fatalf("WorkflowRetention without Retention = %v", *got)
	}
}
