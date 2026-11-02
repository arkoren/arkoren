---
title: Testing Models
nav: Testing
description: Script model turns, inspect requests, and test application outcomes without provider calls.
order: 100
summary:
  - Script replies and tool requests through the real facade without model I/O.
  - Assert on requests, terminal failures, streams, and deterministic embedding vectors.
  - Keep live-provider conformance separate from repeatable application tests.
---

# Testing Models

Routine application tests should not depend on a billable, nondeterministic
model. `llmtest.Fake` implements `llm.Driver` with scripted replies, so your
tests can exercise the real facade, agent loop, and validation without provider
I/O. There is no memory driver for this contract; use the fake for this task.

Import `llm` from `arkoren.dev/core/service/llm` and `llmtest` from
`arkoren.dev/core/service/llm/llmtest`. These fragments run inside a test with
`t *testing.T`, `ctx := t.Context()`, and standard `errors` where shown.

## Scripting Replies

Queue the answer before calling your model. With the text `assistant` from
[Running Agents](running-agents.md#defining-agents):

```go
fake := llmtest.New()
fake.Reply("Search maintenance lasts fifteen minutes.")
model := llm.New(fake, "test-model")
```

Run the real facade and assert the exact accepted output:

```go
result, err := model.Run(ctx, assistant, []llm.Message{
	llm.UserText("How long does search maintenance last?"),
})
if err != nil {
	t.Fatal(err)
}
if result.Output != "Search maintenance lasts fifteen minutes." {
	t.Fatalf("output = %q", result.Output)
}
```

Each `Generate` or `Stream` call consumes one reply. Exhausting the script
returns `llmtest.ErrNoReply`, exposing an unexpected turn. For a structured-output
agent, script JSON text and let the real output schema decode and validate it.
`ReplyParts` scripts multiple parts; `ReplyResponse` sets explicit stop reasons
and usage; `ReplyWith` computes a response or error from the request.

## Testing Tools

For a separate tool-dispatch test, create a fresh fake and model as above, then
script a request followed by the final answer:

```go
fake.ReplyToolCall("lookup-window", `{"service_id":"search"}`)
fake.Reply("Search maintenance starts at 02:00 UTC.")
```

Run an agent containing the `lookupWindow` from [Defining Tools](defining-tools.md).
The script consumes two model turns with your real handler between them.
Supply that handler with a test service: faking the model does not fake tool
effects, resource authorization, or network dependencies.

Test denial with a caller context your policy rejects, and assert that the
protected handler was not invoked. Test the application's resource check
directly too; hiding a capability is not proof of row-level access control.

## Inspecting Requests

After executing the tool scenario with a fresh fake, assert on the advertised
capability rather than only the final text:

```go
fake.AssertGenerated(t, func(request llm.Request) bool {
	return len(request.Tools) == 1 && request.Tools[0].Name == "lookup-window"
})
fake.AssertNotGenerated(t, func(request llm.Request) bool {
	return request.Model != "test-model"
})
```

All predicates passed to one assertion must match the same request.
`Generated()` returns recorded generation/stream requests for detailed checks;
`Embedded()` returns embedding requests. The fake proves what your code sent,
not provider wire compatibility or model quality.

## Testing Failures

Inject an outage and check the portable error your application handles:

```go
fake.FailNext(llm.ErrUnavailable)
_, err := model.Run(ctx, assistant, []llm.Message{llm.UserText("Show the schedule.")})
if !errors.Is(err, llm.ErrUnavailable) {
	t.Fatalf("err = %v", err)
}
```

An injected failure is not recorded and consumes no scripted reply.
`FailWith(err)` keeps failing calls until cleared with nil; `Reset()` clears
scripts, records, and injected failures. Use `ReplyWith` when you need a
recorded scripted turn to fail instead.

To verify truncation handling, script a response with
`StopReason: llm.StopMaxTokens`: `Generate` returns it as data, whereas `Run`
returns `llm.ErrTruncated`. A refusal or content-filter stop similarly becomes
`llm.ErrRefused` in an agent run. Do not assert success only because partial
text exists.

## Streaming Events

The fake synthesizes word-sized text deltas and ends with `Done`. With a fresh
fake/model and a user-message `request` of type `llm.Request`:

```go
fake.Reply("Maintenance is complete.")
var text string
for event, err := range model.Stream(ctx, request) {
	if err != nil {
		t.Fatal(err)
	}
	if delta, ok := event.(llm.TextDelta); ok {
		text += delta.Text
	}
}
if text != "Maintenance is complete." {
	t.Fatalf("text = %q", text)
}
```

This checks consumption and assembly, not provider timing or HTTP framing.
Consume through natural completion when testing an outcome; breaking at `Done`
has different [observation semantics](streaming.md#stopping-streams).

## Testing Vectors

Embeddings need no script. The fake returns hash-based unit vectors with the
requested dimensions, or 8 by default:

```go
embedder := llm.NewEmbedder(fake, "test-embedding")
vectors, err := embedder.Embed(ctx, "Search maintenance", "Search maintenance")
if err != nil {
	t.Fatal(err)
}
if !slices.Equal(vectors[0], vectors[1]) {
	t.Fatal("identical texts produced different test vectors")
}
```

This fragment also imports standard `slices`. Determinism tests storage and
retrieval wiring, not semantic relevance. Use `EmbedWith` to supply known
vectors when testing your own ranking logic.

## Driver Conformance

Driver authors can call `llmtest.Run` with a factory returning their driver.
The suite exercises the actual driver, not a fake transport. With a hosted
driver it makes real, potentially billable requests and does not automatically
skip under `testing.Short()`.

Keep that test opt-in: check both short mode and an explicit environment flag
before constructing a live driver. Configure credentials and model IDs your
account can access; pass them with `llmtest.WithModel(modelID)` and
`llmtest.WithEmbeddingModel(embeddingModelID)`. Do not run it as an ordinary
unit test or treat nondeterministic model output as a deterministic fixture.

Use `WithoutEmbeddings`, `WithoutImages`, `WithoutDocuments`,
`WithoutReasoning`, or `WithoutStructuredOutput` only for capabilities the
selected driver/server cannot provide. For OpenAI Chat Completions, documents
and reasoning are unsupported. [Writing a Driver](../drivers/writing-a-driver.md)
explains the broader contract checks. A failing live-model assertion can be
nondeterministic; investigate before assigning the cause to the driver.

For fake methods and conformance options, see the [llmtest API reference](https://pkg.go.dev/arkoren.dev/core/service/llm/llmtest).
