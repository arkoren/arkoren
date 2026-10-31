package openaillm

import (
	"context"

	"arkoren.dev/core/service/llm"
	"arkoren.dev/driver/openai/openaillm/internal/inference"
)

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
func (driver *Driver) Embed(ctx context.Context, request llm.EmbedRequest) (llm.Embeddings, error) {
	return driver.inference().Embed(ctx, request)
}

// inference captures the published request policy without constructing a native
// client or invoking hooks. Native state and hook captures remain shared.
func (driver *Driver) inference() *inference.Client {
	return &inference.Client{
		SDK: driver.client, Strict: driver.strict,
		EmbedBatchSize: driver.config.EmbedBatchSize,
		ChatHook:       driver.config.ChatHook, ResponsesHook: driver.config.ResponsesHook,
	}
}
