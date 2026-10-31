// Package snapshot defines detached route metadata for documentation export.
// It has no dependency on registration, handlers, or the public router package.
package snapshot

import "encoding/json/jsontext"

// Document contains the completed, validated document-level settings.
type Document struct {
	// Title names the API.
	Title string
	// Version identifies the application contract version.
	Version string
	// Description contains optional Info documentation.
	Description string
	// Schemes contains explicitly declared security scheme objects.
	Schemes map[string]any
}

// Route projects one explicit registration in registration order.
type Route struct {
	// Method retains the case-sensitive HTTP method token.
	Method string
	// Pattern contains the final grouped path.
	Pattern string
	// Operation is absent for unannotated registrations.
	Operation *Operation
}

// Operation contains captured descriptive metadata, never executable behavior.
type Operation struct {
	// Value contains owned JSON-compatible descriptive members.
	Value map[string]any
	// Hidden omits the registration, including its metadata errors.
	Hidden bool
	// Err preserves the first construction diagnostic.
	Err error
}

// Clone detaches only owned JSON containers and rendered bytes. It preserves
// numeric spellings in schemas rather than decoding through float64.
func Clone(value any) any {
	switch value := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(value))

		for key, member := range value {
			cloned[key] = Clone(member)
		}

		return cloned
	case []any:
		cloned := make([]any, len(value))

		for index, member := range value {
			cloned[index] = Clone(member)
		}

		return cloned
	case []string:
		return append([]string{}, value...)
	case jsontext.Value:
		return value.Clone()
	default:
		return value
	}
}
