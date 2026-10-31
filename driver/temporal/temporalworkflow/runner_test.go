package temporalworkflow

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"arkoren.dev/core/service/workflow"
)

// env builds a test environment with the workflows and steps of defs
// registered by a runner.
func env(t *testing.T, defs ...workflow.Definition) (*testsuite.TestWorkflowEnvironment, *Runner) {
	t.Helper()

	registry, err := workflow.Handlers(defs...)

	if err != nil {
		t.Fatal(err)
	}

	environment := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	runner := newRunner(registry, nil, driverSettings())
	runner.register(environment)

	return environment, runner
}

// run executes workflow name with in and returns the decoded output.
func run[Out any](t *testing.T, environment *testsuite.TestWorkflowEnvironment, name string, in any) (Out, error) {
	t.Helper()

	raw, err := workflow.EncodeValue(nil, in)

	if err != nil {
		t.Fatal(err)
	}

	environment.ExecuteWorkflow("arkoren-wf-"+name, payload(raw))

	if !environment.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}

	var out Out

	var result payload

	if err := environment.GetWorkflowResult(&result); err != nil {
		return out, fromTemporal(err)
	}

	if err := workflow.DecodeValue(nil, result, &out); err != nil {
		t.Fatal(err)
	}

	return out, nil
}

// TestSleepAndNow verifies that durable Sleep advances the workflow clock
// observed by Now by the requested duration.
func TestSleepAndNow(t *testing.T) {
	t.Parallel()

	w := workflow.New("sleeper", func(c *workflow.Context, _ string) (int64, error) {
		started := c.Now()
		err := c.Sleep(time.Hour)

		return int64(c.Now().Sub(started)), err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(24 * time.Hour)
	})

	environment, _ := env(t, w)

	got, err := run[int64](t, environment, "sleeper", "")

	if err != nil || time.Duration(got) < time.Hour {
		t.Fatalf("slept %v, %v", got, err)
	}
}

// waiterDefinition waits for a "ping" signal for a minute and returns its
// value, or "timeout" when none arrives.
func waiterDefinition() workflow.Definition {
	sig := workflow.NewSignal[string]("ping")

	return workflow.New("waiter", func(c *workflow.Context, _ string) (string, error) {
		value, err := c.Wait(sig, workflow.Timeout(time.Minute))

		if errors.Is(err, workflow.ErrTimeout) {
			return "timeout", nil
		}

		return value, err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
}

// TestWaitSignal verifies that a durable wait resolves the payload delivered by
// the named native signal.
func TestWaitSignal(t *testing.T) {
	t.Parallel()

	environment, runner := env(t, waiterDefinition())
	raw, _ := workflow.EncodeValue(nil, "hi")
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(runner.config.signalName("ping"), payload(raw))
	}, time.Second)

	if got, err := run[string](t, environment, "waiter", ""); err != nil || got != "hi" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// TestWaitTimeout verifies that an unanswered durable signal wait returns the
// timeout sentinel.
func TestWaitTimeout(t *testing.T) {
	t.Parallel()

	environment, _ := env(t, waiterDefinition())

	if got, err := run[string](t, environment, "waiter", ""); err != nil || got != "timeout" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// selectorDefinition selects between a "pick" signal and a one minute timer
// and returns which one won.
func selectorDefinition() workflow.Definition {
	sig := workflow.NewSignal[string]("pick")

	return workflow.New("selector", func(c *workflow.Context, _ string) (string, error) {
		var winner string

		err := c.Select(
			workflow.On(sig, func(value string) {
				winner = "signal:" + value
			}),
			workflow.After(time.Minute, func() {
				winner = "timer"
			}),
		)

		return winner, err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
}

// TestSelectSignalWins verifies that an arriving signal resolves selection
// before its competing timer.
func TestSelectSignalWins(t *testing.T) {
	t.Parallel()

	environment, runner := env(t, selectorDefinition())
	raw, _ := workflow.EncodeValue(nil, "x")
	environment.RegisterDelayedCallback(func() {
		environment.SignalWorkflow(runner.config.signalName("pick"), payload(raw))
	}, time.Second)

	if got, err := run[string](t, environment, "selector", ""); err != nil || got != "signal:x" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// TestSelectTimerWins verifies that selection resolves the timer when no
// competing signal arrives first.
func TestSelectTimerWins(t *testing.T) {
	t.Parallel()

	environment, _ := env(t, selectorDefinition())

	if got, err := run[string](t, environment, "selector", ""); err != nil || got != "timer" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// TestStepRetry verifies that retryable step failures rerun until the
// configured successful attempt.
func TestStepRetry(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	step := workflow.NewStep("flaky", func(_ context.Context, in string) (string, error) {
		if calls.Add(1) < 3 {
			return "", errors.New("flaky")
		}

		return in + "-ok", nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(3)
		step.Backoff(time.Second, 2, time.Minute)
	})

	w := workflow.New("retrier", func(c *workflow.Context, in string) (string, error) {
		return c.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	environment, _ := env(t, w, step)

	if got, err := run[string](t, environment, "retrier", "job"); err != nil || got != "job-ok" || calls.Load() != 3 {
		t.Fatalf("got %q, %v after %d calls", got, err, calls.Load())
	}
}

// TestStepTerminal verifies that a permanent step failure retains its kind and
// prevents another activity attempt.
func TestStepTerminal(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	step := workflow.NewStep("fatal", func(context.Context, string) (string, error) {
		calls.Add(1)

		return "", workflow.Permanent(workflow.NewError("custom", "do not retry"))
	}, func(step *workflow.StepBuilder) {
		step.Retry(5)
	})

	w := workflow.New("terminator", func(c *workflow.Context, in string) (string, error) {
		return c.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	environment, _ := env(t, w, step)

	_, err := run[string](t, environment, "terminator", "job")

	if !errors.Is(err, workflow.NewError("custom", "")) || !strings.Contains(err.Error(), "do not retry") {
		t.Fatalf("error = %v", err)
	}

	if calls.Load() != 1 {
		t.Fatalf("step ran %d times; want 1", calls.Load())
	}
}

// TestStepTimeout verifies that a blocked step maps its native activity timeout
// to the workflow timeout sentinel.
func TestStepTimeout(t *testing.T) {
	t.Parallel()

	step := workflow.NewStep("slow", func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()

		return "", ctx.Err()
	}, func(step *workflow.StepBuilder) {
		step.Timeout(50 * time.Millisecond)
	})

	w := workflow.New("timeouter", func(c *workflow.Context, in string) (string, error) {
		return c.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	environment, _ := env(t, w, step)

	if _, err := run[string](t, environment, "timeouter", "x"); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("error = %v; want ErrTimeout", err)
	}
}

// TestStepCallOverride verifies that per-call step settings override the
// published retry policy without changing the definition.
func TestStepCallOverride(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	step := workflow.NewStep("override", func(context.Context, string) (string, error) {
		calls.Add(1)

		return "", errors.New("always")
	})

	w := workflow.New("overrider", func(c *workflow.Context, in string) (string, error) {
		override := step.Derive(func(step *workflow.StepBuilder) {
			step.Retry(2)
			step.Backoff(time.Millisecond, 1, time.Millisecond)
		})

		return c.Step(override, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	environment, _ := env(t, w, step)

	if _, err := run[string](t, environment, "overrider", "x"); err == nil || calls.Load() != 2 {
		t.Fatalf("error = %v after %d calls; want failure after 2", err, calls.Load())
	}
}

// TestChildAndUUID verifies that child results and deterministic UUIDs remain
// available through the workflow runtime.
func TestChildAndUUID(t *testing.T) {
	t.Parallel()

	child := workflow.New("kid", func(_ *workflow.Context, in int) (int, error) {
		return in * 10, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	parent := workflow.New("dad", func(c *workflow.Context, in int) (string, error) {
		got, err := c.Child(child, c.ID()+"-kid", in)

		if err != nil {
			return "", err
		}

		return c.UUID() + "|" + string(rune('0'+got/10)), nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	environment, _ := env(t, parent, child)

	got, err := run[string](t, environment, "dad", 4)

	if err != nil || len(got) != 38 || !strings.HasSuffix(got, "|4") {
		t.Fatalf("got %q, %v", got, err)
	}
}

// TestUnknownChild verifies that a missing registered child definition fails
// with ErrNotFound before native execution.
func TestUnknownChild(t *testing.T) {
	t.Parallel()

	ghost := workflow.New("ghost", func(*workflow.Context, string) (string, error) {
		return "", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	parent := workflow.New("haunted", func(c *workflow.Context, in string) (string, error) {
		return c.Child(ghost, "g", in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	environment, _ := env(t, parent)

	if _, err := run[string](t, environment, "haunted", ""); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("error = %v; want ErrNotFound", err)
	}
}

// TestCancel verifies that checks that workflow cancellation closes Done and
// reaches the waiting handler.
func TestCancel(t *testing.T) {
	t.Parallel()

	sig := workflow.NewSignal[string]("never")

	var sawDone atomic.Bool

	w := workflow.New("cancellee", func(c *workflow.Context, _ string) (string, error) {
		_, err := c.Wait(sig)

		select {
		case <-c.Done():
			sawDone.Store(true)
		default:
		}

		if !errors.Is(c.Err(), workflow.ErrCanceled) {
			return "", errors.New("Err is not ErrCanceled")
		}

		return "", err
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	environment, _ := env(t, w)
	environment.RegisterDelayedCallback(environment.CancelWorkflow, time.Second)

	_, err := run[string](t, environment, "cancellee", "")

	if !errors.Is(err, workflow.ErrCanceled) {
		t.Fatalf("error = %v; want ErrCanceled", err)
	}

	if !sawDone.Load() {
		t.Fatal("Done was not closed")
	}
}

// TestMaxDurationInWorkflow verifies that a workflow waiting on an unanswered
// signal stops at its configured duration limit.
func TestMaxDurationInWorkflow(t *testing.T) {
	t.Parallel()

	sig := workflow.NewSignal[string]("never")
	w := workflow.New("limited", func(c *workflow.Context, _ string) (string, error) {
		return c.Wait(sig)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	environment, _ := env(t, w)

	if _, err := run[string](t, environment, "limited", ""); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("error = %v; want ErrTimeout", err)
	}
}

// TestPanicBecomesFailure verifies that an application panic becomes a workflow
// failure with its message retained.
func TestPanicBecomesFailure(t *testing.T) {
	t.Parallel()

	w := workflow.New("panicky", func(*workflow.Context, string) (string, error) {
		panic("boom")
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	environment, _ := env(t, w)

	if _, err := run[string](t, environment, "panicky", ""); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v; want the panic", err)
	}
}

// TestUUIDDeterministicAndDistinct verifies that successive UUID calls differ
// within a run but remain identical on replay.
func TestUUIDDeterministicAndDistinct(t *testing.T) {
	t.Parallel()

	w := workflow.New("uuids", func(c *workflow.Context, _ string) (string, error) {
		return c.UUID() + "," + c.UUID(), nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	first, err := run[string](t, func() *testsuite.TestWorkflowEnvironment {
		e, _ := env(t, w)

		return e
	}(), "uuids", "")

	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(first, ",")

	if len(parts) != 2 || parts[0] == parts[1] || len(parts[0]) != 36 {
		t.Fatalf("uuids = %q", first)
	}
}

// TestChildJoinsExistingRun verifies that a child identifier conflict joins the
// existing execution instead of submitting duplicate work.
func TestChildJoinsExistingRun(t *testing.T) {
	t.Parallel()

	kid := workflow.New("kid", func(_ *workflow.Context, in int) (int, error) {
		return in, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	parent := workflow.New("dad", func(c *workflow.Context, _ int) (int, error) {
		return c.Child(kid, "existing", 1)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	registry, err := workflow.Handlers(parent, kid)

	if err != nil {
		t.Fatal(err)
	}

	joined, err := workflow.EncodeValue(nil, 7)

	if err != nil {
		t.Fatal(err)
	}

	environment := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	runner := newRunner(registry, &fakeBackend{
		resultOut: payload(joined),
	}, driverSettings())
	runner.register(environment)

	environment.OnWorkflow("arkoren-wf-kid", mock.Anything, mock.Anything).Return(nil, &temporal.ChildWorkflowExecutionAlreadyStartedError{})

	if got, err := run[int](t, environment, "dad", 0); err != nil || got != 7 {
		t.Fatalf("got %d, %v; want 7", got, err)
	}
}

// TestDelayExtendsTimeout verifies that submission delay is added to execution
// timeout so it does not consume handler duration.
func TestDelayExtendsTimeout(t *testing.T) {
	t.Parallel()

	w := workflow.New("later", func(*workflow.Context, string) (string, error) {
		return "", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	registry, err := workflow.Handlers(w)

	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeBackend{}
	driver := newTestDriver(t, fake, func(driver *DriverBuilder) {
		driver.Registry(registry)
	})

	if err := driver.Start(t.Context(), "later", "x", nil, workflow.StartOptions{
		Delay: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	if fake.started.timeout != time.Hour+time.Minute {
		t.Fatalf("timeout = %v; want 1h1m", fake.started.timeout)
	}
}

// TestTerminatedMapping verifies that native termination retains its distinct
// application error identity.
func TestTerminatedMapping(t *testing.T) {
	t.Parallel()

	err := fromTemporal(&temporal.TerminatedError{})

	if !errors.Is(err, errTerminated) {
		t.Fatalf("error = %v; want terminated", err)
	}
}
