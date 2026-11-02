---
title: Local Development
description: Run downloaded Ollama models through the OpenAI driver and verify local generation separately from application tests.
order: 90
summary:
  - Connect to a loopback Ollama server without inheriting hosted credentials.
  - Select a supported API mode and use a separate local embedding model.
  - Check model-specific behavior and clean up the server and downloaded models deliberately.
---

# Local Development

Use a downloaded Ollama model when you want to check model requests on your own
machine without a hosted API key. Ollama exposes an OpenAI-compatible API, so
you reuse the [OpenAI driver](drivers.md). A local answer checks connectivity
and prompt behavior for that model, not parity with your hosted deployment.

This workflow uses local inference, not Ollama's cloud service. Cloud models
still need authentication and network access. After installation and model
download, local inference does not require an internet connection.

## Starting Ollama

Install [Ollama](https://ollama.com/download) and start its local server. If
your installation does not start it automatically, run `ollama serve` in
another terminal. Keep the unauthenticated plaintext endpoint on loopback;
do not expose it to an untrusted network.

Download the model before sending a request:

```sh
ollama pull llama3.2
```

The model remains on disk after the server stops. Its identifier is the name
you pulled, not a hosted OpenAI model name.

## Connecting Locally

Install the Go modules in your application's module directory:

```sh
go get arkoren.dev/core arkoren.dev/driver/openai
```

Import `llm` from `arkoren.dev/core/service/llm` and `openaillm` from
`arkoren.dev/driver/openai/openaillm`. Set both the loopback URL and a nonsecret
key value so the SDK cannot inherit a hosted `OPENAI_API_KEY`:

```go
driver, err := openaillm.New(func(driver *openaillm.Builder) {
	driver.APIKey("ollama")
	driver.BaseURL("http://localhost:11434/v1")
})
```

Handle the construction error before creating the facade. Construction performs
no network I/O. With a bounded operation context `ctx`, send a small request:

```go
model := llm.New(driver, "llama3.2")
reply, err := model.Generate(ctx, llm.Request{
	Messages: []llm.Message{llm.UserText("Explain planned maintenance in one sentence.")},
})
```

On success, inspect `reply.Message` and its [stop reason](overview.md#reading-outcomes).
The request goes to your local server, not OpenAI. A successful reply verifies
basic generation, not tools, structured output, or every media input.
`driver.Ping(ctx)` checks `GET /models`; register it as a
[health check](../application/health-and-readiness.md) if you want readiness to
depend on that endpoint.

You can set `OPENAI_BASE_URL` instead of the callback URL, but still prevent
hosted credentials from leaking to the local endpoint. The driver needs no
`Start` or `Stop` calls.

## Selecting Modes

The default is Responses, supported by
[Ollama from 0.13.3](https://docs.ollama.com/api/openai-compatibility#responses-api).
Ollama supports non-stateful Responses: your application still sends the
[conversation history](conversation-history.md). For an older server, or a
compatible server offering only Chat Completions, add this to the existing
driver callback:

```go
driver.API(openaillm.ChatCompletions)
```

Chat mode drops reasoning history/output, rejects `Document` parts with
`llm.ErrUnsupported`, and disables strict-schema rewriting by default.
[Mode Limits](drivers.md#mode-limits) describes the translation boundary.

Responses initializes `store: false`, but [request hooks](drivers.md#request-hooks)
can override it. That field is not a retention guarantee. Local servers,
gateways, and hosted providers have separate logging and storage policies.

## Local Embeddings

Generation and embedding use different models. Download an embedding model
before constructing the separate facade:

```sh
ollama pull nomic-embed-text
```

Reuse the local driver and embed one approved text:

```go
embedder := llm.NewEmbedder(driver, "nomic-embed-text")
vectors, err := embedder.Embed(ctx, "Search is unavailable during maintenance.")
```

On success, `vectors[0]` is a `[]float32` for that text. Keep the model and
dimensions consistent when comparing it with other vectors; [Embeddings](embeddings.md)
explains storage and comparison responsibilities.

## Checking Behavior

Server compatibility and model capability are separate checks. Smaller models
can produce invalid arguments or final output; schema validation and
[output correction](structured-output.md#correcting-replies) handle only the
documented classes of failure. A rejected output format returns
`llm.ErrUnsupported`, without a prompt-based fallback.

Ollama's documented Chat Completions support accepts inline image bytes, not
remote image URLs, and does not support `tool_choice`. With a vision-capable
model, use `llm.Image{Data: pngBytes, MediaType: "image/png"}` instead of assuming
URL input works. Schemas also remain subject to both the
[driver limits](drivers.md#strict-schemas) and the server's support.

Use the [scripted fake](testing.md) for repeatable application tests. Keep
local-server smoke tests separate and opt-in: a generated answer does not prove
tool authorization, output accuracy, or semantic search relevance.

## Cleaning Up

Stop a server you started with `ollama serve` using Ctrl+C. Do not stop a server
owned by another application. Remove downloaded models only when no longer
needed:

```sh
ollama rm llama3.2
ollama rm nomic-embed-text
```

Removing a model frees its local storage and requires another download before
you can use that identifier again.

For driver options, see the [openaillm API reference](https://pkg.go.dev/arkoren.dev/driver/openai/openaillm).
