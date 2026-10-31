package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/llm/internal/engine"
	"arkoren.dev/core/service/llm/internal/genai"
	"arkoren.dev/core/service/telemetry"
)

// runTools runs the tool calls of one turn and returns their results in call
// order. Every failure except [Permanent] becomes an error [ToolResult] for
// the model. A permanent failure cancels the context of the calls still
// running. The first cancellation cause wins, so a sibling's cancellation
// response cannot replace the initiating failure. All launched calls are joined;
// a handler that ignores cancellation can therefore keep the run waiting.
func (model *Model) runTools(
	ctx context.Context,
	plan *runPlan,
	calls []ToolCall,
	emit func(Event) bool,
) ([]Part, error) {
	results, err := engine.Calls(ctx, calls, plan.concurrency, errStopped,
		func(call ToolCall) bool {
			return emit(ToolStarted{ID: call.ID, Name: call.Name})
		},
		func(ctx context.Context, call ToolCall) (ToolResult, error) {
			return model.callTool(ctx, plan, call)
		})

	if err != nil {
		return nil, err
	}

	parts := make([]Part, len(calls))

	for i, call := range calls {
		parts[i] = results[i]

		if !emit(ToolFinished{
			ID: call.ID, Name: call.Name, IsError: results[i].IsError,
		}) {
			return nil, errStopped
		}
	}

	return parts, nil
}

// callTool runs one tool call under its span and publishes its event. A
// failing tool yields an error result and a nil error, except for a
// [Permanent] failure, which is returned. A call to an unknown tool yields
// an error result without a span or event: the name comes from the model
// and is not a tool of the agent.
func (model *Model) callTool(ctx context.Context, plan *runPlan, call ToolCall) (ToolResult, error) {
	tool, known := plan.tools[call.Name]

	if !known {
		return ToolResult{
			CallID:  call.ID,
			Content: fmt.Sprintf("unknown tool %q", call.Name),
			IsError: true,
		}, nil
	}

	ctx, op := genai.Begin(ctx, model.config.Telemetry, model.instruments, telemetry.Internal,
		semconv.GenAIOperationExecuteTool, tool.name,
		append(requestAttrs(model.config, plan.model),
			telemetry.String(semconv.GenAIAgentName, plan.agent),
			telemetry.String(semconv.GenAIToolName, tool.name))...)
	completed := false
	defer endInterrupted(ctx, op, &completed)
	start := time.Now()

	content, err := tool.call(ctx, call.Arguments)

	completed = true
	op.End(ctx, err, errorSentinels...)

	duration := time.Since(start)

	if err == nil {
		model.config.Events.Emit(ctx, ToolInvoked, ToolInvokedEvent{
			Agent:    plan.agent,
			Tool:     tool.name,
			Duration: duration,
		})

		return ToolResult{
			CallID:  call.ID,
			Content: content,
		}, nil
	}

	model.config.Events.Emit(ctx, ToolFailed, ToolFailedEvent{
		Agent:    plan.agent,
		Tool:     tool.name,
		Duration: duration,
	})

	// Fatal errors belong only to the caller. In particular, policy failures
	// must never enter model-facing ToolError extraction, even when wrapped.
	if errors.Is(err, ErrPermanent) {
		return ToolResult{}, err
	}

	return ToolResult{
		CallID:  call.ID,
		Content: modelText(err),
		IsError: true,
	}, nil
}
