package webpushpush

import "fmt"

// Format redacts the owned signing scalar and cached authorizations without
// traversing cache state, transports or caller objects. Keep the constructor's
// pointer: Driver contains a mutex and must not be copied.
//
// Example:
//
//	text := fmt.Sprintf("%#v", driver)
//	_ = text // [REDACTED Web Push driver]
func (driver *Driver) Format(state fmt.State, _ rune) {
	// fmt.Formatter cannot return writer errors; formatting is best effort.
	_, _ = fmt.Fprint(state, "[REDACTED Web Push driver]")
}
