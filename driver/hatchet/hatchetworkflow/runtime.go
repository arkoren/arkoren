package hatchetworkflow

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"arkoren.dev/core/service/workflow"
	native "arkoren.dev/driver/hatchet/hatchetworkflow/internal/execution"
)

// runtime implements [workflow.Runtime] over a Hatchet durable context. The
// workflow function may run again after an eviction, so every value that a
// decision depends on goes through the durable memo of the context.
type runtime struct {
	// trace is deterministic wire data forwarded to actual task attempts.
	trace map[string]string
	// runner provides registered steps and the driver associated with this
	// execution.
	runner *Runner
	// raw is the native durable context whose memo survives worker restoration.
	raw hatchet.DurableContext
	// name identifies the application definition represented by this record.
	name string
	// id is the logical application run identifier, independent of the physical
	// engine execution.
	id string
	// key is the encoded namespaced logical identity validated before application
	// code accesses durable state.
	key string
	// logger receives operational records without changing the engine's execution
	// state.
	logger *slog.Logger

	// deadline adds Delay and MaxDuration to the memoized first worker time,
	// never submission or signal acceptance. It remains unchanged on replay.
	deadline time.Time

	// ctx carries execution cancellation and the effective run deadline.
	ctx context.Context //nolint:containedctx // Done and Err of the run
	// cancel releases the derived context when the owning operation ends.
	cancel context.CancelFunc

	// mutex serializes access to the mutable state below it.
	mutex sync.Mutex
	// nows allocates replay-stable memo keys for successive Now calls.
	nows int
	// selects allocates replay-stable memo keys for successive waits.
	selects int
	// uuids allocates replay-stable memo keys for successive UUID calls.
	uuids int
}

var _ workflow.Runtime = (*runtime)(nil)

// memoPanic is the panic of a clock or identifier that the durable memo could
// not provide. A local fallback would differ on replay, so the run stops.
type memoPanic struct {
	// err is the durable-memo failure that must escape the application panic
	// boundary.
	err error
}

// Repanic tells the workflow handler to raise memoPanic again, so that
// [Runner] fails the attempt instead of turning it into a workflow failure.
func (*runtime) Repanic(v any) bool {
	_, ok := v.(memoPanic)

	return ok
}

// newRuntime binds a task to the runner's effective namespace and logical
// identity before touching durable state. Old or foreign wire keys fail closed
// before application code runs; replaying them requires the old deployment.
// The current signal/wait version is mandatory even for a valid v2 logical key;
// WorkflowRunId supplies the stable engine identity shared with signal senders.
// Its start time comes from the durable memo, so the deadline holds across
// replays without depending on the current worker's clock.
func newRuntime(runner *Runner, raw hatchet.DurableContext, handler workflow.WorkflowHandler, in wire) (*runtime, error) {
	id := in.ID

	if id == "" {
		id = raw.AdditionalMetadata()["workflow_id"]
	}

	if id == "" {
		id = raw.WorkflowRunId()
	}

	key := runner.driver.runKey(handler.Name, id)

	if in.Key != key {
		return nil, fmt.Errorf("workflow input uses a legacy or foreign execution key: %w", workflow.ErrNotFound)
	}

	if in.SignalVersion != signalVersion {
		return nil, fmt.Errorf("workflow input uses a legacy signal protocol: %w", workflow.ErrNotFound)
	}

	if raw.WorkflowRunId() == "" {
		return nil, fmt.Errorf("workflow context has no physical execution ID: %w", workflow.ErrNotFound)
	}

	execution := &runtime{
		trace:  in.Trace,
		runner: runner,
		raw:    raw,
		name:   handler.Name,
		id:     id,
		key:    key,
		logger: runner.logger.With("workflow", handler.Name, "run", id),
	}

	start, err := execution.timeMemo("arkoren-start")

	if err != nil {
		return nil, err
	}

	execution.deadline = start.Add(time.Duration(in.Delay) + handler.Options.MaxDuration)
	// Capture the immutable parent before the worker binds this deadline onto
	// the mutable SDK context. Deriving from raw itself would create a cycle.
	execution.ctx, execution.cancel = context.WithDeadlineCause(raw.GetContext(), execution.deadline, workflow.ErrTimeout)

	return execution, nil
}

// close releases the run context.
func (execution *runtime) close() {
	execution.cancel()
}

// recoverMemo converts only the runtime's durable-memo panic at the native
// boundary. Defer it directly so recover observes the panic. Cancellation keeps
// its portable identity; unrelated panics continue through the SDK boundary.
func (execution *runtime) recoverMemo(err *error) {
	recovered := recover()

	if recovered == nil {
		return
	}

	failure, ok := recovered.(memoPanic)

	if !ok {
		panic(recovered)
	}

	if cause := execution.Err(); cause != nil {
		*err = encodeFailure(cause)

		return
	}

	*err = fmt.Errorf("durable memo: %w", failure.err)
}

// ID returns the run identifier.
func (execution *runtime) ID() string {
	return execution.id
}

// Name returns the workflow name.
func (execution *runtime) Name() string {
	return execution.name
}

// Logger returns the logger of the run.
func (execution *runtime) Logger() *slog.Logger {
	return execution.logger
}

// Raw returns the hatchet.DurableContext of the run.
func (execution *runtime) Raw() any {
	return execution.raw
}

// Done is closed when the run is canceled or reaches MaxDuration.
func (execution *runtime) Done() <-chan struct{} {
	return execution.ctx.Done()
}

// Err returns [workflow.ErrTimeout] or [workflow.ErrCanceled] once the run
// ended, and nil before.
func (execution *runtime) Err() error {
	if execution.ctx.Err() == nil {
		return nil
	}

	if cause := context.Cause(execution.ctx); errors.Is(cause, workflow.ErrTimeout) || errors.Is(cause, context.DeadlineExceeded) {
		return workflow.ErrTimeout
	}

	return workflow.ErrCanceled
}

// memo returns the value of fn from the durable memo under key.
func (execution *runtime) memo(key string, fn func() (any, error)) ([]byte, error) {
	raw, err := execution.raw.Memo(key, fn)

	if err != nil {
		return nil, fmt.Errorf("memo %s: %w", key, err)
	}

	return raw, nil
}

// Now returns a time that is the same on every replay of the run.
func (execution *runtime) Now() time.Time {
	execution.mutex.Lock()
	execution.nows++
	key := fmt.Sprintf("arkoren-now-%d", execution.nows)
	execution.mutex.Unlock()

	now, err := execution.timeMemo(key)

	if err != nil {
		panic(memoPanic{
			err,
		})
	}

	return now
}

// timeMemo returns the memoized time under key.
func (execution *runtime) timeMemo(key string) (time.Time, error) {
	raw, err := execution.memo(key, func() (any, error) {
		return time.Now().UTC(), nil
	})

	if err != nil {
		return time.Time{}, err
	}

	var now time.Time

	if err := json.Unmarshal(raw, &now); err != nil {
		return time.Time{}, fmt.Errorf("decode memoized time: %w", err)
	}

	return now, nil
}

// UUID returns an identifier that is the same on every replay of the run.
func (execution *runtime) UUID() string {
	execution.mutex.Lock()
	execution.uuids++
	key := fmt.Sprintf("arkoren-uuid-%d", execution.uuids)
	execution.mutex.Unlock()

	raw, err := execution.memo(key, func() (any, error) {
		return uuid.NewString(), nil
	})

	if err != nil {
		panic(memoPanic{
			err,
		})
	}

	var out string

	if err := json.Unmarshal(raw, &out); err != nil {
		panic(memoPanic{
			fmt.Errorf("decode memoized uuid: %w", err),
		})
	}

	return out
}

// Sleep pauses the run for d.
func (execution *runtime) Sleep(d time.Duration) error {
	if d <= 0 {
		return execution.Err()
	}

	_, _, err := execution.Select([]workflow.RawCase{{
		After: d,
	}})

	return err
}

// Wait resolves the first value of signal in this physical execution. The
// facade retains resolved values for repeated waits; engine replay uses the
// same event scope and verifies its marker's physical owner again.
func (execution *runtime) Wait(signal string, timeout time.Duration) ([]byte, error) {
	cases := []workflow.RawCase{{
		Signal: signal,
	}}

	if timeout > 0 {
		cases = append(cases, workflow.RawCase{
			After: timeout,
		})
	}

	index, value, err := execution.Select(cases)

	if err != nil {
		return nil, err
	}

	if index != 0 {
		return nil, workflow.ErrTimeout
	}

	return value, nil
}

// remaining returns the time left until the deadline when this wait began.
// Hatchet compares the conditions of a replayed wait with the recorded ones,
// so the value goes through the durable memo, once per wait.
func (execution *runtime) remaining() (time.Duration, error) {
	execution.mutex.Lock()
	execution.selects++
	key := fmt.Sprintf("arkoren-left-%d", execution.selects)
	execution.mutex.Unlock()

	raw, err := execution.memo(key, func() (any, error) {
		return time.Until(execution.deadline).Milliseconds(), nil
	})

	if err != nil {
		return 0, err
	}

	var millis int64

	if err := json.Unmarshal(raw, &millis); err != nil {
		return 0, fmt.Errorf("decode memoized time left: %w", err)
	}

	return time.Duration(millis) * time.Millisecond, nil
}

// Child rejects child workflows with [workflow.ErrUnsupported] before accessing
// run state or submitting work. Ordinary submission can repeat completed work
// after the child's idempotency TTL; native durable submission cannot safely
// join an existing logical execution. The arguments are therefore never used.
func (*runtime) Child(string, string, []byte) ([]byte, error) {
	return nil, workflow.ErrUnsupported
}

// Spawn rejects detached child workflows with [workflow.ErrUnsupported] before
// accessing run state, memos, or either submission path. This contains replay
// duplication rather than relying on a child TTL or an unsafe native fallback.
func (*runtime) Spawn(string, string, []byte) error {
	return workflow.ErrUnsupported
}

// environment binds only the native capabilities needed by waits and steps.
func (execution *runtime) environment() native.Environment {
	return native.Environment{Raw: execution.raw, Context: execution.ctx, Registry: execution.runner.registry,
		Task: execution.runner.stepTask, FirstValue: execution.runner.driver.firstValue,
		SignalKey: func(signal string) string {
			return execution.runner.driver.namespace + execution.runner.driver.signalKey(signal)
		},
		Key: execution.key, Trace: execution.trace, Err: execution.Err, Remaining: execution.remaining}
}

// Select waits through the native engine with this runtime's durable budget.
func (execution *runtime) Select(cases []workflow.RawCase) (int, []byte, error) {
	return execution.environment().Select(cases)
}

// Step joins a registered task while retaining this runtime's cancellation cause.
func (execution *runtime) Step(name string, in []byte, policy workflow.StepOptions) ([]byte, error) {
	return execution.environment().Step(name, in, policy)
}
