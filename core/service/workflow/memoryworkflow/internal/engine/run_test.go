package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/memoryworkflow"
)

// blockedWorkflowDecode pauses the worker before JSON decoding reads its input.
// Its wire format remains ordinary JSON, so the client needs no custom codec.
// Each instance supports exactly one worker decode and belongs to one test.
type blockedWorkflowDecode struct {
	// JSON supplies ordinary output encoding and the eventual input decoding.
	workflow.JSON

	// entered closes once the worker has received, but not decoded, its input.
	entered chan struct{}

	// release permits decoding after the test mutates the submission buffer.
	// Closing it also releases a worker when the test exits early.
	release chan struct{}
}

// Unmarshal announces receipt and waits before inspecting data. The envelope
// fast path passes its inner byte slice directly to the codec, so this barrier
// orders caller mutation before the worker reads the encoded value. JSON errors
// are returned unchanged after release; data and value are not retained.
//
//	codec := &blockedWorkflowDecode{
//		entered: make(chan struct{}),
//		release: make(chan struct{}),
//	}
//	close(codec.release)
//	var value string
//	if err := codec.Unmarshal([]byte(`"original"`), &value); err != nil {
//		return err
//	}
func (codec *blockedWorkflowDecode) Unmarshal(data []byte, value any) error {
	close(codec.entered)
	<-codec.release

	return codec.JSON.Unmarshal(data, value)
}

// TestByteOwnershipDelayedStartInput proves a delayed submission owns its raw
// input before Start returns. The codec barrier prevents decoding from racing
// caller mutation; the positive delay exercises asynchronous delayed starts
// without using a sleep or relying on the scheduler to order the mutation.
func TestByteOwnershipDelayedStartInput(t *testing.T) {
	t.Parallel()

	codec := &blockedWorkflowDecode{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	definition := workflow.New("owned-start", func(_ *workflow.Context, input string) (string, error) {
		return input, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.HandlersWith(codec, definition)

	if err != nil {
		t.Fatal(err)
	}

	driver := memoryworkflow.New(registry)
	defer func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	defer close(codec.release)
	client := workflow.NewClient(driver)
	input := []byte(`{"v":"original"}`)

	if err := client.Driver().Start(t.Context(), definition.Name(), "run-1", input, workflow.StartOptions{
		Delay: time.Nanosecond,
	}); err != nil {
		t.Fatal(err)
	}

	copy(input[len(`{"v":"`):], "mutated!")
	<-codec.entered
	codec.release <- struct{}{}

	output, err := client.Run(definition, "run-1").Result(t.Context())

	if err != nil {
		t.Fatal(err)
	}

	if output != "original" {
		t.Fatalf("Result = %q; want original submission", output)
	}
}

// TestByteOwnershipResultReread proves each raw result is a caller-owned
// snapshot. Changing one read must affect neither another raw read nor a later
// typed client read of the same retained run.
func TestByteOwnershipResultReread(t *testing.T) {
	t.Parallel()

	definition := workflow.New("owned-result", func(_ *workflow.Context, input string) (string, error) {
		return input, nil
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

	run, err := client.Start(t.Context(), definition, "run-1", "original")

	if err != nil {
		t.Fatal(err)
	}

	first, err := client.Driver().Result(t.Context(), definition.Name(), "run-1")

	if err != nil {
		t.Fatal(err)
	}

	copy(first, []byte(`{"v":"mutated!"}`))

	second, err := client.Driver().Result(t.Context(), definition.Name(), "run-1")

	if err != nil {
		t.Fatal(err)
	}

	if string(second) != `{"v":"original"}` {
		t.Fatalf("second Result = %s; want original encoded output", second)
	}

	output, err := run.Result(t.Context())

	if err != nil {
		t.Fatal(err)
	}

	if output != "original" {
		t.Fatalf("typed Result = %q; want original retained output", output)
	}
}

// TestStartContextCanceled checks that a canceled submission context is
// rejected before execution begins.
func TestStartContextCanceled(t *testing.T) {
	w := workflow.New("noop", func(*workflow.Context, string) (string, error) {
		return "", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	client := setup(t, w)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := client.Start(ctx, w, "r", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v; want context.Canceled", err)
	}
}

// TestUnknownWorkflow checks that an empty registry rejects submission with
// the contract's not-found sentinel rather than executing unregistered code.
func TestUnknownWorkflow(t *testing.T) {
	registry, _ := workflow.Handlers()
	driver := memoryworkflow.New(registry)

	err := driver.Start(t.Context(), "missing", "r", nil, workflow.StartOptions{})

	if !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Start error = %v; want ErrNotFound", err)
	}
}
