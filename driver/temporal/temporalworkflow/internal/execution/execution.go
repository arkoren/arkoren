package execution

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"go.temporal.io/sdk/temporal"
	tworkflow "go.temporal.io/sdk/workflow"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/temporal/temporalworkflow/internal/protocol"
)

// Environment supplies immutable registration and native naming dependencies.
// Result observes an existing run; canceling observation never cancels the run.
type Environment struct {
	// Registry supplies registered handlers and trace extraction at attempts.
	Registry *workflow.Registry
	// WorkflowType resolves the registered native workflow name.
	WorkflowType func(string) string
	// StepType resolves the registered activity name.
	StepType func(string) string
	// SignalName resolves the native buffered signal channel name.
	SignalName func(string) string
	// JoinType resolves the activity name used to observe an existing run.
	JoinType func() string
	// Result waits through the native backend without taking cancellation ownership.
	Result func(context.Context, string) (protocol.Payload, error)
}

// Workflow returns the Temporal workflow function of a handler.
func (environment Environment) Workflow(handler workflow.WorkflowHandler) func(tworkflow.Context, protocol.Payload) (protocol.Payload, error) {
	return func(ctx tworkflow.Context, in protocol.Payload) (protocol.Payload, error) {
		input, carrier, err := protocol.DecodeInput(in)

		if err != nil {
			return nil, protocol.Failure(err)
		}

		rt, stop := newRuntime(ctx, environment, handler)
		defer stop()
		rt.trace = carrier

		out, err := handler.Handle(rt, input)

		if err != nil {
			return nil, rt.fail(err)
		}

		return out, nil
	}
}

// Activity returns the Temporal activity function of a step handler. A
// failure becomes an application error that carries the
// [workflow.EncodeError] envelope; a [workflow.Permanent] failure is not
// retried.
func (environment Environment) Activity(handler workflow.StepHandler) func(context.Context, protocol.Payload) (protocol.Payload, error) {
	return func(ctx context.Context, in protocol.Payload) (protocol.Payload, error) {
		input, carrier, err := protocol.DecodeInput(in)

		if err != nil {
			return nil, protocol.Failure(err)
		}

		ctx = environment.Registry.NewTraceContext(ctx, carrier)

		out, err := handler.Handle(ctx, input)

		if err == nil {
			return out, nil
		}

		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
			err = fmt.Errorf("%w: step %s exceeded its timeout: %w", workflow.ErrTimeout, handler.Name, err)
		}

		return nil, protocol.Failure(err)
	}
}

// Join waits for the run id and returns its output. A child that joins an
// existing run uses it. A failure of the run is not retried.
func (environment Environment) Join(ctx context.Context, id string) (protocol.Payload, error) {
	out, err := environment.Result(ctx, id)

	if err == nil {
		return out, nil
	}

	mapped := protocol.FromTemporal(err)

	if ctx.Err() != nil {
		return nil, mapped
	}

	return nil, temporal.NewApplicationErrorWithOptions(mapped.Error(), protocol.ErrorType, temporal.ApplicationErrorOptions{
		NonRetryable: true,
		Details:      []any{protocol.Payload(workflow.EncodeError(mapped))},
	})
}

// retryPolicy maps step options to a Temporal retry policy. Temporal counts
// the first execution as attempt one, like [workflow.StepOptions.Attempts].
// [runtime.Step] normalizes Attempts to at least one and rejects zero Initial when
// retries are enabled. A zero maximum sends Go's largest duration to avoid the
// implicit 100-times-initial cap; backend duration limits can still reduce it.
// A smaller positive maximum also bounds the initial interval. Single-attempt
// policies retain their unused timing fields without inventing positive delays.
func retryPolicy(options workflow.StepOptions) *temporal.RetryPolicy {
	policy := &temporal.RetryPolicy{
		MaximumAttempts:    int32(min(options.Attempts, 1<<30)), //nolint:gosec // bounded above
		InitialInterval:    options.Initial,
		BackoffCoefficient: max(options.Factor, 1),
		MaximumInterval:    cmp.Or(options.Max, time.Duration(math.MaxInt64)),
	}

	policy.InitialInterval = min(policy.MaximumInterval, policy.InitialInterval)

	return policy
}
