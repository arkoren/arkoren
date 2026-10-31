package protocol

import (
	"regexp"
	"unicode"

	"arkoren.dev/core/service/payment"
)

// nativeID bounds identifiers before SDK path construction. Stripe IDs contain
// ASCII letters, digits, and underscores; arbitrary paths and queries are refused.
var nativeID = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)

// CheckFamily prevents unrelated provider IDs from causing avoidable network I/O.
// The caller first performs the core request or ID validation, including ctx.
func CheckFamily(value, prefix string) error {
	if len(value) <= len(prefix) || !nativeID.MatchString(value) {
		return payment.ErrInvalidRequest
	}

	if value[:len(prefix)] != prefix {
		return payment.ErrInvalidRequest
	}

	return nil
}

// CredentialSpace rejects whitespace and control bytes without echoing secrets.
func CredentialSpace(character rune) bool {
	return unicode.IsSpace(character) || unicode.IsControl(character)
}
