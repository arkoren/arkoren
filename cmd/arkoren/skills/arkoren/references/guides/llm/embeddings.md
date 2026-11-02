---
title: Embeddings
description: Generate ordered text vectors and keep model, dimensions, purpose, and storage responsibilities consistent.
order: 80
summary:
  - Embed text into ordered float32 vectors using a separate embedding model.
  - Choose dimensions and query or document purpose consistently for comparison.
  - Bound input batches and store vectors without confusing deterministic test data with semantic relevance.
---

# Embeddings

A search for “planned downtime” should find an article titled “Sunday maintenance”
even if the words differ. An **embedding** represents text as a vector of
numbers so a search system can compare meaning instead of only exact words.
`*llm.Embedder` requests those vectors; it does not store them or run a search.

Use `llm` from `arkoren.dev/core/service/llm`, a configured `driver` from
[Drivers](drivers.md), and an operation context `ctx`. Choose a model intended
for embeddings, separately from the model that generates replies. Here
`embeddingModelID` is that selected ID.

## Embedding Text

To prepare maintenance articles for comparison, pass their approved text to a
reusable embedder:

```go
embedder := llm.NewEmbedder(driver, embeddingModelID)
vectors, err := embedder.Embed(ctx,
	"Search is unavailable during Sunday maintenance.",
	"The status page lists completed maintenance windows.",
)
```

On success, `vectors` has type `[][]float32`: `vectors[0]` belongs to the first
article and `vectors[1]` to the second. No-input calls fail with
`llm.ErrInvalidRequest` before provider I/O. Nil contexts, nil drivers, empty
model IDs, and negative dimensions are also rejected; the facade rejects a
driver result whose vector count does not match the input count.

The OpenAI driver batches at most 2048 inputs by default and restores input
order using response indexes. Configure its `EmbedBatchSize` for a server with a
different input-count limit. Batching does not split an individual text or
enforce the provider's token budget. Bound text lengths and corpus size in your
application. The facade does not retry failed calls.

## Choosing Dimensions

By default, the provider selects the vector length. If the model supports a
smaller representation and your storage needs it, request the dimensions at
construction:

```go
embedder := llm.NewEmbedder(driver, embeddingModelID, func(embedder *llm.EmbedderBuilder) {
	embedder.Dimensions(512)
})
```

Zero restores the provider default. The model must support the requested size;
evaluate relevance with your own data before reducing it. Use the same model
and dimensions for indexed documents and incoming queries. Vectors from
different models are not interchangeable, even if they have equal lengths.

## Query Purpose

Some providers represent stored documents differently from search queries.
Mark the stored text as `llm.PurposeDocument`; construct a corresponding query
embedder with `llm.PurposeQuery`:

```go
queries := llm.NewEmbedder(driver, embeddingModelID, func(embedder *llm.EmbedderBuilder) {
	embedder.Purpose(llm.PurposeQuery)
})
queryVectors, err := queries.Embed(ctx, "When is planned downtime?")
```

`queryVectors[0]` is the query vector to compare with stored document vectors.
Use the same dimension setting on both embedders if you override it. The OpenAI
driver ignores purpose because it uses the same embedding operation for both;
other drivers can use the hint. Omission selects `llm.PurposeUnspecified`.

## Storing Vectors

Store each article's vector with its source identifier and the model/dimension
configuration that produced it. Arkoren does not prescribe a vector store,
similarity function, normalization rule, or relevance threshold. Before using
cosine similarity, verify that vectors are nonzero and have equal lengths;
those conditions are not guaranteed by the facade's vector-count check.

Embeddings are not cached automatically. Cache repeated work in your storage
layer or the [cache service](../cache/overview.md). Changing the model or
dimensions requires rebuilding comparable stored vectors.

For application tests, the [scripted fake](testing.md#testing-vectors) produces
deterministic hash-based vectors, not semantic embeddings. It tests wiring and
storage, not search relevance. [Local Development](local-development-with-ollama.md#local-embeddings)
shows the separate Ollama embedding-model workflow.

## Observing Calls

Embedding calls produce `embeddings {model}` spans. Configure
`EmbedderBuilder.Events(bus)` separately from model events to publish
`llm.embeddings.generated` or `llm.embeddings.failed`. Payloads include model,
input count, and duration; successful events include usage. Failures contain
bounded `ErrorType`, not raw errors or texts.

Apply the [observation and privacy rules](overview.md#observing-calls), including
panic behavior. Stored vectors, source texts, and returned errors remain your
application's data to protect; telemetry privacy is not a storage policy.

For dimensions, purpose, and embedding requests, see the [llm API reference](https://pkg.go.dev/arkoren.dev/core/service/llm).
