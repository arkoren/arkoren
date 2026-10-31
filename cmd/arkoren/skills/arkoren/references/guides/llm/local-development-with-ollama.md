---
title: Local Development with Ollama
nav: Local Development
description: Run models on your own machine with Ollama and point the OpenAI driver at it, including Chat Completions mode.
order: 70
---

# Local Development with Ollama

## Introduction

With a downloaded model running locally, you can develop against the `llm`
contract without a vendor API key or an internet connection.
[Ollama](https://ollama.com) serves an OpenAI-compatible API, so you reuse the
OpenAI driver without paying a hosted provider per request. Treat local results
as a check of your wiring; model quality and prompt behavior can differ from
your hosted deployment.

This page uses a local model, not Ollama's cloud service. Cloud models need
authentication and network access even when selected through a local server;
Ollama's [compatibility documentation](https://docs.ollama.com/api/openai-compatibility)
distinguishes the two setups.

## Pointing the driver at Ollama

Install the Go dependencies using the [overview setup](overview.md#creating-a-model).
The snippets here use `llm` from `arkoren.dev/core/service/llm` and `openaillm`
from `arkoren.dev/driver/openai/openaillm`.

Install [Ollama](https://ollama.com/download) and ensure its local server is
running. If your installation does not run it automatically, run `ollama serve`
in another terminal. It listens on port 11434. Then download a model that supports tools:

```sh
ollama pull llama3.2
```

Then set the base URL. The local server needs no key; the placeholder below also
prevents the SDK from reusing a hosted `OPENAI_API_KEY` from your environment:

```go
driver, err := openaillm.New(func(driver *openaillm.Builder) {
	driver.APIKey("ollama")
	driver.BaseURL("http://localhost:11434/v1")
})

model := llm.New(driver, "llama3.2")
```

Call this `model` using the [first-answer example](overview.md#running-an-agent).
Success prints a generated answer from the downloaded model, without contacting
OpenAI. This verifies a basic request; it does not verify support for every tool,
schema, or input listed in the [driver matrix](drivers.md#compatibility-matrix).

Keep this plaintext endpoint on loopback; do not expose an unauthenticated model
server to an untrusted network. Stop a manually started server with Ctrl+C when
you finish. The downloaded model remains on disk; `ollama rm llama3.2` removes
that local model if you no longer need it.

The driver reads `OPENAI_BASE_URL` too, so you can instead set that variable in
your development environment. Use a nonsecret local API-key value rather than
inheriting hosted credentials. Model IDs are the names you
pulled. Embeddings use an embedding model, for example `ollama pull
nomic-embed-text`, with `llm.NewEmbedder(driver, "nomic-embed-text")`.

Register `driver.Ping` as a [health check](../application/health-and-readiness.md)
to see at startup whether the server is running; it lists the server's models.

## Older Ollama versions

By default the driver speaks the Responses API, which
[Ollama supports from version 0.13.3](https://docs.ollama.com/api/openai-compatibility#responses-api).
Ollama supports non-stateful Responses requests. Do not infer that hosted OpenAI
or another compatible server has the same limitation: native hooks can request
storage, and each provider's policies still apply.
For an older server, or another server that only offers Chat
Completions, choose that mode:

```go
driver, err := openaillm.New(func(driver *openaillm.Builder) {
	driver.APIKey("ollama")
	driver.BaseURL("http://localhost:11434/v1")
	driver.API(openaillm.ChatCompletions)
})
```

Chat Completions has a few differences. Reasoning output is ignored, `Document`
parts return `llm.ErrUnsupported`, and strict schemas are off by default, so the
schema is sent as written. The [driver reference](drivers.md#behavior-and-limits)
lists the mode constraints.

The driver's [request hooks](drivers.md#setting-provider-specific-request-fields)
can change native defaults, including `Store`. This is not a provider-retention
guarantee. Ollama, gateways, and hosted OpenAI have different storage and logging
policies; changing the base URL does not establish one shared privacy policy.

## What to expect from small models

Local models follow tool schemas and output formats less reliably. If an agent
returns invalid output or calls a tool with bad arguments, the retries described
in [Structured Output](structured-output.md#when-the-output-is-invalid) and the
validation errors sent back to the model make up for some of it. If a server
rejects the structured output format, the call returns `llm.ErrUnsupported`;
there is no fallback that rewrites your prompt.

Check the selected model and server as well as the driver: Ollama's documented
Chat Completions support includes inline image bytes but not remote image URLs,
and does not support `tool_choice`. Use `llm.Image{Data: imageBytes, MediaType:
"image/png"}` with an appropriate vision model rather than assuming URL input
or forced tool selection works. Structured schemas remain subject to the
[driver's schema limits](drivers.md#strict-schemas) and the server's support.

## Testing your application

Use the [scripted fake](testing.md#scripting-replies) for repeatable application
tests without running Ollama. Keep a separate opt-in smoke test against this
local `model` when you need to verify the server connection; a generated answer
proves the connection works, not that every prompt or tool policy is correct.

For every option, see [the `openaillm` API
reference](https://pkg.go.dev/arkoren.dev/driver/openai/openaillm).
