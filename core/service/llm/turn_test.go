package llm_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/llm/llmtest"
	"arkoren.dev/foundation/validate"
)

// TestOutputFeedbackStopsOnOperationalCheckError keeps failed infrastructure
// checks out of both the saved conversation and subsequent provider requests.
// The caller must retain the operational cause instead of an invalid-output error.
func TestOutputFeedbackStopsOnOperationalCheckError(t *testing.T) {
	t.Parallel()

	cause := errors.New("private validation database credentials")
	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(context.Context, string) error {
			return fmt.Errorf("check internal catalog: %w", cause)
		})
	})

	agent := llm.NewAgent[string]("summary", func(agent *llm.AgentBuilder[string]) {
		agent.Output(schema)
	})

	fake := llmtest.New()
	fake.Reply(`"draft"`)
	fake.Reply(`"revised"`)

	result, err := llm.New(fake, "m").Run(t.Context(), agent, []llm.Message{llm.UserText("Summarize.")})

	if len(result.Messages) != 2 {
		t.Fatalf("messages = %+v, want only input and model reply", result.Messages)
	}

	if len(fake.Generated()) != 1 {
		t.Fatalf("provider requests = %d, want no correction retry", len(fake.Generated()))
	}

	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want the operational cause", err)
	}

	if errors.Is(err, llm.ErrInvalidOutput) {
		t.Fatalf("error = %v, must not classify infrastructure failure as invalid output", err)
	}
}

// TestOutputFeedbackStopsOnWrappedViolationCheckError treats an arbitrary Check
// error as operational even when it wraps violations. The schema engine returns
// that error unchanged, rather than recording the explicit Fail contract.
func TestOutputFeedbackStopsOnWrappedViolationCheckError(t *testing.T) {
	t.Parallel()

	cause := fmt.Errorf("private catalog lookup: %w", validate.Errors{
		"": {{Code: "required"}},
	})
	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(context.Context, string) error {
			return cause
		})
	})

	agent := llm.NewAgent[string]("summary", func(agent *llm.AgentBuilder[string]) {
		agent.Output(schema)
	})

	fake := llmtest.New()
	fake.Reply(`"draft"`)
	fake.Reply(`"revised"`)

	result, err := llm.New(fake, "m").Run(t.Context(), agent, []llm.Message{llm.UserText("Summarize.")})

	if len(result.Messages) != 2 {
		t.Fatalf("messages = %+v, want no internal error feedback", result.Messages)
	}

	if len(fake.Generated()) != 1 {
		t.Fatalf("provider requests = %d, want no correction retry", len(fake.Generated()))
	}

	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want the original wrapped operational error", err)
	}
}

// TestOutputFeedbackStopsOnCanceledCheck covers cancellation returned by the
// dynamic-value schema's Check. A completed provider turn must not turn that
// error into saved correction text, even if the next provider call would cancel.
func TestOutputFeedbackStopsOnCanceledCheck(t *testing.T) {
	t.Parallel()

	schema := validate.Any(func(value *validate.AnyBuilder) {
		value.Check(func(ctx context.Context, _ any) error {
			return ctx.Err()
		})
	})

	agent := llm.NewAgent[any]("summary", func(agent *llm.AgentBuilder[any]) {
		agent.Output(schema)
	})

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	fake := llmtest.New()
	fake.ReplyWith(func(llm.Request) (llm.Response, error) {
		cancel()

		return llm.Response{
			Message:    llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{llm.Text{Text: `"draft"`}}},
			StopReason: llm.StopEndTurn,
		}, nil
	})

	result, err := llm.New(fake, "m").Run(ctx, agent, []llm.Message{llm.UserText("Summarize.")})

	if len(result.Messages) != 2 {
		t.Fatalf("messages = %+v, want no cancellation feedback", result.Messages)
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}

	if errors.Is(err, llm.ErrInvalidOutput) {
		t.Fatalf("error = %v, must not classify cancellation as invalid output", err)
	}
}

// TestOutputFeedbackStreamStopsOnOperationalCheckError applies the same boundary
// to streamed runs. The stream reports the preserved cause and does not send a
// second request containing an operational diagnostic.
func TestOutputFeedbackStreamStopsOnOperationalCheckError(t *testing.T) {
	t.Parallel()

	cause := errors.New("private validation service credentials")
	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(context.Context, string) error {
			return cause
		})
	})

	agent := llm.NewAgent[string]("summary", func(agent *llm.AgentBuilder[string]) {
		agent.Output(schema)
	})

	fake := llmtest.New()
	fake.Reply(`"draft"`)
	fake.Reply(`"revised"`)

	history := []llm.Message{llm.UserText("Summarize.")}

	for _, err := range llm.New(fake, "m").RunStream(t.Context(), agent, history) {
		if err == nil {
			continue
		}

		if !errors.Is(err, cause) {
			t.Fatalf("error = %v, want the operational cause", err)
		}

		if len(fake.Generated()) != 1 {
			t.Fatalf("provider requests = %d, want no correction retry", len(fake.Generated()))
		}

		return
	}

	t.Fatal("stream completed without reporting the operational error")
}

// TestOutputFeedbackRetriesCheckViolation preserves model correction for Check
// functions that explicitly report a validation failure with validate.Fail.
// The provider receives violations and the corrected reply becomes the result.
func TestOutputFeedbackRetriesCheckViolation(t *testing.T) {
	t.Parallel()

	schema := validate.String(func(text *validate.StringBuilder[string]) {
		text.Check(func(_ context.Context, value string) error {
			if value == "reserved" {
				return validate.Fail("reserved", nil)
			}

			return nil
		})
	})

	agent := llm.NewAgent[string]("summary", func(agent *llm.AgentBuilder[string]) {
		agent.Output(schema)
	})

	fake := llmtest.New()
	fake.Reply(`"reserved"`)
	fake.Reply(`"ready"`)

	result, err := llm.New(fake, "m").Run(t.Context(), agent, []llm.Message{llm.UserText("Summarize.")})

	if err != nil {
		t.Fatal(err)
	}

	if result.Output != "ready" {
		t.Fatalf("output = %q, want the corrected value", result.Output)
	}

	requests := fake.Generated()

	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want one correction retry", len(requests))
	}

	feedback, ok := requests[1].Messages[2].Parts[0].(llm.Text)

	if !ok || !strings.Contains(feedback.Text, "validation failed") {
		t.Fatalf("feedback = %+v, want validation violations", feedback)
	}
}

// TestOutputFeedbackRetriesInvalidJSON preserves the JSON decoding correction
// path independently of schema validation. A malformed reply is retried once
// and the valid JSON reply is decoded into the declared result type.
func TestOutputFeedbackRetriesInvalidJSON(t *testing.T) {
	t.Parallel()

	agent := llm.NewAgent[string]("summary", func(agent *llm.AgentBuilder[string]) {
		agent.Output(validate.String[string]())
	})

	fake := llmtest.New()
	fake.Reply(`not json`)
	fake.Reply(`"ready"`)

	result, err := llm.New(fake, "m").Run(t.Context(), agent, []llm.Message{llm.UserText("Summarize.")})

	if err != nil {
		t.Fatal(err)
	}

	if result.Output != "ready" {
		t.Fatalf("output = %q, want the corrected value", result.Output)
	}

	if len(fake.Generated()) != 2 {
		t.Fatalf("provider requests = %d, want one JSON correction retry", len(fake.Generated()))
	}
}

// TestRunCharacterizesFinalToolTurn preserves partial text, usage, history,
// unknown-tool identity, and the rule that a final-budget tool call is not run.
func TestRunCharacterizesFinalToolTurn(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	lookup := llm.NewTool("lookup", "Find an order.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) {
			calls.Add(1)

			return "found", nil
		})

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(lookup)
		agent.MaxSteps(2)
	})

	fake := llmtest.New()
	fake.ReplyResponse(llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
			llm.Text{Text: "searching"},
			llm.ToolCall{ID: "unknown", Name: "missing", Arguments: []byte(`{}`)},
			llm.ToolCall{ID: "known", Name: "lookup", Arguments: []byte(`{}`)},
		}},
		StopReason: llm.StopToolUse,
		Usage:      llm.Usage{InputTokens: 4, OutputTokens: 2},
	})

	last := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
		llm.Text{Text: "partial "},
		llm.Reasoning{Text: "still searching", Signature: "opaque"},
		llm.Text{Text: "reply"},
		llm.ToolCall{ID: "last-known", Name: "lookup", Arguments: []byte(`{}`)},
		llm.ToolCall{ID: "last-unknown", Name: "another-missing", Arguments: []byte(`{}`)},
	}}
	fake.ReplyResponse(llm.Response{
		Message: last, StopReason: llm.StopToolUse,
		Usage: llm.Usage{InputTokens: 7, OutputTokens: 3},
	})

	result, err := llm.New(fake, "model").Run(t.Context(), agent, []llm.Message{llm.UserText("Find it.")})

	if !errors.Is(err, llm.ErrMaxSteps) || !errors.Is(err, llm.ErrUnknownTool) {
		t.Fatalf("failure identities: %v", err)
	}

	if !strings.Contains(err.Error(), `"another-missing"`) {
		t.Fatalf("last unknown tool identity was lost: %v", err)
	}

	if result.Text != "partial reply" || result.Output != "" {
		t.Fatalf("partial output: text %q, output %q", result.Text, result.Output)
	}

	if result.Steps != 2 || result.StopReason != llm.StopToolUse || calls.Load() != 1 {
		t.Fatalf("steps %d, reason %q, handler calls %d", result.Steps, result.StopReason, calls.Load())
	}

	if result.Usage != (llm.Usage{InputTokens: 11, OutputTokens: 5}) {
		t.Fatalf("usage: %+v", result.Usage)
	}

	if len(result.Messages) != 5 || !reflect.DeepEqual(result.Messages[3], last) {
		t.Fatalf("last assistant turn was not preserved: %+v", result.Messages)
	}

	want := []llm.Part{
		llm.ToolResult{CallID: "last-known", Content: "the step limit was reached; the call was not run", IsError: true},
		llm.ToolResult{CallID: "last-unknown", Content: "the step limit was reached; the call was not run", IsError: true},
	}

	if result.Messages[4].Role != llm.RoleUser || !reflect.DeepEqual(result.Messages[4].Parts, want) {
		t.Fatalf("unanswered final calls: %+v", result.Messages[4])
	}
}

// TestRunCharacterizesTruncatedCalls records an incomplete turn before failing,
// but does not treat its tool calls as eligible for execution or unknown-tool
// tracking. In particular, truncation wins over the final-turn call limit.
func TestRunCharacterizesTruncatedCalls(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	message := llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
		llm.Text{Text: "partial"},
		llm.ToolCall{ID: "incomplete", Name: "missing", Arguments: []byte(`{}`)},
	}}
	fake.ReplyResponse(llm.Response{
		Message: message, StopReason: llm.StopMaxTokens,
		Usage: llm.Usage{InputTokens: 4, OutputTokens: 2},
	})

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.MaxSteps(1)
	})

	result, err := llm.New(fake, "model").Run(t.Context(), agent, []llm.Message{llm.UserText("Find it.")})

	if !errors.Is(err, llm.ErrTruncated) || errors.Is(err, llm.ErrUnknownTool) {
		t.Fatalf("incomplete call changed the failure identity: %v", err)
	}

	if result.Steps != 1 || result.Text != "partial" || result.StopReason != llm.StopMaxTokens {
		t.Fatalf("incomplete turn was not recorded: %+v", result)
	}

	if result.Usage != (llm.Usage{InputTokens: 4, OutputTokens: 2}) {
		t.Fatalf("incomplete turn usage: %+v", result.Usage)
	}

	if len(result.Messages) != 2 || !reflect.DeepEqual(result.Messages[1], message) {
		t.Fatalf("incomplete call was answered or its message was lost: %+v", result.Messages)
	}
}

// TestRunCharacterizesUnknownToolBeforeRefusal retains the last unknown tool
// from eligible turns, not a name from the refused turn. This identity survives
// an intervening known-tool turn without changing the successful tool history.
func TestRunCharacterizesUnknownToolBeforeRefusal(t *testing.T) {
	t.Parallel()

	lookup := llm.NewTool("lookup", "Find an order.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) {
			return "found", nil
		})

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(lookup)
	})

	fake := llmtest.New()
	fake.ReplyToolCall("earlier-missing", `{}`)
	fake.ReplyToolCall("lookup", `{}`)
	fake.ReplyResponse(llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{
			llm.Text{Text: "refused"},
			llm.ToolCall{ID: "refused", Name: "later-missing", Arguments: []byte(`{}`)},
		}},
		StopReason: llm.StopRefusal,
	})

	result, err := llm.New(fake, "model").Run(t.Context(), agent, []llm.Message{llm.UserText("Find it.")})

	if !errors.Is(err, llm.ErrRefused) || !errors.Is(err, llm.ErrUnknownTool) {
		t.Fatalf("earlier unknown tool identity was lost: %v", err)
	}

	if !strings.Contains(err.Error(), `"earlier-missing"`) || strings.Contains(err.Error(), `"later-missing"`) {
		t.Fatalf("refused calls changed the unknown tool identity: %v", err)
	}

	if result.Steps != 3 || result.Text != "refused" || len(result.Messages) != 6 {
		t.Fatalf("partial history was lost: %+v", result)
	}
}

// TestRunCharacterizesCorrectionAtTurnLimit retains feedback already added by
// the last allowed turn, and distinguishes exhausted turns from exhausted output
// correction attempts. Neither invalid reply becomes a typed output.
func TestRunCharacterizesCorrectionAtTurnLimit(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.Reply(`{"priority":"invalid"}`)
	fake.Reply(`not json`)

	agent := llm.NewAgent[ticket]("triage", func(agent *llm.AgentBuilder[ticket]) {
		agent.Output(ticketSchema)
		agent.OutputRetries(2)
		agent.MaxSteps(2)
	})

	result, err := llm.New(fake, "model").Run(t.Context(), agent, []llm.Message{llm.UserText("Classify it.")})

	if !errors.Is(err, llm.ErrMaxSteps) || errors.Is(err, llm.ErrInvalidOutput) {
		t.Fatalf("turn budget was confused with output correction budget: %v", err)
	}

	if result.Output != (ticket{}) || result.Text != "not json" || result.Steps != 2 {
		t.Fatalf("invalid output was published: %+v", result)
	}

	if len(result.Messages) != 5 || result.Messages[4].Role != llm.RoleUser {
		t.Fatalf("last correction feedback was lost: %+v", result.Messages)
	}

	feedback, ok := result.Messages[4].Parts[0].(llm.Text)

	if !ok || feedback.Text == "" {
		t.Fatalf("last correction was not text: %+v", result.Messages[4])
	}

	if requests := fake.Generated(); len(requests) != 2 || len(requests[1].Messages) != 3 {
		t.Fatalf("correction conversation: %+v", requests)
	}
}

// TestRunStreamCharacterizesFinalToolTurn keeps loop events separate from raw
// model events: the last-budget call has no admission or completion event.
func TestRunStreamCharacterizesFinalToolTurn(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	lookup := llm.NewTool("lookup", "Find an order.", validate.Infer[struct{}](),
		func(context.Context, struct{}) (string, error) {
			calls.Add(1)

			return "found", nil
		})

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(lookup)
		agent.MaxSteps(2)
	})

	fake := llmtest.New()
	fake.ReplyParts(llm.ToolCall{ID: "unknown", Name: "missing", Arguments: []byte(`{}`)})
	fake.ReplyParts(llm.ToolCall{ID: "last", Name: "lookup", Arguments: []byte(`{}`)})
	var loop []llm.Event
	var failure error

	for item, err := range llm.New(fake, "model").RunStream(t.Context(), agent, []llm.Message{llm.UserText("Find it.")}) {
		if err != nil {
			failure = err
		}

		switch item.(type) {
		case llm.ToolStarted, llm.ToolFinished, llm.StepCompleted:
			loop = append(loop, item)
		}
	}

	if !errors.Is(failure, llm.ErrMaxSteps) || !errors.Is(failure, llm.ErrUnknownTool) {
		t.Fatalf("stream failure identities: %v", failure)
	}

	want := []llm.Event{
		llm.ToolStarted{ID: "unknown", Name: "missing"},
		llm.ToolFinished{ID: "unknown", Name: "missing", IsError: true},
		llm.StepCompleted{Step: 1, StopReason: llm.StopToolUse},
	}

	if !reflect.DeepEqual(loop, want) || calls.Load() != 0 {
		t.Fatalf("loop events %+v, handler calls %d", loop, calls.Load())
	}
}

// TestRunStreamCharacterizesCorrectionStop stops after correction feedback has
// been prepared. The stream must not request another reply or publish a terminal
// agent outcome after its consumer stops at StepCompleted.
func TestRunStreamCharacterizesCorrectionStop(t *testing.T) {
	t.Parallel()

	bus := event.New()
	outcomes := 0

	if _, err := bus.Listen(llm.AgentCompleted, func(context.Context, llm.AgentCompletedEvent) error {
		outcomes++

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := bus.Listen(llm.AgentFailed, func(context.Context, llm.AgentFailedEvent) error {
		outcomes++

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fake := llmtest.New()
	fake.Reply(`{"priority":"invalid"}`)
	fake.Reply(`{"priority":"high"}`)

	agent := llm.NewAgent[ticket]("triage", func(agent *llm.AgentBuilder[ticket]) {
		agent.Output(ticketSchema)
	})

	model := llm.New(fake, "model", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	for item, err := range model.RunStream(t.Context(), agent, []llm.Message{llm.UserText("Classify it.")}) {
		if err != nil {
			t.Fatal(err)
		}

		if completed, ok := item.(llm.StepCompleted); ok {
			if completed.Step != 1 {
				t.Fatalf("unexpected completed turn: %+v", completed)
			}

			break
		}
	}

	if len(fake.Generated()) != 1 || outcomes != 0 {
		t.Fatalf("requests %d, terminal outcomes %d", len(fake.Generated()), outcomes)
	}
}
