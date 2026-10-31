package restateworkflow

import (
	restatesdk "github.com/restatedev/sdk-go"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/restate/restateworkflow/internal/execution"
)

// definition binds the protocol service to registry and local cancellation state.
func (runner *Runner) definition(handler workflow.WorkflowHandler) restatesdk.ServiceDefinition {
	environment := execution.Environment{Registry: runner.registry, Service: runner.namer.service, Active: &runner.active}

	return environment.Definition(handler)
}
