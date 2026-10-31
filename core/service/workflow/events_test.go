package workflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/workflowtest"
)

// countingCodec shares its call counter with the test. JSON encoding remains
// the wire representation, so registry compatibility is unchanged.
type countingCodec struct {
	// JSON supplies the unmodified decoding implementation.
	workflow.JSON
	// calls counts marshaling requests, not constructor invocations.
	calls int
}

// Marshal records use and delegates encoding to JSON. Construction must not
// call it; retained service objects are intentionally not recursively cloned.
//
//	codec := &countingCodec{}
//	data, err := codec.Marshal("input")
//	if err != nil {
//		return
//	}
//	_ = data
func (codec *countingCodec) Marshal(value any) ([]byte, error) {
	codec.calls++

	return codec.JSON.Marshal(value)
}

// collect listens to topic on bus and returns a pointer to the payloads
// received so far.
func collect[T any](t *testing.T, bus *event.Bus, topic event.Topic[T]) *[]T {
	t.Helper()

	var got []T

	if _, err := bus.Listen(topic, func(_ context.Context, payload T) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return &got
}

// TestClientPublishesRunStarted checks that successful submission announces
// only the workflow and run identities, not the encoded input.
func TestClientPublishesRunStarted(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := collect(t, bus, workflow.RunStarted)
	w := workflow.New("evt-start", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	client := workflow.NewClient(workflowtest.New(), func(client *workflow.ClientBuilder) {
		client.Events(bus)
	})

	if _, err := client.Start(t.Context(), w, "run-1", "secret-input"); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 1 || (*got)[0] != (workflow.RunEvent{
		Workflow: "evt-start",
		RunID:    "run-1",
	}) {
		t.Fatalf("RunStarted = %+v", *got)
	}
}

// TestClientPublishesNoRunStartedWhenStartFails checks that duplicate
// submission does not publish another successful-start event.
func TestClientPublishesNoRunStartedWhenStartFails(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := collect(t, bus, workflow.RunStarted)
	driver := workflowtest.New()
	w := workflow.New("evt-fail", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
		client.Events(bus)
	})

	if _, err := client.Start(t.Context(), w, "run-1", "in"); err != nil {
		t.Fatal(err)
	}

	if _, err := client.Start(t.Context(), w, "run-1", "in"); !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("duplicate Start error = %v", err)
	}

	if len(*got) != 1 {
		t.Fatalf("RunStarted = %+v; want only the first start", *got)
	}
}

// TestClientPublishesRunSignaled checks the successful signal event's routing
// identities while ensuring its payload value is not included.
func TestClientPublishesRunSignaled(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := collect(t, bus, workflow.RunSignaled)
	w := workflow.New("evt-sig", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	note := workflow.NewSignal[string]("note")
	client := workflow.NewClient(workflowtest.New(), func(client *workflow.ClientBuilder) {
		client.Events(bus)
	})

	if _, err := client.Start(t.Context(), w, "run-1", "in"); err != nil {
		t.Fatal(err)
	}

	if err := client.Signal(t.Context(), w, "run-1", note, "secret-value"); err != nil {
		t.Fatal(err)
	}

	want := workflow.RunSignaledEvent{
		Workflow: "evt-sig",
		RunID:    "run-1",
		Signal:   "note",
	}

	if len(*got) != 1 || (*got)[0] != want {
		t.Fatalf("RunSignaled = %+v; want %+v", *got, want)
	}
}

// TestClientPublishesNoRunSignaledWhenSignalFails checks that a driver failure
// cannot announce signal delivery that did not succeed.
func TestClientPublishesNoRunSignaledWhenSignalFails(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := collect(t, bus, workflow.RunSignaled)
	driver := workflowtest.New()
	w := workflow.New("evt-sigfail", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	note := workflow.NewSignal[string]("note")
	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
		client.Events(bus)
	})

	driver.FailNext(errors.New("down"))

	if err := client.Signal(t.Context(), w, "run-1", note, "v"); err == nil {
		t.Fatal("Signal succeeded; want the driver error")
	}

	if len(*got) != 0 {
		t.Fatalf("RunSignaled = %+v; want none", *got)
	}
}

// TestClientWithoutBusPublishesNothing checks the full start/signal/cancel
// sequence without an explicit bus and observes no ambient publication.
func TestClientWithoutBusPublishesNothing(t *testing.T) {
	t.Parallel()

	bus := event.New()
	started := collect(t, bus, workflow.RunStarted)
	signaled := collect(t, bus, workflow.RunSignaled)
	canceled := collect(t, bus, workflow.RunCanceled)
	w := workflow.New("evt-nil", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	note := workflow.NewSignal[string]("note")
	client := workflow.NewClient(workflowtest.New())

	run, err := client.Start(t.Context(), w, "run-1", "in")

	if err != nil {
		t.Fatal(err)
	}

	if err := client.Signal(t.Context(), w, "run-1", note, "v"); err != nil {
		t.Fatal(err)
	}

	if err := run.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(*started)+len(*signaled)+len(*canceled) != 0 {
		t.Fatal("a client without an event bus published an event")
	}
}
