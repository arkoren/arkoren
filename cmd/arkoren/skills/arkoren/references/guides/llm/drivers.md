---
title: Language Model Drivers
nav: Drivers
description: Install, connect, and configure the OpenAI driver, which also serves Ollama, vLLM, and other compatible servers.
order: 80
---

# Language Model Drivers

## Introduction

Use the OpenAI driver for hosted OpenAI models or a server with a compatible
API. Choose the API mode and model together: a server can accept text requests
without supporting your tools, schemas, or media inputs. The matrix below shows
what Arkoren translates in each mode, so you can check those requirements before
connecting.

The driver handles the vendor's wire protocol and maps its errors onto the
contract's sentinels. Defaults, request validation, agents, telemetry, and events
belong to the facade; your calling code uses the same contract types.

There is one vendor driver today, OpenAI. Compatible servers use that driver,
subject to their API and model limitations. There is no memory driver;
tests use the scripted `llmtest.Fake` ([Testing Language Models](testing.md)).
[Choosing a Driver](../drivers/choosing-a-driver.md) compares drivers across
services.

## Compatibility matrix

Compare the two API modes of the shipped OpenAI driver. These are translation
capabilities exposed by Arkoren, not guarantees that every model or compatible
server accepts every request. Ollama, vLLM, and gateways use this same driver;
check their selected API and model rather than assuming OpenAI feature parity.
The scripted fake is for application tests, not a separate provider protocol.

| Driver and mode | Streaming | Tool calls | Structured output | Image input | Document input | Reasoning history | Embeddings |
| --- | --- | --- | --- | --- | --- | --- | --- |
| [OpenAI: Responses](#openai) | Yes | Yes | Yes | Yes | Yes | Partial — Only reasoning items with a signature round-trip. | Yes |
| [OpenAI: Chat Completions](#openai) | Yes | Yes | Yes | Yes | No | No | Yes |

Responses is the default mode. Chat Completions rejects document parts with
`llm.ErrUnsupported` and drops reasoning history, though it still sends the
requested reasoning effort. Responses restores reasoning items only when their
signature contains the item ID and encrypted content. Structured output uses a
JSON Schema response format with no prompt-based fallback if the server rejects
it. Embeddings use the separate embeddings endpoint and model in either mode.
Select a model and server that support your required inputs, tools, and schemas.

## OpenAI

The `openaillm` driver speaks to OpenAI through its Responses API, and through
Chat Completions for servers that only implement that. Choose it for OpenAI's
models, or for a compatible server such as Ollama, vLLM, or a gateway. It
handles text, images, PDFs, tool calls, reasoning, structured output, and
embeddings.

### Installation

Install the OpenAI driver from your application's module directory:

```sh
go get arkoren.dev/core arkoren.dev/driver/openai
```

### Connecting

With an OpenAI account and access to your chosen model, set `OPENAI_API_KEY`
outside source control. Construct the connection and model in your application:

```go
import (
	"arkoren.dev/core/service/llm"
	"arkoren.dev/driver/openai/openaillm"
)

driver, err := openaillm.New() // OPENAI_API_KEY, OPENAI_BASE_URL from the environment

model := llm.New(driver, "gpt-5.2")
```

Handle the construction error before using `driver`. Set `OPENAI_API_KEY`
before calling the hosted service and choose a model your
account can access. `New` does no I/O; connection and credential failures appear
on the first request. Follow the [overview](overview.md#running-an-agent) to get
your first answer. Register `Ping` as a
[health check](../application/health-and-readiness.md) if the server supports listing models.

### Configuration

If your organization routes calls through an OpenAI-compatible gateway, keep
the same calling code and change the connection. Here `key` is the gateway
credential loaded by your application:

```go
driver, err := openaillm.New(func(driver *openaillm.Builder) {
	driver.APIKey(key)
	driver.BaseURL("https://llm-gateway.internal/v1")
})
```

You can also set `OPENAI_BASE_URL` without changing source. Keep SDK retries off
unless you deliberately want retry policy in the connection rather than your
application. They default to zero additional attempts; `MaxRetries(n)` enables
up to `n` SDK retries. Use a request context with a deadline to bound the whole
call. If you supply `HTTPClient`, its timeout and transport remain your
responsibility; `NewFrom` retains the native client's own retry policy.
`New` reads `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_ORG_ID`, and
`OPENAI_PROJECT_ID` through the SDK unless explicitly configured. Servers
without authentication need no API key. Empty API selects Responses; empty
strict mode selects its mode-specific default. These empty values are not
equal to the named constants. Unknown API modes and negative retry or embedding
batch-size settings fail construction.

Embeddings use batches of at most 2048 inputs by default; zero restores that
default. Results are ordered by the response index and input-token usage is
summed across batches. `Purpose` is ignored by this driver. Use
[request hooks](#setting-provider-specific-request-fields) for native parameters
or [reuse an SDK client](#reusing-a-native-client) for existing connection policy.

### Generating replies and embeddings

With the `model` above, follow [running an agent](overview.md#running-an-agent)
to receive text or [streaming one turn](streaming.md#streaming-one-turn) to
display it as it arrives. For vectors, pass the same `driver` to the
[embedding setup](embeddings.md#embedding-texts); the embedding model is
separate from the chat model. No driver `Start` or `Stop` call is required.

### Behavior and limits

Use the default Responses mode when the server supports it. If it only offers
Chat Completions, select that mode in the
[local setup](local-development-with-ollama.md#older-ollama-versions); document parts
and reasoning do not have the same support. Responses requires `/responses`
(available in [Ollama 0.13.3 and later](https://docs.ollama.com/api/openai-compatibility#responses-api)).
`Ping` needs a working `GET /models` endpoint and reads one page. A server without
that endpoint fails readiness even if generation works.

By default, Responses initializes `store: false` and
`include: ["reasoning.encrypted_content"]` before the native hook. These are
request defaults, not enforced storage restrictions: a hook can override either
field, and an explicit `Store = openai.Bool(true)` reaches both generation and
streaming requests. Neither mode guarantees that the server avoids logging or
retaining data. Review your hooks, gateway configuration, and provider policy
before sending private prompts or tool results.

A compatible server must accept the parameters your hook leaves in the request.
Reasoning signatures retain `<item id>:<encrypted content>` for replay with your
saved conversation. Reasoning without that signature is skipped. Document parts
become `input_file` data URLs.
Tool calls use `call_id`; tool-result error flags have no provider field, so
their content is sent unchanged.

Chat Completions sends single text parts as plain strings for older servers and
requests stream usage. It rejects documents with `llm.ErrUnsupported`, drops
reasoning history/output, and sends only reasoning effort. For conformance tests
in this mode, skip those checks with `llmtest.WithoutDocuments()` and
`llmtest.WithoutReasoning()`.

In both modes, maximum tokens map to the corresponding provider output limit.
Reasoning Off sends `none`, Low/Medium/High use their names, and Default omits the
setting. Input-token usage includes cached tokens; output usage includes
reasoning tokens. Unreported counts remain zero. Stop reasons remain successful
single-turn replies: tool calls, exhausted output limits, refusals, and content
filters have distinct portable reasons. The agent loop interprets those reasons
as described in [Language Models Overview](overview.md#errors).

Stopping iteration cancels the HTTP request. A stream missing its terminal event
fails with `llm.ErrUnavailable` wrapping `io.ErrUnexpectedEOF`.

### Strict schemas

Strict schema rewriting defaults on for Responses and off for Chat Completions.
To disable it explicitly, set `StrictSchemas(false)` in the construction
callback. Strict rewriting recursively marks every property required, permits
null for formerly optional properties, and sets `additionalProperties: false`.
It traverses properties, items, composition branches, `$defs`, and definitions.
Open maps without properties remain unchanged; they do not satisfy OpenAI's
strict-object requirements. A compatible server's acceptance depends on its own
schema support. Turning rewriting off does not guarantee that a server accepts
open maps or structured output.

A null optional value decodes to Go zero, which typed validation treats as absent.
With strict mode off, schemas remain unchanged and are sent with `strict: false`.

Tool and output schemas must have JSON-object roots in either mode. JSON `null`,
arrays, and scalar roots fail locally with `llm.ErrInvalidRequest` before the
native hook or an HTTP request. Strict mode does not replace the application's
validation of model output or tool arguments.

Structured output uses `text.format` in Responses or `response_format` in Chat
Completions, with type `json_schema`. Unsupported formats return
`llm.ErrUnsupported`; there is no prompt-based fallback. Use
[Structured Output](structured-output.md) to declare and validate the Go result.

### Interpreting provider failures

The SDK's `*openai.Error` stays available through `errors.As`. The driver maps
429 to `llm.ErrRateLimited`, server failures to `ErrUnavailable`, missing models
to `ErrNotFound`, context-limit failures to `ErrContextLength`, and policy
refusals to `ErrRefused`. Other client failures normally match `ErrInvalidRequest`.

A 400/422 naming structured-output fields while an output schema was sent can
match `ErrUnsupported`, except `invalid_json_schema`, which remains
`ErrInvalidRequest`. A failed Responses body/stream event uses its error code;
unknown body codes also become `ErrInvalidRequest`. Do not treat an invalid
schema as evidence that the server lacks structured output.

### Receiving background notifications

This setup is optional; ordinary generation, streaming, and embeddings do not
require a webhook endpoint. When your application creates background work through
OpenAI's provider API, receive its response lifecycle notifications with `openaiwebhook` from the same
module. This adapter neither starts background work nor changes `openaillm`
requests. Copy the endpoint's `whsec_` signing secret from OpenAI's webhook
settings, not your API key.

With imports `arkoren.dev/core/http/webhook` and
`arkoren.dev/driver/openai/openaiwebhook`, supply your
`accept` function of type `webhook.AcceptFunc[openaiwebhook.Response]`:

```go
source, err := openaiwebhook.New(signingSecret)
if err != nil {
	return err
}
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[openaiwebhook.Envelope]) {
	receiver.Handle(openaiwebhook.ResponseCompleted, accept)
})
if err != nil {
	return err
}
r.Post("/webhooks/openai", handler)
```

Here `r` is your application router; check both construction errors before
registering the handler. Configure its public HTTPS URL and event subscriptions
in OpenAI. Core acknowledges after acceptance succeeds; the
[webhook guide](../http-capabilities/webhooks.md) covers queueing and receipt policy.
The adapter also defines failed, canceled, and incomplete events. These are
response references, not generated output: retrieve the response in your handler
or worker.

The official SDK verifies the raw body and a five-minute past/future timestamp
window. Keep clocks synchronized and use deduplication to reduce replays within
that window. `Delivery.ID` is the signed `webhook-id`; `Envelope.ID` is the
separate JSON event ID. For payloads and failure behavior, see the
[OpenAI webhook reference](https://pkg.go.dev/arkoren.dev/driver/openai/openaiwebhook).

`New` requires a canonical `whsec_` base64 secret with at least 24 decoded bytes.
Invalid configuration wraps `webhook.ErrInvalid`; invalid authentication matches
both `openaiwebhook.ErrInvalidSignature` and `webhook.ErrRejected` (400 by
default). Authenticated malformed envelopes or bound response references return
503. Bound events need `data.id` and a positive Unix-seconds `created_at`; an
optional nonempty `object` must be `event`. Authenticated unbound events receive
200 without event-specific decoding.

No response is fetched automatically. `ResponseCanceled` uses native event key
`response.cancelled`; failed, canceled, and incomplete payloads remain thin
references, not output snapshots or failure reasons. OpenAI's
[webhook delivery policy](https://developers.openai.com/api/docs/guides/webhooks#handling-webhook-requests-on-a-server)
describes retries for up to 72 hours, but that is provider policy, not an
exactly-once receiver guarantee. Scope deduplication by project/endpoint and make
acceptance idempotent.

### Setting provider-specific request fields

If a deployment needs an OpenAI-only setting, use a request hook rather than
changing portable agent code. Import `responses` from
`github.com/openai/openai-go/v3/responses` and add this to the driver's existing
construction callback to request the Flex service tier:

```go
driver.ResponsesHook(func(params *responses.ResponseNewParams) {
	params.ServiceTier = responses.ResponseNewParamsServiceTierFlex
})
```

The hook sees the translated history, tools, output schema, and defaults before
each Responses request, including each agent turn and each streaming request.
It does not run during construction. Its edits are not reset by the driver:
setting `params.Store = openai.Bool(true)` deliberately changes the storage
request. Do not use `store: false` as a guarantee against provider retention.

Keep captures concurrency-safe, do not retain request parameters, and check that
the server supports the fields you set. `ChatHook` provides the corresponding
Chat Completions boundary, including an explicit `Store` override. For a Chat
stream, the driver subsequently sets `stream_options.include_usage` to `true`,
even if the hook disabled it. Neither hook applies to embeddings.

### Reusing a native client

If your application already configures the official SDK, import `openai` from
`github.com/openai/openai-go/v3` and pass your existing `client openai.Client`
to `openaillm.NewFrom(client)`. The caller retains ownership and connection and
retry settings; the driver's API mode, schemas, batch size, and hooks still
apply.

`NewFrom` ignores connection edits but validates settings just like `New`.
Construction callbacks run once in order on a fresh draft; retained drafts
cannot alter published settings. Native client internals and hook captures remain
shared. A nil hook clears it; hooks must not retain request parameters.

To make SDK calls outside the contract, use the existing driver's client:

```go
client := driver.Client()
models, err := client.Models.List(ctx)
```

Here `ctx` is your request or command context. On success, `models.Data` contains
one page of provider model records. Native operations do not inherit the
facade's validation, events, or telemetry and are not portable contract methods.

### Running locally

[Local Development with Ollama](local-development-with-ollama.md) shows how to
point the driver at a server on your machine.

### API references

For every function and option, see [the `openaillm` API
reference](https://pkg.go.dev/arkoren.dev/driver/openai/openaillm) and the optional
[OpenAI webhook adapter](https://pkg.go.dev/arkoren.dev/driver/openai/openaiwebhook).
