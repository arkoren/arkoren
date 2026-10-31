---
title: Embeddings
description: Turn text into vectors with an Embedder, choose dimensions and purpose, and understand batching.
order: 50
---

# Embeddings

## Introduction

Someone searching your help center for "change my password" should be able to
find an article called "Reset your credentials", even though the words differ.
Embeddings let you compare meaning rather than exact wording. An **embedding**
is a list of numbers representing a piece of text; texts with similar meanings
have similar vectors.

You can use them for semantic search, recommendations, and finding duplicates.
Embedding models are separate from the
models that write text, so the `llm` service gives them their own facade, the
`*llm.Embedder`, on the same driver.

The examples use `llm` from `arkoren.dev/core/service/llm` and `math` from the
standard library. Reuse `ctx` from the [overview setup](overview.md#creating-a-model).

## Embedding texts

To prepare text for comparison, choose an embedding model and pass the texts to
an embedder. Reuse the `driver` created in [the overview](overview.md#creating-a-model).
Use the same model for stored articles and incoming searches:

```go
embedder := llm.NewEmbedder(driver, "text-embedding-3-small")

vectors, err := embedder.Embed(ctx, "first text", "second text")
```

`Embed` returns one `[]float32` per input, in the order you gave them. The OpenAI
driver splits inputs into batches of at most 2048 by default and restores reply
order; configure its `EmbedBatchSize` for another compatible server's input-count
limit. This does not split individual texts or enforce a provider's token budget,
so bound your text lengths and corpus size before calling. Calling `Embed` with no
texts is reported as `llm.ErrInvalidRequest` before any request is sent.

By default, the model chooses the vector size. If you need smaller vectors to
reduce storage, request dimensions in the embedder's construction callback:

```go
embedder := llm.NewEmbedder(driver, "text-embedding-3-small", func(vectors *llm.EmbedderBuilder) {
	vectors.Dimensions(512)
})
```

The model must support the requested size; evaluate the effect on search quality
with your own data. Zero uses the model's default. Keep
the model and the dimensions the same for everything you compare: vectors from
different models are not comparable.

## Queries and documents

In a help-center search, an article is the stored document and the visitor's
question is the query. Some vendors embed these two roles differently.
`EmbedderBuilder.Purpose`
tells the driver which you are embedding, `llm.PurposeQuery` or
`llm.PurposeDocument`. Drivers for vendors that do not distinguish them, such
as the OpenAI driver, ignore it, so setting it is safe and keeps your code
portable.

## Comparing vectors

Vectors that point the same way are similar. For nonzero vectors of the same
length, cosine similarity compares their direction:

```go
func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i] * b[i])
		na += float64(a[i] * a[i])
		nb += float64(b[i] * b[i])
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
```

Store the vectors wherever you query them; Arkoren does not prescribe a vector
store. Embeddings are not cached, so cache them yourself, with the
[cache service](../cache/overview.md) or in your database, when the same text is
embedded often.

For deterministic application tests, use the [scripted fake](testing.md#streaming-and-embeddings).
Its hash-based vectors test wiring and storage, not semantic similarity. For
local model-generated embeddings, follow the [Ollama setup](local-development-with-ollama.md#pointing-the-driver-at-ollama).

## Events and telemetry

`Embed` produces an `embeddings {model}` span and, with `llm.EmbedderBuilder.Events`, the
`llm.embeddings.generated` and `llm.embeddings.failed` events with the model,
input count, and duration; successful events also carry usage. Failure events
carry bounded `ErrorType`, not the original error or input texts. The
[observation and privacy rules](overview.md#usage-events-and-telemetry) also apply
to embedding failures and panics.

For every function and option, see [the `llm` API reference](https://pkg.go.dev/arkoren.dev/core/service/llm).
