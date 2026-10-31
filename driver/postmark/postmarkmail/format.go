package postmarkmail

import "fmt"

// Format replaces fmt's value output with a fixed type label, ignoring the
// supplied verb and flags, so reflection cannot disclose the server token.
// Its value receiver protects pointers and copied values without formatting
// endpoints or client internals.
// The formatter has no error return, so fmt-owned writer errors cannot propagate.
// The fmt package still handles type (%T) and pointer (%p) verbs itself.
//
// Example:
//
//	driver, err := postmarkmail.New("server-token")
//	if err != nil {
//		panic(err)
//	}
//	text := fmt.Sprintf("%+v", driver) // "postmarkmail.Driver"
//	_ = text
func (driver Driver) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, "postmarkmail.Driver") // fmt.Formatter cannot return writer errors.
}
