// Package execution owns deterministic Temporal workflow execution.
package execution

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	tworkflow "go.temporal.io/sdk/workflow"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/temporal/temporalworkflow/internal/protocol"
)

// runtime is the [workflow.Runtime] over a Temporal workflow context.
type runtime struct {
	// trace is forwarded as deterministic data, never extracted during replay.
	trace map[string]string
	// ctx is the context of the run. It is canceled by a cancel request and
	// by the MaxDuration timer.
	ctx tworkflow.Context

	// environment supplies the registry and native names without a Runner.
	environment Environment

	// handler is the workflow being run.
	handler workflow.WorkflowHandler

	// logger is the replay-aware logger of the run.
	logger *slog.Logger

	// done is closed once a coroutine of the run observed the cancellation of
	// ctx.
	done chan struct{}

	// closeDone closes done once.
	closeDone sync.Once

	// timedOut records that the MaxDuration timer canceled ctx. Only
	// workflow coroutines touch it, and Temporal runs one at a time.
	timedOut bool

	// uuids counts the UUIDs handed out.
	uuids int
}

var _ workflow.Runtime = (*runtime)(nil)

// newRuntime wraps parent and starts two coroutines: one cancels the run when
// MaxDuration passes, the other closes Done when the run is canceled. The
// returned function stops the timer and must be called when the workflow
// function returns.
func newRuntime(parent tworkflow.Context, environment Environment, handler workflow.WorkflowHandler) (*runtime, func()) {
	ctx, cancelRun := tworkflow.WithCancel(parent)
	timerCtx, cancelTimer := tworkflow.WithCancel(ctx)
	deadline := tworkflow.NewTimer(timerCtx, handler.Options.MaxDuration)

	rt := &runtime{
		ctx:         ctx,
		environment: environment,
		handler:     handler,
		logger:      newLogger(tworkflow.GetLogger(parent)),
		done:        make(chan struct{}),
	}

	tworkflow.Go(ctx, func(gctx tworkflow.Context) {
		ctx.Done().Receive(gctx, nil)
		rt.closeDone.Do(func() {
			close(rt.done)
		})
	})

	tworkflow.Go(ctx, func(gctx tworkflow.Context) {
		if err := deadline.Get(gctx, nil); err == nil {
			rt.timedOut = true

			cancelRun()
		}
	})

	return rt, cancelTimer
}

// ID returns the workflow ID.
// It is the facade's persisted run identifier, not Temporal's native run UUID.
//
// Example:
//
//	id := execution.ID()
//	_ = id // Use this stable value when deriving an idempotent child ID.
func (execution *runtime) ID() string {
	return tworkflow.GetInfo(execution.ctx).WorkflowExecution.ID
}

// Name returns the workflow name.
// This is the registry identity, without the native workflow-type prefix.
//
// Example:
//
//	name := execution.Name()
//	_ = name // "onboard", not "arkoren-wf-onboard".
func (execution *runtime) Name() string {
	return execution.handler.Name
}

// Logger returns the logger of the run. Temporal drops its lines while the
// workflow replays.
//
// Example:
//
//	execution.Logger().InfoContext(context.Background(), "decision recorded", "accepted", true)
func (execution *runtime) Logger() *slog.Logger {
	return execution.logger
}

// Raw returns the [tworkflow.Context] of the run.
// Native operations must preserve Temporal's deterministic workflow rules.
//
// Example:
//
//	native := execution.Raw().(tworkflow.Context)
//	_ = tworkflow.Now(native)
func (execution *runtime) Raw() any {
	return execution.ctx
}

// Now returns the workflow time, which is the same on replays.
// Use it instead of wall time for deterministic orchestration decisions.
//
// Example:
//
//	startedAt := execution.Now()
//	if err := execution.Sleep(time.Second); err != nil {
//		return err
//	}
//	_ = execution.Now().Sub(startedAt)
func (execution *runtime) Now() time.Time {
	return tworkflow.Now(execution.ctx)
}

// UUID returns a UUID derived from the run ID of the execution and a counter.
// It is the same on replays and needs no history event.
//
// Example:
//
//	childID := execution.UUID()
//	_ = childID // A later call advances the deterministic per-execution counter.
func (execution *runtime) UUID() string {
	execution.uuids++

	sum := sha256.Sum256(fmt.Appendf(nil, "%s/%s/%d", tworkflow.GetInfo(execution.ctx).WorkflowExecution.RunID, execution.ID(), execution.uuids))
	raw := sum[:16]

	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:])
}

// Done is closed once the run observed its cancellation or the MaxDuration
// timer: when a runtime call or [runtime.Err] sees the context done, or when
// a coroutine of the runtime that waits on the Temporal channel gets to run.
// Both happen at deterministic points of the history. It is a native channel,
// so a workflow function must only poll it, never block on it with a native
// select, which Temporal cannot schedule.
//
// Example:
//
//	select {
//	case <-execution.Done():
//		return execution.Err()
//	default:
//		// Continue deterministic work without blocking a native Go channel.
//	}
func (execution *runtime) Done() <-chan struct{} {
	return execution.done
}

// Err returns why the run stopped, or nil. It reads the Temporal context
// directly, which is deterministic, so it is accurate before Done is
// closed.
//
// Example:
//
//	if err := execution.Err(); err != nil {
//		return err
//	}
func (execution *runtime) Err() error {
	return execution.cause()
}

// cause returns the reason the context of the run is done, or nil.
func (execution *runtime) cause() error {
	if execution.ctx.Err() == nil {
		return nil
	}

	execution.closeDone.Do(func() {
		close(execution.done)
	})

	if execution.timedOut {
		return fmt.Errorf("%w: max duration %s exceeded", workflow.ErrTimeout, execution.handler.Options.MaxDuration)
	}

	return fmt.Errorf("%w: workflow canceled", workflow.ErrCanceled)
}

// convert maps an error of a Temporal call to a workflow error. A
// cancellation of the run reports the reason of the run.
func (execution *runtime) convert(err error) error {
	if err == nil {
		return nil
	}

	var canceled *temporal.CanceledError

	if errors.As(err, &canceled) {
		if cause := execution.cause(); cause != nil {
			return cause
		}
	}

	return protocol.FromTemporal(err)
}

// awaitFuture separates a caller's cancellation budget from native future
// completion. SDK Future.Get itself does not cancel its wait; some child and
// detached-start futures remain pending after the parent context ends. Await
// is deterministic, produces no history commands, and returns on cancellation.
// Get is called only once ready, so it cannot extend the caller's wait.
func (execution *runtime) awaitFuture(future tworkflow.Future, out any) error {
	if err := tworkflow.Await(execution.ctx, future.IsReady); err != nil {
		return execution.convert(err)
	}

	return execution.convert(future.Get(execution.ctx, out))
}

// fail turns the error of the workflow function into the Temporal failure of
// the run. A canceled run ends as canceled; any other failure carries its
// envelope.
func (execution *runtime) fail(err error) error {
	if errors.Is(err, workflow.ErrCanceled) && !execution.timedOut && execution.ctx.Err() != nil {
		return temporal.NewCanceledError()
	}

	return protocol.Failure(err)
}

// Sleep pauses the run with a durable timer. Nonpositive durations return
// without a delay; cancellation and MaxDuration are checked before scheduling.
//
// Example:
//
//	if err := execution.Sleep(time.Minute); err != nil {
//		return err
//	}
func (execution *runtime) Sleep(d time.Duration) error {
	if cause := execution.cause(); cause != nil {
		return cause
	}

	return execution.convert(tworkflow.Sleep(execution.ctx, max(d, 0)))
}

// Wait waits for the signal and an optional positive timeout. Zero or negative
// timeout has no local timer; parent cancellation and MaxDuration still apply.
//
// Example:
//
//	value, err := execution.Wait("approved", time.Minute)
//	if err != nil {
//		return nil, err
//	}
//	_ = value // Encoded signal value.
func (execution *runtime) Wait(signal string, timeout time.Duration) ([]byte, error) {
	cases := []workflow.RawCase{{
		Signal: signal,
	}}

	if timeout > 0 {
		cases = append(cases, workflow.RawCase{
			After: timeout,
		})
	}

	index, value, err := execution.await(cases)

	if err != nil {
		return nil, err
	}

	if index != 0 {
		return nil, fmt.Errorf("%w: no %s signal within %s", workflow.ErrTimeout, signal, timeout)
	}

	return value, nil
}

// Select waits for the first signal or timer of cases, returning the winning
// index and any encoded signal value. Parent cancellation ends the wait even
// when no signal or timer is ready; losing timers are canceled.
//
// Example:
//
//	index, value, err := execution.Select([]workflow.RawCase{
//		{Signal: "approved"},
//		{After: time.Minute},
//	})
//	if err != nil {
//		return nil, err
//	}
//	_, _ = index, value
func (execution *runtime) Select(cases []workflow.RawCase) (int, []byte, error) {
	return execution.await(cases)
}

// await runs a Temporal selector over the signals and timers of cases and the
// cancellation of the run. The selector consumes a signal only when its case
// wins, and the timers that lose are canceled. Signals that no case
// consumed stay buffered in their channel.
func (execution *runtime) await(cases []workflow.RawCase) (int, []byte, error) {
	if cause := execution.cause(); cause != nil {
		return -1, nil, cause
	}

	timerCtx, cancelTimers := tworkflow.WithCancel(execution.ctx)
	defer cancelTimers()

	var (
		chosen   = -1
		value    protocol.Payload
		timerErr error
		stopped  bool
	)

	selector := tworkflow.NewSelector(execution.ctx)

	for index, option := range cases {
		if option.Signal != "" {
			selector.AddReceive(tworkflow.GetSignalChannel(execution.ctx, execution.environment.SignalName(option.Signal)), func(channel tworkflow.ReceiveChannel, _ bool) {
				chosen = index

				channel.Receive(execution.ctx, &value)
			})

			continue
		}

		selector.AddFuture(tworkflow.NewTimer(timerCtx, max(option.After, 0)), func(future tworkflow.Future) {
			chosen = index
			timerErr = future.Get(execution.ctx, nil)
		})
	}

	selector.AddReceive(execution.ctx.Done(), func(tworkflow.ReceiveChannel, bool) {
		stopped = true
	})

	selector.Select(execution.ctx)

	if stopped {
		return -1, nil, orCanceled(execution.cause())
	}

	if timerErr != nil {
		return -1, nil, execution.convert(timerErr)
	}

	return chosen, value, nil
}

// orCanceled returns cause, or [workflow.ErrCanceled] when the context is
// done but the cause is unknown.
func orCanceled(cause error) error {
	if cause == nil {
		return workflow.ErrCanceled
	}

	return cause
}

// Step runs a registered step as a Temporal activity. Attempts, Initial,
// Factor, and Max become the retry policy and Timeout becomes the
// start-to-close timeout of each attempt. An unlimited attempt (Timeout zero)
// uses only the workflow's MaxDuration as its schedule-to-close bound, because
// Temporal requires at least one activity timeout. The workflow timer remains
// authoritative if some of that duration has already elapsed.
// A completed per-call policy with multiple attempts and zero Initial returns
// workflow.ErrUnsupported before encoding or scheduling any activity. A zero
// initial interval is immaterial for a single attempt; native defaults must not
// silently replace an observable immediate retry. Registry-name and run-state
// errors retain precedence. See the package rollout caveat for existing histories.
//
// Example:
//
//	output, err := execution.Step("charge", []byte(`{"v":"order-42"}`), workflow.StepOptions{
//		Attempts: 1,
//		Timeout:  time.Minute,
//	})
//	if err != nil {
//		return nil, err
//	}
//	_ = output // Encoded step result.
func (execution *runtime) Step(name string, in []byte, policy workflow.StepOptions) ([]byte, error) {
	if _, ok := execution.environment.Registry.Step(name); !ok {
		return nil, fmt.Errorf("step %s is not registered: %w", name, workflow.ErrNotFound)
	}

	if cause := execution.cause(); cause != nil {
		return nil, cause
	}

	policy.Attempts = max(policy.Attempts, 1)

	if policy.Attempts > 1 && policy.Initial == 0 {
		return nil, fmt.Errorf("step %s: immediate retries are unsupported: %w", name, workflow.ErrUnsupported)
	}

	options := tworkflow.ActivityOptions{
		StartToCloseTimeout: policy.Timeout,
		RetryPolicy:         retryPolicy(policy),
	}

	if policy.Timeout == 0 {
		options.ScheduleToCloseTimeout = execution.handler.Options.MaxDuration
	}

	ctx := tworkflow.WithActivityOptions(execution.ctx, options)

	var out protocol.Payload

	input, err := protocol.EncodeInput(protocol.Payload(in), execution.trace)

	if err != nil {
		return nil, err
	}

	future := tworkflow.ExecuteActivity(ctx, execution.environment.StepType(name), input)

	if err := execution.awaitFuture(future, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// childOptions returns the options of a child run of workflow name, or an
// error when the registry lacks it.
func (execution *runtime) childOptions(name, id string, policy enumspb.ParentClosePolicy) (tworkflow.ChildWorkflowOptions, error) {
	handler, ok := execution.environment.Registry.Workflow(name)

	if !ok {
		return tworkflow.ChildWorkflowOptions{}, fmt.Errorf("workflow %s is not registered: %w", name, workflow.ErrNotFound)
	}

	return tworkflow.ChildWorkflowOptions{
		WorkflowID:               id,
		WorkflowExecutionTimeout: handler.Options.MaxDuration,
		WorkflowIDReusePolicy:    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		ParentClosePolicy:        policy,
		WaitForCancellation:      true,
	}, nil
}

// Child runs a child workflow and waits for its result. Parent cancellation
// requests cancellation of a newly owned child, but ends the caller's wait
// independently of native child completion. An existing run is joined through
// an activity; canceling that observation does not cancel the existing run.
// Temporal's atomic native start acknowledgement decides ownership, including
// after replay; no preliminary lookup or additional version marker is needed.
//
// Example:
//
//	output, err := execution.Child("fulfill", "fulfill-42", []byte(`{"v":"order-42"}`))
//	if err != nil {
//		return nil, err
//	}
//	_ = output // Encoded child result, including an already existing run.
func (execution *runtime) Child(name, id string, in []byte) ([]byte, error) {
	options, err := execution.childOptions(name, id, enumspb.PARENT_CLOSE_POLICY_TERMINATE)

	if err != nil {
		return nil, err
	}

	if cause := execution.cause(); cause != nil {
		return nil, cause
	}

	ctx := tworkflow.WithChildOptions(execution.ctx, options)

	var out protocol.Payload

	input, err := protocol.EncodeInput(protocol.Payload(in), execution.trace)

	if err != nil {
		return nil, err
	}

	future := tworkflow.ExecuteChildWorkflow(ctx, execution.environment.WorkflowType(name), input)
	err = execution.awaitFuture(future, &out)

	if err == nil {
		return out, nil
	}

	if !errors.Is(err, workflow.ErrAlreadyStarted) {
		return nil, err
	}

	return execution.join(id, options.WorkflowExecutionTimeout)
}

// join waits for the existing run id with an activity.
func (execution *runtime) join(id string, limit time.Duration) ([]byte, error) {
	if cause := execution.cause(); cause != nil {
		return nil, cause
	}

	ctx := tworkflow.WithActivityOptions(execution.ctx, tworkflow.ActivityOptions{
		StartToCloseTimeout: limit + time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	var out protocol.Payload

	future := tworkflow.ExecuteActivity(ctx, execution.environment.JoinType(), id)

	if err := execution.awaitFuture(future, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// Spawn starts a child workflow that outlives the parent (parent close policy
// abandon) and waits only until Temporal started it. The child runs on a
// context disconnected from the parent, so canceling the parent does not
// cancel it. The acknowledgement wait still ends on the parent's cancellation
// or MaxDuration; an error then does not prove the detached child was not started.
//
// Example:
//
//	err := execution.Spawn("receipt", "receipt-42", []byte(`{"v":"order-42"}`))
//	if err != nil && !errors.Is(err, workflow.ErrAlreadyStarted) {
//		return err
//	}
func (execution *runtime) Spawn(name, id string, in []byte) error {
	options, err := execution.childOptions(name, id, enumspb.PARENT_CLOSE_POLICY_ABANDON)

	if err != nil {
		return err
	}

	if cause := execution.cause(); cause != nil {
		return cause
	}

	detached, _ := tworkflow.NewDisconnectedContext(execution.ctx) //nolint:govet // canceling it would cancel the child

	ctx := tworkflow.WithChildOptions(detached, options)

	input, err := protocol.EncodeInput(protocol.Payload(in), execution.trace)

	if err != nil {
		return err
	}

	future := tworkflow.ExecuteChildWorkflow(ctx, execution.environment.WorkflowType(name), input)

	return execution.awaitFuture(future.GetChildWorkflowExecution(), nil)
}
