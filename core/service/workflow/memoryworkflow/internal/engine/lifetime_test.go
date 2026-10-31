package engine_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/memoryworkflow"
)

// TestStopWaitsForCanceledStepAttempt keeps successful shutdown distinct from
// cancellation of the supervisor while a noncooperative handler still runs.
func TestStopWaitsForCanceledStepAttempt(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		step := workflow.NewStep("blocked", func(context.Context, string) (string, error) {
			close(started)
			<-release

			return "late", nil
		})

		definition := workflow.New("blocked-run", func(ctx *workflow.Context, input string) (string, error) {
			return ctx.Step(step, input)
		}, func(definition *workflow.Builder) {
			definition.MaxDuration(time.Hour)
		})

		registry, err := workflow.Handlers(definition, step)

		if err != nil {
			t.Fatal(err)
		}

		driver := memoryworkflow.New(registry)

		if _, err := workflow.NewClient(driver).Start(t.Context(), definition, "run", "input"); err != nil {
			t.Fatal(err)
		}

		<-started

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		t.Cleanup(cancel)

		err = driver.Stop(ctx)
		close(release)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Stop reported success while the attempt was running: %v", err)
		}

		if err := driver.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestCancelDoesNotAcceptLateSuccessfulReturn exercises the completion race
// repeatedly: every handler returns nil only after observing run cancellation.
func TestCancelDoesNotAcceptLateSuccessfulReturn(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	definition := workflow.New("late-success", func(ctx *workflow.Context, input string) (string, error) {
		started <- struct{}{}
		<-ctx.Done()

		return input, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	registry, err := workflow.Handlers(definition)

	if err != nil {
		t.Fatal(err)
	}

	driver := memoryworkflow.New(registry)
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	client := workflow.NewClient(driver)

	for attempt := range 128 {
		run, err := client.Start(t.Context(), definition, fmt.Sprintf("run-%d", attempt), "late")

		if err != nil {
			t.Fatal(err)
		}

		<-started

		if err := run.Cancel(t.Context()); err != nil {
			t.Fatal(err)
		}

		if _, err := run.Result(t.Context()); !errors.Is(err, workflow.ErrCanceled) {
			t.Fatalf("successful return after observed cancellation: %v", err)
		}
	}
}

// TestTimeoutDoesNotAcceptLateSuccessfulReturn verifies the same terminal
// arbitration when MaxDuration, rather than an explicit cancel, closes Done.
func TestTimeoutDoesNotAcceptLateSuccessfulReturn(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		definition := workflow.New("late-timeout", func(ctx *workflow.Context, input string) (string, error) {
			<-ctx.Done()

			return input, nil
		}, func(definition *workflow.Builder) {
			definition.MaxDuration(time.Second)
		})

		registry, err := workflow.Handlers(definition)

		if err != nil {
			t.Fatal(err)
		}

		driver := memoryworkflow.New(registry)

		run, err := workflow.NewClient(driver).Start(t.Context(), definition, "run", "late")

		if err != nil {
			t.Fatal(err)
		}

		if _, err := run.Result(t.Context()); !errors.Is(err, workflow.ErrTimeout) {
			t.Fatalf("successful return after timeout: %v", err)
		}

		if err := driver.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestMaxDuration checks that the run-wide bound interrupts a longer workflow
// sleep and reports the workflow timeout sentinel.
func TestMaxDuration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workflow.New("long", func(c *workflow.Context, _ string) (string, error) {
			return "", c.Sleep(48 * time.Hour)
		}, func(definition *workflow.Builder) {
			definition.MaxDuration(time.Hour)
		})

		run, err := setup(t, w).Start(t.Context(), w, "r", "")

		if err != nil {
			t.Fatal(err)
		}

		if _, err := run.Result(t.Context()); !errors.Is(err, workflow.ErrTimeout) {
			t.Fatalf("Result error = %v; want ErrTimeout", err)
		}
	})
}

// TestDelay checks that a delayed run is Running before its body begins and
// completes only after the requested initial delay has elapsed.
func TestDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workflow.New("delayed", func(*workflow.Context, string) (string, error) {
			return "ran", nil
		}, func(definition *workflow.Builder) {
			definition.MaxDuration(time.Hour)
		})

		started := time.Now()

		run, err := setup(t, w).Start(t.Context(), w, "r", "", workflow.Delay(time.Minute))

		if err != nil {
			t.Fatal(err)
		}

		if status, _ := run.Status(t.Context()); status != workflow.Running {
			t.Fatalf("Status = %v; want running", status)
		}

		if _, err := run.Result(t.Context()); err != nil {
			t.Fatal(err)
		}

		if elapsed := time.Since(started); elapsed != time.Minute {
			t.Fatalf("run finished after %v; want 1m", elapsed)
		}
	})
}

// TestStop checks that driver shutdown cancels a blocked run and rejects later
// submissions without requiring another lifecycle transition.
func TestStop(t *testing.T) {
	sig := workflow.NewSignal[string]("never")
	w := workflow.New("blocked", func(c *workflow.Context, _ string) (string, error) {
		return c.Wait(sig)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	registry, err := workflow.Handlers(w)

	if err != nil {
		t.Fatal(err)
	}

	driver := memoryworkflow.New(registry)
	client := workflow.NewClient(driver)

	run, err := client.Start(t.Context(), w, "r", "")

	if err != nil {
		t.Fatal(err)
	}

	if err := driver.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := run.Result(t.Context()); !errors.Is(err, workflow.ErrCanceled) {
		t.Fatalf("Result error = %v; want ErrCanceled", err)
	}

	if _, err := client.Start(t.Context(), w, "r2", ""); !errors.Is(err, memoryworkflow.ErrStopped) {
		t.Fatalf("Start after Stop error = %v; want ErrStopped", err)
	}
}
