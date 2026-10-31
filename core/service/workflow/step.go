package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"arkoren.dev/core/internal/name"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/workflow/internal/execution"
)

// defaultStepTimeout bounds one step execution unless [StepBuilder.Timeout] says
// otherwise.
const defaultStepTimeout = time.Minute

// Step is a named, typed unit of side-effecting work that a workflow runs
// through [Context.Step]. A driver may retry and record it. Create it with
// [NewStep] and configure it through [StepBuilder].
//
// A Step is an immutable definition: [Step.Derive] returns a modified copy that
// keeps the name, and the first error found is kept and reported by
// [Step.Err]. [Handlers] and [Context.Step] reject a step whose Err is not
// nil. To change the settings of one call, derive a step and pass that
// instead. Always create steps with [NewStep]; the zero value has no name and
// no function.
//
// Example:
//
//	var Charge = workflow.NewStep("charge-card", charge, func(step *workflow.StepBuilder) {
//		step.Retry(5)
//		step.Backoff(time.Second, 2, time.Minute)
//		step.Timeout(30 * time.Second)
//	})
//
//	// Inside a workflow, for this call only:
//	singleAttempt := Charge.Derive(func(step *workflow.StepBuilder) {
//		step.Retry(1)
//	})
//	receipt, err := workflowContext.Step(singleAttempt, order)
type Step[In, Out any] struct {
	// name is the stable identifier of the step.
	name string

	// fn is the step body.
	fn func(context.Context, In) (Out, error)

	// options are the settings of the step.
	options StepOptions

	// err is the first error found while building the step.
	err error
}

// NewStep returns the step called identity that runs fn. The name must satisfy
// the shared identifier rule of 1-64 lowercase letters, digits, or hyphens
// with no hyphen at either end, and fn must not be nil. Violations are kept
// on the value and reported by [Step.Err], [Handlers], and [Context.Step] as
// [ErrInvalidName] or [ErrNilFunc]. Steps run once by
// default with a one minute timeout. Return [Permanent] from fn to stop
// retries.
// The default retry backoff starts at one second, multiplies by two, and caps
// at 30 seconds; it is used only when the configured attempts allow retries.
// Callbacks run directly once, synchronously in order on one fresh, nonnil
// draft, including invalid declarations. Nil entries and callback panics
// propagate; expanded nil or empty slices supply no edits. Publication copies
// the settings so a retained draft cannot change the step.
//
// Example:
//
//	charge := workflow.NewStep("charge", chargeCard, func(step *workflow.StepBuilder) {
//		step.Retry(3)
//	})
//	registry, err := workflow.Handlers(charge)
//	if err != nil {
//		return err
//	}
//	_ = registry
func NewStep[In, Out any](identity string, fn func(context.Context, In) (Out, error), configure ...StepBuilderFunc) *Step[In, Out] {
	step := &Step[In, Out]{
		name: identity,
		fn:   fn,
		options: StepOptions{
			Attempts: 1,
			Initial:  time.Second,
			Factor:   2,
			Max:      30 * time.Second,
			Timeout:  defaultStepTimeout,
		},
	}

	if err := name.Identifier(identity); err != nil {
		step.err = fmt.Errorf("step name %q: %w: %w", identity, ErrInvalidName, err)
	} else if fn == nil {
		step.err = fmt.Errorf("step %s: %w", identity, ErrNilFunc)
	}

	draft := &StepBuilder{
		name:    identity,
		options: step.options,
		err:     step.err,
	}

	for _, configure := range configure {
		configure(draft)
	}

	step.options = draft.options

	step.err = draft.err

	return step
}

// StepBuilder configures the retry policy of a step before [NewStep] or
// [Step.Derive] publishes it. Each setting returns the same draft; publication
// copies its scalar settings so a retained draft cannot change the step.
//
// Example:
//
//	workflow.NewStep("charge", charge, func(step *workflow.StepBuilder) {
//		step.Retry(3)
//	})
type StepBuilder struct {
	// name identifies the step in validation errors.
	name string
	// options holds the retry and timeout policy copied at publication.
	options StepOptions
	// err retains the first construction or setting error.
	err error
}

// Derive returns a detached step with the same name, function, policy, and
// first error, followed by the supplied edits. Callbacks run directly once,
// synchronously in order on one fresh draft. Nil entries panic; expanded nil
// or empty slices supply no edits. The base is never mutated, and defaults
// are not reapplied. Use the result for one [Context.Step] call.
// The base is captured before callbacks run, including its typed function.
//
// Example:
//
//	singleAttempt := charge.Derive(func(step *workflow.StepBuilder) {
//		step.Retry(1)
//	})
//	receipt, err := workflowContext.Step(singleAttempt, order)
func (step *Step[In, Out]) Derive(configure ...StepBuilderFunc) *Step[In, Out] {
	derived := *step

	derived.err = step.Err()
	draft := &StepBuilder{
		name:    derived.name,
		options: derived.options,
		err:     derived.err,
	}

	for _, configure := range configure {
		configure(draft)
	}

	derived.options = draft.options

	derived.err = draft.err

	return &derived
}

// Retry sets the maximum number of executions of a step. One means no retry,
// which is the default. A smaller value counts as one.
// Retry returns the same draft and does not clear earlier errors.
//
// Example:
//
//	workflow.NewStep("charge", charge, func(step *workflow.StepBuilder) {
//		step.Retry(3)
//	})
func (step *StepBuilder) Retry(attempts int) *StepBuilder {
	step.options.Attempts = max(attempts, 1)

	return step
}

// Backoff sets the delay before the first retry, the factor that multiplies
// it after each retry, and the cap on the delay. A zero maximum means no cap.
// A factor below one counts as one (constant delay). A negative initial or
// maximum is an error.
// Initial zero requests no deliberate positive delay, not an engine default.
// Engines unable to express it with multiple attempts return ErrUnsupported
// before scheduling or executing that call; the declaration itself remains valid.
// Defaults are an initial delay of one second, factor two, and a 30-second cap.
// Backoff returns the same draft and preserves the first error. NaN and
// infinite factors are normalized to one, like a factor below one, so the
// published policy remains usable by native engines and the memory driver.
//
// Example:
//
//	workflow.NewStep("charge", charge, func(step *workflow.StepBuilder) {
//		step.Retry(3)
//		step.Backoff(time.Second, 2, time.Minute)
//	})
func (step *StepBuilder) Backoff(initial time.Duration, factor float64, maximum time.Duration) *StepBuilder {
	if step.err != nil {
		return step
	}

	if initial < 0 || maximum < 0 {
		step.err = fmt.Errorf("step %s: negative backoff: %w", step.name, ErrInvalidOptions)

		return step
	}

	step.options.Initial = initial
	step.options.Factor = max(backoffFactor(factor), 1)
	step.options.Max = maximum

	return step
}

// Timeout bounds one execution of a step. The default is one minute. Zero
// means no limit and a negative value is an error.
// Timeout returns the same draft and preserves the first declaration error.
//
// Example:
//
//	workflow.NewStep("charge", charge, func(step *workflow.StepBuilder) {
//		step.Timeout(30 * time.Second)
//	})
func (step *StepBuilder) Timeout(duration time.Duration) *StepBuilder {
	if step.err != nil {
		return step
	}

	if duration < 0 {
		step.err = fmt.Errorf("step %s: negative timeout %s: %w", step.name, duration, ErrInvalidOptions)

		return step
	}

	step.options.Timeout = duration

	return step
}

// Name returns the stable identifier supplied to [NewStep]. Deriving a policy
// preserves this name so the same registered handler serves the derived step.
//
// Example:
//
//	name := workflow.NewStep("charge", charge).Name()
//	fmt.Println(name) // charge
func (step *Step[In, Out]) Name() string {
	return step.name
}

// Err returns the first declaration error, or nil for a valid step. Derive
// retains this error; later settings cannot repair an invalid declaration.
// The zero Step reports ErrInvalidName because it has no declaration identity.
//
// Example:
//
//	step := workflow.NewStep("Bad", charge)
//	if errors.Is(step.Err(), workflow.ErrInvalidName) {
//		return step.Err()
//	}
func (step *Step[In, Out]) Err() error {
	if step.err == nil && step.name == "" {
		return fmt.Errorf("step was not built with NewStep: %w", ErrInvalidName)
	}

	return step.err
}

// definition seals the set of registry declarations to workflows and steps.
func (step *Step[In, Out]) definition() {
}

// register validates and snapshots the name, policy, and typed function into
// registry. The handler never reads through the published step pointer after
// registration; later assignment through that pointer cannot change execution.
func (step *Step[In, Out]) register(registry *Registry) error {
	if step == nil {
		return fmt.Errorf("register step: %w", ErrNilFunc)
	}

	if err := step.Err(); err != nil {
		return fmt.Errorf("register step: %w", err)
	}

	if registry.taken(step.name) {
		return fmt.Errorf("register step %s: %w", step.name, ErrDuplicateName)
	}

	codec := registry.codec
	identity, fn := step.name, step.fn
	service := registry.telemetry

	registry.steps[identity] = StepHandler{
		Name:    identity,
		Options: step.options,
		Handle: func(ctx context.Context, raw []byte) (out []byte, err error) {
			ctx, observation := beginObservation(ctx, service, "step_attempt", telemetry.Internal)
			defer func() {
				endObservation(ctx, observation, err)
			}()
			defer execution.Protect(ctx, slog.Default().With(slog.String("step", identity)), nil, Permanent, &err)

			var input In

			if err := DecodeValue(codec, raw, &input); err != nil {
				return nil, Permanent(fmt.Errorf("step %s: %w", identity, err))
			}

			result, err := fn(ctx, input)

			if err != nil {
				return nil, err
			}

			return EncodeValue(codec, result)
		},
	}

	return nil
}

// StepBuilderFunc edits a step draft during [NewStep] or [Step.Derive].
// Callbacks run synchronously once in order on one independent draft;
// supplied nil callbacks panic. Edits preserve an inherited declaration error.
//
// Example:
//
//	policy := workflow.StepBuilderFunc(func(step *workflow.StepBuilder) {
//		step.Retry(3)
//	})
//	charge := workflow.NewStep("charge", chargeCard, policy)
//	if err := charge.Err(); err != nil {
//		return err
//	}
type StepBuilderFunc = func(*StepBuilder)
