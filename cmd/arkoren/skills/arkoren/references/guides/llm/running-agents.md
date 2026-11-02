---
title: Running Agents
description: Run reusable agent definitions with bounded turns, concurrent tools, and explicit failure outcomes.
order: 40
summary:
  - Define a reusable text agent and run it over caller-owned messages.
  - Bound model turns and tool concurrency without confusing them with a total token budget.
  - Handle truncation, refusal, cancellation, and partial results before accepting output.
---

# Running Agents

When an answer needs application data, the model can request a tool, read its
result, and then answer. An **agent** defines that repeated-turn behavior:
instructions, available tools, and the final output type. `Model.Run` performs
the loop; the agent itself holds no conversation state.

This is an execution loop in your service, not an installed coding-agent skill
or an [MCP server](../mcp/first-server.md). The examples use `llm` from
`arkoren.dev/core/service/llm`, standard `errors` and `fmt`, a configured `model`,
and a caller-specific `ctx`. [Drivers](drivers.md) owns provider setup.

## Defining Agents

For a maintenance assistant that returns prose, declare a reusable text agent:

```go
var assistant = llm.NewAgent("maintenance-assistant", func(agent *llm.AgentBuilder[string]) {
	agent.Instructions("Explain maintenance notices briefly. Do not invent schedules.")
})
```

Definitions are immutable values, normally package-level variables. A builder
is a construction draft, not a runtime reconfiguration handle. Check
`assistant.Err()` during application construction: invalid names, duplicate or
invalid tools, and output-schema errors are reported before provider I/O.
The zero agent is invalid. Names use 1–64 bytes of lowercase letters, digits,
and inner hyphens.

Only the exact `string` type works without an output schema. Structs and named
string types need [Structured Output](structured-output.md).
Publication detaches tool containers and rendered schema bytes, not schema
implementations, handlers, policies, or their captures. Finish configuring
shared dependencies before concurrent use.

## Running Turns

Send a user question with the request's authenticated context. For a plain-text
agent, the final typed output and text are strings:

```go
result, err := model.Run(ctx, assistant, []llm.Message{
	llm.UserText("Explain this notice: search is offline Sunday, 02:00–02:15 UTC."),
})
fmt.Println(result.Output)
```

On success, `result.Steps` counts model turns and `result.Usage` sums their token
usage. `result.Messages` includes the input and messages added by the run; keep
it for [conversation history](conversation-history.md). The input must end with
a user message and is not modified, but nested message buffers remain shared.

To vary one call without replacing the definition, supply run options:

```go
result, err := model.Run(ctx, assistant, history,
	llm.Instructions("Use plain language for a public status page."),
	llm.MaxTokens(512),
)
```

Here `history` is your caller-owned `[]llm.Message`. Added instructions follow
the stable agent instructions after a blank line. They change the prompt prefix
and can reduce provider prompt-cache reuse. Keep stable guidance in the agent
and per-user facts in messages when possible. `llm.UseModel(modelID)` selects a
different model for one run.

## Adding Tools

When the answer needs a live schedule, attach the `lookupWindow` tool from
[Defining Tools](defining-tools.md) by replacing the earlier definition:

```go
var assistant = llm.NewAgent("maintenance-assistant", func(agent *llm.AgentBuilder[string]) {
	agent.Instructions("Look up maintenance windows before reporting their times.")
	agent.Tools(lookupWindow)
	agent.ToolConcurrency(2)
})
```

The run advertises allowed tools, calls the model, executes guarded tool
requests, appends results, and repeats until it accepts a final answer or stops.
Availability and schema validation still apply to every attempted execution;
model instructions are not an authorization mechanism.

Calls from one reply run concurrently. `ToolConcurrency(1)` serializes them;
the default `0` is unlimited. Results return in model-request order, not finish
order. Make handlers and their dependencies concurrency-safe, and choose a
limit compatible with shared connections and external rate limits.

## Limiting Runs

Each model turn is one step, including structured-output correction attempts.
The first positive limit wins: run `llm.MaxSteps`, agent `MaxSteps`, model
`ModelBuilder.MaxSteps`, then 10. This bounds turns, not total duration, token
usage, or the number of tools requested in one turn.

```go
result, err := model.Run(ctx, assistant, history, llm.MaxSteps(3))
if errors.Is(err, llm.ErrMaxSteps) {
	history = result.Messages
	// Save the repaired history for an explicitly authorized continuation.
}
```

If the last allowed turn requests tools, none of those calls run. Arkoren adds
an error result for each and returns `llm.ErrMaxSteps`, leaving resumable
history. Other errors can leave a partial turn: inspect history rather than
automatically resuming or repeating side effects.

An output cap is per turn: the first positive run or agent cap overrides the
model fallback. A truncated turn returns `llm.ErrTruncated`; refusal or content
filtering returns `llm.ErrRefused`. Those checks happen before tools or output
decoding. A failed run leaves typed `Output` zero, even when `Text` or history
contains content. Unknown tool requests become error results; if the run later
fails after such a request, its error also matches `llm.ErrUnknownTool`.

## Canceling Work

Use a deadline and pass `ctx` to every blocking dependency. A permanent tool
failure cancels sibling calls and ends the run; the initiating failure or parent
cancellation remains the cause, rather than a later sibling cancellation.
Arkoren waits for admitted calls to finish. Cancellation cannot undo effects
or forcibly terminate a handler that ignores its context.

> [!WARNING]
> Tool panics are not recovered. Tools execute in their own goroutines, so HTTP
> recovery middleware around `Run` cannot catch them. An unrecovered panic
> terminates the process. Return errors for expected failures and recover inside
> a tool when a dependency can panic.

For progress without a returned conversation or typed result, use
[RunStream](streaming.md#streaming-agents). Stopping its consumer cancels and
joins admitted work; it is not a rollback.

## Observing Runs

With model events configured, returning observed runs publish
`llm.agent.completed` or `llm.agent.failed`; observed tool outcomes publish
`llm.tool.invoked` or `llm.tool.failed`. Invalid declarations and discovery-policy
failures happen before the agent observation begins. Known admitted tool calls
receive observations; unknown names do not create tool spans or events.

Payloads hold names, counts, durations, and bounded failure classifications,
not arguments or results. Consumer stops and panics do not invent an agent
outcome. Already completed tool observations remain published. Apply the
[observation and privacy rules](overview.md#observing-calls) to returned errors
and saved conversations as well.

For all agent and run options, see the [llm API reference](https://pkg.go.dev/arkoren.dev/core/service/llm).
