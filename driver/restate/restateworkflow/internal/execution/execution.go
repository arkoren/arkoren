package execution

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	restatesdk "github.com/restatedev/sdk-go"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/restate/restateworkflow/internal/protocol"
)

// Environment supplies registry, service naming and local cancellation state.
type Environment struct {
	// Registry supplies immutable handlers and actual-attempt telemetry.
	Registry *workflow.Registry
	// Service resolves exact case-sensitive native service names.
	Service func(string) string
	// Active joins the shared Cancel handler to local active attempts.
	Active *sync.Map
}

// Definition builds the service of one workflow.
func (environment Environment) Definition(handler workflow.WorkflowHandler) restatesdk.ServiceDefinition {
	options := []restatesdk.ServiceDefinitionOption{
		restatesdk.WithInactivityTimeout(protocol.InvocationTimeout),
		restatesdk.WithAbortTimeout(protocol.InvocationTimeout),
	}

	if handler.Options.Retention > 0 {
		options = append(options, restatesdk.WithWorkflowRetention(handler.Options.Retention))
	}

	if handler.Options.Description != "" {
		options = append(options, restatesdk.WithDocumentation(handler.Options.Description))
	}

	run := restatesdk.NewWorkflowHandler(func(ctx restatesdk.WorkflowContext, in protocol.Payload) (protocol.Payload, error) {
		return environment.run(ctx, handler, in)
	})

	signal := restatesdk.NewWorkflowSharedHandler(func(ctx restatesdk.WorkflowSharedContext, request protocol.SignalRequest) (restatesdk.Void, error) {
		return restatesdk.Void{}, resolveSignal(ctx, request)
	})

	service := environment.Service(handler.Name)

	cancel := restatesdk.NewWorkflowSharedHandler(func(ctx restatesdk.WorkflowSharedContext, _ restatesdk.Void) (restatesdk.Void, error) {
		if rt, ok := environment.Active.Load(service + "/" + restatesdk.Key(ctx)); ok {
			_ = rt.(*execution).stop(fmt.Errorf("run canceled: %w", workflow.ErrCanceled)) //nolint:errcheck // only closes Done
		}

		return restatesdk.Void{}, nil
	})

	return restatesdk.NewWorkflow(service, options...).
		Handler(protocol.RunHandler, run).
		Handler(protocol.SignalHandler, signal).
		Handler(protocol.CancelHandler, cancel)
}

// run is the Run handler of a workflow.
func (environment Environment) run(ctx restatesdk.WorkflowContext, handler workflow.WorkflowHandler, in protocol.Payload) (protocol.Payload, error) {
	rt := newRuntime(ctx, environment, handler)

	restatesdk.Set(ctx, protocol.RunningKey, true)

	key := environment.Service(handler.Name) + "/" + rt.id
	environment.Active.Store(key, rt)
	defer environment.Active.Delete(key)

	// SDK panics pass through the facade because the runtime implements
	// Repanic.
	out, err := handler.Handle(rt, in)

	restatesdk.Clear(ctx, protocol.RunningKey)

	if err != nil {
		return nil, failureOf(err)
	}

	return out, nil
}

// failureOf converts a workflow failure to the terminal error that ends the
// invocation. The message is the [workflow.EncodeError] envelope.
func failureOf(err error) error {
	code := restatesdk.Code(500)

	if errors.Is(err, workflow.ErrCanceled) {
		code = protocol.CodeCanceled
	}

	return restatesdk.ToTerminalError(errors.New(string(workflow.EncodeError(err))), restatesdk.WithErrorCode(code))
}

// resolveSignal resolves the promise of a signal. Unless Force is set, a run
// that is not running answers with the terminal error 404, which the driver
// maps to [workflow.ErrNotFound]. A promise that is already resolved keeps its
// first value; Restate reports the second resolve as a terminal failure that
// is not an error here.
func resolveSignal(ctx restatesdk.WorkflowSharedContext, request protocol.SignalRequest) error {
	if !request.Force {
		running, err := restatesdk.Get[bool](ctx, protocol.RunningKey)

		if err != nil {
			return err
		}

		if !running {
			return restatesdk.ToTerminalError(errors.New("run is not running"), restatesdk.WithErrorCode(http.StatusNotFound))
		}
	}

	err := restatesdk.Promise[protocol.Payload](ctx, protocol.SignalPromise+request.Name).Resolve(request.Payload)

	if err != nil && !strings.Contains(err.Error(), protocol.AlreadyCompleted) {
		return err
	}

	return nil
}
