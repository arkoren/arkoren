package workflow_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/memoryworkflow"
)

// TestChildRejectsInvalidDeclarationBeforeDispatch uses a valid worker handler
// with the same name to prove local declaration errors cannot be bypassed.
func TestChildRejectsInvalidDeclarationBeforeDispatch(t *testing.T) {
	t.Parallel()

	body := func(_ *workflow.Context, input string) (string, error) {
		return input, nil
	}

	child := workflow.New("child", body, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	invalid := workflow.New("child", body, func(definition *workflow.Builder) {
		definition.Retention(-time.Second)
	})

	parent := workflow.New("parent", func(ctx *workflow.Context, input string) (string, error) {
		if _, err := ctx.Child(invalid, "child-run", input); !errors.Is(err, workflow.ErrInvalidOptions) {
			return "invalid child was dispatched", nil
		}

		return "rejected", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(parent, child)

	if err != nil {
		t.Fatal(err)
	}

	driver := memoryworkflow.New(registry)
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	run, err := workflow.NewClient(driver).Start(t.Context(), parent, "parent-run", "input")

	if err != nil {
		t.Fatal(err)
	}

	if result, err := run.Result(t.Context()); err != nil || result != "rejected" {
		t.Fatalf("child declaration validation: %q, %v", result, err)
	}
}

// TestSpawnRejectsInvalidDeclarationBeforeDispatch preserves the same sticky
// declaration-error boundary for detached work as for awaited children.
func TestSpawnRejectsInvalidDeclarationBeforeDispatch(t *testing.T) {
	t.Parallel()

	body := func(_ *workflow.Context, input string) (string, error) {
		return input, nil
	}

	child := workflow.New("spawned", body, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	invalid := workflow.New("spawned", body, func(definition *workflow.Builder) {
		definition.Retention(-time.Second)
	})

	parent := workflow.New("spawner", func(ctx *workflow.Context, input string) (string, error) {
		if _, err := ctx.Spawn(invalid, "spawn-run", input); !errors.Is(err, workflow.ErrInvalidOptions) {
			return "invalid spawn was dispatched", nil
		}

		return "rejected", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(parent, child)

	if err != nil {
		t.Fatal(err)
	}

	driver := memoryworkflow.New(registry)
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	run, err := workflow.NewClient(driver).Start(t.Context(), parent, "parent-run", "input")

	if err != nil {
		t.Fatal(err)
	}

	if result, err := run.Result(t.Context()); err != nil || result != "rejected" {
		t.Fatalf("spawn declaration validation: %q, %v", result, err)
	}
}

// TestWaitResolvesOnce checks that repeated facade waits reuse the first
// signal value even when the runtime would return a different value later.
func TestWaitResolvesOnce(t *testing.T) {
	sig := workflow.NewSignal[int]("num")
	first, _ := workflow.EncodeValue(nil, 7)
	rt := &stubRuntime{
		values: map[string][]byte{"num": first},
	}

	got, err := contextOf(t, rt, func(c *workflow.Context) (string, error) {
		a, err := c.Wait(sig)

		if err != nil {
			return "", err
		}

		b, err := c.Wait(sig)

		if err != nil {
			return "", err
		}

		if c.ID() != "id" || c.Name() != "name" || c.UUID() != "uuid" || c.Raw() != "raw" || c.Err() != nil {
			t.Error("Context accessors do not delegate")
		}

		return strings.Repeat("x", a) + "|" + strings.Repeat("x", b), nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if want := "xxxxxxx|xxxxxxx"; got != want {
		t.Fatalf("got %q; want %q", got, want)
	}

	if rt.waits != 1 {
		t.Fatalf("runtime Wait called %d times; want 1", rt.waits)
	}
}

// TestWaitTimeoutError checks that the facade preserves timeout identity when
// the runtime has no scripted value for the requested signal.
func TestWaitTimeoutError(t *testing.T) {
	sig := workflow.NewSignal[int]("none")
	rt := &stubRuntime{
		values: map[string][]byte{},
	}

	_, err := contextOf(t, rt, func(c *workflow.Context) (string, error) {
		_, err := c.Wait(sig, workflow.Timeout(time.Second))

		return "", err
	})

	if !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("error = %v; want ErrTimeout", err)
	}
}

// TestSelectFeedsMemo checks that a selected signal is remembered for both
// later Wait and Select calls without another runtime receive.
func TestSelectFeedsMemo(t *testing.T) {
	sig := workflow.NewSignal[int]("num")
	value, _ := workflow.EncodeValue(nil, 3)
	rt := &stubRuntime{
		values: map[string][]byte{"num": value},
	}

	got, err := contextOf(t, rt, func(c *workflow.Context) (string, error) {
		var fromSelect int

		err := c.Select(workflow.On(sig, func(value int) {
			fromSelect = value
		}), workflow.After(time.Hour, func() {
		}))

		if err != nil {
			return "", err
		}

		// The memo now holds the value: Wait must not reach the runtime.
		waited, err := c.Wait(sig)

		if err != nil {
			return "", err
		}

		// Select with a remembered signal fires at once, before the timer.
		var again int

		err = c.Select(workflow.After(time.Hour, func() {
		}), workflow.On(sig, func(value int) {
			again = value
		}))

		return strings.Join([]string{strconv.Itoa(fromSelect), strconv.Itoa(waited), strconv.Itoa(again)}, ","), err
	})

	if err != nil {
		t.Fatal(err)
	}

	if got != "3,3,3" {
		t.Fatalf("got %q; want 3,3,3", got)
	}

	if rt.waits != 0 || rt.selects != 1 {
		t.Fatalf("runtime waits=%d selects=%d; want 0 and 1", rt.waits, rt.selects)
	}
}

// TestSelectTimerAndEmpty checks a scripted timer winner and the facade's
// rejection of a subsequent Select with no cases.
func TestSelectTimerAndEmpty(t *testing.T) {
	rt := &stubRuntime{
		pick:   1,
		values: map[string][]byte{},
	}
	sig := workflow.NewSignal[int]("num")

	got, err := contextOf(t, rt, func(c *workflow.Context) (string, error) {
		fired := "none"

		err := c.Select(workflow.On(sig, func(int) {
			fired = "signal"
		}), workflow.After(time.Second, func() {
			fired = "timer"
		}))

		return fired, err
	})

	if err != nil || got != "timer" {
		t.Fatalf("got %q, %v; want timer", got, err)
	}

	_, err = contextOf(t, rt, func(workflowContext *workflow.Context) (string, error) {
		return "", workflowContext.Select()
	})

	if !errors.Is(err, workflow.ErrNoCases) {
		t.Fatalf("empty Select error = %v; want ErrNoCases", err)
	}
}
