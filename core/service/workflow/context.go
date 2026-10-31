package workflow

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Context is what a workflow function uses to run steps, wait for signals,
// sleep, and start other workflows. It wraps the [Runtime] of the driver. Do
// not use the clock, random numbers, or the network directly in a workflow
// function; use Now, UUID, and steps so a driver can replay it.
// Drivers supply the Context to a registered workflow handler; callers do not
// construct it directly. Side effects belong in registered steps.
//
//	definition := workflow.New("welcome", func(ctx *workflow.Context, user string) (string, error) {
//		if err := ctx.Sleep(time.Minute); err != nil {
//			return "", err
//		}
//		return user, nil
//	}, func(definition *workflow.Builder) {
//		definition.MaxDuration(time.Hour)
//	})
//	_ = definition.Err()
type Context struct {
	// rt is the driver runtime.
	rt Runtime

	// codec encodes payloads.
	codec Codec

	// mu guards memo.
	mu sync.Mutex

	// memo holds the first value received per signal name.
	memo map[string][]byte
}

// newContext binds the driver runtime and payload codec to one workflow run.
// Its signal memo starts empty and is shared by Wait and Select.
func newContext(rt Runtime, codec Codec) *Context {
	return &Context{
		rt:    rt,
		codec: codec,
		memo:  map[string][]byte{},
	}
}

// ID returns the run identifier.
//
// Example:
//
//	runID := workflowContext.ID()
func (workflowContext *Context) ID() string {
	return workflowContext.rt.ID()
}

// Name returns the workflow name.
//
// Example:
//
//	name := workflowContext.Name()
func (workflowContext *Context) Name() string {
	return workflowContext.rt.Name()
}

// Logger returns the logger of the run.
//
// Example:
//
//	workflowContext.Logger().Info("processing order")
func (workflowContext *Context) Logger() *slog.Logger {
	return workflowContext.rt.Logger()
}

// Now returns the time of the run. Use it instead of [time.Now].
//
// Example:
//
//	startedAt := workflowContext.Now()
func (workflowContext *Context) Now() time.Time {
	return workflowContext.rt.Now()
}

// UUID returns a new random identifier. Use it instead of random numbers.
//
// Example:
//
//	childID := workflowContext.UUID()
func (workflowContext *Context) UUID() string {
	return workflowContext.rt.UUID()
}

// Done is closed when the run is canceled or timed out. Poll it with a
// non-blocking select; blocking on it is not replay-safe on every engine.
// Durable calls such as Sleep and Wait return early on cancellation.
//
// Example:
//
//	select {
//	case <-workflowContext.Done():
//		return "", workflowContext.Err()
//	default:
//	}
func (workflowContext *Context) Done() <-chan struct{} {
	return workflowContext.rt.Done()
}

// Err returns why Done is closed, [ErrCanceled] or [ErrTimeout], or nil.
//
// Example:
//
//	if err := workflowContext.Err(); err != nil {
//		return "", err
//	}
func (workflowContext *Context) Err() error {
	return workflowContext.rt.Err()
}

// Sleep pauses the run for d. It returns early with the error of [Context.Err]
// when the run ends.
//
// Example:
//
//	if err := workflowContext.Sleep(time.Minute); err != nil {
//		return "", err
//	}
func (workflowContext *Context) Sleep(d time.Duration) error {
	return workflowContext.rt.Sleep(d)
}

// Raw returns the native context of the engine behind the driver, for
// example a Temporal workflow.Context. Its type depends on the driver.
//
// Example:
//
//	nativeContext := workflowContext.Raw()
func (workflowContext *Context) Raw() any {
	return workflowContext.rt.Raw()
}

// Step runs step s with input in and returns its output. To change the
// settings for one call, use [Step.Derive]; it keeps the name. A step with a
// non-nil [Step.Err] fails without running. A failure that the step marked
// with [Permanent] is returned without further retries.
//
// Example:
//
//	singleAttempt := charge.Derive(func(step *workflow.StepBuilder) {
//		step.Retry(1)
//	})
//	receipt, err := workflowContext.Step(singleAttempt, order)
//	if err != nil {
//		return "", err
//	}
func (workflowContext *Context) Step[In, Out any](s *Step[In, Out], in In) (Out, error) {
	var zero Out

	if err := s.Err(); err != nil {
		return zero, fmt.Errorf("step %s: %w", s.name, err)
	}

	raw, err := EncodeValue(workflowContext.codec, in)

	if err != nil {
		return zero, fmt.Errorf("step %s: %w", s.name, err)
	}

	out, err := workflowContext.rt.Step(s.name, raw, s.options)

	if err != nil {
		return zero, fmt.Errorf("step %s: %w", s.name, err)
	}

	return workflowContext.decode[Out](out, s.name)
}

// Child runs workflow w as run id and waits for its output. If this call creates
// the run, parent cancellation or MaxDuration also stops that child. When a run
// with the same id already exists, Child joins it and ignores in: joining acquires
// no cancellation ownership. Parent cancellation or MaxDuration ends only that
// wait and must not stop the existing run, even if another parent created it.
// Creation versus joining must be decided atomically and retained across replay.
// A driver unable to preserve these guarantees returns [ErrUnsupported] before
// submitting child work. An empty id, or the id of the calling run itself,
// fails with [ErrInvalidID].
// Workflow.Err is checked before encoding or asking the runtime to start/join.
//
// Example:
//
//	receipt, err := workflowContext.Child(fulfill, "order-42", order)
//	if err != nil {
//		return "", err
//	}
func (workflowContext *Context) Child[In, Out any](w *Workflow[In, Out], id string, in In) (Out, error) {
	var zero Out

	if err := w.Err(); err != nil {
		return zero, fmt.Errorf("child %s: %w", w.name, err)
	}

	if err := workflowContext.checkChildID(w.name, id); err != nil {
		return zero, fmt.Errorf("child %s: %w", w.name, err)
	}

	raw, err := EncodeValue(workflowContext.codec, in)

	if err != nil {
		return zero, fmt.Errorf("child %s: %w", w.name, err)
	}

	out, err := workflowContext.rt.Child(w.name, id, raw)

	if err != nil {
		return zero, fmt.Errorf("child %s %s: %w", w.name, id, err)
	}

	return workflowContext.decode[Out](out, w.name)
}

// Spawn starts workflow w as run id without waiting and returns id. The run
// outlives the parent. A run that already exists is not an error, so a replay
// of the parent spawns once; use [Client.Run] with the id to query it. A driver
// that cannot start runs from a workflow returns [ErrUnsupported]. An empty id,
// or the id of the calling run itself, fails with [ErrInvalidID].
// Workflow.Err is checked before encoding or submitting detached work.
//
// Example:
//
//	childID, err := workflowContext.Spawn(sendReceipt, "receipt-42", order)
//	if err != nil {
//		return "", err
//	}
func (workflowContext *Context) Spawn[In, Out any](w *Workflow[In, Out], id string, in In) (string, error) {
	if err := w.Err(); err != nil {
		return "", fmt.Errorf("spawn %s: %w", w.name, err)
	}

	if err := workflowContext.checkChildID(w.name, id); err != nil {
		return "", fmt.Errorf("spawn %s: %w", w.name, err)
	}

	raw, err := EncodeValue(workflowContext.codec, in)

	if err != nil {
		return "", fmt.Errorf("spawn %s: %w", w.name, err)
	}

	if err := workflowContext.rt.Spawn(w.name, id, raw); err != nil && !errors.Is(err, ErrAlreadyStarted) {
		return "", fmt.Errorf("spawn %s %s: %w", w.name, id, err)
	}

	return id, nil
}

// checkChildID rejects an invalid id (see [validID]) and the id of the run itself.
func (workflowContext *Context) checkChildID(name, id string) error {
	if !validID(id) {
		return ErrInvalidID
	}

	if id == workflowContext.rt.ID() && name == workflowContext.rt.Name() {
		return fmt.Errorf("%w: a run cannot start itself", ErrInvalidID)
	}

	return nil
}

// Wait blocks until signal s arrives and returns its value. A signal resolves
// once per run: the first value received per signal name is kept, and every
// later Wait or Select for the same signal returns that value at once, even if
// the sender sent more. [Timeout] bounds the wait and yields [ErrTimeout]; zero
// or no Timeout waits without limit.
//
// Example:
//
//	confirmed := workflow.NewSignal[string]("confirmed")
//	token, err := workflowContext.Wait(confirmed, workflow.Timeout(time.Minute))
//	if err != nil {
//		return "", err
//	}
func (workflowContext *Context) Wait[T any](s Signal[T], opts ...WaitOption) (T, error) {
	var (
		zero   T
		config waitConfig
	)

	for _, opt := range opts {
		opt(&config)
	}

	raw, ok := workflowContext.remembered(s.name)

	if !ok {
		var err error

		raw, err = workflowContext.rt.Wait(s.name, config.timeout)

		if err != nil {
			return zero, fmt.Errorf("wait %s: %w", s.name, err)
		}

		raw = workflowContext.remember(s.name, raw)
	}

	var value T

	if err := DecodeValue(workflowContext.codec, raw, &value); err != nil {
		return zero, fmt.Errorf("wait %s: %w", s.name, err)
	}

	return value, nil
}

// Select blocks until one case fires and runs its function. A signal case
// whose signal was already received fires at once, in case order; otherwise
// the first signal or timer to fire wins. A signal resolves once per run: a
// signal case records the value, and later [Context.Wait] or Select calls for
// that signal return the same value without waiting for a new one. A timer
// case with a zero or negative delay fires after the shortest delay.
//
// Example:
//
//	confirmed := workflow.NewSignal[string]("confirmed")
//	var result string
//	err := workflowContext.Select(workflow.On(confirmed, func(token string) {
//		result = token
//	}), workflow.After(time.Minute, func() {
//		result = "expired"
//	}))
//	if err != nil {
//		return "", err
//	}
//	return result, nil
func (workflowContext *Context) Select(cases ...Case) error {
	if len(cases) == 0 {
		return ErrNoCases
	}

	for _, option := range cases {
		if option.signal == "" {
			continue
		}

		if raw, ok := workflowContext.remembered(option.signal); ok {
			return option.fire(workflowContext, raw)
		}
	}

	raws := make([]RawCase, len(cases))

	for index, option := range cases {
		raws[index] = RawCase{
			Signal: option.signal,
			After:  option.after,
		}
	}

	index, raw, err := workflowContext.rt.Select(raws)

	if err != nil {
		return fmt.Errorf("select: %w", err)
	}

	if index < 0 || index >= len(cases) {
		return fmt.Errorf("select: driver returned index %d of %d cases", index, len(cases))
	}

	chosen := cases[index]

	if chosen.signal != "" {
		raw = workflowContext.remember(chosen.signal, raw)
	}

	return chosen.fire(workflowContext, raw)
}

// remembered returns the first value received for signal.
func (workflowContext *Context) remembered(signal string) ([]byte, bool) {
	workflowContext.mu.Lock()
	defer workflowContext.mu.Unlock()

	raw, ok := workflowContext.memo[signal]

	return raw, ok
}

// remember keeps raw as the value of signal unless one exists, and returns
// the kept value.
func (workflowContext *Context) remember(signal string, raw []byte) []byte {
	workflowContext.mu.Lock()
	defer workflowContext.mu.Unlock()

	if kept, ok := workflowContext.memo[signal]; ok {
		return kept
	}

	workflowContext.memo[signal] = raw

	return raw
}

// decode decodes the output of the step or workflow called name.
func (workflowContext *Context) decode[Out any](raw []byte, name string) (Out, error) {
	var out Out

	if err := DecodeValue(workflowContext.codec, raw, &out); err != nil {
		return out, fmt.Errorf("%s: %w", name, err)
	}

	return out, nil
}
