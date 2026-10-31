package statement

import (
	"cmp"
	"fmt"
	"strings"
	"unicode"

	"arkoren.dev/core/service/search"
	"arkoren.dev/foundation/query"
)

// statement accumulates the SQL text and the arguments of one raw statement
// in the order they appear, so a fragment is written once, next to the
// arguments it binds. Every value reaches PostgreSQL as a bound parameter;
// only table names validated by the caller are formatted into the text.
type statement struct {
	// sql is the statement text with ? placeholders.
	sql strings.Builder

	// args are the values of the placeholders, in order.
	args []any

	// err is the first value the driver cannot compile, wrapping
	// [search.ErrInvalidQuery]. The engine rules such values out; the check
	// keeps a misbehaving caller from reaching SQL with a wrong type.
	err error
}

// write appends text, which holds one ? per argument, and the arguments.
func (statement *statement) write(text string, args ...any) {
	statement.sql.WriteString(text)
	statement.args = append(statement.args, args...)
}

// raw returns the finished [query.RawStatement].
func (statement *statement) raw() query.RawStatement {
	return query.Raw(statement.sql.String(), statement.args...)
}

// writeDocumentValues binds an ordered batch of document IDs and JSON sources
// as comma-separated VALUES rows. It writes only row tuples, not their enclosing
// clause, and never interpolates document bytes into SQL. Empty docs adds nothing.
func writeDocumentValues(statement *statement, docs []search.Document) {
	for i, doc := range docs {
		if i > 0 {
			statement.write(", ")
		}

		statement.write("(?::text, ?::jsonb)", doc.ID, string(doc.Source))
	}
}

// fieldGap is a tsvector holding one lexeme that no query word can match. It
// is concatenated between the tsvectors of two searchable fields: the second
// field's positions then start two after the first field's last word, so a
// phrase never matches across fields.
const fieldGap = `''' '':1'::tsvector`

// writeTSVector writes the tsvector expression of a document whose JSON is
// source: the searchable fields, each with every character that is not a
// letter or digit replaced by a space (so hyphenated words, URLs, and
// numbers split into plain words, as the contract requires) and indexed with
// the "simple" configuration, joined by [fieldGap].
func writeTSVector(statement *statement, source string, searchable []string) {
	for i, field := range searchable {
		if i > 0 {
			statement.write(" || " + fieldGap + " || ")
		}

		statement.write("to_tsvector('simple', regexp_replace(coalesce("+source+" ->> ?::text, ''), '[^[:alnum:]]+', ' ', 'g'))", field)
	}
}

// words splits text into its words: the runs of letters and digits, the same
// split the contract prescribes. The words hold no quote, backslash, or
// operator character, so they are safe inside a quoted tsquery lexeme.
func words(text string) []string {
	return strings.FieldsFunc(text, func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsDigit(char)
	})
}

// writeTSQuery writes the tsquery expression of the parsed query text: one
// tsquery per term joined with &&, an excluded term negated with !!. A word
// term requires each of its words; a phrase uses phraseto_tsquery; the last
// word of the prefix term matches as a prefix. The terms must not be empty.
func writeTSQuery(statement *statement, terms []search.Term) {
	for i, term := range terms {
		if i > 0 {
			statement.write(" && ")
		}

		if term.Exclude {
			statement.write("!!")
		}

		statement.write("(")
		termWords := words(term.Value)

		if term.Phrase {
			statement.write("phraseto_tsquery('simple', ?)", strings.Join(termWords, " "))
		} else {
			for j, word := range termWords {
				if j > 0 {
					statement.write(" && ")
				}

				lexeme := "'" + word + "'"

				if term.Prefix && j == len(termWords)-1 {
					lexeme += ":*"
				}

				statement.write("to_tsquery('simple', ?)", lexeme)
			}
		}

		statement.write(")")
	}
}

// writeConditions writes a filter sequence as SQL. SQL gives AND the same
// higher precedence as the contract, so the sequence is written flat and
// only groups are parenthesized.
func writeConditions(statement *statement, conditions []search.Condition) {
	for i, condition := range conditions {
		if i > 0 && condition.Or {
			statement.write(" OR ")
		} else if i > 0 {
			statement.write(" AND ")
		}

		switch {
		case len(condition.Group) > 0:
			statement.write("(")
			writeConditions(statement, condition.Group)
			statement.write(")")
		case condition.Operator == search.OpIn:
			writeMembership(statement, condition, "=", " OR ")
		case condition.Operator == search.OpNotIn:
			writeMembership(statement, condition, "<>", " AND ")
		default:
			writeComparison(statement, condition.Field, sqlOperator(condition.Operator), condition.Value)
		}
	}
}

// writeMembership writes an "in" or "not in" condition as comparisons of the
// field with each value, joined by join. A document whose field is missing,
// null, or of another type compares as NULL with every value, so neither form
// matches it.
func writeMembership(statement *statement, condition search.Condition, operator, join string) {
	statement.write("(")

	for i, value := range condition.Values {
		if i > 0 {
			statement.write(join)
		}

		writeComparison(statement, condition.Field, operator, value)
	}

	statement.write(")")
}

// sqlOperator returns the SQL spelling of a comparison operator.
func sqlOperator(operator search.Operator) string {
	if operator == search.OpNotEqual {
		return "<>"
	}

	return string(operator)
}

// writeComparison writes "field operator value". The field's JSON value is
// read only when it has the type of value, so any other document yields NULL
// and the comparison is not true: strings compare lowercase and byte-wise,
// numbers as float8, booleans as boolean.
func writeComparison(statement *statement, field, operator string, value any) {
	switch value := value.(type) {
	case string:
		statement.write(`(CASE WHEN jsonb_typeof(source -> ?::text) = 'string' THEN lower(source ->> ?::text) END) COLLATE "C" `+
			operator+` lower(?::text)`, field, field, value)
	case float64:
		statement.write(`(CASE WHEN jsonb_typeof(source -> ?::text) = 'number' THEN (source ->> ?::text)::float8 END) `+
			operator+` ?::float8`, field, field, value)
	case bool:
		statement.write(`(CASE WHEN jsonb_typeof(source -> ?::text) = 'boolean' THEN (source ->> ?::text)::boolean END) `+
			operator+` ?::boolean`, field, field, value)
	default:
		statement.err = cmp.Or(statement.err, fmt.Errorf("%w: unsupported value %T for field %s", search.ErrInvalidQuery, value, field))
	}
}

// writeOrder writes the sort key of one order. The field's type is not known,
// so the key is three expressions, one per JSON type; a document has a value
// in at most one of them, and NULLS LAST puts documents without a value of
// the field's type last in both directions.
func writeOrder(statement *statement, order search.Order) {
	direction := "ASC"

	if order.Desc {
		direction = "DESC"
	}

	statement.write(`(CASE WHEN jsonb_typeof(source -> ?::text) = 'number' THEN (source ->> ?::text)::float8 END) `+direction+` NULLS LAST, `+
		`(CASE WHEN jsonb_typeof(source -> ?::text) = 'string' THEN lower(source ->> ?::text) END) COLLATE "C" `+direction+` NULLS LAST, `+
		`(CASE WHEN jsonb_typeof(source -> ?::text) = 'boolean' THEN (source ->> ?::text)::boolean END) `+direction+` NULLS LAST, `,
		order.Field, order.Field, order.Field, order.Field, order.Field, order.Field)
}
