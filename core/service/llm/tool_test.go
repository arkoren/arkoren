package llm_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/llm/llmtest"
)

// TestRunToolErrorTextReachesModelOnlyThroughToolError protects internal failures.
// Only explicitly model-safe error text may be returned in tool results.
func TestRunToolErrorTextReachesModelOnlyThroughToolError(t *testing.T) {
	t.Parallel()

	visible := llm.NewTool("visible", "x", orderSchema, func(context.Context, orderArgs) (string, error) {
		return "", errors.New("wrapped: " + llm.ToolError("no such order").Error() + " select * from orders")
	})

	wrapped := llm.NewTool("wrapped", "x", orderSchema, func(context.Context, orderArgs) (string, error) {
		return "", errors.Join(errors.New("db at 10.0.0.7 down"), llm.ToolError("try again later"))
	})

	hidden := llm.NewTool("hidden", "x", orderSchema, func(context.Context, orderArgs) (string, error) {
		return "", errors.New("db at 10.0.0.7 down")
	})

	fake := llmtest.New()
	fake.ReplyParts(call("1", "visible", `{"number":"A-42"}`), call("2", "wrapped", `{"number":"A-42"}`), call("3", "hidden", `{"number":"A-42"}`))
	fake.Reply("done")

	result, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(visible, wrapped, hidden)
	}), userHistory())

	if err != nil {
		t.Fatal(err)
	}

	results := toolResults(t, result.Messages[:3])

	if len(results) != 3 {
		t.Fatalf("results = %+v", results)
	}

	if results[1].Content != "try again later" || !results[1].IsError {
		t.Fatalf("ToolError result = %+v", results[1])
	}

	if results[2].Content != "the tool failed" || !results[2].IsError {
		t.Fatalf("generic result = %+v", results[2])
	}

	if strings.Contains(results[0].Content, "orders") && results[0].Content != "the tool failed" {
		t.Fatalf("a plain error leaked: %q", results[0].Content)
	}
}

// TestRunEncodesNonStringToolResultsAsJSON checks typed tool result encoding.
// Structured return values must reach the next model turn as JSON text.
func TestRunEncodesNonStringToolResultsAsJSON(t *testing.T) {
	t.Parallel()

	tool := llm.NewTool("lookup", "x", orderSchema, func(_ context.Context, in orderArgs) (ticket, error) {
		return ticket{Priority: in.Number}, nil
	})

	fake := llmtest.New()
	fake.ReplyToolCall("lookup", `{"number":"abc"}`)
	fake.Reply("done")

	result, err := llm.New(fake, "m").Run(t.Context(), llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(tool)
	}), userHistory())

	if err != nil {
		t.Fatal(err)
	}

	if got := toolResults(t, result.Messages[:3])[0].Content; got != `{"priority":"abc"}` {
		t.Fatalf("content = %q", got)
	}
}
