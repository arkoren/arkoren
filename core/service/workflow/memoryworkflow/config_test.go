package memoryworkflow_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/memoryworkflow"
)

// TestConstructionRetainedDraftCannotReplaceLogger verifies publication and
// registry preservation through a real in-process run, without real-time waits.
func TestConstructionRetainedDraftCannotReplaceLogger(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	w := workflow.New("logger-owner", func(ctx *workflow.Context, input string) (string, error) {
		ctx.Logger().InfoContext(context.Background(), "selected logger")

		return input, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(w)

	if err != nil {
		t.Fatal(err)
	}

	var retained *memoryworkflow.Builder
	driver := memoryworkflow.New(registry, func(driver *memoryworkflow.Builder) {
		retained = driver
		driver.Logger(slog.New(slog.NewTextHandler(&logs, nil)))
	})
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	retained.Logger(slog.New(slog.DiscardHandler))

	run, err := workflow.NewClient(driver).Start(t.Context(), w, "run-1", "input")

	if err != nil {
		t.Fatal(err)
	}

	if _, err := run.Result(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(logs.String(), "selected logger") {
		t.Fatalf("selected logger lost: %s", logs.String())
	}
}

// TestConstructionPoliciesRunOnceInOrder checks required registry placement,
// callback order, fresh drafts and Logger receiver identity without replaying.
func TestConstructionPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *memoryworkflow.Builder
	calls := 0
	driver := memoryworkflow.New(nil, func(driver *memoryworkflow.Builder) {
		retained = driver
		calls++

		if driver.Logger(slog.New(slog.DiscardHandler)) != driver {
			t.Fatal("Logger changed receiver")
		}
	}, func(driver *memoryworkflow.Builder) {
		calls++

		if driver != retained {
			t.Fatal("callbacks received different drafts")
		}

		driver.Logger(nil)
	})
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	retained.Logger(nil)

	if calls != 2 {
		t.Fatalf("callbacks=%d", calls)
	}

	memoryworkflow.New(nil, func(driver *memoryworkflow.Builder) {
		if driver == retained {
			t.Fatal("construction reused a draft")
		}
	})
}

// TestConstructionEmptyPolicies preserves empty-registry and default-logger
// behavior for omitted callbacks and expanded nil or empty lists.
func TestConstructionEmptyPolicies(t *testing.T) {
	t.Parallel()

	var policies []func(*memoryworkflow.Builder)
	memoryworkflow.New(nil, policies...)
	memoryworkflow.New(nil, []func(*memoryworkflow.Builder){}...)
	memoryworkflow.New(nil, func(driver *memoryworkflow.Builder) {
	})
}

// TestConstructionNilPolicyPanics proves nil policies are not ignored or
// interpreted as nil registry input, which retains its separate valid behavior.
func TestConstructionNilPolicyPanics(t *testing.T) {
	t.Parallel()

	calls := 0
	defer func() {
		if recover() == nil || calls != 1 {
			t.Fatalf("nil policy did not interrupt in order: calls=%d", calls)
		}
	}()
	memoryworkflow.New(nil, func(driver *memoryworkflow.Builder) {
		calls++
	}, nil, func(driver *memoryworkflow.Builder) {
		calls++
	})
}

// TestConstructionApplicationPanicPropagates keeps construction outside the
// workflow execution recovery boundary and preserves the exact panic value.
func TestConstructionApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	failure := "construction policy"
	defer func() {
		if recover() != failure {
			t.Fatal("application panic was replaced")
		}
	}()
	memoryworkflow.New(nil, func(driver *memoryworkflow.Builder) {
		panic(failure)
	})
}
