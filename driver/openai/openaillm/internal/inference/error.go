package inference

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/openai/openai-go/v3"

	"arkoren.dev/core/service/llm"
)

// MapError turns an SDK or transport error into one wrapping the matching
// [llm] sentinel while keeping err in the chain, so callers can use both
// errors.Is(err, llm.ErrRateLimited) and errors.As(err, *openai.Error).
//
// Context errors and errors that are not API errors pass through unchanged
// (apart from the operation prefix). output is the request's schema, or nil
// when absent: a 400 that blames the format of requested structured output
// means the server cannot support it and maps to [llm.ErrUnsupported].
func MapError(err error, output []byte) error {
	var apiErr *openai.Error

	if !errors.As(err, &apiErr) {
		return fmt.Errorf("openaillm: %w", err)
	}

	message := strings.ToLower(apiErr.Message)
	sentinel := apiErrorSentinel(apiErr, message)
	formatRequested := len(output) > 0 && apiErr.Code != "invalid_json_schema"

	if errors.Is(sentinel, llm.ErrInvalidRequest) && formatRequested && rejectsFormat(apiErr.StatusCode, message) {
		sentinel = llm.ErrUnsupported
	}

	return fmt.Errorf("openaillm: %w: %w", sentinel, err)
}

// apiErrorSentinel classifies native codes and HTTP status without inspecting
// the request. Context/model diagnostics outrank retryable status codes; other
// provider failures are considered before a structured-format fallback.
// message is the already lowercased native message from MapError.
func apiErrorSentinel(apiErr *openai.Error, message string) error {
	switch {
	case apiErr.Code == "context_length_exceeded" || strings.Contains(message, "maximum context length"):
		return llm.ErrContextLength
	case apiErr.Code == "model_not_found" ||
		(apiErr.StatusCode == http.StatusNotFound && strings.Contains(message, "model")):
		return llm.ErrNotFound
	case apiErr.StatusCode == http.StatusTooManyRequests:
		return llm.ErrRateLimited
	case apiErr.StatusCode >= http.StatusInternalServerError || apiErr.Type == "overloaded_error":
		return llm.ErrUnavailable
	case slices.Contains([]string{"content_filter", "content_policy_violation", "invalid_prompt"}, apiErr.Code):
		return llm.ErrRefused
	}

	return llm.ErrInvalidRequest
}

// rejectsFormat reports whether a client error complains about the structured
// output format itself rather than about the schema's content.
func rejectsFormat(status int, message string) bool {
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}

	for _, word := range []string{"response_format", "json_schema", "text.format", "structured output"} {
		if strings.Contains(message, word) {
			return true
		}
	}

	return false
}

// mapFailure maps the error a Responses API response or stream event reports
// in its body (code and message) rather than in an HTTP status. The codes are
// those of the response error object; unknown codes map to
// [llm.ErrInvalidRequest].
func mapFailure(code, message string) error {
	var sentinel error

	switch code {
	case "rate_limit_exceeded":
		sentinel = llm.ErrRateLimited
	case "server_error", "vector_store_timeout":
		sentinel = llm.ErrUnavailable
	case "context_length_exceeded":
		sentinel = llm.ErrContextLength
	case "model_not_found":
		sentinel = llm.ErrNotFound
	case "content_filter", "content_policy_violation", "bio_policy", "misalignment_policy_violation":
		sentinel = llm.ErrRefused
	default:
		sentinel = llm.ErrInvalidRequest
	}

	return fmt.Errorf("openaillm: %w: %s: %s", sentinel, code, message)
}
