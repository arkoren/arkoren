package llm_test

import (
	"errors"
	"testing"

	"arkoren.dev/core/service/llm"
	"arkoren.dev/core/service/llm/llmtest"
	"arkoren.dev/foundation/validate"
)

// TestRunRejectsZeroAgent prevents an unnamed definition from consuming a
// model reply even when its exact string output would otherwise be valid.
func TestRunRejectsZeroAgent(t *testing.T) {
	t.Parallel()

	fake := llmtest.New()
	fake.Reply("must not be requested")
	var agent llm.Agent[string]

	_, err := llm.New(fake, "model").Run(t.Context(), agent, []llm.Message{llm.UserText("Help.")})

	if !errors.Is(err, llm.ErrInvalidName) {
		t.Fatalf("zero agent accepted: %v", err)
	}

	if len(fake.Generated()) != 0 {
		t.Fatal("invalid agent reached the model")
	}
}

// TestAgentDefinitionErrors checks invalid identities, tools, and output schemas.
// Declaration errors must be observable before an agent is run.
func TestAgentDefinitionErrors(t *testing.T) {
	t.Parallel()

	if err := llm.NewAgent[string]("Bad Name").Err(); !errors.Is(err, llm.ErrInvalidName) {
		t.Fatalf("bad agent name: %v", err)
	}

	if err := llm.NewAgent[ticket]("triage").Err(); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("non-string output without schema: %v", err)
	}

	if err := llm.NewAgent[ticket]("triage", func(agent *llm.AgentBuilder[ticket]) {
		agent.Output(ticketSchema)
	}).Err(); err != nil {
		t.Fatalf("with schema: %v", err)
	}

	if err := llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("x"), echoTool("x"))
	}).Err(); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("duplicate tool: %v", err)
	}

	if err := llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("Bad Tool"))
	}).Err(); !errors.Is(err, llm.ErrInvalidName) {
		t.Fatalf("bad tool name: %v", err)
	}

	if err := llm.NewTool[orderArgs, string]("x", "x", nil, nil).Err(); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("nil schema: %v", err)
	}

	if err := (llm.Tool{}).Err(); err == nil {
		t.Fatal("zero Tool has no error")
	}
}

// TestAgentPublicationDetachesRetainedDraftAndToolInput verifies that input
// replacement and later draft mutations cannot change published settings/errors.
func TestAgentPublicationDetachesRetainedDraftAndToolInput(t *testing.T) {
	t.Parallel()

	tools := []llm.Tool{echoTool("lookup")}
	var retained *llm.AgentBuilder[string]
	base := llm.NewAgent[string]("a", func(agent *llm.AgentBuilder[string]) {
		retained = agent
		agent.Instructions("original")
		agent.MaxTokens(123)
		agent.Tools(tools...)

		tools[0] = echoTool("replaced-input")
	})
	retained.Instructions("changed")
	retained.Tools(echoTool("changed"))
	retained.MaxTokens(7)
	retained.Output(nil)

	fake := llmtest.New()
	fake.Reply("hi")

	if _, err := llm.New(fake, "m").Run(t.Context(), base, userHistory()); err != nil {
		t.Fatal(err)
	}

	request := fake.Generated()[0]

	if request.Instructions != "original" || len(request.Tools) != 1 || request.Tools[0].Name != "lookup" {
		t.Fatalf("request = %+v, want the published snapshot", request)
	}

	if request.MaxTokens != 123 {
		t.Fatalf("max tokens = %d, want published limit", request.MaxTokens)
	}
}

// TestAgentCallbacksShareDraft verifies ordered synchronous edits and identity
// returns, including a required output supplied only by the final callback.
func TestAgentCallbacksShareDraft(t *testing.T) {
	t.Parallel()

	var retained *llm.AgentBuilder[ticket]
	calls := 0
	agent := llm.NewAgent[ticket]("triage", func(agent *llm.AgentBuilder[ticket]) {
		retained = agent
		calls++

		if agent.Instructions("first") != agent {
			t.Fatal("Instructions returned another draft")
		}
	}, func(agent *llm.AgentBuilder[ticket]) {
		calls++

		if agent != retained {
			t.Fatal("callbacks received different drafts")
		}

		if agent.Tools() != agent || agent.Output(ticketSchema) != agent {
			t.Fatal("Tools or Output returned another draft")
		}

		agent.Instructions("last")
	})

	if calls != 2 || agent.Err() != nil || agent.Name() != "triage" {
		t.Fatalf("callbacks = %d, name = %q, error = %v", calls, agent.Name(), agent.Err())
	}

	fake := llmtest.New()
	fake.Reply(`{"priority":"high"}`)

	if _, err := llm.New(fake, "m").Run(t.Context(), agent, userHistory()); err != nil {
		t.Fatal(err)
	}

	if fake.Generated()[0].Instructions != "last" {
		t.Fatal("later callback did not replace instructions")
	}
}

// TestAgentLimitSettingsReturnSameDraft checks fluent draft identity without
// publishing intermediate agents or changing the existing per-run options.
func TestAgentLimitSettingsReturnSameDraft(t *testing.T) {
	t.Parallel()

	llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		if agent.MaxSteps(3) != agent || agent.MaxTokens(100) != agent {
			t.Fatal("turn limits returned another draft")
		}

		if agent.ToolConcurrency(1) != agent || agent.OutputRetries(-1) != agent {
			t.Fatal("execution limits returned another draft")
		}
	})
}

// TestAgentEmptyCallbackSlicesKeepDefaults covers omitted, nil and empty
// callback lists as equivalent declarations, not nil callback entries.
func TestAgentEmptyCallbackSlicesKeepDefaults(t *testing.T) {
	t.Parallel()

	var callbacks []func(*llm.AgentBuilder[string])
	agents := []llm.Agent[string]{
		llm.NewAgent[string]("support"),
		llm.NewAgent[string]("support", callbacks...),
		llm.NewAgent[string]("support", []func(*llm.AgentBuilder[string]){}...),
	}

	for _, agent := range agents {
		fake := llmtest.New()
		fake.Reply("done")

		if _, err := llm.New(fake, "m").Run(t.Context(), agent, userHistory()); err != nil {
			t.Fatal(err)
		}

		request := fake.Generated()[0]

		if request.Instructions != "" || len(request.Tools) != 0 || len(request.Output) != 0 {
			t.Fatalf("default request = %+v", request)
		}
	}
}

// TestAgentNilCallbackPanics distinguishes an explicit nil callback from an
// expanded nil slice. Construction does not recover application misuse.
func TestAgentNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil callback did not panic")
		}
	}()
	llm.NewAgent[string]("support", nil)
}

// TestAgentToolsReplace verifies later callbacks replace complete tool lists
// instead of appending, including removal with an empty call.
func TestAgentToolsReplace(t *testing.T) {
	t.Parallel()

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("old"))
	}, func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("new"))
	})

	fake := llmtest.New()
	fake.Reply("done")

	if _, err := llm.New(fake, "m").Run(t.Context(), agent, userHistory()); err != nil {
		t.Fatal(err)
	}

	tools := fake.Generated()[0].Tools

	if len(tools) != 1 || tools[0].Name != "new" {
		t.Fatalf("tools = %+v", tools)
	}
}

// TestAgentToolsEmptyRemovesTools checks that empty replacement is a valid
// declaration rather than a request to keep the previous tool list.
func TestAgentToolsEmptyRemovesTools(t *testing.T) {
	t.Parallel()

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("old"))
		agent.Tools()
	})

	fake := llmtest.New()
	fake.Reply("done")

	if _, err := llm.New(fake, "m").Run(t.Context(), agent, userHistory()); err != nil {
		t.Fatal(err)
	}

	if len(fake.Generated()[0].Tools) != 0 {
		t.Fatal("empty replacement retained tools")
	}
}

// TestAgentDuplicateErrorSticksBeforeIO checks that replacing an invalid
// registration cannot repair the first error, in either execution path.
func TestAgentDuplicateErrorSticksBeforeIO(t *testing.T) {
	t.Parallel()

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("same"), echoTool("same"))
	}, func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("valid"))
	})

	fake := llmtest.New()
	model := llm.New(fake, "m")

	if _, err := model.Run(t.Context(), agent, userHistory()); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("Run error = %v", err)
	}

	for _, err := range model.RunStream(t.Context(), agent, userHistory()) {
		if !errors.Is(err, llm.ErrInvalidRequest) {
			t.Fatalf("RunStream error = %v", err)
		}
	}

	if len(fake.Generated()) != 0 {
		t.Fatal("invalid agent reached driver")
	}
}

// TestAgentNilOutputErrorSticks checks immediate output validation and the
// first-error rule even when a later callback supplies a valid schema.
func TestAgentNilOutputErrorSticks(t *testing.T) {
	t.Parallel()

	agent := llm.NewAgent[ticket]("triage", func(agent *llm.AgentBuilder[ticket]) {
		agent.Output(nil)
	}, func(agent *llm.AgentBuilder[ticket]) {
		agent.Output(ticketSchema)
	})

	if !errors.Is(agent.Err(), llm.ErrInvalidRequest) {
		t.Fatalf("error = %v", agent.Err())
	}
}

// TestAgentToolErrorSticks preserves the tool's original sentinel when later
// valid tools and a separate invalid output setting edit the same draft.
func TestAgentToolErrorSticks(t *testing.T) {
	t.Parallel()

	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
		agent.Tools(echoTool("Bad Tool"))
		agent.Tools(echoTool("valid"))
		agent.Output(nil)
	})

	if !errors.Is(agent.Err(), llm.ErrInvalidName) {
		t.Fatalf("error = %v, want first tool error", agent.Err())
	}
}

// TestAgentOutputRenderErrorSticks uses an uninitialized slice schema to prove
// rendering errors are captured during configuration, not deferred to a run.
func TestAgentOutputRenderErrorSticks(t *testing.T) {
	t.Parallel()

	agent := llm.NewAgent[[]string]("summary", func(agent *llm.AgentBuilder[[]string]) {
		agent.Output(validate.SliceSchema[string]{})
		agent.Output(validate.Slice(func(items *validate.SliceBuilder[string]) {
			items.Values(func(value *validate.StringBuilder[string]) {
			})
		}))
	})

	if !errors.Is(agent.Err(), llm.ErrInvalidRequest) {
		t.Fatalf("error = %v", agent.Err())
	}
}

// TestAgentNamedStringRequiresOutput preserves the exact-string assertion;
// a type whose underlying type is string is not a plain-text result type.
func TestAgentNamedStringRequiresOutput(t *testing.T) {
	t.Parallel()

	type summary string

	if err := llm.NewAgent[summary]("summary").Err(); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("named string error = %v", err)
	}
}

// TestAgentPublicationDetachesOutput checks both parsing and the advertised
// JSON Schema after a retained draft selects a different structured schema.
func TestAgentPublicationDetachesOutput(t *testing.T) {
	t.Parallel()

	var retained *llm.AgentBuilder[ticket]
	agent := llm.NewAgent[ticket]("triage", func(agent *llm.AgentBuilder[ticket]) {
		retained = agent
		agent.Output(ticketSchema)
	})
	retained.Output(validate.Struct(func(object *validate.StructBuilder[ticket]) {
		object.Field("Priority", func(priority *validate.StringBuilder[string]) {
			priority.OneOf("changed")
		})
	}))

	fake := llmtest.New()
	fake.Reply(`{"priority":"high"}`)

	result, err := llm.New(fake, "m").Run(t.Context(), agent, userHistory())

	if err != nil {
		t.Fatal(err)
	}

	want, err := ticketSchema.JSONSchema()

	if err != nil {
		t.Fatal(err)
	}

	if result.Output.Priority != "high" || string(fake.Generated()[0].Output) != string(want) {
		t.Fatal("retained draft changed the published output")
	}
}

// TestAgentCallbacksUseFreshDrafts checks that a reusable policy gets a new
// draft per construction rather than sharing previous agents' settings.
func TestAgentCallbacksUseFreshDrafts(t *testing.T) {
	t.Parallel()

	var drafts []*llm.AgentBuilder[string]
	policy := func(agent *llm.AgentBuilder[string]) {
		drafts = append(drafts, agent)
		agent.Instructions("shared policy")
	}

	first := llm.NewAgent[string]("first", policy)
	second := llm.NewAgent[string]("second", policy)

	if drafts[0] == drafts[1] {
		t.Fatal("constructors reused a draft")
	}

	if first.Name() != "first" || second.Name() != "second" {
		t.Fatal("constructors shared an identity")
	}
}

// TestAgentInvalidNameStillInvokesCallbacks checks direct callback execution
// and first-error precedence rather than suppressing application declarations.
func TestAgentInvalidNameStillInvokesCallbacks(t *testing.T) {
	t.Parallel()

	calls := 0
	agent := llm.NewAgent[string]("Bad Name", func(agent *llm.AgentBuilder[string]) {
		calls++
		agent.Output(nil)
	})

	if calls != 1 || !errors.Is(agent.Err(), llm.ErrInvalidName) {
		t.Fatalf("callbacks = %d, error = %v", calls, agent.Err())
	}
}
