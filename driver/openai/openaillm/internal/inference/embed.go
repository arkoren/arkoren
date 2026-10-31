package inference

import (
	"context"
	"fmt"
	"slices"

	"github.com/openai/openai-go/v3"

	"arkoren.dev/core/service/llm"
)

// embeddingVectors validates one batch's cardinality and unique indexes before
// publishing float32 vectors in input order. count is the submitted batch size;
// malformed native indexes or missing vectors return llm.ErrUnavailable without
// a partial result. Empty vectors retain the SDK's existing acceptance behavior.
func embeddingVectors(data []openai.Embedding, count int) ([][]float32, error) {
	if len(data) != count {
		return nil, fmt.Errorf("openaillm: got %d embeddings for %d inputs: %w", len(data), count, llm.ErrUnavailable)
	}

	vectors := make([][]float32, count)

	for _, item := range data {
		if item.Index < 0 || int(item.Index) >= count || vectors[item.Index] != nil {
			return nil, fmt.Errorf("openaillm: embedding index %d is out of range or repeated: %w",
				item.Index, llm.ErrUnavailable)
		}

		vector := make([]float32, len(item.Embedding))

		for i, value := range item.Embedding {
			vector[i] = float32(value)
		}

		vectors[item.Index] = vector
	}

	return vectors, nil
}

// Embed returns one vector per input, in input order, splitting the inputs
// into batches of at most the [Builder.EmbedBatchSize] setting and summing token usage
// of the batches. Purpose is ignored because OpenAI embeds queries and
// documents alike. A batch whose reply does not hold exactly one vector per
// input fails the call.
//
// Example:
//
//	result, err := driver.Embed(ctx, llm.EmbedRequest{Model: "text-embedding-3-small", Inputs: []string{"red shoes"}})
//	if err != nil {
//		return err
//	}
//	_ = result.Vectors[0]
func (driver *Client) Embed(ctx context.Context, request llm.EmbedRequest) (llm.Embeddings, error) {
	if request.Model == "" {
		return llm.Embeddings{}, fmt.Errorf("openaillm: embedding model is empty: %w", llm.ErrInvalidRequest)
	}

	if len(request.Inputs) == 0 {
		return llm.Embeddings{}, fmt.Errorf("openaillm: no inputs: %w", llm.ErrInvalidRequest)
	}

	size := driver.EmbedBatchSize

	if size == 0 {
		size = defaultEmbedBatchSize
	}

	result := llm.Embeddings{
		Vectors: make([][]float32, 0, len(request.Inputs)),
	}

	for batch := range slices.Chunk(request.Inputs, size) {
		if err := ctx.Err(); err != nil {
			return llm.Embeddings{}, err
		}

		params := openai.EmbeddingNewParams{
			Model: request.Model,
			Input: openai.EmbeddingNewParamsInputUnion{
				OfArrayOfStrings: batch,
			},
		}

		if request.Dimensions > 0 {
			params.Dimensions = openai.Int(int64(request.Dimensions))
		}

		reply, err := driver.SDK.Embeddings.New(ctx, params)

		if err != nil {
			return llm.Embeddings{}, MapError(err, nil)
		}

		vectors, err := embeddingVectors(reply.Data, len(batch))

		if err != nil {
			return llm.Embeddings{}, err
		}

		result.Vectors = append(result.Vectors, vectors...)
		result.Usage.InputTokens += int(reply.Usage.PromptTokens)
	}

	return result, nil
}

// defaultEmbedBatchSize is the existing OpenAI embedding batch limit.
const defaultEmbedBatchSize = 2048
