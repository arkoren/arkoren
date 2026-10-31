package mcp

import "errors"

// publicError retains exactly the message explicitly approved for a client.
// Wrapping adds diagnostic context without expanding that approved message.
type publicError struct {
	// message is application-approved text, including the empty string.
	message string
}

// ErrUnavailable means the capability policy explicitly declined this request.
// No arguments were parsed and no handler ran. It does not classify policy
// execution failures, which instead wrap ErrAvailability.
//
// Example: if errors.Is(err, mcp.ErrUnavailable) { return nil }
var ErrUnavailable = errors.New("mcp: tool unavailable")

// ErrAvailability classifies a failed capability-policy evaluation. Its wrapped
// cause is for local diagnostics only, including causes marked with ToolError.
// Adapters must not expose policy diagnostics as client-safe handler messages.
//
// Example: if errors.Is(err, mcp.ErrAvailability) { logger.Error("policy failed", "error", err) }
var ErrAvailability = errors.New("mcp: availability evaluation failed")

// ErrInvalidTool classifies malformed declarations, including a zero Tool.
// The wrapped cause describes the declaration defect; never expose it blindly.
//
// Example: if errors.Is(tool.Err(), mcp.ErrInvalidTool) { return tool.Err() }
var ErrInvalidTool = errors.New("mcp: invalid tool")

// ErrInvalidInput classifies argument decoding and input-schema violations.
// Its wrapped cause remains available through errors.Is and errors.As. An
// operational Check failure instead wraps ErrInputSchema, not ErrInvalidInput.
//
// Example: if errors.Is(err, mcp.ErrInvalidInput) { return err }
var ErrInvalidInput = errors.New("mcp: invalid input")

// ErrInputSchema classifies an operational error from input schema execution,
// not a validation violation. Its original cause remains available locally;
// adapters must redact it even when it contains a ToolError or ToolFailure.
// A schema's Check should use validate.Fail for a caller-correctable violation.
//
// Example:
//
//	_, err := tool.Call(ctx, arguments)
//	if errors.Is(err, mcp.ErrInputSchema) {
//		logger.Error("input schema failed", "error", err)
//	}
var ErrInputSchema = errors.New("mcp: input schema execution failed")

// ErrInvalidOutput classifies encoding and nonobject output failures.
// Output-schema enforcement belongs to the protocol adapter, not Call.
//
// Example: if errors.Is(err, mcp.ErrInvalidOutput) { return err }
var ErrInvalidOutput = errors.New("mcp: invalid output")

// ToolError marks message as safe for the protocol adapter to disclose.
// Only explicitly approved, nonsecret text belongs here. Wrappers can retain
// internal context; ToolErrorMessage extracts only the original message.
// Core retains the complete message; an adapter may bound its wire length.
// This marker approves handler failures, not framework failures: availability,
// input decoding/schema execution, and output encoding classifications take
// precedence over markers nested inside their diagnostic causes.
//
// Example: return mcp.ToolError("document is not available")
func ToolError(message string) error {
	return &publicError{message: message}
}

// ToolErrorMessage extracts approved text through ordinary error wrapping.
// It returns ("", false) for nil or unmarked errors. An approved empty message
// returns ("", true); adapters must not substitute the surrounding error text.
//
// Example:
//
//	if message, ok := mcp.ToolErrorMessage(err); ok {
//		fmt.Println(message)
//	}
func ToolErrorMessage(err error) (string, bool) {
	var failure *publicError

	if !errors.As(err, &failure) {
		return "", false
	}

	return failure.message, true
}

// Error returns only the approved message retained by ToolError, including an
// empty string. It adds no classification or diagnostic context; adapters still
// apply their framework-error precedence and wire-length limits separately.
//
// Example:
//
//	err := mcp.ToolError("document is not available")
//	fmt.Println(err.Error()) // document is not available
func (publicerror *publicError) Error() string {
	return publicerror.message
}
