// Package inference translates portable inference records into OpenAI requests
// and reconstructs replies, stream events, schemas, and provider errors.
package inference

import (
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

// Client observes an already configured native client and request policy. It
// neither constructs the SDK client nor adds retries or retains request data.
type Client struct {
	// SDK is the unchanged native client used for each request.
	SDK openai.Client
	// Strict selects schema rewriting and native strict flags.
	Strict bool
	// EmbedBatchSize bounds embedding batches; zero selects the existing default.
	EmbedBatchSize int
	// ChatHook runs after conversion for each Chat request.
	ChatHook func(*openai.ChatCompletionNewParams)
	// ResponsesHook runs after conversion for each Responses request.
	ResponsesHook func(*responses.ResponseNewParams)
}
