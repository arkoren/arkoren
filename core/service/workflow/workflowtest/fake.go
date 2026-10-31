package workflowtest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"

	"arkoren.dev/core/service/workflow"
)

// StartCall is one recorded [workflow.Driver.Start].
type StartCall struct {
	// Name is the workflow name.
	Name string

	// ID is the run identifier.
	ID string

	// Input is the encoded input, as produced by [workflow.EncodeValue].
	Input []byte

	// Options are the start options.
	Options workflow.StartOptions
}

// SignalCall is one recorded [workflow.Driver.Signal].
type SignalCall struct {
	// Name is the workflow name.
	Name string

	// ID is the run identifier.
	ID string

	// Signal is the signal name.
	Signal string

	// Value is the encoded value.
	Value []byte
}

// CancelCall is one recorded [workflow.Driver.Cancel].
type CancelCall struct {
	// Name is the workflow name.
	Name string

	// ID is the run identifier.
	ID string
}

// runKey identifies a run.
type runKey struct {
	// name identifies the workflow associated with the run.
	name string

	// id distinguishes runs within that workflow.
	id string
}

// outcome is the scripted end of a run.
type outcome struct {
	// status is the terminal state to apply when the script is consumed.
	status workflow.Status

	// output is the encoded successful result, absent for failed runs.
	output []byte

	// err is the scripted failure, absent for successful runs.
	err error
}

// fakeRun is the state of one started run.
type fakeRun struct {
	// status starts Running and becomes terminal when end closes done.
	status workflow.Status

	// output stores the encoded successful result for Result callers.
	output []byte

	// err stores the failure or cancellation reported by Result.
	err error

	// done is closed when the run ends.
	done chan struct{}
}

// Fake is a [workflow.Driver] for unit tests. It records the calls of the
// code under test and never executes workflow bodies: a run stays Running
// until the test ends it with [Fake.Complete], [Fake.Fail], or a cancel.
// Payloads use the default [workflow.JSON] codec.
//
// Fake follows the contract of the driver for the conditions that do not need
// a body: a second Start of an id, finished or not, returns
// [workflow.ErrAlreadyStarted], and unknown runs return [workflow.ErrNotFound].
// Every workflow name is considered registered. Because it cannot execute
// bodies, Fake does not run the [Run] suite.
//
// A Fake is safe for concurrent use. Create it with [New].
type Fake struct {
	// mu guards run state, scripts, call histories, and injected failures.
	mu sync.Mutex

	// runs contains every started run until Reset.
	runs map[runKey]*fakeRun

	// scripted holds outcomes supplied before the corresponding Start.
	scripted map[runKey]outcome

	// starts records successful Start calls in arrival order.
	starts []StartCall

	// signals records successful Signal calls in arrival order.
	signals []SignalCall

	// cancels records successful Cancel calls in arrival order.
	cancels []CancelCall

	// failWith is the persistent failure returned by driver operations.
	failWith error

	// failNext overrides failWith once before being cleared.
	failNext error
}

var _ workflow.Driver = (*Fake)(nil)

// New returns an empty Fake.
//
// # Examples
//
//	fake := workflowtest.New()
//	client := workflow.NewClient(fake)
//	run, err := client.Start(ctx, Onboard, "user-42", user)
//	fake.AssertStarted(t, Onboard, func(in User) bool {
//		return in.ID == 42
//	})
func New() *Fake {
	return &Fake{
		runs:     map[runKey]*fakeRun{},
		scripted: map[runKey]outcome{},
	}
}

// FailWith makes every following driver call return err, until FailWith(nil).
// Failed calls are not recorded.
func (fake *Fake) FailWith(err error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.failWith = err
}

// FailNext makes the next driver call return err. It takes precedence over
// [Fake.FailWith] for that call.
func (fake *Fake) FailNext(err error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.failNext = err
}

// Reset forgets every run, recorded call, scripted outcome, and injected
// failure. Callers blocked in Result stay blocked until their context ends.
func (fake *Fake) Reset() {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.runs = map[runKey]*fakeRun{}
	fake.scripted = map[runKey]outcome{}
	fake.starts, fake.signals, fake.cancels = nil, nil, nil
	fake.failWith, fake.failNext = nil, nil
}

// Started returns the recorded Start calls in order.
// The returned slice and Options.Trace maps are detached snapshots; editing
// propagation fields cannot change retained observations or another snapshot.
// Other record members retain their existing shallow-copy ownership.
//
//	calls := fake.Started()
//	if len(calls) != 0 && calls[0].Options.Trace != nil {
//		calls[0].Options.Trace["traceparent"] = "local-edit"
//	}
func (fake *Fake) Started() []StartCall {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	calls := slices.Clone(fake.starts)

	for index := range calls {
		calls[index].Options.Trace = maps.Clone(calls[index].Options.Trace)
	}

	return calls
}

// Signaled returns the recorded Signal calls in order.
func (fake *Fake) Signaled() []SignalCall {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	return slices.Clone(fake.signals)
}

// Canceled returns the recorded Cancel calls in order.
func (fake *Fake) Canceled() []CancelCall {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	return slices.Clone(fake.cancels)
}

// Complete ends run id of workflow name with out, encoded like the output of a
// real run, so [workflow.Run.Result] returns it. Completing a run that was
// not started yet scripts it: the later Start returns a run that is already
// completed. Completing a finished run changes nothing. It panics when out
// cannot be encoded.
//
// # Examples
//
//	fake.Complete(Onboard.Name(), "user-42", "welcome")
func (fake *Fake) Complete[Out any](name, id string, out Out) {
	raw, err := workflow.EncodeValue(nil, out)

	if err != nil {
		panic(fmt.Errorf("workflowtest: complete %s %s: %w", name, id, err))
	}

	fake.finish(runKey{
		name: name,
		id:   id,
	}, outcome{
		status: workflow.Completed,
		output: raw,
	})
}

// Fail ends run id of workflow name with err. The error crosses the wire
// encoding like a real failure, so only its kind, message, and permanent mark
// survive: use [workflow.NewError] for errors the code under test recognizes
// with [errors.Is]. An error that matches [workflow.ErrCanceled] ends the run
// as canceled. Like [Fake.Complete], it scripts a run that was not started.
// It panics when err is nil, because a failed run without an error would make
// Result report success.
//
// # Examples
//
//	fake.Fail(Onboard.Name(), "user-42", workflow.NewError("card-declined", "declined"))
func (fake *Fake) Fail(name, id string, err error) {
	if err == nil {
		panic(fmt.Errorf("workflowtest: fail %s %s: nil error", name, id))
	}

	status := workflow.Failed

	if errors.Is(err, workflow.ErrCanceled) {
		status = workflow.Canceled
	}

	fake.finish(runKey{
		name: name,
		id:   id,
	}, outcome{
		status: status,
		err:    workflow.DecodeError(workflow.EncodeError(err)),
	})
}

// finish ends the run, or scripts the outcome when the run does not exist.
func (fake *Fake) finish(key runKey, result outcome) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	run, ok := fake.runs[key]

	if !ok {
		fake.scripted[key] = result

		return
	}

	run.end(result)
}

// end records the outcome of a running run and wakes its waiters.
func (run *fakeRun) end(result outcome) {
	if run.status != workflow.Running {
		return
	}

	run.status, run.output, run.err = result.status, result.output, result.err
	close(run.done)
}

// injected returns the failure to inject into the current call, if any. The
// caller holds the lock.
func (fake *Fake) injected() error {
	if err := fake.failNext; err != nil {
		fake.failNext = nil

		return err
	}

	return fake.failWith
}

// lookup returns the run, or the injected failure or [workflow.ErrNotFound].
// The caller holds the lock.
func (fake *Fake) lookup(key runKey) (*fakeRun, error) {
	if err := fake.injected(); err != nil {
		return nil, err
	}

	run, ok := fake.runs[key]

	if !ok {
		return nil, fmt.Errorf("run %s %s: %w", key.name, key.id, workflow.ErrNotFound)
	}

	return run, nil
}

// Start records the call and creates a running run, or one that is already
// finished when its outcome was scripted. It returns an error wrapping the
// context error when ctx is already done.
func (fake *Fake) Start(ctx context.Context, name, id string, in []byte, o workflow.StartOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start %s %s: %w", name, id, err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	if err := fake.injected(); err != nil {
		return err
	}

	key := runKey{
		name: name,
		id:   id,
	}

	switch {
	case id == "":
		return fmt.Errorf("start %s: %w", name, workflow.ErrInvalidID)
	case fake.runs[key] != nil:
		return fmt.Errorf("start %s %s: %w", name, id, workflow.ErrAlreadyStarted)
	}

	run := &fakeRun{
		status: workflow.Running,
		done:   make(chan struct{}),
	}
	fake.runs[key] = run

	if result, ok := fake.scripted[key]; ok {
		delete(fake.scripted, key)
		run.end(result)
	}

	options := o
	options.Metadata = maps.Clone(o.Metadata)
	options.Trace = maps.Clone(o.Trace)
	fake.starts = append(fake.starts, StartCall{
		Name:    name,
		ID:      id,
		Input:   slices.Clone(in),
		Options: options,
	})

	return nil
}

// Result blocks until the run is ended by the test or ctx is done. A run that
// has already ended wins over a done ctx. The output is a copy.
func (fake *Fake) Result(ctx context.Context, name, id string) ([]byte, error) {
	fake.mu.Lock()

	run, err := fake.lookup(runKey{
		name: name,
		id:   id,
	})
	fake.mu.Unlock()

	if err != nil {
		return nil, err
	}

	select {
	case <-run.done:
	default:
		select {
		case <-run.done:
		case <-ctx.Done():
			return nil, fmt.Errorf("result %s %s: %w", name, id, ctx.Err())
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	return slices.Clone(run.output), run.err
}

// Status returns the state of the run.
func (fake *Fake) Status(_ context.Context, name, id string) (workflow.Status, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	run, err := fake.lookup(runKey{
		name: name,
		id:   id,
	})

	if err != nil {
		return "", err
	}

	return run.status, nil
}

// Cancel records the call and ends a running run as canceled. A finished run
// is left as it is.
func (fake *Fake) Cancel(_ context.Context, name, id string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	run, err := fake.lookup(runKey{
		name: name,
		id:   id,
	})

	if err != nil {
		return err
	}

	fake.cancels = append(fake.cancels, CancelCall{
		Name: name,
		ID:   id,
	})
	run.end(outcome{
		status: workflow.Canceled,
		err:    workflow.DecodeError(workflow.EncodeError(workflow.ErrCanceled)),
	})

	return nil
}

// Signal records the call. It returns [workflow.ErrNotFound] for an unknown or
// finished run.
func (fake *Fake) Signal(_ context.Context, name, id, signal string, v []byte) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	run, err := fake.lookup(runKey{
		name: name,
		id:   id,
	})

	if err != nil {
		return err
	}

	if run.status != workflow.Running {
		return fmt.Errorf("signal %s %s: run has ended: %w", name, id, workflow.ErrNotFound)
	}

	fake.signals = append(fake.signals, SignalCall{
		Name:   name,
		ID:     id,
		Signal: signal,
		Value:  slices.Clone(v),
	})

	return nil
}

// matchingStarts returns the calls of name whose input decodes as In and satisfies
// every predicate.
func matchingStarts[In any](calls []StartCall, name string, preds []func(In) bool) []StartCall {
	return slices.DeleteFunc(slices.Clone(calls), func(call StartCall) bool {
		if call.Name != name {
			return true
		}

		var in In

		if err := workflow.DecodeValue(nil, call.Input, &in); err != nil {
			return true
		}

		return !all(in, preds)
	})
}

// all reports whether v satisfies every predicate.
func all[T any](v T, preds []func(T) bool) bool {
	return !slices.ContainsFunc(preds, func(pred func(T) bool) bool {
		return !pred(v)
	})
}

// AssertStarted fails the test unless a run of workflow w was started whose
// input satisfies every predicate. Without predicates any start of w matches.
// The input type comes from w, so the predicates are type checked.
//
// # Examples
//
//	fake.AssertStarted(t, Onboard, func(in User) bool {
//		return in.ID == 42
//	})
func (fake *Fake) AssertStarted[In, Out any](t testing.TB, w *workflow.Workflow[In, Out], preds ...func(In) bool) {
	t.Helper()

	if len(matchingStarts(fake.Started(), w.Name(), preds)) == 0 {
		t.Errorf("workflow %q was not started with a matching input; started: %s", w.Name(), describeStarts(fake.Started()))
	}
}

// AssertNotStarted fails the test when a run of workflow w was started whose
// input satisfies every predicate. Without predicates any start of w fails
// the test.
//
// # Examples
//
//	fake.AssertNotStarted(t, Onboard)
func (fake *Fake) AssertNotStarted[In, Out any](t testing.TB, w *workflow.Workflow[In, Out], preds ...func(In) bool) {
	t.Helper()

	if matched := matchingStarts(fake.Started(), w.Name(), preds); len(matched) > 0 {
		t.Errorf("workflow %q was started %d time(s) with a matching input, want none", w.Name(), len(matched))
	}
}

// AssertStartedCount fails the test unless workflow w was started exactly n
// times.
//
// # Examples
//
//	fake.AssertStartedCount(t, Onboard, 2)
func (fake *Fake) AssertStartedCount[In, Out any](t testing.TB, w *workflow.Workflow[In, Out], n int) {
	t.Helper()

	got := len(matchingStarts[any](fake.Started(), w.Name(), nil))

	if got != n {
		t.Errorf("workflow %q was started %d time(s), want %d", w.Name(), got, n)
	}
}

// AssertNothingStarted fails the test when any workflow was started.
func (fake *Fake) AssertNothingStarted(t testing.TB) {
	t.Helper()

	if starts := fake.Started(); len(starts) > 0 {
		t.Errorf("want no workflow started; started: %s", describeStarts(starts))
	}
}

// AssertSignaled fails the test unless run id of workflow w received signal
// with a value that satisfies every predicate.
//
// # Examples
//
//	fake.AssertSignaled(t, Onboard, "user-42", EmailConfirmed, func(token string) bool {
//		return token != ""
//	})
func (fake *Fake) AssertSignaled[In, Out, T any](t testing.TB, w *workflow.Workflow[In, Out], id string, signal workflow.Signal[T], preds ...func(T) bool) {
	t.Helper()

	for _, call := range fake.Signaled() {
		if call.Name != w.Name() || call.ID != id || call.Signal != signal.Name() {
			continue
		}

		var value T

		if workflow.DecodeValue(nil, call.Value, &value) == nil && all(value, preds) {
			return
		}
	}

	t.Errorf("run %s %s did not receive a matching signal %q", w.Name(), id, signal.Name())
}

// AssertCanceled fails the test unless run id of workflow w was canceled.
//
// # Examples
//
//	fake.AssertCanceled(t, Onboard, "user-42")
func (fake *Fake) AssertCanceled[In, Out any](t testing.TB, w *workflow.Workflow[In, Out], id string) {
	t.Helper()

	if !slices.Contains(fake.Canceled(), CancelCall{
		Name: w.Name(),
		ID:   id,
	}) {
		t.Errorf("run %s %s was not canceled", w.Name(), id)
	}
}

// describeStarts lists the started runs for failure messages.
func describeStarts(calls []StartCall) string {
	if len(calls) == 0 {
		return "none"
	}

	described := make([]string, len(calls))

	for i, call := range calls {
		described[i] = fmt.Sprintf("%s/%s(%s)", call.Name, call.ID, call.Input)
	}

	return fmt.Sprint(described)
}
