# OpenAI LLM Driver

`arkoren.dev/driver/openai/openaillm` implements `llm.Driver` with the official OpenAI Go SDK
(`github.com/openai/openai-go/v3`). It speaks the Responses API by default and the Chat Completions API with
`Builder.API(openaillm.ChatCompletions)`, against OpenAI or any compatible server (Ollama 0.13.3 or later for Responses,
vLLM, LiteLLM).

`New` does no I/O and reads `OPENAI_API_KEY` and `OPENAI_BASE_URL` from the environment unless given `Builder.APIKey` and
`Builder.BaseURL`. The SDK's retries are off by default. Tool and output schemas are rewritten for strict mode in Responses
mode, and `Builder.ResponsesHook` and `Builder.ChatHook` expose the SDK's request structs for settings the contract leaves out,
such as sampling.

Install from your application's module directory:

```sh
go get arkoren.dev/core arkoren.dev/driver/openai
```

Import `llm` from `arkoren.dev/core/service/llm` and `openaillm` from
`arkoren.dev/driver/openai/openaillm`. With Ollama running on loopback and
`llama3.2` downloaded, configure a local connection:

```go
driver, err := openaillm.New(func(driver *openaillm.Builder) {
	driver.BaseURL("http://localhost:11434/v1")
	driver.API(openaillm.ChatCompletions)
})
model := llm.New(driver, "llama3.2")
```

Callbacks run directly once in order on one fresh draft before validation. Nil entries panic;
expanded nil or empty slices omit edits. Retained drafts cannot change published settings.
`StrictSchemas(false)` explicitly disables strict rewriting; `MaxRetries(0)` disables retries.
Hooks are stored until each matching request. `NewFrom(client, configure...)` keeps the
caller-owned SDK client and ignores connection settings, but validates them as `New` does.

The model is ready for calls; no driver start or stop hook is needed. Follow the
[language model overview](../../../cmd/arkoren/skills/arkoren/references/guides/llm/overview.md)
for an agent call and visible answer, or the
[Ollama guide](../../../cmd/arkoren/skills/arkoren/references/guides/llm/local-development-with-ollama.md)
for local server setup. For hosted calls, set `OPENAI_API_KEY` outside source
control and choose a model your account can use. Calls can incur provider charges.
Responses requests default to `store: false`, which is not a guarantee against
provider logging or retention. `ResponsesHook` can override this default and
the requested encrypted reasoning content; review its policy before sharing
it across models.

For request modes, hooks, native access, and optional notifications, see the
[driver guide](../../../cmd/arkoren/skills/arkoren/references/guides/llm/drivers.md) and
[API reference](https://pkg.go.dev/arkoren.dev/driver/openai/openaillm).

## Contributor tests

`go test -short` replays fixtures from `testdata/` and needs no network. Without `-short`, the `llmtest` conformance
suite runs in both modes when `OPENAI_API_KEY` or `OPENAI_BASE_URL` is set; `OPENAI_MODEL` and
`OPENAI_EMBEDDING_MODEL` choose the models. These commands run from this module's
source checkout, not from a consuming application.
