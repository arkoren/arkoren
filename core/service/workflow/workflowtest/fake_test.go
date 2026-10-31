package workflowtest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/workflowtest"
)

// user is the typed input recorded by fake workflow starts. A single identity
// field is sufficient to distinguish matching and nonmatching assertions.
type user struct {
	// ID identifies the user carried by a workflow start.
	ID int
}

var (
	// onboard is the main workflow identity; the fake never executes its body.
	onboard = workflow.New("onboard", func(*workflow.Context, user) (string, error) {
		return "", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	// other distinguishes an unstarted definition from the onboard history.
	other = workflow.New("other", func(*workflow.Context, user) (string, error) {
		return "", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})
	// confirmed carries a token used to check recorded signal payload decoding.
	confirmed = workflow.NewSignal[string]("confirmed")
)

// TestFakeRecordsStartAndCompletes checks typed start recording, options, and
// the transition from Running to an explicitly scripted successful result.
func TestFakeRecordsStartAndCompletes(t *testing.T) {
	fake := workflowtest.New()
	client := workflow.NewClient(fake)

	run, err := client.Start(t.Context(), onboard, "u1", user{
		ID: 7,
	}, workflow.Delay(time.Minute))

	if err != nil {
		t.Fatal(err)
	}

	fake.AssertStarted(t, onboard, func(in user) bool {
		return in.ID == 7
	})
	fake.AssertStarted(t, onboard)
	fake.AssertNotStarted(t, onboard, func(in user) bool {
		return in.ID == 8
	})
	fake.AssertNotStarted(t, other)
	fake.AssertStartedCount(t, onboard, 1)

	if got := fake.Started()[0].Options.Delay; got != time.Minute {
		t.Fatalf("recorded delay = %v", got)
	}

	if status, _ := run.Status(t.Context()); status != workflow.Running {
		t.Fatalf("Status = %v; want running", status)
	}

	fake.Complete("onboard", "u1", "welcome")

	got, err := run.Result(t.Context())

	if err != nil || got != "welcome" {
		t.Fatalf("Result = %q, %v", got, err)
	}
}

// TestFakeResultBlocksUntilScripted checks caller cancellation of an unfinished
// result and later unblocking when the test supplies a failure outcome.
func TestFakeResultBlocksUntilScripted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := workflowtest.New()
		client := workflow.NewClient(fake)

		run, err := client.Start(t.Context(), onboard, "u1", user{})

		if err != nil {
			t.Fatal(err)
		}

		short, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		if _, err := run.Result(short); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Result error = %v; want deadline", err)
		}

		done := make(chan error, 1)

		go func() {
			_, err := run.Result(t.Context())
			done <- err
		}()

		synctest.Wait()
		fake.Fail("onboard", "u1", workflow.NewError("declined", "no"))

		if err := <-done; !errors.Is(err, workflow.NewError("declined", "")) {
			t.Fatalf("Result error = %v; want declined", err)
		}
	})
}

// TestFakeScriptedBeforeStart checks that a predeclared successful outcome is
// applied when the corresponding run is subsequently started.
func TestFakeScriptedBeforeStart(t *testing.T) {
	fake := workflowtest.New()
	fake.Complete("onboard", "u1", "early")

	run, err := workflow.NewClient(fake).Start(t.Context(), onboard, "u1", user{})

	if err != nil {
		t.Fatal(err)
	}

	if got, err := run.Result(t.Context()); err != nil || got != "early" {
		t.Fatalf("Result = %q, %v", got, err)
	}
}

// TestFakeContractErrors checks duplicate start and unknown-run operations
// against one fake without requiring any workflow body to execute.
func TestFakeContractErrors(t *testing.T) {
	fake := workflowtest.New()
	client := workflow.NewClient(fake)

	if _, err := client.Start(t.Context(), onboard, "u1", user{}); err != nil {
		t.Fatal(err)
	}

	if _, err := client.Start(t.Context(), onboard, "u1", user{}); !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("duplicate Start error = %v", err)
	}

	missing := client.Run(onboard, "nope")

	if _, err := missing.Status(t.Context()); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Status error = %v", err)
	}

	if err := client.Signal(t.Context(), onboard, "nope", confirmed, "x"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Signal error = %v", err)
	}
}

// TestFakeSignalAndCancel checks signal recording, cancellation state, and
// rejection of signals delivered after the run has ended.
func TestFakeSignalAndCancel(t *testing.T) {
	fake := workflowtest.New()
	client := workflow.NewClient(fake)

	run, err := client.Start(t.Context(), onboard, "u1", user{})

	if err != nil {
		t.Fatal(err)
	}

	if err := client.Signal(t.Context(), onboard, "u1", confirmed, "tok"); err != nil {
		t.Fatal(err)
	}

	fake.AssertSignaled(t, onboard, "u1", confirmed, func(value string) bool {
		return value == "tok"
	})

	if err := run.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}

	fake.AssertCanceled(t, onboard, "u1")

	if _, err := run.Result(t.Context()); !errors.Is(err, workflow.ErrCanceled) {
		t.Fatalf("Result error = %v; want ErrCanceled", err)
	}

	if status, _ := run.Status(t.Context()); status != workflow.Canceled {
		t.Fatalf("Status = %v", status)
	}

	if err := client.Signal(t.Context(), onboard, "u1", confirmed, "late"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Signal after cancel error = %v", err)
	}
}

// TestFakeFailureInjectionAndReset checks one-shot versus persistent failures
// and verifies that Reset clears both histories and injected failure state.
func TestFakeFailureInjectionAndReset(t *testing.T) {
	fake := workflowtest.New()
	client := workflow.NewClient(fake)
	boom := errors.New("boom")

	fake.FailNext(boom)

	if _, err := client.Start(t.Context(), onboard, "u1", user{}); !errors.Is(err, boom) {
		t.Fatalf("FailNext error = %v", err)
	}

	if _, err := client.Start(t.Context(), onboard, "u1", user{}); err != nil {
		t.Fatalf("Start after FailNext: %v", err)
	}

	fake.FailWith(boom)

	if _, err := client.Start(t.Context(), onboard, "u2", user{}); !errors.Is(err, boom) {
		t.Fatalf("FailWith error = %v", err)
	}

	fake.FailWith(nil)
	fake.Reset()

	fake.AssertNothingStarted(t)

	if _, err := client.Start(t.Context(), onboard, "u1", user{}); err != nil {
		t.Fatalf("Start after Reset: %v", err)
	}
}

// recorder captures assertion failures.
type recorder struct {
	// TB forwards testing methods not intercepted by this assertion probe.
	testing.TB

	// failed records whether an assertion called Errorf.
	failed bool
}

// Helper deliberately ignores helper registration while probing assertions.
// This avoids altering the enclosing test handle's helper metadata.
func (recorder *recorder) Helper() {
}

// Errorf records a failure without failing the enclosing test. These probes
// check whether a failure is reported, not how its message is formatted.
func (recorder *recorder) Errorf(string, ...any) {
	recorder.failed = true
}

// TestFakeAssertionsFail checks detection of both a missing expected start
// and unexpected recorded starts using the non-failing recorder.
func TestFakeAssertionsFail(t *testing.T) {
	fake := workflowtest.New()
	r := &recorder{
		TB: t,
	}

	fake.AssertStarted(r, onboard)

	if !r.failed {
		t.Fatal("AssertStarted passed without a start")
	}

	if _, err := workflow.NewClient(fake).Start(t.Context(), onboard, "u1", user{}); err != nil {
		t.Fatal(err)
	}

	r = &recorder{
		TB: t,
	}

	fake.AssertNothingStarted(r)

	if !r.failed {
		t.Fatal("AssertNothingStarted passed after a start")
	}
}

// TestFakeStartRejectsDoneContext checks that caller cancellation is observed
// before a run or its start-history entry can be created.
func TestFakeStartRejectsDoneContext(t *testing.T) {
	fake := workflowtest.New()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := fake.Start(ctx, "onboard", "u1", nil, workflow.StartOptions{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v; want context.Canceled", err)
	}

	fake.AssertNothingStarted(t)

	if _, err := fake.Status(t.Context(), "onboard", "u1"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Status = %v; want the run not to exist", err)
	}
}

// TestFakeResultPrefersEndedRunOverDoneContext checks deterministic outcome
// precedence when both the run result and caller cancellation are ready.
func TestFakeResultPrefersEndedRunOverDoneContext(t *testing.T) {
	fake := workflowtest.New()

	if err := fake.Start(t.Context(), "onboard", "u1", nil, workflow.StartOptions{}); err != nil {
		t.Fatal(err)
	}

	fake.Complete("onboard", "u1", "welcome")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	for range 20 {
		if _, err := fake.Result(ctx, "onboard", "u1"); err != nil {
			t.Fatalf("Result = %v; want the completed output", err)
		}
	}
}

// TestFakeFailPanicsOnNilError checks that a failure script cannot contain a
// nil error, which would contradict the requested failure outcome.
func TestFakeFailPanicsOnNilError(t *testing.T) {
	fake := workflowtest.New()

	defer func() {
		if recover() == nil {
			t.Fatal("Fail(nil) did not panic")
		}
	}()

	fake.Fail("onboard", "u1", nil)
}

// TestFakeResultReturnsCopy checks that mutating returned result bytes cannot
// corrupt the fake's retained outcome for later Result calls.
func TestFakeResultReturnsCopy(t *testing.T) {
	fake := workflowtest.New()

	if err := fake.Start(t.Context(), "onboard", "u1", nil, workflow.StartOptions{}); err != nil {
		t.Fatal(err)
	}

	fake.Complete("onboard", "u1", "welcome")

	first, err := fake.Result(t.Context(), "onboard", "u1")

	if err != nil {
		t.Fatal(err)
	}

	want := string(first)
	clear(first)

	second, err := fake.Result(t.Context(), "onboard", "u1")

	if err != nil || string(second) != want {
		t.Fatalf("Result after mutation = %q, %v; want %q", second, err, want)
	}
}

// TestStartedTraceDetachesInvocationInput verifies caller-owned propagation
// maps cannot modify an already-recorded start after the invocation returns.
func TestStartedTraceDetachesInvocationInput(t *testing.T) {
	t.Parallel()

	fake := workflowtest.New()
	carrier := map[string]string{"traceparent": "original"}

	if err := fake.Start(t.Context(), "flow", "run", []byte(`{"v":"input"}`), workflow.StartOptions{Trace: carrier}); err != nil {
		t.Fatal(err)
	}

	carrier["traceparent"] = "caller-edit"

	if got := fake.Started()[0].Options.Trace["traceparent"]; got != "original" {
		t.Fatalf("retained caller map: %q", got)
	}
}

// TestStartedTraceSnapshotsAreIndependent verifies both existing snapshots and
// later readers remain unchanged when one returned carrier is edited.
func TestStartedTraceSnapshotsAreIndependent(t *testing.T) {
	t.Parallel()

	fake := workflowtest.New()

	if err := fake.Start(t.Context(), "flow", "run", []byte(`{"v":"input"}`), workflow.StartOptions{
		Trace: map[string]string{"traceparent": "original"},
	}); err != nil {
		t.Fatal(err)
	}

	first, second := fake.Started(), fake.Started()
	first[0].Options.Trace["traceparent"] = "snapshot-edit"

	if got := second[0].Options.Trace["traceparent"]; got != "original" {
		t.Fatalf("snapshots share carrier: %q", got)
	}

	if got := fake.Started()[0].Options.Trace["traceparent"]; got != "original" {
		t.Fatalf("snapshot modified retained observation: %q", got)
	}
}

// TestStartedTraceSnapshotsPermitConcurrentEdits verifies the snapshot contract
// under the race detector: readers clone only retained maps, never edited ones.
func TestStartedTraceSnapshotsPermitConcurrentEdits(t *testing.T) {
	t.Parallel()

	fake := workflowtest.New()

	if err := fake.Start(t.Context(), "flow", "run", []byte(`{"v":"input"}`), workflow.StartOptions{
		Trace: map[string]string{"traceparent": "original"},
	}); err != nil {
		t.Fatal(err)
	}

	snapshot := fake.Started()
	var workers sync.WaitGroup
	workers.Go(func() {
		for range 100 {
			snapshot[0].Options.Trace["traceparent"] = "local-edit"
		}
	})

	for range 100 {
		if got := fake.Started()[0].Options.Trace["traceparent"]; got != "original" {
			t.Errorf("concurrent snapshot modified observation: %q", got)
		}
	}

	workers.Wait()
}
