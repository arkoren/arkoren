package llm_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/llm/llmtest"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestGenerateEmitsSpanAndMetricsWithoutContent checks generation instrumentation.
// Token counts may be recorded, but prompt and response content must stay private.
func TestGenerateEmitsSpanAndMetricsWithoutContent(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	model := llm.New(newStub(), "gpt-5.2", func(model *llm.ModelBuilder) {
		model.Telemetry(fake.Telemetry(t))
		model.Provider("openai")
	})

	if _, err := model.Generate(t.Context(), userRequest(secretPrompt)); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "chat gpt-5.2",
		telemetry.String("gen_ai.operation.name", "chat"),
		telemetry.String("gen_ai.provider.name", "openai"),
		telemetry.String("gen_ai.request.model", "gpt-5.2"),
		telemetry.String("gen_ai.response.model", "gpt-5.2-2026-01-01"),
		telemetry.String("gen_ai.response.id", "resp_1"),
		telemetry.Int("gen_ai.usage.input_tokens", 12),
		telemetry.Int("gen_ai.usage.output_tokens", 5))
	fake.AssertHistogram(t, "gen_ai.client.operation.duration", 1, telemetry.String("gen_ai.operation.name", "chat"))
	fake.AssertHistogram(t, "gen_ai.client.token.usage", 1, telemetry.String("gen_ai.token.type", "input"))
	fake.AssertHistogram(t, "gen_ai.client.token.usage", 1, telemetry.String("gen_ai.token.type", "output"))

	span, _ := fake.Span("chat gpt-5.2")

	for _, attr := range span.Attrs {
		if attr.Text() == secretPrompt || attr.Text() == "hi" {
			t.Errorf("span attribute %q carries content", attr.Key())
		}
	}
}

// TestGenerateFailureClassifiesErrorWithoutText checks low-cardinality error labels.
// Backend error text must not become recorded telemetry attributes.
func TestGenerateFailureClassifiesErrorWithoutText(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	driver := newStub()

	driver.err = errors.Join(llm.ErrRateLimited, errors.New(secretPrompt))
	model := llm.New(driver, "gpt-5.2", func(model *llm.ModelBuilder) {
		model.Telemetry(fake.Telemetry(t))
	})

	if _, err := model.Generate(t.Context(), userRequest("x")); err == nil {
		t.Fatal("want an error")
	}

	fake.AssertSpan(t, "chat gpt-5.2", telemetry.String("error.type", "llm.ErrRateLimited"))

	span, _ := fake.Span("chat gpt-5.2")

	if span.Status != telemetry.Error || len(span.Events) != 0 {
		t.Fatalf("status = %v, events = %d, want error and no events", span.Status, len(span.Events))
	}
}

// TestRunEmitsSpansWithoutContent checks agent and tool instrumentation.
// Conversation content must not leak into recorded spans.
func TestRunEmitsSpansWithoutContent(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.ReplyToolCall("lookup", `{"number":"A-42"}`)
	fake.Reply("done")

	telemetryFake := telemetrytest.New()
	model := llm.New(fake, "gpt-5.2", func(model *llm.ModelBuilder) {
		model.Telemetry(telemetryFake.Telemetry(t))
		model.Provider("openai")
	})

	if _, err := model.Run(t.Context(), llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("lookup"))
	}), []llm.Message{llm.UserText(secretPrompt)}); err != nil {
		t.Fatal(err)
	}

	telemetryFake.AssertSpan(t, "invoke_agent support",
		telemetry.String("gen_ai.operation.name", "invoke_agent"),
		telemetry.String("gen_ai.agent.name", "support"),
		telemetry.Int("arkoren.llm.agent.steps", 2))
	telemetryFake.AssertSpan(t, "execute_tool lookup",
		telemetry.String("gen_ai.tool.name", "lookup"),
		telemetry.String("gen_ai.agent.name", "support"))
	telemetryFake.AssertSpan(t, "chat gpt-5.2")
	telemetryFake.AssertSpanCount(t, 4)

	for _, name := range []string{"invoke_agent support", "execute_tool lookup"} {
		span, _ := telemetryFake.Span(name)

		for _, attr := range span.Attrs {
			if strings.Contains(attr.Text(), secretPrompt) || strings.Contains(attr.Text(), "A-42") {
				t.Errorf("%s attribute %q carries content", name, attr.Key())
			}
		}
	}
}

// TestRunPublishesAgentAndToolEventsOnly checks the agent-level event boundary.
// Internal turns must not additionally publish facade response events.
func TestRunPublishesAgentAndToolEventsOnly(t *testing.T) {
	t.Parallel()

	bus := event.New()

	var completed []llm.AgentCompletedEvent

	var tools, failedTools, responses int

	listen := func(err error) {
		t.Helper()

		if err != nil {
			t.Fatal(err)
		}
	}

	_, err := bus.Listen(llm.AgentCompleted, func(_ context.Context, payload llm.AgentCompletedEvent) error {
		completed = append(completed, payload)

		return nil
	})
	listen(err)

	_, err = bus.Listen(llm.ToolInvoked, func(context.Context, llm.ToolInvokedEvent) error {
		tools++

		return nil
	})
	listen(err)

	_, err = bus.Listen(llm.ToolFailed, func(context.Context, llm.ToolFailedEvent) error {
		failedTools++

		return nil
	})
	listen(err)

	_, err = bus.Listen(llm.ResponseGenerated, func(context.Context, llm.ResponseGeneratedEvent) error {
		responses++

		return nil
	})
	listen(err)

	fake := llmtest.New()
	fake.ReplyToolCall("lookup", `{"number":"x"}`)
	fake.ReplyToolCall("lookup", `{"number":"A-42"}`)
	fake.Reply("done")

	model := llm.New(fake, "gpt-5.2", func(model *llm.ModelBuilder) {
		model.Events(bus)
	})

	if _, err := model.Run(t.Context(), llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("lookup"))
	}), userHistory()); err != nil {
		t.Fatal(err)
	}

	if len(completed) != 1 || completed[0].Agent != "support" || completed[0].Steps != 3 || completed[0].Model != "gpt-5.2" {
		t.Fatalf("completed = %+v", completed)
	}

	if tools != 1 || failedTools != 1 || responses != 0 {
		t.Fatalf("tools = %d, failed tools = %d, responses = %d", tools, failedTools, responses)
	}
}

// TestRunPublishesAgentFailed checks reporting when the agent cannot complete.
// The failure event must describe the run without exposing prompt content.
func TestRunPublishesAgentFailed(t *testing.T) {
	t.Parallel()

	bus := event.New()

	var failed []llm.AgentFailedEvent

	if _, err := bus.Listen(llm.AgentFailed, func(_ context.Context, payload llm.AgentFailedEvent) error {
		failed = append(failed, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fake := llmtest.New()
	fake.FailNext(llm.ErrRateLimited)

	if _, err := llm.New(fake, "m", func(model *llm.ModelBuilder) {
		model.Events(bus)
	}).Run(t.Context(), llm.NewAgent[string]("a"), userHistory()); !errors.Is(err, llm.ErrRateLimited) {
		t.Fatalf("error = %v", err)
	}

	if len(failed) != 1 || failed[0].ErrorType != "llm.ErrRateLimited" || failed[0].Steps != 0 {
		t.Fatalf("failed = %+v", failed)
	}
}
