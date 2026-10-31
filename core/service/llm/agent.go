package llm

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"slices"

	"arkoren.dev/core/internal/name"
	"arkoren.dev/foundation/validate"
)

// defaultOutputRetries is how many times an agent sends validation errors
// back to the model when its final reply is invalid.
const defaultOutputRetries = 1

// Agent is an immutable definition of a model loop: instructions, tools and
// an optional output type, run with [Model.Run] or [Model.RunStream]. Out is
// the type of the final result: string for plain text, or the type that the
// [AgentBuilder.Output] schema decodes the final reply into.
//
// Configure it through [NewAgent] callbacks, normally in a package-level
// variable. The first invalid input is kept and reported by [Agent.Err];
// [Model.Run] returns it before any I/O.
//
//	var triage = llm.NewAgent[string]("triage", func(agent *llm.AgentBuilder[string]) {
//		agent.Instructions("Classify the ticket.")
//		agent.MaxSteps(5)
//	})
type Agent[Out any] struct {
	// name identifies the agent in telemetry and events.
	name string

	// instructions are the system instructions.
	instructions string

	// tools are the tools the model may call.
	tools []Tool

	// output validates and decodes the final reply. Nil means the reply is
	// plain text.
	output validate.Schema[Out]

	// outputSchema is the JSON Schema of output, sent to the model.
	outputSchema []byte

	// maxSteps, maxTokens, toolConcurrency and outputRetries are the
	// settings documented on their setters; zero means unset.
	maxSteps, maxTokens, toolConcurrency, outputRetries int

	// err is the first invalid input.
	err error
}

// AgentBuilder is the mutable declaration draft supplied by [NewAgent]. Its
// methods return the same draft. A retained draft cannot change a published
// Agent; schema implementations and function captures remain shared.
//
//	agent := llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
//		agent.Instructions("Answer briefly.")
//	})
//	fmt.Println(agent.Name())
type AgentBuilder[Out any] struct {
	// definition holds the draft settings, detached when NewAgent publishes.
	definition Agent[Out]
}

// outputFeedback marks a decoding error or schema violation that the model can
// correct. Only parse creates it; arbitrary schema errors remain unmarked so
// their text cannot enter the saved conversation or provider correction requests.
// The run returns cause on exhaustion, keeping this marker inside the package.
type outputFeedback struct {
	// cause retains the correctable failure for feedback and error matching.
	cause error
}

// Error returns the correctable diagnostic without changing its text. Operational
// schema errors must never be stored in cause, because this text is model-facing.
//
//	failure := &outputFeedback{cause: errors.New("invalid JSON")}
//	fmt.Println(failure.Error())
func (feedback *outputFeedback) Error() string {
	return feedback.cause.Error()
}

// Unwrap preserves the decoding or validation cause when correction retries are
// exhausted. Callers can inspect the original failure through errors.Is/As.
//
//	cause := errors.New("invalid JSON")
//	failure := &outputFeedback{cause: cause}
//	fmt.Println(errors.Is(failure, cause))
func (feedback *outputFeedback) Unwrap() error {
	return feedback.cause
}

// NewAgent publishes an Agent called agentName from optional declaration callbacks.
// Without callbacks it has no instructions or tools. agentName
// follows the naming rules of the async packages: 1-64 bytes of lowercase
// letters, digits and inner hyphens; a bad name is reported by [Agent.Err]
// wrapped in [ErrInvalidName].
//
// Callbacks run directly once, synchronously in order on one fresh draft.
// Nil entries panic; expanded nil or empty slices supply no callbacks. Final
// validation follows all callbacks. Owned tool containers and rendered schema
// bytes are detached on input and publication, not arbitrary schema values.
//
// A result type other than exact string needs [AgentBuilder.Output]; otherwise
// [Agent.Err] fails with [ErrInvalidRequest].
//
//	var summarizer = llm.NewAgent[string]("summarizer", func(agent *llm.AgentBuilder[string]) {
//		agent.Instructions("Summarize in two sentences.")
//	})
func NewAgent[Out any](agentName string, configure ...AgentBuilderFunc[Out]) Agent[Out] {
	agent := &AgentBuilder[Out]{
		definition: Agent[Out]{name: agentName},
	}

	if err := name.Identifier(agentName); err != nil {
		agent.definition.err = fmt.Errorf("%w: agent %q: %w", ErrInvalidName, agentName, err)
	}

	for _, configure := range configure {
		configure(agent)
	}

	published := agent.definition.publish()

	return published
}

// Name returns the identity used in telemetry and events, without validation.
//
//	fmt.Println(llm.NewAgent[string]("support").Name())
func (agent Agent[Out]) Name() string {
	return agent.name
}

// Err returns the first invalid input given to the agent or its tools, or nil:
// a bad name, a tool with an error or a duplicate tool name, an output schema
// that cannot render, or a result type other than string without an
// [AgentBuilder.Output] schema. Named string types also require a schema.
// The zero Agent is invalid and reports ErrInvalidName before output checks.
//
//	if err := llm.NewAgent[string]("support").Err(); err != nil {
//		return err
//	}
func (agent Agent[Out]) Err() error {
	if agent.err != nil {
		return agent.err
	}

	if agent.name == "" {
		return fmt.Errorf("%w: agent was not built with NewAgent", ErrInvalidName)
	}

	if _, ok := any(*new(Out)).(string); agent.output == nil && !ok {
		return fmt.Errorf("%w: agent %q needs an Output schema for a result that is not a string", ErrInvalidRequest, agent.name)
	}

	return nil
}

// Instructions sets text as the system instructions and returns the same draft.
// [Instructions] adds to them for one run.
//
//	llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
//		agent.Instructions("Answer briefly.")
//	})
func (agent *AgentBuilder[Out]) Instructions(text string) *AgentBuilder[Out] {
	agent.definition.instructions = text

	return agent
}

// Tools replaces the allowed tools and returns the same draft. Empty removes
// all tools. It copies the container and rendered schemas on input. A
// tool with an error, or two tools with the same name, make [Agent.Err]
// report it.
// Earlier errors remain even when a later replacement has valid tools.
//
//	llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
//		agent.Tools()
//	})
func (agent *AgentBuilder[Out]) Tools(tools ...Tool) *AgentBuilder[Out] {
	agent.definition.tools = cloneAgentTools(tools)

	if agent.definition.err != nil {
		return agent
	}

	seen := make(map[string]bool, len(tools))

	for _, tool := range tools {
		if err := tool.Err(); err != nil {
			agent.definition.err = err

			return agent
		}

		if seen[tool.name] {
			agent.definition.err = fmt.Errorf("%w: agent %q has two tools named %q", ErrInvalidRequest, agent.definition.name, tool.name)

			return agent
		}

		seen[tool.name] = true
	}

	return agent
}

// Output configures JSON output matching schema and returns the same draft.
// During a run, the final reply is decoded into Out with json/v2 and
// checked with [validate.Schema.Parse]. JSON decoding failures and
// [validate.Errors] violations are sent back as correction feedback, up to
// [AgentBuilder.OutputRetries] times, before the run fails with [ErrInvalidOutput].
// Other schema errors, including operational Check errors and cancellation,
// end the run immediately with their cause preserved and no correction feedback.
// The schema renders immediately; its bytes are copied, not the schema itself.
// Nil schemas and rendering errors are sticky configuration errors.
//
//	llm.NewAgent[string]("summary", func(agent *llm.AgentBuilder[string]) {
//		agent.Output(validate.String(func(summary *validate.StringBuilder[string]) {
//			summary.Required()
//		}))
//	})
func (agent *AgentBuilder[Out]) Output(schema validate.Schema[Out]) *AgentBuilder[Out] {
	agent.definition.output = schema

	if agent.definition.err != nil {
		return agent
	}

	if schema == nil {
		agent.definition.err = fmt.Errorf("%w: agent %q output schema cannot be nil", ErrInvalidRequest, agent.definition.name)

		return agent
	}

	doc, err := schema.JSONSchema()

	if err != nil {
		agent.definition.err = fmt.Errorf("%w: agent %q output schema: %w", ErrInvalidRequest, agent.definition.name, err)

		return agent
	}

	agent.definition.outputSchema = slices.Clone(doc)

	return agent
}

// MaxSteps allows n model turns per run and returns the same draft. Zero or less
// uses [Config.MaxSteps]. [MaxSteps] overrides it for one run.
//
//	llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
//		agent.MaxSteps(5)
//	})
func (agent *AgentBuilder[Out]) MaxSteps(n int) *AgentBuilder[Out] {
	agent.definition.maxSteps = n

	return agent
}

// MaxTokens caps the output of every turn at n tokens and returns the same
// draft. Zero or less uses [Config.MaxTokens]. [MaxTokens] overrides it per run.
//
//	llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
//		agent.MaxTokens(2048)
//	})
func (agent *AgentBuilder[Out]) MaxTokens(n int) *AgentBuilder[Out] {
	agent.definition.maxTokens = n

	return agent
}

// ToolConcurrency returns the same draft and runs at most n of one turn's tool
// calls at a time. Zero or less, the default, runs them all at once; 1 runs
// them one after another in the order the model made them.
//
//	llm.NewAgent[string]("support", func(agent *llm.AgentBuilder[string]) {
//		agent.ToolConcurrency(1)
//	})
func (agent *AgentBuilder[Out]) ToolConcurrency(n int) *AgentBuilder[Out] {
	agent.definition.toolConcurrency = n

	return agent
}

// OutputRetries returns the same draft and sends an invalid final reply to the
// model up to n times. Zero uses the default, 1; a negative n never retries.
// Each retry is another turn and counts toward the step limit.
// Only JSON decoding failures and schema violations are correctable; operational
// schema errors end the run without consuming a retry or adding feedback.
//
//	llm.NewAgent[string]("summary", func(agent *llm.AgentBuilder[string]) {
//		agent.OutputRetries(-1)
//	})
func (agent *AgentBuilder[Out]) OutputRetries(n int) *AgentBuilder[Out] {
	agent.definition.outputRetries = n

	return agent
}

// parse returns the decoded reply or its failure. Only JSON decoding errors and
// direct validate.Errors are marked with outputFeedback for model correction;
// operational schema errors must remain outside the saved conversation and
// provider requests. An arbitrary Check error may wrap violations, so inspecting
// its error chain would incorrectly make an operational failure correctable.
// [Agent.Err] guarantees that Out is string without an output schema.
func (agent Agent[Out]) parse(ctx context.Context, text string) (Out, error) {
	if agent.output == nil {
		return any(text).(Out), nil
	}

	var out Out

	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return out, &outputFeedback{cause: err}
	}

	out, err := agent.output.Parse(ctx, out)

	if violations, ok := any(err).(validate.Errors); ok {
		return out, &outputFeedback{cause: violations}
	}

	return out, err
}

// AgentBuilderFunc edits one fresh typed agent declaration draft.
// Callbacks run once in order before final validation; nil entries panic.
// Publication detaches owned containers, not functions or schema implementations.
//
//	var policy llm.AgentBuilderFunc[string] = func(agent *llm.AgentBuilder[string]) {
//		agent.Instructions("Answer briefly.")
//	}
//	agent := llm.NewAgent[string]("support", policy)
//	if err := agent.Err(); err != nil {
//		return err
//	}
type AgentBuilderFunc[Out any] = func(*AgentBuilder[Out])
