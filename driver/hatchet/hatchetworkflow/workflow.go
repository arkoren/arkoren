package hatchetworkflow

import (
	"fmt"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/hatchet/hatchetworkflow/internal/execution"
)

// workflowFunc returns the durable task function of a workflow.
func (runner *Runner) workflowFunc(handler workflow.WorkflowHandler) func(hatchet.DurableContext, wire) (wire, error) {
	return func(ctx hatchet.DurableContext, in wire) (out wire, err error) {
		if err := execution.RequireDurableMemo(ctx); err != nil {
			return wire{}, encodeFailure(err)
		}

		execution, err := newRuntime(runner, ctx, handler, in)

		if err != nil {
			return wire{}, fmt.Errorf("start run: %w", err)
		}

		// Keep the concrete durable SDK context and its replay hooks. Native
		// joins read GetContext; restore its parent before the SDK reports the
		// terminal outcome, so closing our deadline does not suppress success.
		parent := ctx.GetContext()
		ctx.SetContext(execution.ctx)
		defer ctx.SetContext(parent)
		defer execution.close()

		// A memoPanic leaves the handler through Repanic. The run fails
		// without a facade envelope, because the replay could not be trusted.
		defer execution.recoverMemo(&err)

		if in.Delay > 0 {
			if err := execution.Sleep(time.Duration(in.Delay)); err != nil {
				return wire{}, encodeFailure(err)
			}
		}

		result, err := handler.Handle(execution, []byte(in.Data))

		if cause := execution.Err(); cause != nil {
			err = cause
		}

		if err != nil {
			return wire{}, encodeFailure(err)
		}

		return wire{
			Data: string(result),
		}, nil
	}
}
