package cloudflarecaptcha

import "fmt"

// Format redacts the verifier's owned secret in value and pointer diagnostics.
// It does not traverse the borrowed native transport, jar or caller objects and
// performs no I/O other than writing the fixed diagnostic marker.
//
// Example:
//
//	text := fmt.Sprintf("%#v", verifier)
//	_ = text // [REDACTED Turnstile verifier]
func (verifier Verifier) Format(state fmt.State, _ rune) {
	// fmt.Formatter cannot return writer errors; formatting is best effort.
	_, _ = fmt.Fprint(state, "[REDACTED Turnstile verifier]")
}
