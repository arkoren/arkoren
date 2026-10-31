package mcp

import (
	"encoding/json/jsontext"
	"errors"
	"slices"
)

// Failure is an explicitly approved, detached client diagnostic. Details is an
// optional JSON object; neither wrapping context nor arbitrary errors are safe.
//
// Example: if failure, ok := mcp.ToolFailureInfo(err); ok { fmt.Println(failure.Code) }
type Failure struct {
	// Code is the stable application error classification, such as unavailable.
	// Example: fmt.Println(failure.Code)
	Code string `json:"code"`
	// Message is approved client-facing text, without internal context.
	// Example: fmt.Println(failure.Message)
	Message string `json:"message"`
	// Details contains copied, explicitly approved fields, or nil when omitted.
	// Example: fmt.Println(string(failure.Details))
	Details jsontext.Value `json:"details,omitempty"`
}

// toolFailure privately marks an approved record. Its retained bytes are never
// exposed without a copy, including through ToolFailureInfo on wrapped errors.
type toolFailure struct {
	// info owns the original, application-approved diagnostic record.
	info Failure
}

// ToolFailure marks code, message, and details as safe for clients. Details must
// be nil or a valid JSON object, and code must be nonempty; invalid declarations
// return an unmarked error that adapters redact. Bytes are copied immediately.
// Framework input/output failures and policy errors override this marker and
// remain private at the adapter boundary; only handler failures approve details.
//
// Example:
//
//	return mcp.ToolFailure("unavailable", "capture is disabled", nil)
func ToolFailure(code, message string, details jsontext.Value) error {
	if code == "" {
		return errors.New("mcp: failure code is empty")
	}

	if details != nil {
		if details.Kind() != '{' || !details.IsValid() {
			return errors.New("mcp: failure details must be a JSON object")
		}
	}

	return &toolFailure{info: Failure{Code: code, Message: message, Details: slices.Clone(details)}}
}

// ToolFailureInfo extracts an approved record through ordinary error wrapping.
// Missing markers return the zero record and false. The returned details bytes
// are independent of both the original caller's input and previous reports.
//
// Example:
//
//	if failure, ok := mcp.ToolFailureInfo(err); ok {
//		fmt.Println(failure.Code, failure.Message)
//	}
func ToolFailureInfo(err error) (Failure, bool) {
	var marker *toolFailure

	if !errors.As(err, &marker) {
		return Failure{}, false
	}

	info := marker.info
	info.Details = slices.Clone(info.Details)

	return info, true
}

// Error returns only the approved message retained by a valid ToolFailure.
// The code and details are available through ToolFailureInfo, not concatenated
// into error text. Diagnostic wrapping remains outside this marker.
//
// Example:
//
//	err := mcp.ToolFailure("unavailable", "capture is disabled", nil)
//	fmt.Println(err.Error()) // capture is disabled
func (toolfailure *toolFailure) Error() string {
	return toolfailure.info.Message
}
