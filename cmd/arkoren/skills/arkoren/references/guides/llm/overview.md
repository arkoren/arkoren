---
title: Language Models Overview
nav: Overview
description: Generate a model reply and distinguish response data, agent execution, and application-owned conversation state.
order: 10
summary:
  - Generate one reply through a configured model without running tools automatically.
  - Interpret stop reasons, token caps, and provider failures before accepting an answer.
  - Observe usage and outcomes without recording prompt or tool contents.
---

# Language Models Overview

A language model receives a conversation and produces its next message. Use
`*llm.Model` to send those requests through a driver with shared defaults,
validation, events, and telemetry. Configure a hosted or local connection in
[Drivers](drivers.md); the examples here assume a configured `driver`, a model
ID in `modelID`, and an operation context `ctx`.

The model does not store a conversation or execute requested tools by itself.
Your application owns [conversation history](conversation-history.md). An
[agent](running-agents.md) adds the repeated turns needed to execute tools and
validate a final answer. [Embeddings](embeddings.md) use a separate facade for
vectors rather than generated replies.

Focused examples show error results but omit routine propagation. Handle errors
before using results in your application. Import `llm` from
`arkoren.dev/core/service/llm`; the snippets also use standard `fmt` and `errors`.

## Generating Replies

To summarize a maintenance notice, create a reusable model and send one user
message. Use the incoming request context in an HTTP handler, or a bounded
command context, so cancellation reaches the provider.

```go
model := llm.New(driver, modelID)
reply, err := model.Generate(ctx, llm.Request{
	Instructions: "Summarize the notice in one sentence.",
	Messages: []llm.Message{
		llm.UserText("Search will be unavailable Sunday from 02:00 to 02:15 UTC."),
	},
})
```

`Generate` performs one turn without facade retries. The reply's message can
contain text, reasoning, or tool requests; it is not necessarily a final answer.
To display its text parts:

```go
for _, part := range reply.Message.Parts {
	if text, ok := part.(llm.Text); ok {
		fmt.Println(text.Text)
	}
}
```

The wording depends on the model. A requested tool is data in this response,
not an executed application action. Use [Running Agents](running-agents.md) when
Arkoren should manage that loop, or [Streaming Replies](streaming.md) to display
fragments as they arrive.

## Reading Outcomes

Check `reply.StopReason` before treating the reply as complete:

| Reason | Meaning |
| --- | --- |
| `llm.StopEndTurn` | The model ended its turn. |
| `llm.StopToolUse` | The model requested tool execution. |
| `llm.StopMaxTokens` | The output limit stopped the reply. |
| `llm.StopRefusal` | The model refused. |
| `llm.StopContentFilter` | A content filter stopped the reply. |

`Generate` returns truncated and refused replies as successful response data.
Agent runs instead return `llm.ErrTruncated` or `llm.ErrRefused` for those
outcomes, before tool execution or final-output decoding.

## Setting Defaults

If all calls need an output cap, publish it with the model:

```go
model := llm.New(driver, modelID, func(model *llm.ModelBuilder) {
	model.MaxTokens(1024)
})
```

A request's positive `MaxTokens` overrides that fallback; zero leaves it to the
facade default, then the provider. This cap applies to each turn, not the total
tokens of an agent run. Reaching it can leave incomplete output.
`Request.Model` similarly overrides the facade's model ID for one request.

Construction does no I/O. An empty model ID or nil driver is reported when you
call the facade. Share the model when its driver and shared callbacks support
concurrent use; the acquiring owner manages the driver's lifetime separately.
Construction callbacks publish copied settings, not a reconfigurable facade.

## Handling Failures

Drivers preserve provider errors while adding portable sentinels. Match them
with `errors.Is`, rather than parsing a diagnostic:

```go
switch {
case errors.Is(err, llm.ErrContextLength):
	// Trim the conversation before another attempt.
case errors.Is(err, llm.ErrRateLimited), errors.Is(err, llm.ErrUnavailable):
	// Apply your application's retry or fallback policy.
case errors.Is(err, llm.ErrRefused):
	// Return an application-approved refusal response.
}
```

The facade does not retry provider failures. The OpenAI driver's SDK retries
also default to off. Decide whether another attempt is safe, especially after
an agent has performed tool effects; cancellation cannot undo those effects.

## Observing Calls

`Response.Usage` reports input, output, cache, and reasoning tokens. Counts the
provider omits stay zero. `Result.Usage` sums the turns of an agent run.

Set `ModelBuilder.Events(bus)` to publish standalone response outcomes and agent
or tool outcomes. Agent turns do not also publish standalone response events.
An embedder has its own event setting. See [built-in LLM events](../events/built-in-events.md#language-models)
for payloads and topics, and [stream outcomes](streaming.md#stopping-streams)
for consumer stops.

Telemetry creates `chat {model}` spans, with `invoke_agent` and `execute_tool`
spans for agent execution. Prompts, replies, tool arguments, and tool results
are absent from telemetry and event payloads. Failure events carry bounded
`ErrorType` values rather than raw errors: a sentinel name, `context.Canceled`,
`context.DeadlineExceeded`, or `unknown`. Context classifications take precedence;
execution-policy failures otherwise use `llm.ErrToolAvailability`, not their
`ErrPermanent` wrapper.

A panic still propagates. An observation it unwinds ends with telemetry class
`llm.interrupted`, without panic data or an invented outcome event. Previously
completed observations remain published; this is not panic recovery.

Returned errors and saved history can still contain provider, tool, policy, or
schema diagnostics. Review them before logging or returning them to a client.
Bounded event classifications do not sanitize application-owned data.

For all functions and options, see the [llm API reference](https://pkg.go.dev/arkoren.dev/core/service/llm).
