package workflow

import (
	"context"
	"log/slog"
	"math"
	"time"
)

// Driver runs workflows. It works on bytes: names, run identifiers, and
// payloads made by [EncodeValue]. Failures cross it as errors that
// [EncodeError] and [DecodeError] can carry; a driver returns the sentinels
// such as [ErrNotFound] and [ErrAlreadyStarted] for the matching conditions.
// Applications normally use Client; raw callers own encoding and engine
// lifecycle. Driver methods must preserve error identity through wrapping.
//
//	client := workflow.NewClient(driver)
//	run, err := client.Start(ctx, Onboard, "user-42", user)
//	if err != nil {
//		return err
//	}
//	_ = run.ID()
type Driver interface {
	// Start begins run id of the workflow called name with input in. It
	// returns [ErrAlreadyStarted] when the run exists, finished or not, until
	// the engine forgets it after its retention, and [ErrNotFound] when the
	// workflow is not registered. Concurrent starts of one id have exactly one
	// winner.
	// Trace is a separate, owned propagation carrier: retain a copy, carry it
	// through durable child/spawn scheduling, and extract it only at actual
	// step attempts. Never merge it with Metadata or retain the caller context.
	//
	//	err := driver.Start(ctx, "onboard", "user-42", input, workflow.StartOptions{})
	//	if err != nil && !errors.Is(err, workflow.ErrAlreadyStarted) {
	//		return err
	//	}
	Start(ctx context.Context, name, id string, in []byte, o StartOptions) error

	// Result blocks until the run ends, or until ctx is done, and returns the
	// encoded output or the failure of the run. [ErrNotFound] for an unknown
	// run.
	Result(ctx context.Context, name, id string) ([]byte, error)

	// Status returns the state of the run: [Running] while it has not ended,
	// [Failed] after an error or [ErrTimeout], [Canceled] after a cancel.
	// [ErrNotFound] for an unknown run.
	Status(ctx context.Context, name, id string) (Status, error)

	// Cancel stops the run. Its result becomes [ErrCanceled]. Canceling a
	// finished run is not an error; an unknown run is [ErrNotFound].
	Cancel(ctx context.Context, name, id string) error

	// Signal delivers a named value to a running run. It returns
	// [ErrNotFound] for an unknown or finished run.
	Signal(ctx context.Context, name, id, signal string, v []byte) error
}

// Runtime is the execution environment a driver gives to a workflow function.
// A Runtime may also implement Repanic(v any) bool; the workflow handler
// raises a recovered panic again when it returns true, so engine control-flow
// panics are not turned into failures.
// Every call that blocks returns early with an error when the run is
// canceled or timed out. A driver that replays workflows must make these
// calls deterministic and durable, including the ownership decision of Child.
// Engine implementations receive one from their own execution adapter; an
// application uses Context rather than constructing a native runtime.
//
//	handler, ok := registry.Workflow("onboard")
//	if !ok {
//		return workflow.ErrNotFound
//	}
//	out, err := handler.Handle(runtime, input) // runtime is supplied by the engine.
//	if err != nil {
//		return err
//	}
//	_ = out
type Runtime interface {
	// ID returns the run identifier.
	ID() string

	// Name returns the workflow name.
	Name() string

	// Now returns the current time of the run.
	Now() time.Time

	// UUID returns a new random identifier.
	UUID() string

	// Logger returns the logger of the run.
	Logger() *slog.Logger

	// Sleep pauses the run for d.
	Sleep(d time.Duration) error

	// Done is closed when the run is canceled or timed out.
	Done() <-chan struct{}

	// Err returns why Done is closed, or nil.
	Err() error

	// Step runs the registered step called name with completed per-call options o,
	// not the registered handler's default policy. Initial zero means no deliberate
	// positive backoff. An engine unable to express Attempts > 1 with Initial == 0
	// returns ErrUnsupported before scheduling or executing the affected step;
	// it must not substitute a positive delay or retry the whole workflow instead.
	// This policy rejection does not disable single attempts or positive backoff.
	//
	//	policy := workflow.StepOptions{Attempts: 3, Initial: 0, Factor: 1}
	//	output, err := runtime.Step("charge", input, policy)
	//	if err != nil {
	//		return err
	//	}
	//	_ = output
	Step(name string, in []byte, o StepOptions) ([]byte, error)

	// Child atomically creates or joins run id of workflow name and waits for
	// its result. Only creation acquires cancellation ownership: cancellation
	// or timeout of the caller also stops a newly created child. Joining ignores
	// in and never acquires ownership, including when another parent created
	// the existing run. Cancellation or timeout ends the joining wait without
	// stopping that run. Replay must retain the original ownership decision;
	// a lookup followed by a separate create is not sufficient. A driver unable
	// to guarantee this returns [ErrUnsupported] before submission, not a
	// detached child or a join that can cancel another owner's run.
	//
	//	output, err := runtime.Child("fulfill", "order-42", input)
	//	if err != nil {
	//		return err
	//	}
	//	_ = output // An existing run keeps its original input and owner.
	Child(name, id string, in []byte) ([]byte, error)

	// Spawn starts run id of workflow name without waiting; the run outlives
	// the caller. An existing run is not an error: [Context.Spawn] treats
	// [ErrAlreadyStarted] as success so a replay is idempotent. A driver that
	// cannot do it returns [ErrUnsupported].
	Spawn(name, id string, in []byte) error

	// Wait blocks for the next value of signal. A zero timeout waits without
	// limit. It returns [ErrTimeout] when the timeout expires.
	Wait(signal string, timeout time.Duration) ([]byte, error)

	// Select blocks until one case fires and returns its index and, for a
	// signal case, the value.
	Select(cases []RawCase) (index int, value []byte, err error)

	// Raw returns the native context of the engine, such as a
	// workflow.Context from Temporal.
	Raw() any
}

// RawCase is one alternative of [Runtime.Select]: a signal name or, when
// Signal is empty, a timer of After.
// This raw record is for driver adapters. Application workflows create typed
// alternatives with On and After rather than decoding signal bytes themselves.
//
//	cases := []workflow.RawCase{{Signal: "approved"}, {After: time.Minute}}
//	index, value, err := runtime.Select(cases)
//	if err != nil {
//		return err
//	}
//	_, _ = index, value
type RawCase struct {
	// Signal is the signal to wait for. Empty for a timer case.
	Signal string

	// After is the delay of a timer case.
	After time.Duration
}

// StartOptions are the settings of one [Driver.Start]. Trace is transport
// propagation state, not application metadata; Client.Start supplies it after
// applying start options. Drivers retain copies rather than caller contexts.
//
//	options := workflow.StartOptions{Metadata: map[string]string{"source": "signup"}}
//	err := driver.Start(ctx, "onboard", "user-42", input, options)
//	if err != nil && !errors.Is(err, workflow.ErrAlreadyStarted) {
//		return err
//	}
type StartOptions struct {
	// Trace carries injected trace fields separately from application metadata.
	// Drivers copy retained maps and forward fields without invoking telemetry
	// in replayed orchestration. Nil means no propagated parent. This is not an
	// authorization boundary; only trusted submitters should reach workers.
	//
	//	options := workflow.StartOptions{Trace: map[string]string{"traceparent": parent}}
	Trace map[string]string
	// Delay postpones the start of the run.
	Delay time.Duration

	// Metadata is free-form data kept with the run.
	Metadata map[string]string
}

// StepOptions are the settings of one step execution.
// NewStep publishes completed defaults. Raw adapters retain finite factors;
// Backoff normalizes nonfinite factors to one before calculating a delay.
//
//	handler, ok := registry.Step("charge")
//	if !ok {
//		return workflow.ErrNotFound
//	}
//	delay := handler.Options.Backoff(1)
//	_ = delay
type StepOptions struct {
	// Attempts is the maximum number of executions. One means no retry.
	Attempts int

	// Initial is the delay before the first retry. Zero means no deliberate
	// positive backoff, not an engine default. A driver unable to express this
	// with multiple attempts rejects the completed policy with ErrUnsupported
	// before affected step scheduling or execution; see Runtime.Step.
	//
	//	policy := workflow.StepOptions{Attempts: 3, Initial: 0, Factor: 1}
	//	fmt.Println(policy.Backoff(1)) // 0s
	Initial time.Duration

	// Factor multiplies the delay after every retry.
	Factor float64

	// Max caps the delay between retries. Zero means no cap.
	Max time.Duration

	// Timeout bounds one execution.
	Timeout time.Duration
}

// Backoff returns the delay before retry n, counting from 1. It never
// overflows: the result is capped by Max, or by the largest duration.
//
// Example:
//
//	policy := workflow.StepOptions{
//		Initial: time.Second,
//		Factor:  2,
//		Max:     time.Minute,
//	}
//	delay := policy.Backoff(2)
func (options StepOptions) Backoff(n int) time.Duration {
	delay := float64(max(options.Initial, 0))
	factor := backoffFactor(options.Factor)

	for range n - 1 {
		delay *= factor

		if delay >= math.MaxInt64 || (options.Max > 0 && delay >= float64(options.Max)) {
			break
		}
	}

	if options.Max > 0 {
		delay = min(delay, float64(options.Max))
	}

	if delay >= math.MaxInt64 {
		return time.Duration(math.MaxInt64)
	}

	return time.Duration(delay)
}

// backoffFactor completes the shared declaration/raw-policy finite invariant.
// Nonfinite input means constant delay; ordinary finite raw factors retain
// their existing arithmetic. Builders separately clamp factors below one.
func backoffFactor(factor float64) float64 {
	if math.IsNaN(factor) || math.IsInf(factor, 0) {
		return 1
	}

	return factor
}

// WorkflowOptions are the settings of a workflow definition.
// Configure definitions through Builder; engine adapters read this completed
// reporting record from registered WorkflowHandler values.
//
//	handler, ok := registry.Workflow("onboard")
//	if !ok {
//		return workflow.ErrNotFound
//	}
//	limit := handler.Options.MaxDuration
//	_ = limit
type WorkflowOptions struct { //nolint:revive // preserve the public raw record paired with StepOptions
	// MaxDuration bounds the whole run. Required.
	MaxDuration time.Duration

	// Retention is how long the engine keeps a finished run. Zero uses the
	// engine default.
	Retention time.Duration

	// Description documents the workflow.
	Description string
}
