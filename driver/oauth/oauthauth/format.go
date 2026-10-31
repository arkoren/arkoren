package oauthauth

import (
	"fmt"
)

// Format redacts client-owned credentials and login configuration without
// traversing native state, services or callback captures. Client contains atomic
// state and must not be copied; retain the pointer returned by New.
//
// Example:
//
//	text := fmt.Sprintf("%+v", client)
//	_ = text // [REDACTED OAuth client]
func (client *Client) Format(state fmt.State, _ rune) {
	// fmt.Formatter cannot return writer errors; formatting is best effort.
	_, _ = fmt.Fprint(state, "[REDACTED OAuth client]")
}

// Format redacts the draft's owned secret for both value and pointer formatting.
// It traverses no caller-owned dependencies and does not change draft settings.
//
// Example:
//
//	text := fmt.Sprintf("%#v", oauthauth.ClientBuilder{})
//	_ = text // [REDACTED OAuth draft]
func (client ClientBuilder) Format(state fmt.State, _ rune) {
	// fmt.Formatter cannot return writer errors; formatting is best effort.
	_, _ = fmt.Fprint(state, "[REDACTED OAuth draft]")
}
