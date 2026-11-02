---
title: Conversation History
description: Continue and persist caller-owned conversations without losing tool results, reasoning signatures, or buffer ownership.
order: 30
summary:
  - Continue a conversation using complete messages rather than only displayed text.
  - Persist tagged message parts and preserve tool pairs and reasoning signatures.
  - Keep mutable buffers stable during calls and inspect partial history before resuming.
---

# Conversation History

A model sees only the messages you send on that call. Arkoren does not maintain
a session or trim old turns for you. Keep the conversation in your application,
authorize access to it for the current caller, and decide what content may be
sent to the provider. Stored messages can contain private prompts, media, tool
results, and validation feedback.

These examples use `llm` from `arkoren.dev/core/service/llm`, standard
`encoding/json/v2`, a configured `model`, and an operation context `ctx`.
[Drivers](drivers.md) owns connection setup; [Running Agents](running-agents.md)
owns the tool loop.

## Continuing Conversations

To ask a follow-up to a single-turn reply, retain the entire assistant message
before adding another user message:

```go
history := []llm.Message{llm.UserText("Explain the Sunday search maintenance.")}
reply, err := model.Generate(ctx, llm.Request{Messages: history})
history = append(history, reply.Message, llm.UserText("Which time zone is that?"))
followup, err := model.Generate(ctx, llm.Request{Messages: history})
```

Use this path only when your application handles any requested tools itself.
`Generate` does not execute them. Check its [stop reason](overview.md#reading-outcomes)
before treating it as a finished answer.

For an agent run, the returned `Result.Messages` already includes the original
history, assistant messages, and tool results. With an agent named `assistant`:

```go
result, err := model.Run(ctx, assistant, history)
history = append(result.Messages, llm.UserText("Give me a shorter explanation."))
next, err := model.Run(ctx, assistant, history)
```

These fragments assume successful calls. Do not append only `result.Text`:
that discards tool calls, results, and reasoning needed for later turns. An
agent history must end with a user message. System instructions belong in the
request or agent definition, not a synthetic system-role message.

## Saving Messages

To save a successful run in your own storage, encode its complete conversation:

```go
data, err := json.Marshal(result.Messages)
```

The encoded parts have a `"type"` discriminator. Loading the same representation
restores concrete `Text`, `Image`, `Document`, `ToolCall`, `ToolResult`, and
`Reasoning` parts:

```go
var restored []llm.Message
err = json.Unmarshal(data, &restored)
```

Unknown part types fail decoding. Preserve opaque `Reasoning.Signature` values
unchanged; do not interpret or rewrite them. Driver modes differ in whether
they replay reasoning at all; consult the [compatibility matrix](drivers.md#compatibility-matrix).

When trimming history to fit a model's context limit, keep tool calls and their
matching results together. A result's `CallID` answers a call's `ID`. Removing
one side can leave a conversation the provider cannot accept. Arkoren does not
perform automatic compaction, retention, or storage encryption.

## Adding Media

If the selected model accepts images, include the question and bytes in one
user message. Here `pngBytes` contains the image your application has approved
for provider access:

```go
message := llm.User(
	llm.Text{Text: "Which maintenance window is shown on this notice?"},
	llm.Image{Data: pngBytes, MediaType: "image/png"},
)
```

`Image{URL: imageURL}` is an alternative when the server supports remote images.
URL retrieval policy belongs to that server. Documents use
`llm.Document{Data: documentBytes, MediaType: "application/pdf"}` and require a
driver mode and model that support them.

`llm.User(parts...)` copies the outer parts slice, not nested buffers.
`Image.Data`, `Document.Data`, and `ToolCall.Arguments` remain shared. Configure
them before a call and do not mutate messages or their buffers during a call
or while consuming a stream. A copied message slice is not a deep snapshot.
Serialize or deep-copy content yourself when another owner needs to mutate it.

## Handling Partial History

After model turns have occurred, a failed `Run` returns the conversation and
usage so far; its typed `Output` remains zero. Inspect the error before deciding
whether that history can be resumed.

At `llm.ErrMaxSteps`, final-turn tool calls receive error results saying they
were not run. That repaired history can be passed to a new run. Other failures
can leave a partial turn, including unanswered tool calls. Do not blindly
resubmit it or repeat tool effects. [Run limits](running-agents.md#limiting-runs)
explain the execution boundary.

`RunStream` does not return `Result.Messages`. Its `Done` events contain model
responses, not the tool-result messages appended inside the run. Use `Run` when
you need the complete conversation to persist or resume.

For message formats and ownership, see the [llm API reference](https://pkg.go.dev/arkoren.dev/core/service/llm).
