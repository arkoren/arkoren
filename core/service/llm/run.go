package llm

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/service/llm/internal/genai"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/foundation/collection"
)

// defaultMaxSteps is the step limit when neither the run, the agent nor the
// [Config] sets one.
const defaultMaxSteps = 10

// errStopped ends a run whose [Model.RunStream] consumer stopped reading. It
// never leaves the package.
var errStopped = errors.New("llm: run stopped by the consumer")

// RunOption varies one call of [Model.Run] or [Model.RunStream] without
// changing the [Agent].
// Options run synchronously in order and apply only to this invocation; nil
// option callbacks are misuse and panic rather than being ignored.
//
//	options := []llm.RunOption{llm.MaxSteps(3), llm.MaxTokens(1024)}
//	result, err := model.Run(ctx, agent, history, options...)
//	if err != nil {
//		return err
//	}
//	_ = result.Output
type RunOption func(*runConfig)

// runConfig holds the per-call overrides of a run.
type runConfig struct {
	// instructions are appended to the agent's instructions.
	instructions string

	// model replaces the facade's model ID.
	model string

	// maxSteps and maxTokens replace the agent's limits when positive.
	maxSteps, maxTokens int
}

// Instructions appends text to the agent's instructions for one run, after a
// blank line. Per-run text changes the prompt prefix, which defeats the
// vendor's prompt caching, so keep stable text in [AgentBuilder.Instructions].
//
//	result, err := model.Run(ctx, triage, history, llm.Instructions("The user is Ana."))
func Instructions(text string) RunOption {
	return func(run *runConfig) {
		run.instructions = text
	}
}

// UseModel sets the model ID of one run, replacing the one given to [New].
//
//	result, err := model.Run(ctx, triage, history, llm.UseModel("gpt-5.2-mini"))
func UseModel(id string) RunOption {
	return func(run *runConfig) {
		run.model = id
	}
}

// MaxSteps sets the step limit of one run, replacing [AgentBuilder.MaxSteps]. Zero
// or less keeps it.
// It does not change tool concurrency or the provider's per-turn token cap.
//
//	result, err := model.Run(ctx, agent, history, llm.MaxSteps(3))
//	if err != nil {
//		return err
//	}
//	_ = result.Steps
func MaxSteps(n int) RunOption {
	return func(run *runConfig) {
		run.maxSteps = n
	}
}

// MaxTokens sets the per-turn output cap of one run, replacing
// [AgentBuilder.MaxTokens]. Zero or less keeps it.
// This is an output cap per model turn, not a total run-token budget.
//
//	result, err := model.Run(ctx, agent, history, llm.MaxTokens(1024))
//	if err != nil {
//		return err
//	}
//	_ = result.Usage
func MaxTokens(n int) RunOption {
	return func(run *runConfig) {
		run.maxTokens = n
	}
}

// Result is the outcome of [Model.Run].
// On failure it may contain partial history and usage, but Output remains its
// zero value. Messages contain model/tool content and are not safe telemetry.
//
//	result, err := model.Run(ctx, agent, history)
//	if err != nil {
//		return err
//	}
//	fmt.Println(result.Output)
type Result[Out any] struct {
	// Output is the agent's final result. It is the zero value when Run
	// failed.
	Output Out

	// Text is the text of the last reply.
	Text string

	// StopReason is why the last turn ended.
	StopReason StopReason

	// Messages is the input history followed by every message of the run,
	// including tool calls and results, ready to save and to pass to a later
	// run. After a failed run it ends where the run stopped.
	Messages []Message

	// Steps is the number of model turns taken.
	Steps int

	// Usage sums the usage of all turns.
	Usage Usage
}

// runPlan is the settings of one run after the agent, the [RunOption] values
// and the [Config] have been combined.
type runPlan struct {
	// agent is the agent's name.
	agent string

	// model, instructions and maxTokens fill each [Request].
	model, instructions string
	// maxTokens is the completed per-turn output token limit.
	maxTokens int

	// maxSteps limits the model turns.
	maxSteps int

	// outputRetries is how many invalid final replies are sent back.
	outputRetries int

	// concurrency limits parallel tool calls; zero means no limit.
	concurrency int

	// specs and tools describe the agent's tools to the model and look them
	// up by name.
	specs []ToolSpec
	// tools resolves model-issued tool names to their executable definitions.
	tools map[string]Tool

	// outputSchema is the JSON Schema of the final reply, empty for text.
	outputSchema []byte
}

// prepareTools retains every known tool for execution, including tools omitted
// from discovery. It evaluates each policy once with the run context, preserving
// declaration order in the advertised specs and failing closed before model I/O.
// Execution independently rechecks capability policy after acquiring its slot.
func (plan *runPlan) prepareTools(ctx context.Context, tools []Tool) error {
	for _, tool := range tools {
		plan.tools[tool.name] = tool

		offered, err := tool.Available(ctx)

		if err != nil {
			return err
		}

		if !offered {
			continue
		}

		plan.specs = append(plan.specs, ToolSpec{
			Name:        tool.name,
			Description: tool.description,
			InputSchema: tool.schema,
		})
	}

	return nil
}

// firstPositive returns the first value greater than zero, or zero.
func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}

	return 0
}

// add returns the sum of two usages.
func (usage Usage) add(other Usage) Usage {
	return Usage{
		InputTokens:      usage.InputTokens + other.InputTokens,
		OutputTokens:     usage.OutputTokens + other.OutputTokens,
		CacheReadTokens:  usage.CacheReadTokens + other.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens + other.CacheWriteTokens,
		ReasoningTokens:  usage.ReasoningTokens + other.ReasoningTokens,
	}
}

// Run runs agent over history until the model ends its turn: it calls the
// model, runs the tools the model asks for and sends their results back,
// repeating up to the step limit. history is the conversation so far and is
// not modified; it must end with a user message. The returned
// [Result.Messages] is history plus everything the run added.
//
// Each tool call's arguments are decoded and validated against the tool's
// schema; invalid arguments, a call to an unknown tool, and a tool error all
// go back to the model as an error [ToolResult], and the model sees only the
// text of a [ToolError], otherwise "the tool failed". A [Permanent] tool error
// cancels the sibling calls of that turn and ends the run. Calls of one turn
// run concurrently, limited by [AgentBuilder.ToolConcurrency], and their results are
// returned in the order the model made the calls. Panics in tools are not
// recovered.
// The first permanent failure or parent cancellation supplies the turn's cause;
// a sibling's subsequent cancellation response does not replace it. Admitted
// calls are joined cooperatively, so a handler ignoring its context can delay
// cancellation or a stream consumer stop indefinitely.
//
// Tool availability filters advertised specs once per run, without caching
// across runs or principals. Known but hidden tools remain guarded if the model
// guesses their names. Execution rechecks policy after acquiring a concurrency
// slot, before decoding arguments: denial returns only "tool unavailable".
// Policy errors preserve ErrToolAvailability and their cause, abort the run
// without exposing text to the model, and cancel sibling calls during execution
// using ErrPermanent. Discovery policy errors occur before any model I/O.
//
// With an [AgentBuilder.Output] schema the final reply is decoded and validated;
// JSON decoding failures and validation violations go back to the model, up to
// [AgentBuilder.OutputRetries] times, then Run fails with [ErrInvalidOutput].
// Operational schema errors end the run immediately with their cause preserved;
// their text never enters Result.Messages or correction requests to the model.
//
// Run fails with the agent's [Agent.Err] before any I/O, with
// [ErrInvalidRequest] for a nil ctx, and with:
//
//   - [ErrTruncated] when a turn hit the token limit,
//   - [ErrRefused] when the model refused or a filter blocked a turn,
//   - [ErrMaxSteps] when the step limit ended the run before the model did;
//     tool calls of that last turn are answered with an error result and not
//     run,
//   - [ErrInvalidOutput], [ErrPermanent], the context's error, or a driver
//     error mapped onto the package sentinels.
//
// If the run fails after a model turn and any call to an unknown tool was
// made, the error also matches [ErrUnknownTool]. After the first turn, Run
// returns the [Result] so far together with the error.
//
// The run is one "invoke_agent {agent}" span with a "chat {model}" span per
// turn and an "execute_tool {tool}" span per tool call. With [ModelBuilder.Events] it
// publishes [AgentCompleted] or [AgentFailed] and [ToolInvoked] or
// [ToolFailed] per call; the turns publish no [ResponseGenerated] events.
//
//	result, err := model.Run(ctx, triage, []llm.Message{llm.UserText(ticket)}, llm.MaxSteps(3))
//	if err != nil {
//		return err
//	}
//	fmt.Println(result.Output.Priority)
func (model *Model) Run[Out any](ctx context.Context, agent Agent[Out], history []Message, opts ...RunOption) (Result[Out], error) {
	return model.execute(ctx, agent, history, opts,
		func(ctx context.Context, request Request) (Response, error) {
			return model.generate(ctx, request, nil)
		},
		func(Event) bool {
			return true
		})
}

// RunStream is [Model.Run] that yields events as the run progresses: for
// each turn the driver's [TextDelta], [ReasoningDelta], [ToolCallStarted],
// [ToolCallDelta] and [Done] events; then a [ToolStarted] for every tool call
// of the turn as they start, a [ToolFinished] for each once all have
// finished, in the order of the calls; and a [StepCompleted] to close the
// turn. The sequence ends after the last [StepCompleted]. A failure is
// yielded as the second value, after which the sequence ends; the [Result]
// is not available from a stream, so use [Model.Run] when you need it.
//
// Breaking out cancels the run, joins admitted calls, and publishes no agent
// outcome event; completed tool observations remain published. The agent span
// ends without error. Queued calls never announced as ToolStarted are not run.
//
// The collection is deferred: validation, options, I/O and telemetry run on each
// traversal, not on construction. Traversing again starts a new run and can
// incur additional provider charges and tool effects. Collection projections
// do not redefine turn or run completion; reaching a Take limit cancels early.
// Items preserves the event prefix on error, not a completed Result. Materialize
// only finite streams; JSON encoding collects events instead of streaming them
// over the network.
// Use Iter for standard iterator consumers, as with [Model.Stream].
//
//	for event, err := range model.RunStream(ctx, triage, history) {
//		if err != nil {
//			return err
//		}
//		if delta, ok := event.(llm.TextDelta); ok {
//			fmt.Print(delta.Text)
//		}
//	}
func (model *Model) RunStream[Out any](ctx context.Context, agent Agent[Out], history []Message, opts ...RunOption) collection.TryLazySlice[Event] {
	return func(yield func(Event, error) bool) {
		emit := func(event Event) bool {
			return yield(event, nil)
		}

		step := func(ctx context.Context, request Request) (Response, error) {
			var response Response

			for event, err := range model.stream(ctx, request, nil) {
				if err != nil {
					return Response{}, err
				}

				if done, ok := event.(Done); ok {
					response = done.Response
				}

				if !emit(event) {
					return Response{}, errStopped
				}
			}

			return response, nil
		}

		if _, err := model.execute(ctx, agent, history, opts, step, emit); err != nil && !errors.Is(err, errStopped) {
			yield(nil, err)
		}
	}
}

// execute resolves the settings of a run, wraps the turns in the agent span
// and publishes the agent event. step performs one model turn and emit hands
// an event to the stream consumer, returning false when it stopped reading.
func (model *Model) execute[Out any](ctx context.Context, agent Agent[Out], history []Message, opts []RunOption, step func(context.Context, Request) (Response, error), emit func(Event) bool) (Result[Out], error) {
	if ctx == nil {
		return Result[Out]{}, fmt.Errorf("%w: context cannot be nil", ErrInvalidRequest)
	}

	if err := agent.Err(); err != nil {
		return Result[Out]{}, err
	}

	var run runConfig

	for _, opt := range opts {
		opt(&run)
	}

	plan := runPlan{
		agent: agent.name,
		model: cmp.Or(run.model, model.model),
		instructions: strings.Join(slices.DeleteFunc([]string{agent.instructions, run.instructions}, func(text string) bool {
			return text == ""
		}), "\n\n"),
		maxTokens:     firstPositive(run.maxTokens, agent.maxTokens),
		maxSteps:      firstPositive(run.maxSteps, agent.maxSteps, model.config.MaxSteps, defaultMaxSteps),
		concurrency:   agent.toolConcurrency,
		outputRetries: cmp.Or(agent.outputRetries, defaultOutputRetries),
		tools:         make(map[string]Tool, len(agent.tools)),
		outputSchema:  agent.outputSchema,
	}

	if err := plan.prepareTools(ctx, agent.tools); err != nil {
		return Result[Out]{}, err
	}

	ctx, op := genai.Begin(ctx, model.config.Telemetry, model.instruments, telemetry.Internal,
		semconv.GenAIOperationInvokeAgent, agent.name,
		append(requestAttrs(model.config, plan.model), telemetry.String(semconv.GenAIAgentName, agent.name))...)
	completed := false
	defer endInterrupted(ctx, op, &completed)
	start := time.Now()

	result, err := turns(ctx, model, agent, &plan, history, step, emit)

	if errors.Is(err, errStopped) {
		completed = true
		op.End(ctx, nil)

		return result, err
	}

	recordUsage(op, result.Usage)
	op.SetAttrs(telemetry.Int(semconv.LLMAgentSteps, result.Steps))

	completed = true
	op.End(ctx, err, errorSentinels...)

	duration := time.Since(start)

	if err != nil {
		model.config.Events.Emit(ctx, AgentFailed, AgentFailedEvent{
			Agent:     agent.name,
			Model:     plan.model,
			Steps:     result.Steps,
			Usage:     result.Usage,
			ErrorType: eventErrorType(err),
			Duration:  duration,
		})

		return result, err
	}

	model.config.Events.Emit(ctx, AgentCompleted, AgentCompletedEvent{
		Agent:    agent.name,
		Model:    plan.model,
		Steps:    result.Steps,
		Usage:    result.Usage,
		Duration: duration,
	})

	return result, nil
}
