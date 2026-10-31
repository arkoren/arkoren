package hatchetworkflow

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"arkoren.dev/core/service/workflow"
)

// signalContext models only the durable context operations used to construct
// waits. Matching deliberately uses the engine's key/scope/time predicates,
// without inventing a per-execution or consumed-event restriction. The embedded
// SDK interface makes any unexpected call fail rather than silently succeed.
type signalContext struct {
	// DurableContext supplies the unused SDK surface; unexpected calls fail
	// through the nil embedded interface.
	hatchet.DurableContext

	// ctx supplies cancellation while the runtime owns its execution deadline.
	ctx context.Context

	// executionID is the stable engine workflow-run identity. attemptID can
	// change independently on restart and must never influence signal scope.
	executionID string
	// attemptID identifies the worker attempt and may change without changing the
	// physical execution.
	attemptID string

	// memos survive recreation of the runtime, as the durable engine log does.
	memos map[string][]byte

	// be retains historical events across physical executions and replays.
	be *fake

	// scope and matched expose the actual condition and event chosen by WaitFor.
	scope string
	// matched retains the event selected by WaitFor for scope and payload
	// assertions.
	matched wire

	// since exposes the serialized historical boundary independently of the
	// fake's current time, so a larger fixed worker lookback cannot pass.
	since time.Time
}

// WorkflowRunId returns the physical execution, independent of worker attempts.
// It corresponds to RunNoWait's RunId rather than a task invocation counter.
func (raw *signalContext) WorkflowRunId() string {
	return raw.executionID
}

// StepRunId returns a different worker identity so replay tests detect accidental
// use of an attempt identity instead of WorkflowRunId.
func (raw *signalContext) StepRunId() string {
	return raw.attemptID
}

// Deadline delegates cancellation to the test context. The fake never supplies
// an engine timeout in place of the runtime's MaxDuration deadline.
func (raw *signalContext) Deadline() (time.Time, bool) {
	return raw.ctx.Deadline()
}

// Done allows newRuntime to derive a deadline context without accessing the
// embedded, deliberately unimplemented SDK interface.
func (raw *signalContext) Done() <-chan struct{} {
	return raw.ctx.Done()
}

// Err reports cancellation from the test context, not an invented task state.
// Tests control time through synctest rather than sleeping on the wall clock.
func (raw *signalContext) Err() error {
	return raw.ctx.Err()
}

// Value forwards context values used by context.WithDeadlineCause. It retains
// normal context semantics while leaving unrelated SDK methods unimplemented.
func (raw *signalContext) Value(key any) any {
	return raw.ctx.Value(key)
}

// GetContext supplies the immutable native parent used for deadline binding.
// It implements the SDK's public context surface without changing replay data.
func (raw *signalContext) GetContext() context.Context {
	return raw.ctx
}

// memo retains v2-encoded JSON across runtime recreation. Only the first call
// executes fn; signalMemoContext supplies the SDK's exact named return type
// without using the legacy JSON serializer or changing the cached bytes.
func (raw *signalContext) memo(key string, fn func() (any, error)) ([]byte, error) {
	if value, ok := raw.memos[key]; ok {
		return value, nil
	}

	value, err := fn()

	if err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(value)

	if err != nil {
		return nil, err
	}

	if raw.memos == nil {
		raw.memos = map[string][]byte{}
	}

	raw.memos[key] = encoded

	return encoded, nil
}

// signalMemoContext adapts only Memo's return type at the pinned SDK boundary.
// T is inferred from DurableContext.Memo, whose named byte slice has no SDK
// alias. Embedding the original fake preserves all other context dispatch.
type signalMemoContext[T ~[]byte] struct {
	// signalContext retains the fake context behavior while Memo adapts the SDK
	// return type.
	*signalContext
}

// Memo returns the fake's v2-encoded bytes with the SDK's exact named type.
// The conversion changes neither serialization nor memo ownership/replay state.
func (raw signalMemoContext[T]) Memo(key string, fn func() (any, error)) (T, error) {
	encoded, err := raw.memo(key, fn)

	return T(encoded), err
}

// signalMemos infers the SDK's Memo result type from its method expression;
// that expression is never invoked. Passing its concrete result to newRuntime
// checks the full native interface, including Memo's exact result signature.
func signalMemos[T ~[]byte](raw *signalContext,
	_ func(hatchet.DurableContext, string, func() (any, error)) (T, error),
) signalMemoContext[T] {
	return signalMemoContext[T]{
		signalContext: raw,
	}
}

// TestSignalMemoAdapterUsesJSONV2 checks the exact SDK interface and promoted
// context dispatch. A literal less-than sign distinguishes v2's JSON encoding
// from the legacy serializer's default HTML escaping; a second call verifies
// replay retains those original bytes without invoking its callback again.
func TestSignalMemoAdapterUsesJSONV2(t *testing.T) {
	t.Parallel()

	raw := &signalContext{
		ctx:         t.Context(),
		executionID: "physical",
		attemptID:   "attempt",
	}

	var native hatchet.DurableContext = signalMemos(raw, hatchet.DurableContext.Memo)

	if native.WorkflowRunId() != raw.executionID || native.StepRunId() != raw.attemptID || native.Done() != raw.ctx.Done() {
		t.Fatal("memo adapter changed native context dispatch")
	}

	value, err := native.Memo("payload", func() (any, error) {
		return map[string]string{"value": "<"}, nil
	})

	if err != nil || string(value) != `{"value":"<"}` {
		t.Fatalf("v2 Memo = %s, %v", value, err)
	}

	replayed, err := native.Memo("payload", func() (any, error) {
		return nil, errors.New("memo callback replayed")
	})

	if err != nil || string(replayed) != string(value) || string(raw.memos["payload"]) != string(value) {
		t.Fatalf("replayed Memo = %s, %v; original = %s", replayed, err, value)
	}
}

// WaitFor scans newest-first like Hatchet's historical-event matching. The
// sentinel error exposes matching without fabricating an SDK WaitResult, whose
// representation is private. Timers are not advanced or claimed to have fired.
func (raw *signalContext) WaitFor(condition hatchet.Condition) (*hatchet.WaitResult, error) {
	raw.matched = wire{}

	for _, event := range condition.ToPB(0).UserEventConditions {
		raw.scope = event.GetEventScope()
		raw.since = event.ConsiderEventsSince.AsTime()

		for index := len(raw.be.pushed) - 1; index >= 0; index-- {
			pushed := raw.be.pushed[index]

			if pushed.key == event.UserEventKey && pushed.scope == raw.scope &&
				!pushed.at.Before(event.ConsiderEventsSince.AsTime()) {
				raw.matched = pushed.payload

				return nil, errors.New("historical event matched")
			}
		}
	}

	return nil, errors.New("no historical event")
}

// TestReusedLogicalIDDoesNotReceivePreviousExecutionSignal reclaims only the
// workflow idempotency record, keeping both the event and the longer-lived marker.
// It fails if historical matching adds any ownership rule absent from Hatchet.
func TestReusedLogicalIDDoesNotReceivePreviousExecutionSignal(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f := &fake{
			keys:      map[string]string{},
			runTTL:    11 * time.Second,
			markerTTL: 30 * 24 * time.Hour,
			details_: map[string]runDetail{"run-1": {
				Status: statusRunning,
			}, "run-3": {
				Status: statusRunning,
			}},
		}
		driver := startedDriver(t, f)

		if err := driver.Signal(t.Context(), "order", "o-1", "approved", []byte("true")); err != nil {
			t.Fatal(err)
		}

		detail := f.details_["run-1"]
		detail.Status = statusCompleted
		f.details_["run-1"] = detail

		time.Sleep(12 * time.Second)

		if err := driver.Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{}); err != nil {
			t.Fatal(err)
		}

		entry, _ := driver.cached(driver.runKey("order", "o-1"))

		if entry.runID != "run-3" {
			t.Fatalf("TTL did not create a second physical execution: %q", entry.runID)
		}

		raw := &signalContext{
			ctx:         t.Context(),
			executionID: entry.runID,
			be:          f,
		}
		runner := &Runner{
			driver: driver,
			logger: slog.Default(),
		}

		rt, err := newRuntime(runner, signalMemos(raw, hatchet.DurableContext.Memo), workflow.WorkflowHandler{
			Name: "order",
			Options: workflow.WorkflowOptions{
				MaxDuration: 10 * time.Second,
			},
		}, f.triggered[2])

		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(rt.close)

		_, err = rt.Wait("approved", time.Second)

		if err == nil || err.Error() != "wait: no historical event" {
			t.Fatalf("second execution Wait = %v; old approval matched", err)
		}
	})
}

// TestSignalAcceptedWhileQueuedSurvivesDelayedWorkerStart ages acknowledged
// events beyond the former one-minute cutoff before constructing any runtime.
// Both first invocation and replay must discover the original marker without
// moving the workflow deadline back to submission or changing durable memo keys.
func TestSignalAcceptedWhileQueuedSurvivesDelayedWorkerStart(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f := &fake{
			details_: map[string]runDetail{"run-1": {
				Status: statusQueued,
			}},
		}
		driver := startedDriver(t, f)

		for _, value := range []string{"first", "second"} {
			if err := driver.Signal(t.Context(), "order", "o-1", "approved", []byte(value)); err != nil {
				t.Fatal(err)
			}
		}

		time.Sleep(2 * time.Minute)

		workerStartedAt := time.Now()
		raw := &signalContext{
			ctx:         t.Context(),
			executionID: "run-1",
			be:          f,
		}
		runner := &Runner{
			driver: driver,
			logger: slog.Default(),
		}

		handler := workflow.WorkflowHandler{
			Name: "order",
			Options: workflow.WorkflowOptions{
				MaxDuration: time.Hour,
			},
		}

		for range 2 {
			rt, err := newRuntime(runner, signalMemos(raw, hatchet.DurableContext.Memo), handler, f.triggered[0])

			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(rt.close)

			if !rt.deadline.Equal(workerStartedAt.Add(time.Hour)) {
				t.Fatalf("queued time changed timeout origin: worker=%s deadline=%s", workerStartedAt, rt.deadline)
			}

			if _, err := rt.Wait("approved", time.Second); err == nil || err.Error() != "wait: historical event matched" {
				t.Fatalf("queued accepted signal Wait = %v", err)
			}

			if !raw.since.Equal(time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC)) {
				t.Fatalf("historical cutoff = %s; want fixed epoch", raw.since)
			}

			first, err := driver.firstValue(t.Context(), raw.matched.Marker,
				signalMarkerKey(raw.scope, "approved"), "run-1")

			if err != nil || string(first) != "first" {
				t.Fatalf("queued first value = %q, %v", first, err)
			}

			time.Sleep(time.Minute)
		}

		if len(raw.memos) != 2 || raw.memos["arkoren-start"] == nil || raw.memos["arkoren-left-1"] == nil {
			t.Fatalf("changed durable memo sequence: %v", raw.memos)
		}
	})
}

// TestRuntimeRejectsWorkerRelativeSignalProtocol prevents a version-one run
// from replaying with different serialized historical conditions. Rejection
// happens before any durable memo is accessed or application code can run.
func TestRuntimeRejectsWorkerRelativeSignalProtocol(t *testing.T) {
	t.Parallel()

	driver := newTestDriver(&fake{})
	runner := &Runner{
		driver: driver,
		logger: slog.Default(),
	}
	raw := &signalContext{
		ctx:         t.Context(),
		executionID: "physical",
	}

	rt, err := newRuntime(runner, signalMemos(raw, hatchet.DurableContext.Memo), workflow.WorkflowHandler{
		Name: "order",
	},
		wire{
			ID:            "o-1",
			Key:           driver.runKey("order", "o-1"),
			SignalVersion: 1,
		})

	if rt != nil {
		t.Cleanup(rt.close)
	}

	if !errors.Is(err, workflow.ErrNotFound) || len(raw.memos) != 0 {
		t.Fatalf("worker-relative protocol = %v; touched memos = %v", err, raw.memos)
	}
}

// TestNewExecutionFirstFalseDoesNotReuseOldTrueMarker ages the old event beyond
// the former worker-relative cutoff while keeping its marker within its TTL.
// Physical scope, not age, must isolate the second execution's fresh false.
func TestNewExecutionFirstFalseDoesNotReuseOldTrueMarker(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f := &fake{
			keys:      map[string]string{},
			runTTL:    11 * time.Second,
			markerTTL: 30 * 24 * time.Hour,
			details_: map[string]runDetail{"run-1": {
				Status: statusRunning,
			}, "run-3": {
				Status: statusRunning,
			}},
		}
		driver := startedDriver(t, f)

		if err := driver.Signal(t.Context(), "order", "o-1", "approved", []byte("true")); err != nil {
			t.Fatal(err)
		}

		detail := f.details_["run-1"]
		detail.Status = statusCompleted
		f.details_["run-1"] = detail

		time.Sleep(2 * time.Minute)

		if err := driver.Start(t.Context(), "order", "o-1", nil, workflow.StartOptions{}); err != nil {
			t.Fatal(err)
		}

		if err := driver.Signal(t.Context(), "order", "o-1", "approved", []byte("false")); err != nil {
			t.Fatal(err)
		}

		first, err := driver.firstValue(t.Context(), f.pushed[1].payload.Marker,
			signalMarkerKey(f.pushed[1].scope, "approved"), "run-3")

		if err != nil || string(first) != "false" {
			t.Fatalf("fresh event resolved %q, %v; want false with old marker retained", first, err)
		}
	})
}

// TestSignalConcurrentSendersKeepOnePhysicalFirstValue races independent values
// through the real marker-collision seam. Every accepted event must name the
// same verified marker and resolve the value that won its atomic insert.
func TestSignalConcurrentSendersKeepOnePhysicalFirstValue(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusRunning,
		}},
	}
	driver := startedDriver(t, f)

	var senders sync.WaitGroup

	for _, value := range []string{"true", "false", "other", "last"} {
		senders.Go(func() {
			if err := driver.Signal(t.Context(), "order", "o-1", "approved", []byte(value)); err != nil {
				t.Errorf("Signal(%s) = %v", value, err)
			}
		})
	}

	senders.Wait()

	if len(f.pushed) != 4 || len(f.triggered) != 2 {
		t.Fatalf("events = %d, executions = %d; want four events and one marker", len(f.pushed), len(f.triggered))
	}

	for _, event := range f.pushed {
		first, err := driver.firstValue(t.Context(), event.payload.Marker,
			signalMarkerKey(event.scope, "approved"), "run-1")

		if err != nil || string(first) != f.triggered[1].Data || event.payload.ExecutionID != "run-1" {
			t.Fatalf("event = %+v, first = %q, %v", event, first, err)
		}
	}
}

// TestSignalMarkerRejectsDifferentPhysicalOwner keeps the expected marker key
// but corrupts its stored physical owner. This covers both direct marker reads
// and an idempotency-collision response: neither may publish the foreign value.
func TestSignalMarkerRejectsDifferentPhysicalOwner(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusRunning,
		}},
	}
	driver := startedDriver(t, f, func(driver *DriverBuilder) {
		driver.LookupGrace(-1)
	})

	if err := driver.Signal(t.Context(), "order", "o-1", "approved", []byte("true")); err != nil {
		t.Fatal(err)
	}

	input := f.triggered[1]
	input.ExecutionID = "previous-physical"
	detail := f.details_["run-2"]

	encoded, err := json.Marshal(input)

	if err != nil {
		t.Fatal(err)
	}

	detail.Input = encoded
	f.details_["run-2"] = detail

	if err := driver.Signal(t.Context(), "order", "o-1", "approved", []byte("false")); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("foreign collision = %v", err)
	}

	if len(f.pushed) != 1 {
		t.Fatal("foreign collision published an event")
	}

	if _, err := driver.firstValue(t.Context(), "run-2", input.Key, "run-1"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("foreign read = %v", err)
	}
}

// TestSignalMarkerRejectsLegacyProtocol proves even a correctly scoped marker
// cannot omit the wire version. No fallback may expose retained legacy data.
func TestSignalMarkerRejectsLegacyProtocol(t *testing.T) {
	t.Parallel()

	f := &fake{}
	driver := newTestDriver(f, func(driver *DriverBuilder) {
		driver.LookupGrace(-1)
	})
	key := signalMarkerKey(signalScope(driver.runKey("order", "o-1"), "physical"), "approved")
	f.record(driver.signalWorkflow(), "marker", wire{
		Key:         key,
		ExecutionID: "physical",
		Data:        "secret",
	})

	if _, err := driver.firstValue(t.Context(), "marker", key, "physical"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("legacy marker = %v", err)
	}

	if _, err := driver.firstValue(t.Context(), "", key, "physical"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("empty marker = %v", err)
	}
}

// TestRuntimePhysicalSignalScopeSurvivesRestart reuses the durable memo with
// changing task-attempt IDs and a newly constructed runtime. Multiple waits and
// Select must keep the sender's exact scope; another signal keeps a distinct key.
// This is deterministic fake replay evidence, not evidence of engine eviction.
func TestRuntimePhysicalSignalScopeSurvivesRestart(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusRunning,
		}},
	}
	driver := startedDriver(t, f)

	if err := driver.Signal(t.Context(), "order", "o-1", "approved", []byte("false")); err != nil {
		t.Fatal(err)
	}

	raw := &signalContext{
		ctx:         t.Context(),
		executionID: "run-1",
		attemptID:   "attempt-1",
		be:          f,
	}
	runner := &Runner{
		driver: driver,
		logger: slog.Default(),
	}
	handler := workflow.WorkflowHandler{
		Name: "order",
		Options: workflow.WorkflowOptions{
			MaxDuration: time.Hour,
		},
	}

	for _, attempt := range []string{"attempt-1", "attempt-2"} {
		raw.attemptID = attempt

		rt, err := newRuntime(runner, signalMemos(raw, hatchet.DurableContext.Memo), handler, f.triggered[0])

		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(rt.close)

		if _, err := rt.Wait("approved", time.Second); err == nil || err.Error() != "wait: historical event matched" {
			t.Fatalf("fake replay Wait = %v", err)
		}

		if raw.scope != f.pushed[0].scope || raw.matched.Marker != f.pushed[0].payload.Marker {
			t.Fatalf("attempt %s changed scope: %q", attempt, raw.scope)
		}

		if _, _, err := rt.Select([]workflow.RawCase{{
			Signal: "approved",
		}}); err == nil || err.Error() != "wait: historical event matched" {
			t.Fatalf("fake replay Select = %v", err)
		}

		if raw.scope != f.pushed[0].scope {
			t.Fatalf("Select scope = %q", raw.scope)
		}
	}

	if err := driver.Signal(t.Context(), "order", "o-1", "rejected", []byte("different")); err != nil {
		t.Fatal(err)
	}

	if f.pushed[0].payload.Marker == f.pushed[1].payload.Marker {
		t.Fatal("different signal names shared a marker")
	}
}

// TestRuntimeRejectsPrePhysicalSignalProtocol fails before durable state access,
// even with a valid v2 logical key. The earlier namespace format is insufficient
// to make a logical-scope in-flight execution safe to replay after this upgrade.
func TestRuntimeRejectsPrePhysicalSignalProtocol(t *testing.T) {
	t.Parallel()

	driver := newTestDriver(&fake{})
	runner := &Runner{
		driver: driver,
	}

	_, err := newRuntime(runner, nil, workflow.WorkflowHandler{
		Name: "order",
	},
		wire{
			ID:  "o-1",
			Key: driver.runKey("order", "o-1"),
		})

	if !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("legacy runtime = %v", err)
	}
}

// TestPhysicalSignalScopeEncoding fixes the private version independently of its
// callers and covers ambiguous punctuation, signal-name, and physical-owner
// boundaries. Namespace separation remains inside the independently encoded key.
func TestPhysicalSignalScopeEncoding(t *testing.T) {
	t.Parallel()

	if got := signalScope("key", "run"); got != "arkoren.workflow.signal.v1.a2V5.cnVu" {
		t.Fatalf("physical scope = %q", got)
	}

	if got := signalMarkerKey(signalScope("key", "run"), "go"); got != "arkoren.workflow.signal.v1.a2V5.cnVu.Z28" {
		t.Fatalf("marker key = %q", got)
	}

	if signalScope("a.b", "c") == signalScope("a", "b.c") || signalScope("key", "run") == signalScope("key", "other") {
		t.Fatal("physical scope components collided")
	}
}

// TestSignalRejectsPrePhysicalProtocol checks the sender side of the upgrade
// boundary for v2 logical keys. Queryable legacy input must not accept a new
// physical-protocol event that its old worker would never consume safely.
func TestSignalRejectsPrePhysicalProtocol(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusRunning,
		}},
	}
	driver := startedDriver(t, f)

	input := f.triggered[0]
	input.SignalVersion = 0

	encoded, err := json.Marshal(input)

	if err != nil {
		t.Fatal(err)
	}

	detail := f.details_["run-1"]
	detail.Input = encoded
	f.details_["run-1"] = detail

	if err := driver.Signal(t.Context(), "order", "o-1", "approved", []byte("true")); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("legacy Signal = %v", err)
	}

	if len(f.pushed) != 0 || len(f.triggered) != 1 {
		t.Fatal("legacy signal created a marker or event")
	}
}

// TestSignalRejectsWorkerRelativeSignalProtocol prevents upgraded senders from
// acknowledging data for a version-one worker that still excludes queued events.
// The older run stays queryable, but rejection must precede marker/event writes.
func TestSignalRejectsWorkerRelativeSignalProtocol(t *testing.T) {
	t.Parallel()

	f := &fake{
		details_: map[string]runDetail{"run-1": {
			Status: statusQueued,
		}},
	}
	driver := startedDriver(t, f)

	input := f.triggered[0]
	input.SignalVersion = 1

	encoded, err := json.Marshal(input)

	if err != nil {
		t.Fatal(err)
	}

	detail := f.details_["run-1"]
	detail.Input = encoded
	f.details_["run-1"] = detail

	if err := driver.Signal(t.Context(), "order", "o-1", "approved", []byte("first")); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("worker-relative protocol Signal = %v", err)
	}

	if len(f.pushed) != 0 || len(f.triggered) != 1 {
		t.Fatal("incompatible signal created a marker or event")
	}
}
