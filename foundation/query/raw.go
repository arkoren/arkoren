package query

import (
	"fmt"
	"strings"
)

// writeRaw writes a raw expression, rewriting each ? outside quoted text and
// comments to the dialect placeholder for the next of values. ?? writes a
// literal ?. It fails when the placeholder count differs from len(values).
//
// Only standard quoting is understood: '...', "...", and `...` quotes, --
// line comments, and /* */ block comments. Block comments nest on ANSI and
// PostgreSQL, but end at the first */ on MySQL and SQLite. PostgreSQL line
// comments end at CR or LF; other dialects use LF. Backslash escapes inside
// MySQL strings and PostgreSQL dollar quoting are not understood. MySQL --
// comments require following whitespace or a control character.
func (compiler *compiler) writeRaw(expression string, values []any) {
	next, placeholders := 0, 0
	compiler.scanRaw(expression, func(rest string) int {
		switch {
		case strings.HasPrefix(rest, "??"):
			compiler.WriteByte('?')

			return 2
		case rest[0] == '?':
			placeholders++

			if next < len(values) {
				compiler.bind(values[next])
				next++
			}

			return 1
		}

		return 0
	})

	if placeholders != len(values) {
		compiler.fail(fmt.Errorf("raw expression has %d placeholders for %d arguments", placeholders, len(values)))
	}
}

// writeNamed writes a raw expression, rewriting each :name outside quoted text
// and comments to the dialect placeholder and binding values[name]. A name may
// repeat and binds again each time. :: is written as is, for PostgreSQL casts.
// It fails on a name without a value. The quoting rules are those of
// [compiler.writeRaw].
func (compiler *compiler) writeNamed(expression string, values map[string]any) {
	compiler.scanRaw(expression, func(rest string) int {
		if rest[0] != ':' {
			return 0
		}

		if strings.HasPrefix(rest, "::") {
			compiler.WriteString("::")

			return 2
		}

		length := nameLength(rest[1:])

		if length == 0 {
			return 0
		}

		name := rest[1 : 1+length]

		value, ok := values[name]

		if !ok {
			compiler.fail(fmt.Errorf("named parameter :%s has no argument", name))
		}

		compiler.bind(value)

		return 1 + length
	})
}

// nameLength returns the length of the parameter name at the start of text: a
// letter or underscore followed by letters, digits, and underscores. It is
// zero when text does not start with a name.
func nameLength(text string) int {
	length := 0

	for length < len(text) {
		character := text[length]
		letter := character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'

		if !letter && (length == 0 || character < '0' || character > '9') {
			break
		}

		length++
	}

	return length
}

// scanRaw copies expression to the compiler. Outside quoted text and comments
// it offers the rest of the expression, starting at each character, to
// placeholder, which returns the number of bytes it consumed after writing
// their replacement, or zero to have the character copied.
func (compiler *compiler) scanRaw(expression string, placeholder func(rest string) int) {
	for index := 0; index < len(expression); {
		rest := expression[index:]

		if length := rawProtectedLength(rest, compiler.dialect); length > 0 {
			compiler.WriteString(rest[:length])
			index += length

			continue
		}

		if consumed := placeholder(rest); consumed > 0 {
			index += consumed

			continue
		}

		compiler.WriteByte(rest[0])
		index++
	}
}

// rawProtectedLength returns the leading quote or comment span in nonempty
// text, or zero when a placeholder can start there. Adjacent doubled quotes
// are copied as adjacent quoted spans. Unterminated spans consume the rest;
// SQL syntax validation remains the database's responsibility.
func rawProtectedLength(text string, dialect Dialect) int {
	switch {
	case text[0] == '\'' || text[0] == '"' || text[0] == '`':
		end := strings.IndexByte(text[1:], text[0])

		if end < 0 {
			return len(text)
		}

		return end + 2
	case strings.HasPrefix(text, "--"):
		return rawLineCommentLength(text, dialect)
	case strings.HasPrefix(text, "/*"):
		return rawBlockCommentLength(text, dialect)
	default:
		return 0
	}
}

// rawLineCommentLength finds the end of a leading -- comment, or returns zero
// for MySQL double minus without its required following whitespace/control.
// PostgreSQL ends comments at CR or LF; the remaining dialects use LF.
func rawLineCommentLength(text string, dialect Dialect) int {
	if dialect.kind == mysqlDialect {
		if len(text) == 2 {
			return 0
		}

		if text[2] > ' ' && text[2] != '\x7f' {
			return 0
		}
	}

	lineEndings := "\n"

	if dialect.kind == postgresDialect {
		lineEndings = "\r\n"
	}

	end := strings.IndexAny(text, lineEndings)

	if end < 0 {
		return len(text)
	}

	return end
}

// rawBlockCommentLength finds the matching end of text's leading /* comment.
// ANSI and PostgreSQL support nested comments; MySQL and SQLite do not. An
// unclosed comment consumes all text without interpreting its placeholders.
func rawBlockCommentLength(text string, dialect Dialect) int {
	nested := dialect == ANSI || dialect == Postgres
	depth, index := 1, 2

	for index < len(text) && depth > 0 {
		switch {
		case strings.HasPrefix(text[index:], "*/"):
			depth--
			index += 2
		case nested && strings.HasPrefix(text[index:], "/*"):
			depth++
			index += 2
		default:
			index++
		}
	}

	return index
}
