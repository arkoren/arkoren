package workflowtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/workflow"
)

// suite carries a bounded client and unique run identity for one subtest.
// Capability declarations change only the corresponding unsupported assertions.
type suite struct {
	// t reports failures and owns cleanup for this conformance scenario.
	t *testing.T

	// ctx bounds all client calls made by this scenario.
	ctx context.Context

	// client provides typed access to the driver under test.
	client *workflow.Client

	// id isolates the scenario from other runs and retained engine history.
	id string

	// opts declares unsupported capabilities for this invocation. Every other
	// operation retains the default contract assertions.
	opts options
}

// Option declares driver capabilities or controls execution of [Run]. An absent
// capability is tested for ErrUnsupported, not silently skipped; all remaining
// operations must satisfy the ordinary workflow contract.
//
//	options := []workflowtest.Option{workflowtest.WithoutImmediateStepRetries()}
//	workflowtest.Run(t, newDriver, options...)
type Option func(*options)

// options holds capability declarations and execution settings for one [Run].
// Zero values require full functionality and use ordinary parallel subtests.
type options struct {
	// synctest creates a fresh driver inside each virtual-time test bubble.
	synctest bool

	// withoutChild requires Child to reject before submission. False keeps
	// the success, failure, ownership, and existing-run joining assertions.
	withoutChild bool

	// withoutSpawn requires Spawn to reject before submission. False keeps
	// the detached lifetime and idempotency assertions.
	withoutSpawn bool

	// withoutImmediateStepRetries requires completed multi-attempt policies
	// with Initial zero to fail before executing the affected step handler.
	// Single attempts and positive-backoff retry assertions remain enabled.
	withoutImmediateStepRetries bool
}

// WithSynctest runs every subtest inside a testing/synctest bubble, so
// sleeps, delays, and timeouts take no real time. Use it for drivers that
// live in the process and use the time package, such as the memory driver.
// Because goroutines of the driver must belong to the bubble, [Run] then calls
// newDriver once per subtest, inside the bubble, and subtests run one after
// another. Never use it with a driver that talks to an external engine.
func WithSynctest() Option {
	return func(opts *options) {
		opts.synctest = true
	}
}

// WithoutChild declares that the driver cannot run child workflows. The suite
// checks ErrUnsupported and absence of a newly submitted child instead of Child
// success/error semantics, and checks rejection when the target already exists.
// Ownership scenarios still require the independent target to remain running
// and finish with its original input after rejection; no test assumes a child
// that the driver has explicitly refused to submit can be canceled.
// Facade self-ID validation and every Spawn assertion remain enabled.
//
// Example:
//
//	workflowtest.Run(t, newDriver, workflowtest.WithoutChild())
func WithoutChild() Option {
	return func(opts *options) {
		opts.withoutChild = true
	}
}

// WithoutSpawn declares that the driver cannot spawn detached workflows. The
// suite requires ErrUnsupported and no submitted child rather than omitting the
// Spawn case. Child behavior remains required unless [WithoutChild] is also set.
//
// Example:
//
//	workflowtest.Run(t, newDriver, workflowtest.WithoutSpawn())
func WithoutSpawn() Option {
	return func(opts *options) {
		opts.withoutSpawn = true
	}
}

// WithoutImmediateStepRetries declares that the engine cannot express a
// completed per-call step policy with Attempts > 1 and Initial == 0. The suite
// requires ErrUnsupported and zero affected-handler executions, not a skipped
// case, a substituted positive delay, or retries of the whole workflow.
// Single-attempt zero-backoff and positive-backoff cases remain required.
// Register every Definitions entry even when selecting this option.
//
//	workflowtest.Run(t, newDriver, workflowtest.WithoutImmediateStepRetries())
func WithoutImmediateStepRetries() Option {
	return func(opts *options) {
		opts.withoutImmediateStepRetries = true
	}
}

// Run runs the conformance suite against the drivers that newDriver returns.
// Every driver must run the workflows and steps of [Definitions]; build its
// registry with [Registry]. Without [WithSynctest], newDriver is called once
// with the t of Run, and the subtests run in parallel against that driver.
// Register cleanup of the driver with t.Cleanup.
//
// The suite uses short durations, up to a few hundred milliseconds, so engines
// with real clocks can run it. Every run id has a random prefix, so the suite
// can run again against an engine that keeps its history.
// [WithoutChild] and [WithoutSpawn] require rejection without submission for
// those operations; they do not relax any other contract assertions.
// WithoutImmediateStepRetries similarly asserts rejection before affected step
// execution for zero-initial multi-attempt policies. It does not disable the
// immediate single-attempt or existing positive-backoff retry controls.
//
// Example:
//
//	workflowtest.Run(t, newDriver, workflowtest.WithoutChild(), workflowtest.WithoutSpawn())
func Run(t *testing.T, newDriver func(t *testing.T) workflow.Driver, opts ...Option) {
	t.Helper()

	var settings options

	for _, opt := range opts {
		opt(&settings)
	}

	var token [4]byte

	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}

	prefix := hex.EncodeToString(token[:])

	var shared workflow.Driver

	if !settings.synctest {
		shared = newDriver(t)
	}

	cases := []struct {
		// name is the contract scenario label shown in test output.
		name string

		// run performs one contract assertion using the shared suite plumbing.
		run func(*suite)
	}{
		{
			name: "typed start and result",
			run:  testTyped,
		},
		{
			name: "serialized trace reaches worker attempt",
			run:  testTraceCarrier,
		},
		{
			name: "serialized trace reaches child attempt",
			run:  testChildTraceCarrier,
		},
		{
			name: "serialized trace reaches spawned attempt",
			run:  testSpawnTraceCarrier,
		},
		{
			name: "duplicate id",
			run:  testDuplicate,
		},
		{
			name: "step retry then success",
			run:  testRetry,
		},
		{
			name: "step immediate retries",
			run:  testImmediateRetries,
		},
		{
			name: "step immediate single attempt",
			run:  testImmediateSingleAttempt,
		},
		{
			name: "permanent stops retry",
			run:  testPermanent,
		},
		{
			name: "step timeout",
			run:  testStepTimeout,
		},
		{
			name: "child",
			run:  testChild,
		},
		{
			name: "child error kind and permanent",
			run:  testChildError,
		},
		{
			name: "child joins existing id",
			run:  testChildJoin,
		},
		{
			name: "child join cancellation leaves existing run alive",
			run:  testChildJoinCancel,
		},
		{
			name: "child join timeout leaves existing run alive",
			run:  testChildJoinTimeout,
		},
		{
			name: "created child stops on parent cancellation",
			run:  testChildCreatedCancel,
		},
		{
			name: "created child stops on parent timeout",
			run:  testChildCreatedTimeout,
		},
		{
			name: "child rejects own id",
			run:  testSelfJoin,
		},
		{
			name: "spawn is idempotent and outlives parent",
			run:  testSpawn,
		},
		{
			name: "sleep",
			run:  testSleep,
		},
		{
			name: "wait signal before",
			run:  testWaitBefore,
		},
		{
			name: "wait signal after",
			run:  testWaitAfter,
		},
		{
			name: "wait timeout",
			run:  testWaitTimeout,
		},
		{
			name: "wait resolves once",
			run:  testResolveOnce,
		},
		{
			name: "select signal wins",
			run:  testSelectSignal,
		},
		{
			name: "select timer wins",
			run:  testSelectTimer,
		},
		{
			name: "cancel",
			run:  testCancel,
		},
		{
			name: "cancel finished run",
			run:  testCancelFinished,
		},
		{
			name: "context err after cancel",
			run:  testContextErr,
		},
		{
			name: "signal finished run",
			run:  testSignalFinished,
		},
		{
			name: "unknown run",
			run:  testUnknown,
		},
		{
			name: "finished run still exists",
			run:  testRestartFinished,
		},
		{
			name: "unregistered workflow",
			run:  testUnregistered,
		},
		{
			name: "empty id",
			run:  testEmptyID,
		},
		{
			name: "concurrent start",
			run:  testConcurrentStart,
		},
		{
			name: "error kind round trip",
			run:  testErrorKind,
		},
		{
			name: "panic fails the run",
			run:  testPanic,
		},
		{
			name: "delay",
			run:  testDelay,
		},
		{
			name: "result context canceled",
			run:  testResultCanceled,
		},
		{
			name: "max duration",
			run:  testMaxDuration,
		},
	}

	for i, tc := range cases {
		id := prefix + "-" + strconv.Itoa(i)

		t.Run(tc.name, func(t *testing.T) {
			if settings.synctest {
				synctest.Test(t, func(t *testing.T) {
					runCase(t, newDriver(t), id, tc.run, settings)
				})

				return
			}

			t.Parallel()

			runCase(t, shared, id, tc.run, settings)
		})
	}
}

// runCase runs one subtest against driver with a bounded context. Capability
// declarations travel with the case so unsupported operations are still checked.
func runCase(t *testing.T, driver workflow.Driver, id string, run func(*suite), opts options) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)

	run(&suite{
		t:      t,
		ctx:    ctx,
		client: workflow.NewClient(driver),
		id:     id,
		opts:   opts,
	})
}

// testTyped checks distinct typed input/output objects and the transition to
// Completed using the same Run handle returned by Start.
func testTyped(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, typedFlow, s.id, Order{
		Item: "tea",
		Qty:  4,
	})

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	got, err := run.Result(s.ctx)

	if err != nil {
		t.Fatalf("Result: %v", err)
	}

	if got != (Receipt{
		Summary: "tea",
		Total:   8,
	}) {
		t.Fatalf("Result = %+v", got)
	}

	status, err := run.Status(s.ctx)

	if err != nil || status != workflow.Completed {
		t.Fatalf("Status = %v, %v; want completed", status, err)
	}
}

// testDuplicate checks that a repeated start returns the existing handle and
// preserves the first input until the original run is released by a signal.
func testDuplicate(s *suite) {
	t := s.t

	first, err := s.client.Start(s.ctx, duplicateFlow, s.id, "a")

	if err != nil {
		t.Fatalf("first Start: %v", err)
	}

	second, err := s.client.Start(s.ctx, duplicateFlow, s.id, "b")

	if !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("second Start error = %v; want ErrAlreadyStarted", err)
	}

	if second == nil || second.ID() != first.ID() {
		t.Fatalf("second Start returned %v; want the existing run", second)
	}

	if err := s.client.Signal(s.ctx, duplicateFlow, s.id, duplicateReady, "!"); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	got, err := second.Result(s.ctx)

	if err != nil || got != "a!" {
		t.Fatalf("Result = %q, %v; want a!", got, err)
	}
}

// testRetry checks both the successful workflow result and the exact number
// of step calls required by the retry policy.
func testRetry(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, retryFlow, s.id, s.id)

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	got, err := run.Result(s.ctx)

	if err != nil || got != s.id+"-ok" {
		t.Fatalf("Result = %q, %v; want %s-ok", got, err, s.id)
	}

	if n := counter(&retryCalls, s.id).Load(); n != 3 {
		t.Fatalf("step ran %d times; want 3", n)
	}
}

// testPermanent checks that a fatal step stops retries while preserving its
// custom kind, message, permanent marker, and failed run status.
func testPermanent(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, permanentFlow, s.id, s.id)

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, err = run.Result(s.ctx)

	if err == nil || !strings.Contains(err.Error(), "do not retry") {
		t.Fatalf("Result error = %v; want the step error", err)
	}

	if !errors.Is(err, workflow.ErrPermanent) || !errors.Is(err, workflow.NewError("conf-fatal", "")) {
		t.Fatalf("Result error = %v; want permanent with kind conf-fatal", err)
	}

	if n := counter(&permanentCalls, s.id).Load(); n != 1 {
		t.Fatalf("step ran %d times; want 1", n)
	}

	if status, _ := run.Status(s.ctx); status != workflow.Failed {
		t.Fatalf("Status = %v; want failed", status)
	}
}

// testStepTimeout checks the step-attempt deadline independently of the longer
// workflow lifetime, requiring the timeout sentinel at the result boundary.
func testStepTimeout(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, stepTimeoutFlow, s.id, "x")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := run.Result(s.ctx); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("Result error = %v; want ErrTimeout", err)
	}
}

// testChild requires the typed child result by default. An unsupported driver
// must reject the parent call and leave no run under the child's requested ID.
func testChild(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, parentFlow, s.id, 4)

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	got, err := run.Result(s.ctx)

	if s.opts.withoutChild {
		if err := unsupportedResult(s.ctx, s.client.Driver(), childFlow.Name(), s.id+"-child", err); err != nil {
			t.Fatal(err)
		}

		return
	}

	if err != nil || got != 41 {
		t.Fatalf("Result = %d, %v; want 41", got, err)
	}
}

// testSleep checks that runtime time advances across a durable sleep rather
// than resuming the workflow before its requested duration has elapsed.
func testSleep(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, sleepFlow, s.id, 50)

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	got, err := run.Result(s.ctx)

	if err != nil || !got {
		t.Fatalf("Result = %v, %v; want true", got, err)
	}
}

// testWaitBefore sends while the workflow is still sleeping and checks that
// the engine buffers the value until its later Wait call.
func testWaitBefore(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, waitBeforeFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := s.client.Signal(s.ctx, waitBeforeFlow, s.id, earlySignal, "hello"); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	got, err := run.Result(s.ctx)

	if err != nil || got != "hello" {
		t.Fatalf("Result = %q, %v; want hello", got, err)
	}
}

// testWaitAfter sends a typed object after allowing the workflow to enter its
// wait, checking wakeup and decoding through the engine boundary.
func testWaitAfter(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, waitAfterFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if err := s.client.Signal(s.ctx, waitAfterFlow, s.id, lateSignal, Order{
		Item: "x",
		Qty:  2,
	}); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	got, err := run.Result(s.ctx)

	if err != nil || got != (Order{
		Item: "x",
		Qty:  2,
	}) {
		t.Fatalf("Result = %+v, %v", got, err)
	}
}

// testWaitTimeout checks the workflow's recovery from an expected signal wait
// timeout; an engine that never wakes the wait cannot return the marker.
func testWaitTimeout(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, waitTimeoutFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	got, err := run.Result(s.ctx)

	if err != nil || got != "timed out" {
		t.Fatalf("Result = %q, %v; want timed out", got, err)
	}
}

// testResolveOnce sends two values but requires both workflow waits to resolve
// to the first, including engines that finish before the second send arrives.
func testResolveOnce(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, resolveOnceFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	for _, value := range []string{"a", "b"} {
		if err := s.client.Signal(s.ctx, resolveOnceFlow, s.id, valueSignal, value); err != nil && !errors.Is(err, workflow.ErrNotFound) {
			t.Fatalf("Signal: %v", err)
		}
	}

	got, err := run.Result(s.ctx)

	if err != nil || got != "a,a" {
		t.Fatalf("Result = %q, %v; want a,a", got, err)
	}
}

// testSelectSignal checks that a delivered signal wins against a longer timer
// and that the selected callback receives its decoded value.
func testSelectSignal(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, selectSignalFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := s.client.Signal(s.ctx, selectSignalFlow, s.id, pickSignal, "x"); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	got, err := run.Result(s.ctx)

	if err != nil || got != "signal:x" {
		t.Fatalf("Result = %q, %v; want signal:x", got, err)
	}
}

// testSelectTimer checks the timer branch of Select when no signal arrives,
// requiring the workflow to resume with the timer callback's marker.
func testSelectTimer(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, selectTimerFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	got, err := run.Result(s.ctx)

	if err != nil || got != "timer" {
		t.Fatalf("Result = %q, %v; want timer", got, err)
	}
}

// testCancel checks explicit cancellation of a blocked workflow through both
// Result's cancellation error and the terminal Canceled status.
func testCancel(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, cancelFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := run.Cancel(s.ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if _, err := run.Result(s.ctx); !errors.Is(err, workflow.ErrCanceled) {
		t.Fatalf("Result error = %v; want ErrCanceled", err)
	}

	if status, err := run.Status(s.ctx); err != nil || status != workflow.Canceled {
		t.Fatalf("Status = %v, %v; want canceled", status, err)
	}
}

// testUnknown checks consistent not-found errors for all operations on a run
// ID that has not been started under an otherwise registered workflow.
func testUnknown(s *suite) {
	t := s.t
	run := s.client.Run(unknownFlow, s.id)

	if _, err := run.Result(s.ctx); !errors.Is(err, workflow.ErrNotFound) {
		t.Errorf("Result error = %v; want ErrNotFound", err)
	}

	if _, err := run.Status(s.ctx); !errors.Is(err, workflow.ErrNotFound) {
		t.Errorf("Status error = %v; want ErrNotFound", err)
	}

	if err := s.client.Signal(s.ctx, unknownFlow, s.id, unknownSignal, "x"); !errors.Is(err, workflow.ErrNotFound) {
		t.Errorf("Signal error = %v; want ErrNotFound", err)
	}

	if err := run.Cancel(s.ctx); !errors.Is(err, workflow.ErrNotFound) {
		t.Errorf("Cancel error = %v; want ErrNotFound", err)
	}
}

// testRestartFinished checks that completing a run does not release its ID for
// another Start while the engine still retains its history.
func testRestartFinished(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, typedFlow, s.id, Order{
		Item: "tea",
		Qty:  1,
	})

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := run.Result(s.ctx); err != nil {
		t.Fatalf("Result: %v", err)
	}

	if _, err := s.client.Start(s.ctx, typedFlow, s.id, Order{
		Item: "tea",
		Qty:  1,
	}); !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("Start of a finished run error = %v; want ErrAlreadyStarted", err)
	}
}

// testErrorKind checks both errors.Is identity and errors.As message access
// after a custom workflow failure crosses the driver's result boundary.
func testErrorKind(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, errorKindFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, err = run.Result(s.ctx)

	if !errors.Is(err, errorKind) {
		t.Fatalf("Result error = %v; want kind conf-custom", err)
	}

	var typed *workflow.Error

	if !errors.As(err, &typed) || typed.Message != "boom" {
		t.Fatalf("Result error = %#v; want message boom", err)
	}
}

// testMaxDuration checks that a run-wide timeout wakes a blocked workflow and
// records Failed rather than treating the timeout as ordinary cancellation.
func testMaxDuration(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, maxDurationFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := run.Result(s.ctx); !errors.Is(err, workflow.ErrTimeout) {
		t.Fatalf("Result error = %v; want ErrTimeout", err)
	}

	if status, _ := run.Status(s.ctx); status != workflow.Failed {
		t.Fatalf("Status = %v; want failed", status)
	}
}

// testChildError checks kind and permanence across a supported child's failure.
// An unsupported driver must reject before even the failing child is submitted.
func testChildError(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, childFailParentFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, err = run.Result(s.ctx)

	if s.opts.withoutChild {
		if err := unsupportedResult(s.ctx, s.client.Driver(), childFailFlow.Name(), s.id+"-child", err); err != nil {
			t.Fatal(err)
		}

		return
	}

	var typed *workflow.Error

	if !errors.Is(err, errorKind) || !errors.As(err, &typed) || typed.Message != "child boom" || !errors.Is(err, workflow.ErrPermanent) {
		t.Fatalf("Result error = %#v; want permanent conf-custom child boom", err)
	}
}

// testChildJoin starts a target independently and verifies that Child either
// joins it or rejects with ErrUnsupported, without changing its input or result.
func testChildJoin(s *suite) {
	t := s.t

	existing, err := s.client.Start(s.ctx, duplicateFlow, s.id+"-x", "a")

	if err != nil {
		t.Fatalf("Start existing: %v", err)
	}

	parent, err := s.client.Start(s.ctx, joinFlow, s.id, s.id+"-x")

	if err != nil {
		t.Fatalf("Start parent: %v", err)
	}

	if err := s.client.Signal(s.ctx, duplicateFlow, s.id+"-x", duplicateReady, "!"); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	got, err := parent.Result(s.ctx)

	if s.opts.withoutChild {
		if !errors.Is(err, workflow.ErrUnsupported) {
			t.Fatalf("parent Result = %q, %v; want ErrUnsupported for an existing child", got, err)
		}
	} else if err != nil || got != "a!" {
		t.Fatalf("parent Result = %q, %v; want a!", got, err)
	}

	if got, err := existing.Result(s.ctx); err != nil || got != "a!" {
		t.Fatalf("existing Result = %q, %v; want a!", got, err)
	}
}

// testSelfJoin checks facade rejection of a child request for the calling run
// itself, even when the driver declares no child execution capability.
func testSelfJoin(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, selfJoinFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if got, err := run.Result(s.ctx); err != nil || got != "rejected" {
		t.Fatalf("Result = %q, %v; want rejected", got, err)
	}
}

// testSpawn checks that supported detached work outlives its completed parent.
// An unsupported driver must instead reject and leave no detached execution.
func testSpawn(s *suite) {
	t := s.t

	parent, err := s.client.Start(s.ctx, spawnFlow, s.id, "a")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	childID, err := parent.Result(s.ctx)

	if s.opts.withoutSpawn {
		if err := unsupportedResult(s.ctx, s.client.Driver(), duplicateFlow.Name(), s.id+"-spawned", err); err != nil {
			t.Fatal(err)
		}

		return
	}

	if err != nil || childID != s.id+"-spawned" {
		t.Fatalf("Result = %q, %v; want the child id", childID, err)
	}

	child := s.client.Run(duplicateFlow, childID)

	if status, err := child.Status(s.ctx); err != nil || status != workflow.Running {
		t.Fatalf("child Status = %v, %v; want running after the parent ended", status, err)
	}

	if err := s.client.Signal(s.ctx, duplicateFlow, childID, duplicateReady, "!"); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	if got, err := child.Result(s.ctx); err != nil || got != "a!" {
		t.Fatalf("child Result = %q, %v; want a!", got, err)
	}
}

// unsupportedResult validates both the parent failure and absence of child work.
// It returns an error if a runtime submitted a run before rejecting, even when
// that run already finished. Existing-child rejection is checked separately.
func unsupportedResult(ctx context.Context, driver workflow.Driver, name, id string, err error) error {
	if !errors.Is(err, workflow.ErrUnsupported) {
		return errors.Join(errors.New("parent Result must fail with ErrUnsupported"), err)
	}

	if _, err := driver.Status(ctx, name, id); !errors.Is(err, workflow.ErrNotFound) {
		return errors.Join(fmt.Errorf("unsupported operation left child %s %s; want ErrNotFound", name, id), err)
	}

	return nil
}

// testCancelFinished checks that canceling a completed run is an idempotent
// success and leaves its successful terminal status unchanged.
func testCancelFinished(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, typedFlow, s.id, Order{
		Item: "tea",
		Qty:  1,
	})

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := run.Result(s.ctx); err != nil {
		t.Fatalf("Result: %v", err)
	}

	if err := run.Cancel(s.ctx); err != nil {
		t.Fatalf("Cancel finished run: %v", err)
	}

	if status, err := run.Status(s.ctx); err != nil || status != workflow.Completed {
		t.Fatalf("Status = %v, %v; want completed", status, err)
	}
}

// testContextErr waits until the body starts, cancels it, then checks that the
// body itself observes the workflow cancellation sentinel while unwinding.
func testContextErr(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, ctxErrFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Cancel only after the function runs, or it never observes anything.
	waitFor(t, func() bool {
		_, ok := ctxErrSeen.Load(s.id)

		return ok
	})

	if err := run.Cancel(s.ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	if _, err := run.Result(s.ctx); !errors.Is(err, workflow.ErrCanceled) {
		t.Fatalf("Result error = %v; want ErrCanceled", err)
	}

	// The function may still be unwinding when the run is already final.
	waitFor(t, func() bool {
		seen, _ := ctxErrSeen.Load(s.id)
		_, ok := seen.(error)

		return ok
	})

	seen, _ := ctxErrSeen.Load(s.id)

	if err, _ := seen.(error); !errors.Is(err, workflow.ErrCanceled) {
		t.Fatalf("Context.Err = %v; want ErrCanceled", seen)
	}
}

// waitFor polls cond until it holds or five seconds pass.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}

		time.Sleep(time.Millisecond)
	}
}

// testSignalFinished checks that a retained completed run cannot receive new
// signal values even though status and result queries still know its ID.
func testSignalFinished(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, typedFlow, s.id, Order{
		Item: "tea",
		Qty:  1,
	})

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := run.Result(s.ctx); err != nil {
		t.Fatalf("Result: %v", err)
	}

	if err := s.client.Signal(s.ctx, typedFlow, s.id, unknownSignal, "x"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Signal error = %v; want ErrNotFound", err)
	}
}

// testUnregistered checks submission of a valid definition intentionally left
// out of the driver's registry and requires the not-found sentinel.
func testUnregistered(s *suite) {
	if _, err := s.client.Start(s.ctx, unregisteredFlow, s.id, ""); !errors.Is(err, workflow.ErrNotFound) {
		s.t.Fatalf("Start error = %v; want ErrNotFound", err)
	}
}

// testEmptyID checks invalid caller-supplied identities at submission and
// existing-handle operations, including empty and control-character inputs.
func testEmptyID(s *suite) {
	if _, err := s.client.Start(s.ctx, unknownFlow, "", "x"); !errors.Is(err, workflow.ErrInvalidID) {
		s.t.Fatalf("Start error = %v; want ErrInvalidID", err)
	}

	if _, err := s.client.Start(s.ctx, unknownFlow, "bad\nid", "x"); !errors.Is(err, workflow.ErrInvalidID) {
		s.t.Fatalf("Start with control character error = %v; want ErrInvalidID", err)
	}

	if err := s.client.Run(unknownFlow, "bad\x00id").Cancel(s.ctx); !errors.Is(err, workflow.ErrInvalidID) {
		s.t.Fatalf("Cancel with control character error = %v; want ErrInvalidID", err)
	}
}

// testConcurrentStart checks atomic duplicate handling: exactly one concurrent
// submission wins and all others report the already-started sentinel.
func testConcurrentStart(s *suite) {
	t := s.t

	const starters = 8

	errs := make(chan error, starters)

	for range starters {
		go func() {
			_, err := s.client.Start(s.ctx, duplicateFlow, s.id, "a")
			errs <- err
		}()
	}

	winners := 0

	for range starters {
		err := <-errs

		switch {
		case err == nil:
			winners++
		case !errors.Is(err, workflow.ErrAlreadyStarted):
			t.Errorf("Start error = %v; want nil or ErrAlreadyStarted", err)
		}
	}

	if winners != 1 {
		t.Fatalf("%d starts won; want exactly 1", winners)
	}

	if err := s.client.Run(duplicateFlow, s.id).Cancel(s.ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

// testPanic checks that an application panic produces a failed run carrying
// the panic diagnostic instead of escaping or leaving execution Running.
func testPanic(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, panicFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := run.Result(s.ctx); err == nil || !strings.Contains(err.Error(), "conf-boom") {
		t.Fatalf("Result error = %v; want the panic value", err)
	}

	if status, _ := run.Status(s.ctx); status != workflow.Failed {
		t.Fatalf("Status = %v; want failed", status)
	}
}

// testDelay checks that an accepted delayed run is initially Running and
// eventually executes its body with the configured delay option.
func testDelay(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, delayFlow, s.id, "", workflow.Delay(100*time.Millisecond))

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if status, err := run.Status(s.ctx); err != nil || status != workflow.Running {
		t.Fatalf("Status = %v, %v; want running during the delay", status, err)
	}

	if got, err := run.Result(s.ctx); err != nil || got != "ran" {
		t.Fatalf("Result = %q, %v; want ran", got, err)
	}
}

// testResultCanceled checks that canceling a caller's Result wait leaves the
// underlying workflow Running until it is explicitly canceled separately.
func testResultCanceled(s *suite) {
	t := s.t

	run, err := s.client.Start(s.ctx, cancelFlow, s.id, "")

	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	short, cancel := context.WithCancel(s.ctx)
	cancel()

	if _, err := run.Result(short); err == nil {
		t.Fatal("Result with a canceled context returned no error")
	}

	if status, err := run.Status(s.ctx); err != nil || status != workflow.Running {
		t.Fatalf("Status = %v, %v; want running", status, err)
	}

	if err := run.Cancel(s.ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}
