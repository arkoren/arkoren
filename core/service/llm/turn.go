package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"arkoren.dev/core/service/llm/internal/engine"
)

// turns binds public conversation records to the private turn engine.
func turns[Out any](ctx context.Context, model *Model, agent Agent[Out], plan *runPlan, history []Message, step func(context.Context, Request) (Response, error), emit func(Event) bool) (Result[Out], error) {
	return engine.Turns(ctx, Result[Out]{Messages: slices.Clone(history)}, engine.Plan[Result[Out], Response, ToolCall, Part]{
		MaxSteps: plan.maxSteps,
		Retries:  plan.outputRetries,
		Step: func(ctx context.Context, result Result[Out]) (Response, error) {
			return step(ctx, Request{
				Model: plan.model, Instructions: plan.instructions,
				Messages: result.Messages, Tools: plan.specs,
				Output: plan.outputSchema, MaxTokens: plan.maxTokens,
			})
		},
		Record:  (*Result[Out]).recordTurn,
		Unknown: plan.lastUnknownTool,
		Accept:  agent.acceptOutput,
		Reject:  (*Result[Out]).rejectToolCalls,
		Tools: func(ctx context.Context, calls []ToolCall) ([]Part, error) {
			return model.runTools(ctx, plan, calls, emit)
		},
		Append: func(result *Result[Out], parts []Part) {
			result.Messages = append(result.Messages, Message{Role: RoleUser, Parts: parts})
		},
		Complete: func(turn int, response Response) bool {
			return emit(StepCompleted{Step: turn, StopReason: response.StopReason, Usage: response.Usage})
		},
		MaxStepsError: ErrMaxSteps,
		StoppedError:  errStopped,
	})
}

// recordTurn accumulates usage and history even for refused or truncated turns,
// replaces Text with concatenated text parts, and returns tool calls in part
// order. Empty messages are not appended. It retains message parts rather than
// cloning their payloads, matching the run's existing history ownership rules.
// ErrTruncated and ErrRefused stop processing before tool-name tracking,
// execution, or output decoding; other stop reasons remain raw observations.
func (result *Result[Out]) recordTurn(response Response) ([]ToolCall, error) {
	result.Steps++
	result.Usage = result.Usage.add(response.Usage)
	result.StopReason = response.StopReason
	result.Text = ""

	var calls []ToolCall

	for _, part := range response.Message.Parts {
		switch part := part.(type) {
		case Text:
			result.Text += part.Text
		case ToolCall:
			calls = append(calls, part)
		}
	}

	if len(response.Message.Parts) > 0 {
		result.Messages = append(result.Messages, response.Message)
	}

	switch response.StopReason {
	case StopMaxTokens:
		return nil, ErrTruncated
	case StopRefusal, StopContentFilter:
		return nil, ErrRefused
	}

	return calls, nil
}

// acceptOutput decodes the recorded text once and sets Output only on success,
// returning true when the run can finish. A correctable failure consumes one
// remaining retry and appends model-safe feedback before turn completion, even
// on the last allowed turn. Exhausted retries return ErrInvalidOutput with the
// original cause; operational errors return unchanged without adding feedback
// or consuming a retry. The retry counter belongs to this run, not the agent.
func (agent Agent[Out]) acceptOutput(ctx context.Context, result *Result[Out], retries *int) (bool, error) {
	output, err := agent.parse(ctx, result.Text)

	if err == nil {
		result.Output = output

		return true, nil
	}

	feedback, correctable := errors.AsType[*outputFeedback](err)

	if !correctable {
		return false, err
	}

	if *retries <= 0 {
		return false, fmt.Errorf("%w: %w", ErrInvalidOutput, feedback.cause)
	}

	*retries -= 1
	result.Messages = append(result.Messages, UserText("Your reply was invalid: "+feedback.Error()+"\nReply again with JSON that matches the schema."))

	return false, nil
}

// rejectToolCalls repairs history when a nonempty tool turn exhausts the step
// budget. It appends one denial per call in call order without running handlers
// or emitting tool/completion events. The caller returns ErrMaxSteps after this
// repair so saved conversations do not contain unanswered final-turn calls.
func (result *Result[Out]) rejectToolCalls(calls []ToolCall) {
	parts := make([]Part, len(calls))

	for i, call := range calls {
		parts[i] = ToolResult{
			CallID:  call.ID,
			Content: "the step limit was reached; the call was not run",
			IsError: true,
		}
	}

	result.Messages = append(result.Messages, Message{Role: RoleUser, Parts: parts})
}

// lastUnknownTool returns the last unregistered call's error in call order,
// retaining previous when this turn has none. previous is nil until an eligible
// turn requests an unknown tool. Refused and truncated turns must not reach this
// phase, because their calls cannot replace the earlier diagnostic.
func (plan *runPlan) lastUnknownTool(calls []ToolCall, previous error) error {
	for _, call := range calls {
		if _, known := plan.tools[call.Name]; !known {
			previous = fmt.Errorf("%w: %q", ErrUnknownTool, call.Name)
		}
	}

	return previous
}
