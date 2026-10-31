package query

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// maxNameBytes is the longest identifier part, in bytes, that the compiler
// accepts. It is the PostgreSQL limit and the strictest of the dialects.
const maxNameBytes = 63

// identifier is a validated, qualified SQL identifier, or a raw SQL
// expression supplied through a *Raw method.
type identifier struct {
	// parts holds the dot-separated name segments of a validated identifier.
	parts []string

	// raw is a caller-supplied expression rendered verbatim when isRaw is set.
	raw string

	// isRaw marks a raw expression explicitly, so an empty raw string cannot
	// pass as a structured identifier.
	isRaw bool
}

// rawIdentifier wraps a caller-supplied expression. It rejects an empty one.
func rawIdentifier(expression string) (identifier, error) {
	if strings.TrimSpace(expression) == "" {
		return identifier{}, errors.New("raw expression cannot be empty")
	}

	return identifier{
		raw:   expression,
		isRaw: true,
	}, nil
}

// parseIdentifier validates and separates a qualified identifier. Each part
// may hold at most [maxNameBytes] bytes, the strictest dialect limit.
func parseIdentifier(value string, wildcard bool) (identifier, error) {
	parts := strings.Split(value, ".")

	for index, part := range parts {
		if wildcard && part == "*" && index == len(parts)-1 {
			continue
		}

		if !validIdentifierPart(part) {
			return identifier{}, fmt.Errorf("invalid identifier %q", value)
		}

		if len(part) > maxNameBytes {
			return identifier{}, fmt.Errorf("identifier part %q is longer than %d bytes", part, maxNameBytes)
		}
	}

	return identifier{
		parts: parts,
	}, nil
}

// validIdentifierPart reports whether value is an unquoted SQL identifier.
func validIdentifierPart(value string) bool {
	for index, character := range value {
		if index == 0 && character != '_' && !unicode.IsLetter(character) {
			return false
		}

		if index > 0 && character != '_' && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return false
		}
	}

	return value != ""
}

// quoted returns the identifier quoted for dialect, or the raw expression.
func (identifier identifier) quoted(dialect Dialect) string {
	if identifier.isRaw {
		return identifier.raw
	}

	quoted := make([]string, len(identifier.parts))

	for index, part := range identifier.parts {
		if part == "*" {
			quoted[index] = part

			continue
		}

		quoted[index] = dialect.Quote(part)
	}

	return strings.Join(quoted, ".")
}
