package fields

import "strings"

// EscapePointer escapes a segment per RFC 6901.
func EscapePointer(segment string) string {
	if !strings.ContainsAny(segment, "~/") {
		return segment
	}

	segment = strings.ReplaceAll(segment, "~", "~0")

	return strings.ReplaceAll(segment, "/", "~1")
}
