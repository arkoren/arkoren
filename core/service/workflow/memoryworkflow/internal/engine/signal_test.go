package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/memoryworkflow"
)

// TestByteOwnershipQueuedSignal proves queued values detach from a caller's
// reused buffer. A separate workflow signal gates consumption until both sends
// and mutations finish. Repeated Wait still returns the first value, preserving
// signal memoization even when a second value was queued under the same name.
func TestByteOwnershipQueuedSignal(t *testing.T) {
	t.Parallel()

	release := workflow.NewSignal[bool]("release")
	payload := workflow.NewSignal[string]("payload")
	definition := workflow.New("owned-signal", func(ctx *workflow.Context, _ string) (string, error) {
		if _, err := ctx.Wait(release); err != nil {
			return "", err
		}

		first, err := ctx.Wait(payload)

		if err != nil {
			return "", err
		}

		repeated, err := ctx.Wait(payload)

		if err != nil {
			return "", err
		}

		return first + ":" + repeated, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(definition)

	if err != nil {
		t.Fatal(err)
	}

	driver := memoryworkflow.New(registry)
	defer func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	client := workflow.NewClient(driver)

	run, err := client.Start(t.Context(), definition, "run-1", "")

	if err != nil {
		t.Fatal(err)
	}

	value := []byte(`{"v":"original"}`)

	if err := client.Driver().Signal(t.Context(), definition.Name(), "run-1", payload.Name(), value); err != nil {
		t.Fatal(err)
	}

	copy(value, []byte(`{"v":"mutated!"}`))

	if err := client.Driver().Signal(t.Context(), definition.Name(), "run-1", payload.Name(), value); err != nil {
		t.Fatal(err)
	}

	copy(value, []byte(`{"v":"changed!"}`))

	if err := client.Signal(t.Context(), definition, "run-1", release, true); err != nil {
		t.Fatal(err)
	}

	output, err := run.Result(t.Context())

	if err != nil {
		t.Fatal(err)
	}

	if output != "original:original" {
		t.Fatalf("Result = %q; want original first signal on both waits", output)
	}
}

// TestSignalQueueFull checks bounded buffering for signals the workflow does
// not consume, without relying on the driver's private capacity constant.
func TestSignalQueueFull(t *testing.T) {
	sig := workflow.NewSignal[int]("n")
	w := workflow.New("queue", func(c *workflow.Context, _ string) (int, error) {
		return c.Wait(workflow.NewSignal[int]("other"))
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	client := setup(t, w)

	if _, err := client.Start(t.Context(), w, "r", ""); err != nil {
		t.Fatal(err)
	}

	var err error

	for i := 0; i < 2000 && err == nil; i++ {
		err = client.Signal(t.Context(), w, "r", sig, i)
	}

	if !errors.Is(err, memoryworkflow.ErrQueueFull) {
		t.Fatalf("Signal error = %v; want ErrQueueFull", err)
	}
}
