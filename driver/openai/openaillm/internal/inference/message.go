package inference

import (
	"encoding/base64"
	"fmt"

	"github.com/openai/openai-go/v3/shared"

	"arkoren.dev/core/service/llm"
)

// dataURL returns data as a base64 data: URL of the given media type, the
// form both APIs accept for inline images and files.
func dataURL(mediaType string, data []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// imageURL returns the URL to send for image: its URL, or its bytes as a data
// URL. An image with neither, or with bytes but no media type, is invalid.
func imageURL(image llm.Image) (string, error) {
	switch {
	case image.URL != "":
		return image.URL, nil
	case len(image.Data) > 0 && image.MediaType != "":
		return dataURL(image.MediaType, image.Data), nil
	}

	return "", fmt.Errorf("openaillm: image needs a URL, or data with a media type: %w", llm.ErrInvalidRequest)
}

// toolArguments returns the JSON arguments to send for a tool call, "{}" when
// the call has none.
func toolArguments(call llm.ToolCall) string {
	if len(call.Arguments) == 0 {
		return "{}"
	}

	return string(call.Arguments)
}

// callArguments is the inverse of [toolArguments]: the arguments of a reply's
// tool call as bytes, "{}" when the model sent none.
func callArguments(arguments string) []byte {
	if arguments == "" {
		return []byte("{}")
	}

	return []byte(arguments)
}

// effort maps the contract's reasoning effort to OpenAI's. EffortDefault maps
// to the empty value, which the SDK omits.
func effort(effort llm.Effort) shared.ReasoningEffort {
	switch effort {
	case llm.EffortOff:
		return shared.ReasoningEffortNone
	case llm.EffortLow:
		return shared.ReasoningEffortLow
	case llm.EffortMedium:
		return shared.ReasoningEffortMedium
	case llm.EffortHigh:
		return shared.ReasoningEffortHigh
	}

	return ""
}

// checkRequest rejects requests no mode can send: an empty model or no
// messages.
func checkRequest(request llm.Request) error {
	switch {
	case request.Model == "":
		return fmt.Errorf("openaillm: model is empty: %w", llm.ErrInvalidRequest)
	case len(request.Messages) == 0:
		return fmt.Errorf("openaillm: no messages: %w", llm.ErrInvalidRequest)
	}

	return nil
}
