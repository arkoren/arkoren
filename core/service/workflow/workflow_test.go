package workflow_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
)

// noopWorkflow supplies a successful typed workflow body for declaration and
// client tests; it performs no runtime operations or external effects.
func noopWorkflow(*workflow.Context, string) (string, error) {
	return "", nil
}

// noopStep supplies a successful typed step body where only registration or
// policy publication is under test, not execution side effects.
func noopStep(context.Context, string) (string, error) {
	return "", nil
}

// TestWorkflowCallbacksPublishDetachedSettings covers callback ordering, shared
// draft identity, same-receiver settings, and isolation after publication.
func TestWorkflowCallbacksPublishDetachedSettings(t *testing.T) {
	t.Parallel()

	var retained *workflow.Builder
	calls := 0
	definition := workflow.New("draft", noopWorkflow, func(definition *workflow.Builder) {
		retained = definition
		calls++

		if definition.MaxDuration(time.Hour) != definition {
			t.Fatal("MaxDuration returned a different draft")
		}

		if definition.Retention(time.Hour) != definition {
			t.Fatal("Retention returned a different draft")
		}
	}, func(definition *workflow.Builder) {
		if definition != retained || calls != 1 {
			t.Fatal("callbacks did not run in order on one draft")
		}

		calls++

		if definition.Description("published") != definition {
			t.Fatal("Description returned a different draft")
		}

		definition.MaxDuration(time.Minute)
	})
	retained.MaxDuration(time.Second)
	retained.Retention(0)
	retained.Description("changed")

	registry, err := workflow.Handlers(definition)

	if err != nil {
		t.Fatal(err)
	}

	handler, _ := registry.Workflow("draft")
	want := workflow.WorkflowOptions{
		MaxDuration: time.Minute,
		Retention:   time.Hour,
		Description: "published",
	}

	if calls != 2 || handler.Options != want {
		t.Fatalf("calls = %d; options = %+v", calls, handler.Options)
	}
}

// TestDefinitionEmptyCallbackExpansion preserves omitted-setting validation and
// defaults when callers expand nil or empty callback slices.
func TestDefinitionEmptyCallbackExpansion(t *testing.T) {
	t.Parallel()

	var workflowCallbacks []func(*workflow.Builder)
	definition := workflow.New("empty", noopWorkflow, workflowCallbacks...)

	if definition.Err() != nil {
		t.Fatal(definition.Err())
	}

	if _, err := workflow.Handlers(definition); !errors.Is(err, workflow.ErrMissingMaxDuration) {
		t.Fatalf("missing duration = %v", err)
	}

	workflowCallbacks = []func(*workflow.Builder){}

	if _, err := workflow.Handlers(workflow.New("empty", noopWorkflow, workflowCallbacks...)); !errors.Is(err, workflow.ErrMissingMaxDuration) {
		t.Fatalf("empty callbacks = %v", err)
	}

	var stepCallbacks []func(*workflow.StepBuilder)
	base := workflow.NewStep("empty", noopStep, stepCallbacks...)
	stepCallbacks = []func(*workflow.StepBuilder){}

	registry, err := workflow.Handlers(base.Derive(stepCallbacks...))

	if err != nil {
		t.Fatal(err)
	}

	handler, _ := registry.Step("empty")
	want := workflow.StepOptions{
		Attempts: 1,
		Initial:  time.Second,
		Factor:   2,
		Max:      30 * time.Second,
		Timeout:  time.Minute,
	}

	if handler.Options != want {
		t.Fatalf("defaults = %+v", handler.Options)
	}
}

// TestDefinitionErrorsRemainSticky checks invalid declarations still call their
// supplied callbacks and subsequent settings or Derive cannot clear an error.
func TestDefinitionErrorsRemainSticky(t *testing.T) {
	t.Parallel()

	calls := 0
	definition := workflow.New[string, string]("Bad", nil, func(definition *workflow.Builder) {
		calls++
		definition.MaxDuration(0)
		definition.Retention(-time.Second)
	})

	if calls != 1 || !errors.Is(definition.Err(), workflow.ErrInvalidName) {
		t.Fatalf("calls = %d; error = %v", calls, definition.Err())
	}

	invalid := workflow.NewStep("bad", noopStep, func(step *workflow.StepBuilder) {
		step.Timeout(-time.Second)
		step.Backoff(-time.Second, 2, 0)
	})

	first := invalid.Err()
	derived := invalid.Derive(func(step *workflow.StepBuilder) {
		step.Timeout(time.Minute)
		step.Retry(2)
	})

	if !errors.Is(first, workflow.ErrInvalidOptions) || !errors.Is(derived.Err(), first) {
		t.Fatalf("error changed: %v -> %v", first, derived.Err())
	}
}

// TestWorkflowExplicitZeroDurationIsInvalid distinguishes an explicitly invalid
// setting from a missing required setting, which is rejected at registration.
func TestWorkflowExplicitZeroDurationIsInvalid(t *testing.T) {
	t.Parallel()

	definition := workflow.New("zero", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(0)
		definition.MaxDuration(time.Hour)
	})

	if _, err := workflow.Handlers(definition); !errors.Is(err, workflow.ErrInvalidOptions) {
		t.Fatalf("explicit zero duration = %v", err)
	}
}

// TestNilBodiesStillInvokeCallbacks checks the initial function error remains
// sticky while both constructors still execute the supplied configuration.
func TestNilBodiesStillInvokeCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	definition := workflow.New[string, string]("nil", nil, func(definition *workflow.Builder) {
		calls++
		definition.MaxDuration(time.Minute)
	})

	step := workflow.NewStep[string, string]("nil", nil, func(step *workflow.StepBuilder) {
		calls++
		step.Timeout(-time.Second)
	})

	if calls != 2 {
		t.Fatalf("callback calls = %d", calls)
	}

	if !errors.Is(definition.Err(), workflow.ErrNilFunc) || !errors.Is(step.Err(), workflow.ErrNilFunc) {
		t.Fatalf("nil body errors = %v, %v", definition.Err(), step.Err())
	}
}

// callbackMustPanic checks direct invocation without assuming the runtime's nil
// function panic type or message. The caller supplies only callback misuse.
func callbackMustPanic(t *testing.T, invoke func()) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Fatal("supplied nil callback did not panic")
		}
	}()
	invoke()
}

// TestWorkflowNilCallbackPanics distinguishes a supplied nil entry from an
// expanded nil slice, including declarations that already have an error.
func TestWorkflowNilCallbackPanics(t *testing.T) {
	t.Parallel()

	callbackMustPanic(t, func() {
		workflow.New("Bad", noopWorkflow, nil)
	})
}

// TestDefinitionCallbackPanicsPropagate preserves the application's original
// panic rather than treating it as a definition validation or runtime failure.
func TestDefinitionCallbackPanicsPropagate(t *testing.T) {
	t.Parallel()

	failure := errors.New("callback failed")

	mustPanic(t, failure, func() {
		workflow.New("flow", noopWorkflow, func(definition *workflow.Builder) {
			panic(failure)
		})
	})
	mustPanic(t, failure, func() {
		workflow.NewStep("step", noopStep, func(step *workflow.StepBuilder) {
			panic(failure)
		})
	})
	mustPanic(t, failure, func() {
		workflow.NewStep("step", noopStep).Derive(func(step *workflow.StepBuilder) {
			panic(failure)
		})
	})
}

// TestRegisteredWorkflowSnapshotsFunction replays a registered handler after
// assignment through its original pointer; identity, policy, and code stay fixed.
func TestRegisteredWorkflowSnapshotsFunction(t *testing.T) {
	t.Parallel()

	definition := workflow.New("snapshot", func(_ *workflow.Context, input string) (string, error) {
		return "original:" + input, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	registry, err := workflow.Handlers(definition)

	if err != nil {
		t.Fatal(err)
	}

	*definition = *workflow.New("replacement", noopWorkflow)
	handler, _ := registry.Workflow("snapshot")

	input, err := workflow.EncodeValue(nil, "input")

	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		output, err := handler.Handle(&stubRuntime{}, input)

		if err != nil {
			t.Fatal(err)
		}

		var result string

		if err := workflow.DecodeValue(nil, output, &result); err != nil {
			t.Fatal(err)
		}

		if result != "original:input" {
			t.Fatalf("registered function changed: %q", result)
		}
	}

	if handler.Name != "snapshot" || handler.Options.MaxDuration != time.Hour {
		t.Fatalf("registered metadata changed: %+v", handler)
	}
}

// mustPanic invokes fn and requires an error panic matching want through
// errors.Is. It fails the test for a normal return or a different panic value.
func mustPanic(t *testing.T, want error, fn func()) {
	t.Helper()

	defer func() {
		t.Helper()

		recovered := recover()

		err, ok := recovered.(error)

		if !ok || !errors.Is(err, want) {
			t.Fatalf("panic = %v; want %v", recovered, want)
		}
	}()

	fn()
}

// TestInvalidDefinitionsAreReportedNotPanicked checks declaration diagnostics
// and the separate panic contract of invalid signals and select cases.
func TestInvalidDefinitionsAreReportedNotPanicked(t *testing.T) {
	for _, name := range []string{"", "Upper", "under_score", "sp ace", "dot.name", "-edge", "edge-", strings.Repeat("a", 65)} {
		workflowDef := workflow.New(name, noopWorkflow, func(definition *workflow.Builder) {
			definition.MaxDuration(time.Minute)
		})

		stepDef := workflow.NewStep(name, noopStep)

		for _, def := range []workflow.Definition{workflowDef, stepDef} {
			if _, err := workflow.Handlers(def); !errors.Is(err, workflow.ErrInvalidName) {
				t.Errorf("Handlers(%q) error = %v; want ErrInvalidName", name, err)
			}
		}

		if !errors.Is(workflowDef.Err(), workflow.ErrInvalidName) || !errors.Is(stepDef.Err(), workflow.ErrInvalidName) {
			t.Errorf("Err(%q) = %v, %v; want ErrInvalidName", name, workflowDef.Err(), stepDef.Err())
		}

		mustPanic(t, workflow.ErrInvalidName, func() {
			workflow.NewSignal[string](name)
		})
	}

	nilWorkflow := workflow.New[string, string]("ok", nil, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	nilStep := workflow.NewStep[string, string]("ok", nil)

	for _, def := range []workflow.Definition{nilWorkflow, nilStep} {
		if _, err := workflow.Handlers(def); !errors.Is(err, workflow.ErrNilFunc) {
			t.Errorf("Handlers error = %v; want ErrNilFunc", err)
		}
	}

	mustPanic(t, workflow.ErrNilFunc, func() {
		workflow.On(workflow.NewSignal[string]("s"), nil)
	})
	mustPanic(t, workflow.ErrNilFunc, func() {
		workflow.After(time.Second, nil)
	})

	if err := workflow.New("bad-retention", noopWorkflow, func(definition *workflow.Builder) {
		definition.Retention(-1)
	}).Err(); !errors.Is(err, workflow.ErrInvalidOptions) {
		t.Errorf("negative retention error = %v; want ErrInvalidOptions", err)
	}

	first := workflow.New("Bad", noopWorkflow, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
		definition.Retention(-1)
	})

	if !errors.Is(first.Err(), workflow.ErrInvalidName) {
		t.Errorf("first error not kept: %v", first.Err())
	}
}

// TestNames checks that published workflow and signal identities preserve
// their valid caller-supplied names without normalization.
func TestNames(t *testing.T) {
	if got := workflow.New("a-1", noopWorkflow).Name(); got != "a-1" {
		t.Fatalf("Name = %q", got)
	}

	if got := workflow.NewSignal[int]("sig-2").Name(); got != "sig-2" {
		t.Fatalf("Signal Name = %q", got)
	}
}

// stubRuntime is a Runtime that serves scripted signals.
type stubRuntime struct {
	// waits counts runtime waits so tests can detect repeated signal resolution.
	waits int

	// values supplies encoded signal values keyed by signal name.
	values map[string][]byte

	// selects counts runtime selects separately from facade memo hits.
	selects int

	// pick selects the scripted case index returned by Select.
	pick int
}

// replayRuntime adds an engine control-flow panic hook to the scripted runtime.
// Its signal stands in for a driver's replay or yield instruction.
type replayRuntime struct {
	// stubRuntime supplies the ordinary runtime operations.
	stubRuntime
	// signal is the one panic that must cross the handler unchanged.
	signal error
}

// Repanic accepts only this runtime's engine control-flow signal. Other panics
// remain ordinary application failures handled by workflow protection.
//
// Example:
//
//	runtime := &replayRuntime{
//		signal: errors.New("yield"),
//	}
//	accepted := runtime.Repanic(runtime.signal)
func (runtime *replayRuntime) Repanic(value any) bool {
	err, ok := value.(error)

	return ok && errors.Is(err, runtime.signal)
}

// TestRegisteredWorkflowPreservesReplayPanic ensures declaration publication
// and function snapshotting do not convert an engine yield into an error.
func TestRegisteredWorkflowPreservesReplayPanic(t *testing.T) {
	t.Parallel()

	signal := errors.New("yield")
	definition := workflow.New("replay", func(*workflow.Context, string) (string, error) {
		panic(signal)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(definition)

	if err != nil {
		t.Fatal(err)
	}

	handler, _ := registry.Workflow("replay")

	mustPanic(t, signal, func() {
		_, err := handler.Handle(&replayRuntime{
			signal: signal,
		}, nil)
		t.Fatalf("engine panic became result: %v", err)
	})
}

// ID supplies a fixed run identity so facade delegation can be checked without
// starting an engine or introducing a source of randomness.
func (*stubRuntime) ID() string {
	return "id"
}

// Name supplies a fixed workflow identity distinct from the run identity.
// Accessor assertions detect accidental delegation to the wrong runtime method.
func (*stubRuntime) Name() string {
	return "name"
}

// Now supplies a deterministic instant for runtime-interface completeness.
// No wall-clock progression is needed by the scripted-signal tests.
func (*stubRuntime) Now() time.Time {
	return time.Unix(0, 0)
}

// UUID supplies a deterministic identifier distinct from ID so facade
// delegation can be verified without real random-number generation.
func (*stubRuntime) UUID() string {
	return "uuid"
}

// Logger supplies the default logger for handler panic diagnostics. The
// scripted runtime does not need its own execution-specific logging state.
func (*stubRuntime) Logger() *slog.Logger {
	return slog.Default()
}

// Sleep succeeds immediately because these facade tests do not exercise
// elapsed-time behavior; driver conformance tests own that contract.
func (*stubRuntime) Sleep(time.Duration) error {
	return nil
}

// Done returns a disabled cancellation channel because scripted signal tests
// have no background execution lifecycle to terminate.
func (*stubRuntime) Done() <-chan struct{} {
	return nil
}

// Err reports an active runtime consistently with the nil Done channel.
// Signal timeout errors are supplied by Wait instead of this accessor.
func (*stubRuntime) Err() error {
	return nil
}

// Raw exposes a recognizable native-context marker for delegation assertions.
// It carries no engine state or external resource ownership.
func (*stubRuntime) Raw() any {
	return "raw"
}

// Step rejects execution because this scripted runtime models signal
// reception only; real step execution belongs to driver conformance tests.
func (*stubRuntime) Step(string, []byte, workflow.StepOptions) ([]byte, error) {
	return nil, workflow.ErrUnsupported
}

// Child rejects nested execution rather than inventing a child result. The
// facade tests use this runtime only to exercise local signal memoization.
func (*stubRuntime) Child(string, string, []byte) ([]byte, error) {
	return nil, workflow.ErrUnsupported
}

// Spawn rejects detached execution consistently with Child. No background
// workflows or engine state are created by this test substitute.
func (*stubRuntime) Spawn(string, string, []byte) error {
	return workflow.ErrUnsupported
}

// Wait returns the scripted signal or ErrTimeout, then changes the stored
// value so a second runtime receive would expose a missing facade memo hit.
func (runtime *stubRuntime) Wait(signal string, _ time.Duration) ([]byte, error) {
	runtime.waits++

	value, ok := runtime.values[signal]

	if !ok {
		return nil, workflow.ErrTimeout
	}

	// A different value each time proves the facade does not ask twice.
	runtime.values[signal], _ = workflow.EncodeValue(nil, runtime.waits+100)

	return value, nil
}

// Select returns the configured case index and its scripted signal bytes.
// Timer cases return no payload; the facade owns invoking the winning callback.
func (runtime *stubRuntime) Select(cases []workflow.RawCase) (int, []byte, error) {
	runtime.selects++

	if cases[runtime.pick].Signal == "" {
		return runtime.pick, nil, nil
	}

	return runtime.pick, runtime.values[cases[runtime.pick].Signal], nil
}

// contextOf runs fn with a Context over rt, using the registry handler.
func contextOf(t *testing.T, rt workflow.Runtime, fn func(*workflow.Context) (string, error)) (string, error) {
	t.Helper()

	w := workflow.New("probe", func(c *workflow.Context, _ string) (string, error) {
		return fn(c)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.Handlers(w)

	if err != nil {
		t.Fatal(err)
	}

	handler, _ := registry.Workflow("probe")

	in, _ := workflow.EncodeValue(nil, "")

	out, err := handler.Handle(rt, in)

	if err != nil {
		return "", err
	}

	var result string

	if err := workflow.DecodeValue(nil, out, &result); err != nil {
		t.Fatal(err)
	}

	return result, nil
}

// TestHandlerRecoversPanicAndDecodeFailure checks registered handler failure
// boundaries: application panics become errors and bad step input is terminal.
func TestHandlerRecoversPanicAndDecodeFailure(t *testing.T) {
	boom := workflow.New("boom", func(*workflow.Context, string) (string, error) {
		panic("kaput")
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	number := workflow.NewStep("number", func(_ context.Context, n int) (int, error) {
		return n, nil
	})

	registry, err := workflow.Handlers(boom, number)

	if err != nil {
		t.Fatal(err)
	}

	handler, _ := registry.Workflow("boom")
	in, _ := workflow.EncodeValue(nil, "")

	if _, err := handler.Handle(&stubRuntime{}, in); err == nil || !strings.Contains(err.Error(), "kaput") {
		t.Fatalf("panic error = %v", err)
	}

	step, _ := registry.Step("number")

	_, err = step.Handle(t.Context(), []byte(`{"v":"text"}`))

	if err == nil || !errors.Is(err, workflow.ErrPermanent) {
		t.Fatalf("decode failure = %v; want terminal", err)
	}
}
