// Package openaillm provides an OpenAI [llm.Driver] for the Responses and Chat
// Completions APIs, built on the official Go SDK (github.com/openai/openai-go/v3),
// for OpenAI itself or any server that implements either API (Ollama, vLLM,
// LiteLLM, and others).
//
// # Overview
//
// [New] builds a [Driver] and does no I/O; [Driver.Ping] lists the models to
// check the connection. The driver translates [llm.Request] values and
// nothing else: defaults come from the [llm.New] facade, and the SDK's own
// retries are off ([Builder.MaxRetries]) so the caller decides what to retry.
//
//	driver, err := openaillm.New() // reads OPENAI_API_KEY and OPENAI_BASE_URL
//	if err != nil {
//		return err
//	}
//
//	model := llm.New(driver, "gpt-5.2")
//	embedder := llm.NewEmbedder(driver, "text-embedding-3-small")
//
// # API modes
//
// [Responses] (the default) sets store=false and requests
// reasoning.encrypted_content before invoking the native hook. The hook can
// override both. This default avoids response-history storage, not all provider
// logging or retention; check the provider's data policy independently.
// An encrypted reasoning item comes back as an [llm.Reasoning] part whose
// Signature holds the item's ID and encrypted content; send the assistant
// message back unchanged and the driver restores the item. A reasoning part
// without such a signature is skipped. Tool calls map to function_call items
// and tool results to function_call_output items.
//
// [ChatCompletions] maps tool results to tool messages, turns on
// stream_options.include_usage, returns [llm.ErrUnsupported] for [llm.Document]
// parts, and ignores reasoning in both directions apart from reasoning_effort.
//
// # Structured output and strict schemas
//
// Output schemas go out as json_schema response formats, and a server that
// rejects the format yields [llm.ErrUnsupported]; there is no prompt-based
// fallback. With strict schemas on (the default for [Responses], see
// [Builder.StrictSchemas]) the driver rewrites tool and output schemas: every
// property is required, optional properties accept null, and
// additionalProperties is false.
//
// # Errors
//
// HTTP and response errors map to the llm sentinels with the SDK error kept in
// the chain: 429 to [llm.ErrRateLimited], 5xx and overloaded to
// [llm.ErrUnavailable], an unknown model to [llm.ErrNotFound],
// context_length_exceeded to [llm.ErrContextLength], content-policy rejections
// to [llm.ErrRefused], and other 4xx to [llm.ErrInvalidRequest]. A refusal
// inside a successful reply is not an error; it ends with [llm.StopRefusal].
//
// # Native settings
//
// The llm request has no sampling parameters. [Builder.ResponsesHook] and
// [Builder.ChatHook] expose the SDK's parameter structs just before they are sent,
// and [Driver.Client] exposes the client.
//
// Constructors invoke supplied Builder callbacks directly once in order on a
// fresh draft before validation. Nil entries panic; expanded nil or empty slices
// omit edits. Retained drafts cannot change a driver's published settings. Native
// hooks run at request time, not during construction; SDK objects stay shared.
package openaillm
