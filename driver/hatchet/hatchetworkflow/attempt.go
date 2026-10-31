package hatchetworkflow

import (
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/hatchet/hatchetworkflow/internal/execution"
)

// stepFunc binds the registry to one actual native task attempt.
func (runner *Runner) stepFunc(handler workflow.StepHandler) func(hatchet.Context, wire) (wire, error) {
	return execution.Attempt(runner.registry, handler)
}
