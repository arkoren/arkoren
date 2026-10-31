---
title: Language Models Overview
nav: Overview
description: Create a model, run an agent, generate a single turn, keep a conversation, handle errors, and see what is reported.
order: 10
---

# Language Models Overview

## Introduction

When your service needs to summarize a ticket or answer a question, you send a
conversation to a language model and receive its next message. The model may
ask your code to run a tool before it can answer. The server can be hosted by a
vendor or run locally. Arkoren models these calls as the `llm` contract. A `Driver` performs one
model turn against a vendor, and the `*llm.Model` facade wraps it with the
model ID, defaults, validation, telemetry, and events. Your calling code uses the
same request types for hosted and local servers. Switching servers changes the
driver configuration and model ID; check the [driver compatibility matrix](drivers.md#compatibility-matrix)
before relying on the same tools, schemas, or media inputs.

The facade retains no conversation state: you keep the slice of messages, so
you can store, trim, and test it. `Generate` performs one turn without facade
retries. An agent manages repeated turns and tool calls for you; start with the
text agent below, then add [tools](agents-and-tools.md) when it needs application
data or [structured output](structured-output.md) when your code needs typed
fields rather than prose.

To display progress instead of waiting for the complete answer, use
[streaming](streaming.md). For similarity search rather than generated replies,
use the separate [embedding facade](embeddings.md).

## Creating a model

Start in a Go module with Go 1.27 or later and install both modules:

```sh
go get arkoren.dev/core arkoren.dev/driver/openai
```

For the hosted path below, you need an OpenAI API key, network access to OpenAI,
and access to the chosen model (`gpt-5.2` here). Calls may incur provider charges.
Set `OPENAI_API_KEY` in your environment before running your code. For a local
server without an OpenAI key, follow [Local Development with Ollama](local-development-with-ollama.md)
instead. Keep keys out of source control.

The OpenAI driver speaks to OpenAI and compatible APIs. These imports are used
by the examples on this page:

```go
import (
	"context"
	"errors"
	"fmt"

	"arkoren.dev/core/service/llm"
	"arkoren.dev/driver/openai/openaillm"
)
```

Create a driver and wrap it with the model ID:

```go
driver, err := openaillm.New() // reads OPENAI_API_KEY

model := llm.New(driver, "gpt-5.2")
```

`New` does no I/O, so a wrong key or an unreachable server shows up on the
first call. To check at startup, register the driver's `Ping` as a
[health check](../application/health-and-readiness.md). Build the model once and
share it; it is safe for concurrent use when its driver and shared callbacks are.
The model ID is required, and `Request.Model` overrides it for one call. [Language Model
Drivers](drivers.md) lists the options of the driver.

Focused examples keep error results visible but omit routine error handling.
Check errors before using results in your application.

## Running an agent

The simplest way to get an answer is an agent that returns text. An agent is a
package-level definition: a name, instructions, and optionally tools and an
output type. Declare this definition outside your calling function:

```go
var assistant = llm.NewAgent("assistant", func(agent *llm.AgentBuilder[string]) {
	agent.Instructions("Answer in one short sentence.")
})
```

With `model` from the setup above, call the agent from your function. Here
`context.Background()` is suitable for a command; in an HTTP handler, use the
incoming request's context so disconnects cancel the call:

```go
ctx := context.Background()
result, err := model.Run(ctx, assistant, []llm.Message{
	llm.UserText("Why is the sky blue?"),
})
fmt.Println(result.Text)
```

`result.Messages` is the input plus every message the run added. Store it and
pass it back with a new user message to continue the conversation:

```go
history := append(result.Messages, llm.UserText("And at sunset?"))
followup, err := model.Run(ctx, assistant, history)
```

On success, `result.Text` contains the first answer and `followup.Text` contains
the answer with the earlier conversation included. The wording varies by model.

A `Message` encodes to JSON with a `"type"` tag on every part, so history
round-trips through `json.Marshal` and `json.Unmarshal` from `encoding/json/v2`
and can live in your database. Images and PDFs are parts too:
`llm.User(llm.Text{Text: "What is this?"}, llm.Image{URL: url})`.

`llm.User(parts...)` copies the outer parts slice, so replacing an element in
your original slice does not change the constructed message. It does not copy
nested buffers such as `Image.Data`, `Document.Data`, or `ToolCall.Arguments`.
Finish configuring those buffers before submitting a request, and do not mutate
messages or their buffers during a call or while consuming a stream.

## Generating one turn

If your application already controls the conversation or handles tool calls
itself, you may not need an agent to manage the loop. `Generate` gives you one
model response to one request, without running further turns:

```go
response, err := model.Generate(ctx, llm.Request{
	Instructions: "Answer in one short sentence.",
	Messages:     []llm.Message{llm.UserText("Why is the sky blue?")},
})
for _, part := range response.Message.Parts {
	if text, ok := part.(llm.Text); ok {
		fmt.Println(text.Text)
	}
}
```

`response.StopReason` says why the turn ended: `StopEndTurn`, `StopToolUse`,
`StopMaxTokens`, `StopRefusal`, or `StopContentFilter`. `Generate` returns a
truncated or refused reply as a successful call, because the reply is still
data. `Run` is stricter: a final `StopMaxTokens` becomes `llm.ErrTruncated`, and
a refusal or filtered reply becomes `llm.ErrRefused`.

### Limiting output

If calls need a shared output-token budget, set it when constructing the model:

```go
model := llm.New(driver, "gpt-5.2", func(generation *llm.ModelBuilder) {
	generation.MaxTokens(2048)
})
```

This supplies the cap when a request or run sets none. Without it, the provider's
default applies. Reaching the cap can truncate the reply; inspect the stop reason
or agent error described above rather than assuming the answer is complete.

## Errors

Drivers map vendor failures onto sentinel errors that you match with
`errors.Is`, and the vendor's own error stays in the chain:

```go
switch {
case errors.Is(err, llm.ErrRateLimited), errors.Is(err, llm.ErrUnavailable):
	// try again later
case errors.Is(err, llm.ErrContextLength):
	// shorten the history
case errors.Is(err, llm.ErrRefused):
	// the model declined to answer
}
```

The facade does not retry failed provider requests, and the OpenAI driver's SDK
retries are off by default. A rate limit or a 5xx is the moment to decide whether
to wait, switch to another model, or fail the request, and that depends on your
application. Send the call from a [queue](../queues/overview.md) job when a
retry should survive a restart. The full list of sentinels is in the
[`llm` API reference](https://pkg.go.dev/arkoren.dev/core/service/llm).

## Usage, events, and telemetry

Every response carries `Usage`: input, output, cache, and reasoning tokens. A
count the vendor does not report stays 0. `Run` sums usage across its steps in
`Result.Usage`.

With `llm.ModelBuilder.Events(bus)` standalone turns publish response events and
agent runs publish agent and tool events instead. An embedder has its own event
setting. A consumer-stopped stream publishes no operation outcome, even if the
consumer stops at `Done`; [streaming outcomes](streaming.md#stopping-a-stream)
explains the distinction from a provider failure.
[Built-in Events](../events/built-in-events.md#language-models) lists the topics.
With telemetry set, calls produce spans and metrics that follow the OpenTelemetry
generative AI conventions: a `chat {model}` span per turn, `invoke_agent` and
`execute_tool` spans around agents. Prompts, replies, tool arguments, and tool
results are never recorded by telemetry. Event payloads omit those contents and
raw errors. `ResponseFailedEvent`, `EmbeddingsFailedEvent`, and `AgentFailedEvent`
expose `ErrorType` instead of the former `Err` field. Update listeners to compare
that string rather than call `errors.Is` on a payload error. Its value is a
package sentinel name such as `llm.ErrRateLimited`, a context classification
(`context.Canceled` or `context.DeadlineExceeded`), or `unknown` for an
unrecognized failure. Context errors take precedence; execution policy failures
otherwise use `llm.ErrToolAvailability` rather than their `ErrPermanent` wrapper.

If a panic unwinds an operation whose observation has begun, Arkoren finalizes
that observation with the bounded telemetry class `llm.interrupted`. It does not
record the panic's value, type, or text, convert the panic to an error, or invent
a success or failure event for the interrupted operation. The panic still
propagates; observations that completed before it are not retracted. This is not
panic recovery, especially for [tool goroutines](agents-and-tools.md#errors-a-tool-returns).

Errors returned from the operation still preserve their original causes and can
contain vendor, tool, policy, or schema diagnostics. Review them before forwarding
them to logs or clients; bounded event classifications do not sanitize returned
errors or your saved conversation. Auditing what users asked belongs in your own
code. See [Events](../events/overview.md)
and [Telemetry](../observability/telemetry.md) for how both work.

## Testing

Use a scripted fake for repeatable application tests. Keep live-provider checks
separate and opt-in; [Testing Language Models](testing.md) shows both boundaries.

For every function and option, see [the `llm` API reference](https://pkg.go.dev/arkoren.dev/core/service/llm).
