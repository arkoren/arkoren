// Package engine owns agent turn sequencing and bounded tool execution.
// Public messages, events, definitions, and observations stay in llm.
package engine

import (
	"context"
	"errors"
	"fmt"
)

// Plan binds public representations to the private turn state machine.
// Callbacks are synchronous except for the separately bounded tool executor.
type Plan[State, Reply, Call, Part any] struct {
	// MaxSteps bounds provider turns, including correction attempts.
	MaxSteps int
	// Retries is the remaining correctable-output budget.
	Retries int
	// Step performs one provider turn using the current history.
	Step func(context.Context, State) (Reply, error)
	// Record retains partial usage and history before interpreting the reply.
	Record func(*State, Reply) ([]Call, error)
	// Unknown remembers the last eligible unknown-tool diagnostic.
	Unknown func([]Call, error) error
	// Accept parses output and prepares safe correction feedback when needed.
	Accept func(context.Context, *State, *int) (bool, error)
	// Reject answers calls that cannot run within the remaining turn budget.
	Reject func(*State, []Call)
	// Tools runs an admitted tool turn and joins its callbacks.
	Tools func(context.Context, []Call) ([]Part, error)
	// Append retains successful tool results in call order.
	Append func(*State, []Part)
	// Complete emits the synchronous completion event after a successful turn.
	Complete func(int, Reply) bool
	// MaxStepsError identifies an exhausted turn budget.
	MaxStepsError error
	// StoppedError identifies a consumer stop, never an agent outcome.
	StoppedError error
}

// Turns drives one run while preserving partial state on every exit path.
// Refused and truncated replies stop before unknown-tool tracking. Consumer
// stops retain their identity and do not acquire remembered tool diagnostics.
func Turns[State, Reply, Call, Part any](
	ctx context.Context,
	state State,
	plan Plan[State, Reply, Call, Part],
) (result State, err error) {
	result = state
	retries := plan.Retries

	var unknown error
	defer func() {
		if err != nil && unknown != nil && !errors.Is(err, plan.StoppedError) {
			err = fmt.Errorf("%w (%w)", err, unknown)
		}
	}()

	for turn := 1; turn <= plan.MaxSteps; turn++ {
		response, err := plan.Step(ctx, result)

		if err != nil {
			return result, err
		}

		calls, err := plan.Record(&result, response)

		if err != nil {
			return result, err
		}

		unknown = plan.Unknown(calls, unknown)
		accepted := false
		var parts []Part

		switch {
		case len(calls) == 0:
			accepted, err = plan.Accept(ctx, &result, &retries)
		case turn == plan.MaxSteps:
			plan.Reject(&result, calls)

			return result, plan.MaxStepsError
		default:
			parts, err = plan.Tools(ctx, calls)
		}

		if err != nil {
			return result, err
		}

		if parts != nil {
			plan.Append(&result, parts)
		}

		if !plan.Complete(turn, response) {
			return result, plan.StoppedError
		}

		if accepted {
			return result, nil
		}
	}

	return result, plan.MaxStepsError
}
