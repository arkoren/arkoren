// Package name holds the name rules shared by definitions and routing keys
// across the repository: queue jobs, schedule entries, broadcast topics and
// app events.
//
// [Identifier] checks a single name and [Routable] checks a dot-separated
// routing key, optionally with wildcards. Both return plain errors; callers
// wrap them with their own ErrInvalidName sentinel.
//
//	if err := name.Identifier("send-receipt"); err != nil {
//		return fmt.Errorf("invalid job name: %w", err)
//	}
package name

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// maxIdentifierLength is the longest allowed identifier in bytes.
	maxIdentifierLength = 64

	// maxRoutableLength is the longest allowed routable name in bytes.
	maxRoutableLength = 255
)

// Identifier validates s as a name of 1-64 bytes made of lowercase letters,
// digits, and hyphens that neither starts nor ends with a hyphen.
//
// Example:
//
//	if err := name.Identifier("send-welcome-mail"); err != nil {
//		return fmt.Errorf("invalid job name: %w", err)
//	}
func Identifier(s string) error {
	if s == "" {
		return errors.New("must not be empty")
	}

	if len(s) > maxIdentifierLength {
		return fmt.Errorf("exceeds %d bytes", maxIdentifierLength)
	}

	if strings.Trim(s, "-") != s {
		return fmt.Errorf("%q must be lowercase letters, digits, or inner hyphens", s)
	}

	for _, character := range s {
		letter := character >= 'a' && character <= 'z'
		digit := character >= '0' && character <= '9'
		alphanumeric := letter || digit

		if !alphanumeric && character != '-' {
			return fmt.Errorf("%q must be lowercase letters, digits, or inner hyphens", s)
		}
	}

	return nil
}

// Routable validates s as a routing key: dot-separated tokens, each a valid
// [Identifier], at most 255 bytes in total.
//
// With pattern set, s is a receiver pattern: the token "*" may stand for any
// one token, and the token "#" may stand for the remaining tokens but only as
// the last token. Without pattern, wildcards are rejected.
//
// Example:
//
//	if err := name.Routable("user.#", true); err != nil {
//		return fmt.Errorf("invalid subscription: %w", err)
//	}
func Routable(s string, pattern bool) error {
	if s == "" {
		return errors.New("must not be empty")
	}

	if len(s) > maxRoutableLength {
		return fmt.Errorf("exceeds %d bytes", maxRoutableLength)
	}

	tokens := strings.Split(s, ".")

	for index, token := range tokens {
		if token == "" {
			return errors.New("contains empty token")
		}

		wildcard := strings.ContainsAny(token, "*#")

		if wildcard && !pattern {
			return errors.New("wildcard must occupy a complete token")
		}

		var err error

		if wildcard {
			err = wildcardToken(token, index, len(tokens))
		} else {
			err = Identifier(token)
		}

		if err == nil {
			continue
		}

		if wildcard {
			return err
		}

		return fmt.Errorf("token: %w", err)
	}

	return nil
}

// wildcardToken validates one pattern token containing * or #. index is its
// zero-based position among count tokens. Partial wildcards and a nonfinal #
// return the existing plain routing errors; callers add their package sentinel.
// It validates only wildcard grammar, not ordinary identifier characters.
//
// Example: wildcardToken("#", 1, 2) succeeds; wildcardToken("#", 0, 2) fails.
func wildcardToken(token string, index, count int) error {
	if token != "*" && token != "#" {
		return errors.New("wildcard must occupy a complete token")
	}

	if token == "#" && index != count-1 {
		return errors.New("# wildcard must be final")
	}

	return nil
}
