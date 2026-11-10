// Package mismatch deliberately contains a loader whose identifier type differs
// from the one its user reports, to test the Identifiable constraint.
package mismatch

import (
	"context"

	"arkoren.dev/core/http/auth"
)

// account reports a string identifier.
type account struct{}

// AuthID reports a string, which the integer loader below cannot accept.
func (account) AuthID() string {
	return "1"
}

// wrongLoader loads by integer although the user reports a string. Compilation
// must fail at the constructor, without running this function.
func wrongLoader() auth.SessionIdentity[int, account] {
	return auth.NewSessionIdentity(func(context.Context, int) (account, error) {
		return account{}, nil
	})
}
