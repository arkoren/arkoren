package wire

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/core/mcp"
	"arkoren.dev/foundation/validate"
)

// maxToolErrorBytes bounds explicitly approved plain-text failures on the wire.
// Core retains the full message and cause; the adapter truncates at a UTF-8
// boundary and applies the same text to content and failure metadata.
const maxToolErrorBytes = 4096

// ErrorResult preserves explicit safe failures outside successful structured
// content. Protected classifications override markers anywhere in a cause chain.
func ErrorResult(err error) *sdkmcp.CallToolResult {
	info := failureInfo(err)
	result := Failure(info.Message)
	record, err := nativeRecord(info)

	if err != nil {
		return Failure("tool failed")
	}

	result.Meta = sdkmcp.Meta{"arkoren.dev/failure": record}

	return result
}

// OutputFailure distinguishes genuine schema violations from operational and
// resource failures. Only a mismatch is reported as invalid handler output.
func OutputFailure(err error) *sdkmcp.CallToolResult {
	message := "invalid tool output"
	var violations validate.Errors

	if !errors.As(err, &violations) {
		message = "tool failed"
	}

	return Failure(message)
}

// failureInfo chooses stable safe classifications before application markers.
// Internal error text, schema params, and decoding diagnostics stay private.
func failureInfo(err error) mcp.Failure {
	if info, protected := protectedFailure(err); protected {
		return info
	}

	if info, ok := mcp.ToolFailureInfo(err); ok {
		return info
	}

	if message, ok := mcp.ToolErrorMessage(err); ok {
		return mcp.Failure{Code: "internal", Message: boundedToolError(message)}
	}

	return mcp.Failure{Code: "internal", Message: "tool failed"}
}

// boundedToolError limits approved text without splitting a UTF-8 code point.
// Short messages, including empty ones, remain unchanged. Invalid UTF-8 uses
// generic safe text rather than passing malformed strings to SDK serialization.
func boundedToolError(message string) string {
	if !utf8.ValidString(message) {
		return "tool failed"
	}

	if len(message) <= maxToolErrorBytes {
		return message
	}

	// end backs up only across continuation bytes after UTF-8 validity is known.
	end := maxToolErrorBytes

	for !utf8.RuneStart(message[end]) {
		end--
	}

	return message[:end]
}

// protectedFailure gives framework failures precedence over application markers.
// Only actual input violations may carry schema messages; operational failures
// and output encoding causes never become approved text or structured details.
func protectedFailure(err error) (mcp.Failure, bool) {
	switch {
	case errors.Is(err, mcp.ErrAvailability), errors.Is(err, mcp.ErrInputSchema):
		return mcp.Failure{Code: "internal", Message: "tool failed"}, true
	case errors.Is(err, mcp.ErrUnavailable):
		return mcp.Failure{Code: "unavailable", Message: "tool unavailable"}, true
	case errors.Is(err, mcp.ErrInvalidInput):
		return mcp.Failure{Code: "invalid_input", Message: "invalid tool input", Details: inputDetails(err)}, true
	case errors.Is(err, mcp.ErrInvalidOutput):
		return mcp.Failure{Code: "invalid_output", Message: "invalid tool output"}, true
	}

	return mcp.Failure{}, false
}

// inputDetails exposes only validation pointers and explicitly rendered schema
// messages, never arbitrary decoding errors, violation params, or input values.
func inputDetails(err error) jsontext.Value {
	var violations validate.Errors

	if !errors.As(err, &violations) {
		return nil
	}

	fields := make(map[string][]string, len(violations))

	for pointer, entries := range violations {
		for _, entry := range entries {
			fields[pointer] = append(fields[pointer], entry.Message)
		}
	}

	data, err := json.Marshal(struct {
		// Fields maps JSON pointers to approved validation messages.
		Fields map[string][]string `json:"fields"`
	}{Fields: fields})

	if err != nil {
		return nil // Only strings are encoded; retain redaction if encoding changes.
	}

	return data
}

// Failure produces an execution failure, not a protocol error. Keeping this
// shape common prevents accidental exposure through native error serialization.
func Failure(message string) *sdkmcp.CallToolResult {
	code := "internal"

	if message == "invalid tool output" {
		code = "invalid_output"
	}

	return &sdkmcp.CallToolResult{
		IsError: true,
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: message}},
		Meta: sdkmcp.Meta{"arkoren.dev/failure": map[string]any{
			"code": code, "message": message,
		}},
	}
}
