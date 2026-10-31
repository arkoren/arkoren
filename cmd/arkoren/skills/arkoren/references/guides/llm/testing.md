---
title: Testing Language Models
nav: Testing
description: Test code that calls a model with a scripted llmtest.Fake, assert on requests, inject failures, and run the conformance suite.
order: 60
---

# Testing Language Models

## Introduction

A real model can be slow, nondeterministic, or billable, so routine application
tests should not call one. The `llmtest` package provides a
`Fake`, a scripted `llm.Driver`. You queue the replies the model should give,
pass the fake to the real `llm.New`, and exercise the facade's agent loop, tools,
and validation without provider I/O. The fake scripts responses and synthesizes
stream events; it does not reproduce a provider's timing, model quality, or wire
protocol. There is no memory driver for this contract; the fake takes its place.

Here `history` is the `[]llm.Message` your application sends.
Import `llm` from `arkoren.dev/core/service/llm`, `llmtest` from
`arkoren.dev/core/service/llm/llmtest`, and the standard packages `errors` and
`testing`. The driver-author example also imports the standard `os` package and
`openaillm` from `arkoren.dev/driver/openai/openaillm`.

## Scripting replies

Queue one reply per model turn, in order. These fragments run inside your test
with `t *testing.T` and `ctx := t.Context()`. The `support` agent is the one
defined in [Agents and Tools](agents-and-tools.md):

```go
fake := llmtest.New()
fake.Reply("Your order shipped yesterday.")

model := llm.New(fake, "test-model")
result, err := model.Run(ctx, support, []llm.Message{llm.UserText("Where is order 4012?")})
if err != nil {
	t.Fatal(err)
}
if result.Text != "Your order shipped yesterday." {
	t.Fatalf("text = %q", result.Text)
}
```

To test an agent that uses tools, script the tool call first and the answer
after it. `ReplyToolCall` takes the tool name and its JSON arguments, and the
agent runs your real tool function in between. Supply that tool with a test
repository too; faking the model does not fake the tool's external effects:

```go
fake := llmtest.New()
fake.ReplyToolCall("lookup-order", `{"number": 4012}`)
fake.Reply("It shipped yesterday.")
```

`ReplyParts` scripts a reply of several parts, `ReplyResponse` a full
`llm.Response` with a stop reason and usage, and `ReplyWith` computes the reply
from the request, or returns an error:

```go
fake.ReplyWith(func(request llm.Request) (llm.Response, error) {
	return llm.Response{}, llm.ErrRateLimited
})
```

Each `Generate` or `Stream` call consumes one reply. When the script runs out,
the call fails with `llmtest.ErrNoReply`, so a test notices a model call it did
not expect. A structured-output agent is scripted the same way: reply with the
JSON text, and the agent decodes and validates it.

## Asserting on requests

The fake records every request it receives. Assert on what your code sent, with
predicates that receive the `llm.Request`:

```go
fake.AssertGenerated(t, func(request llm.Request) bool {
	return len(request.Tools) == 1 && request.Tools[0].Name == "lookup-order"
})
fake.AssertNotGenerated(t, func(request llm.Request) bool {
	return request.Model == "expensive-model"
})
```

All predicates of one assertion must match the same request. `Generated()`
returns the recorded requests for anything more detailed, such as checking that
a user's name reached the instructions. `Embedded()` does the same for embedding
requests.

## Streaming and embeddings

`Stream` splits a scripted text reply into word-sized `TextDelta` events and
ends with `Done`, so streaming code is tested with the same script. Here
`request` is the `llm.Request` your application sends:

```go
fake.Reply("one two three")
var text string
for event, err := range model.Stream(ctx, request) {
	if err != nil {
		t.Fatal(err)
	}
	if delta, ok := event.(llm.TextDelta); ok {
		text += delta.Text
	}
}
if text != "one two three" {
	t.Fatalf("text = %q", text)
}
```

`Embed` needs no script. It returns deterministic hash-based unit vectors, so
repeating a text repeats its vector. These vectors let you test embedding calls,
storage, and retrieval wiring, but they do not represent meaning and cannot
verify search relevance. The vectors have the dimensions you request, or 8 by
default. `EmbedWith(fn)` replaces them with your own, for example when a test
needs a known similarity ranking.

## Testing failures

Your code should handle a rate limit or an outage. `FailNext(err)` fails the
next call only, `FailWith(err)` fails every call until you call it with `nil`,
and `Reset` clears the script, the records, and the failures:

```go
fake.FailNext(llm.ErrUnavailable)

_, err := model.Run(ctx, support, history)
if !errors.Is(err, llm.ErrUnavailable) {
	t.Fatalf("err = %v", err)
}
```

A call that fails this way is not recorded and uses no scripted reply.

## Testing a driver

Driver authors check the contract with `llmtest.Run`, which exercises the driver
returned by the factory. With a vendor driver, this makes real provider requests;
the suite does not skip itself under `testing.Short()`. Keep live checks opt-in
and add the short-mode guard in your test. For hosted OpenAI, configure
`OPENAI_API_KEY` and access to both selected models before setting
`LLM_LIVE_TEST=1`; those calls can incur provider charges:

```go
func TestDriver(t *testing.T) {
	if testing.Short() || os.Getenv("LLM_LIVE_TEST") != "1" {
		t.Skip("set LLM_LIVE_TEST=1 outside short mode to call a real provider")
	}
	llmtest.Run(t, func(t *testing.T) llm.Driver {
		driver, err := openaillm.New()
		if err != nil {
			t.Fatal(err)
		}
		return driver
	}, llmtest.WithModel("gpt-5.2"), llmtest.WithEmbeddingModel("text-embedding-3-small"))
}
```

Options such as `WithoutEmbeddings`, `WithoutImages`, `WithoutDocuments`,
`WithoutReasoning`, and `WithoutStructuredOutput` skip checks for what a vendor
cannot do. A real model can occasionally fail a check, so rerun before blaming
the driver. [Writing a Driver](../drivers/writing-a-driver.md) explains the
process.

For every fake method and option, see [the `llmtest` API
reference](https://pkg.go.dev/arkoren.dev/core/service/llm/llmtest).
