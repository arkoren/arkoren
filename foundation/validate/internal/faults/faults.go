// Package faults carries error identities shared by the public adapters and
// private validation engines. It does not depend on schema implementations.
package faults

import "errors"

// ErrInvalidSchema identifies an invalid schema declaration.
var ErrInvalidSchema = errors.New("validate: invalid schema")

// ErrEncodedLimit identifies a strict JSON resource refusal.
var ErrEncodedLimit = errors.New("validate: encoded JSON resource limit")

// ErrEncodedJSON identifies malformed strict instance JSON.
var ErrEncodedJSON = errors.New("validate: malformed encoded JSON")

// Mismatch carries an encoded assertion failure across the private boundary.
// Public adapters materialize their own Errors and Violation values.
type Mismatch struct {
	// Pointer locates the failed instance value.
	Pointer string
	// Keyword identifies the failed assertion without retaining schema literals.
	Keyword string
}

// Error implements error without including instance values or schema literals.
func (mismatch Mismatch) Error() string {
	return "validate: encoded schema mismatch"
}
