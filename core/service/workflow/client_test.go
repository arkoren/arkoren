package workflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/memoryworkflow"
	"arkoren.dev/core/service/workflow/workflowtest"
)

// TestClientConstructionSharesCodecButDetachesDraft verifies codec calls are
// deferred until Start and later retained-draft edits cannot replace the codec.
func TestClientConstructionSharesCodecButDetachesDraft(t *testing.T) {
	t.Parallel()

	codec := &countingCodec{}
	var retained *workflow.ClientBuilder
	client := workflow.NewClient(workflowtest.New(), func(client *workflow.ClientBuilder) {
		retained = client
		client.Codec(codec)
	})
	retained.Codec(nil)

	if codec.calls != 0 {
		t.Fatal("construction invoked codec")
	}

	w := workflow.New("codec-owner", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	if _, err := client.Start(t.Context(), w, "run-1", "input"); err != nil {
		t.Fatal(err)
	}

	if codec.calls != 1 {
		t.Fatalf("shared codec calls=%d", codec.calls)
	}
}

// TestClientConstructionFinalNilCodecRestoresJSON proves final defaulting uses
// the completed draft rather than an earlier setting, with ordinary envelopes.
func TestClientConstructionFinalNilCodecRestoresJSON(t *testing.T) {
	t.Parallel()

	codec := &countingCodec{}
	client := workflow.NewClient(workflowtest.New(), func(client *workflow.ClientBuilder) {
		client.Codec(codec)
	}, func(client *workflow.ClientBuilder) {
		client.Codec(nil)
	})

	w := workflow.New("codec-reset", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	if _, err := client.Start(t.Context(), w, "run-1", "input"); err != nil {
		t.Fatal(err)
	}

	if codec.calls != 0 {
		t.Fatal("final nil did not restore default codec")
	}
}

// TestClientConstructionPublishesDetachedSettings proves callback order and
// once-only execution, receiver identity, shared bus ownership and root isolation.
func TestClientConstructionPublishesDetachedSettings(t *testing.T) {
	t.Parallel()

	bus := event.New()
	got := collect(t, bus, workflow.RunStarted)
	var retained *workflow.ClientBuilder
	calls := 0
	client := workflow.NewClient(workflowtest.New(), func(client *workflow.ClientBuilder) {
		retained = client
		calls++

		if client.Codec(nil) != client {
			t.Fatal("Codec changed receiver")
		}

		if client.Events(nil) != client {
			t.Fatal("Events changed receiver")
		}
	}, func(client *workflow.ClientBuilder) {
		calls++

		if client != retained {
			t.Fatal("callbacks received different drafts")
		}

		client.Events(bus)
	})
	retained.Events(nil)

	w := workflow.New("constructed", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	if _, err := client.Start(t.Context(), w, "run-1", "input"); err != nil {
		t.Fatal(err)
	}

	if calls != 2 || len(*got) != 1 {
		t.Fatalf("callbacks=%d events=%v", calls, *got)
	}
}

// TestClientConstructionEmptyPoliciesUseFreshDrafts covers omitted and expanded
// nil/empty policy lists without invoking a driver or a workflow handler.
func TestClientConstructionEmptyPoliciesUseFreshDrafts(t *testing.T) {
	t.Parallel()

	var policies []func(*workflow.ClientBuilder)
	workflow.NewClient(nil, policies...)
	workflow.NewClient(nil, []func(*workflow.ClientBuilder){}...)
	var first *workflow.ClientBuilder
	workflow.NewClient(nil, func(client *workflow.ClientBuilder) {
		first = client
	})
	workflow.NewClient(nil, func(client *workflow.ClientBuilder) {
		if client == first {
			t.Fatal("construction reused a draft")
		}
	})
}

// TestClientConstructionNilPolicyPanics preserves direct ordered callback
// invocation rather than treating nil entries as an omitted optional edit.
func TestClientConstructionNilPolicyPanics(t *testing.T) {
	t.Parallel()

	calls := 0
	defer func() {
		if recover() == nil || calls != 1 {
			t.Fatalf("nil policy did not interrupt in order: calls=%d", calls)
		}
	}()
	workflow.NewClient(nil, func(client *workflow.ClientBuilder) {
		calls++
	}, nil, func(client *workflow.ClientBuilder) {
		calls++
	})
}

// TestClientConstructionApplicationPanicPropagates prevents the workflow replay
// recovery mechanism from swallowing construction-policy panics.
func TestClientConstructionApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	failure := "construction policy"
	defer func() {
		if recover() != failure {
			t.Fatal("application panic was replaced")
		}
	}()
	workflow.NewClient(nil, func(client *workflow.ClientBuilder) {
		panic(failure)
	})
}

// TestClientRejectsInvalidDeclarationBeforeDispatch keeps a sticky policy error
// meaningful even when the worker has a valid definition under the same name.
func TestClientRejectsInvalidDeclarationBeforeDispatch(t *testing.T) {
	t.Parallel()

	body := func(_ *workflow.Context, input string) (string, error) {
		return input, nil
	}

	valid := workflow.New("registered", body, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	invalid := workflow.New("registered", body, func(definition *workflow.Builder) {
		definition.MaxDuration(-time.Second)
	})

	registry, err := workflow.Handlers(valid)

	if err != nil {
		t.Fatal(err)
	}

	driver := memoryworkflow.New(registry)
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	if _, err := workflow.NewClient(driver).Start(t.Context(), invalid, "run", "input"); !errors.Is(err, workflow.ErrInvalidOptions) {
		t.Fatalf("invalid declaration reached dispatch: %v", err)
	}
}

// TestClient checks typed facade submission, start options, result decoding,
// signaling, driver access, and failed submission on one fake call history.
func TestClient(t *testing.T) {
	w := workflow.New("client-wf", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	sig := workflow.NewSignal[string]("note")
	driver := workflowtest.New()
	client := workflow.NewClient(driver)

	run, err := client.Start(t.Context(), w, "id-1", "in",
		workflow.Delay(time.Second), workflow.Metadata(map[string]string{"k": "v"}))

	if err != nil || run.ID() != "id-1" {
		t.Fatalf("Start = %v, %v", run, err)
	}

	call := driver.Started()[0]

	if call.Options.Delay != time.Second || call.Options.Metadata["k"] != "v" || string(call.Input) != `{"v":"in"}` {
		t.Fatalf("driver got options=%+v input=%s", call.Options, call.Input)
	}

	driver.Complete("client-wf", "id-1", "done")

	if got, err := run.Result(t.Context()); err != nil || got != "done" {
		t.Fatalf("Result = %q, %v", got, err)
	}

	if _, err := client.Start(t.Context(), w, "id-3", "in"); err != nil {
		t.Fatal(err)
	}

	if err := client.Signal(t.Context(), w, "id-3", sig, "hi"); err != nil {
		t.Fatalf("Signal = %v", err)
	}

	if signal := driver.Signaled()[0]; signal.Signal != "note" || string(signal.Value) != `{"v":"hi"}` {
		t.Fatalf("Signal = %q, %s", signal.Signal, signal.Value)
	}

	if _, err := client.Start(t.Context(), w, "", "in"); !errors.Is(err, workflow.ErrInvalidID) {
		t.Fatalf("empty id error = %v", err)
	}

	existing, err := client.Start(t.Context(), w, "id-1", "in")

	if !errors.Is(err, workflow.ErrAlreadyStarted) || existing == nil || existing.ID() != "id-1" {
		t.Fatalf("duplicate Start = %v, %v", existing, err)
	}

	driver.FailNext(errors.New("down"))

	if failed, err := client.Start(t.Context(), w, "id-2", "in"); err == nil || failed != nil {
		t.Fatalf("failed Start = %v, %v; want nil run and error", failed, err)
	}

	if client.Driver() != workflow.Driver(driver) {
		t.Fatal("Driver does not return the driver")
	}

	if got, ok := client.DriverAs[*workflowtest.Fake](); !ok || got != driver {
		t.Fatal("DriverAs did not find the driver")
	}
}
