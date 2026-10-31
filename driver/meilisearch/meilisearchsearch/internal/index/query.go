package index

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"arkoren.dev/core/service/search"
)

// maxQueryTerms is how many words and phrases Meilisearch evaluates in one
// query. It ignores the rest without a word, so a longer query would match
// documents the contract excludes.
const maxQueryTerms = 10

// words splits text into its words as the contract defines them: runs of
// letters and digits.
func words(text string) []string {
	return strings.FieldsFunc(text, func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsDigit(char)
	})
}

// queryText builds the Meilisearch q parameter for the parsed query text. It
// writes only words, so user input never needs escaping and cannot inject
// syntax: exclusions first (-word or -"two words"), then phrases, then the
// plain words, and last the prefix word, because Meilisearch matches only the
// last word of a query as a prefix. An excluded phrase stays a phrase; excluded
// multiword nonphrases fail rather than changing their meaning. It also returns
// [search.ErrUnsupported] above [maxQueryTerms] positive words and phrases.
func queryText(terms []search.Term) (string, error) {
	var excluded, phrases, plain []string
	prefix := ""

	for _, term := range terms {
		termWords := words(term.Value)

		if term.Exclude {
			value, err := excludedTerm(term, termWords)

			if err != nil {
				return "", err
			}

			excluded = append(excluded, value)
			continue
		}

		switch {
		case term.Phrase:
			phrases = append(phrases, `"`+strings.Join(termWords, " ")+`"`)
		case term.Prefix:
			plain = append(plain, termWords[:len(termWords)-1]...)
			prefix = termWords[len(termWords)-1]
		default:
			plain = append(plain, termWords...)
		}
	}

	if prefix != "" {
		plain = append(plain, prefix)
	}

	if len(phrases)+len(plain) > maxQueryTerms {
		return "", fmt.Errorf("%w: a query may hold at most %d words and phrases on Meilisearch", search.ErrUnsupported, maxQueryTerms)
	}

	return strings.Join(slices.Concat(excluded, phrases, plain), " "), nil
}

// excludedTerm translates only exclusions q can express without changing their
// meaning. Core word terms can span fields and positions; a native quoted
// exclusion cannot represent that conjunction, so it fails before any request.
func excludedTerm(term search.Term, termWords []string) (string, error) {
	if !term.Phrase && len(termWords) > 1 {
		return "", fmt.Errorf("%w: Meilisearch cannot exclude a nonphrase multiword term", search.ErrUnsupported)
	}

	if len(termWords) == 1 {
		return "-" + termWords[0], nil
	}

	return `-"` + strings.Join(termWords, " ") + `"`, nil
}

// filterExpression compiles a condition sequence to a Meilisearch filter
// string, or "" for no conditions. Every value is written as a quoted and
// escaped string, a decimal number, or true or false, and every field name as
// a quoted string, so values cannot change the structure of the filter.
//
// AND binds tighter than OR, as in the contract; the output spells the
// grouping out with parentheses rather than relying on Meilisearch's own
// precedence.
func filterExpression(conditions []search.Condition) string {
	var groups [][]string

	for i, condition := range conditions {
		if i == 0 || condition.Or {
			groups = append(groups, nil)
		}

		last := len(groups) - 1
		groups[last] = append(groups[last], filterCondition(condition))
	}

	parts := make([]string, len(groups))

	for i, group := range groups {
		parts[i] = "(" + strings.Join(group, " AND ") + ")"
	}

	return strings.Join(parts, " OR ")
}

// filterCondition compiles one comparison or group.
//
// Meilisearch's != and NOT IN also match documents where the field is missing
// or null, while the contract says such documents match no comparison, so
// those two operators are paired with a check that the field has a value.
func filterCondition(condition search.Condition) string {
	if len(condition.Group) > 0 {
		return "(" + filterExpression(condition.Group) + ")"
	}

	field := quote(condition.Field)
	hasValue := field + " EXISTS AND " + field + " IS NOT NULL AND "

	switch condition.Operator {
	case search.OpNotEqual:
		return "(" + hasValue + field + " != " + literal(condition.Value) + ")"
	case search.OpIn:
		return field + " IN [" + literals(condition.Values) + "]"
	case search.OpNotIn:
		return "(" + hasValue + field + " NOT IN [" + literals(condition.Values) + "])"
	default:
		return field + " " + string(condition.Operator) + " " + literal(condition.Value)
	}
}

// literals writes a comma-separated list of filter values.
func literals(values []any) string {
	parts := make([]string, len(values))

	for i, value := range values {
		parts[i] = literal(value)
	}

	return strings.Join(parts, ", ")
}

// literal writes a [search.Condition] value as a Meilisearch filter value:
// a quoted string, a decimal number, or true or false.
func literal(value any) string {
	switch value := value.(type) {
	case string:
		return quote(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	default:
		panic(fmt.Sprintf("meilisearchsearch: condition value of type %T", value))
	}
}

// quote writes text as a double-quoted Meilisearch filter string, escaping
// backslashes and double quotes, the only characters with meaning inside one.
func quote(text string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text) + `"`
}

// sortRules writes the orders as Meilisearch sort rules (field:asc or
// field:desc). Meilisearch places documents without the field last in both
// directions, as the contract requires.
func sortRules(orders []search.Order) []string {
	rules := make([]string, len(orders))

	for i, order := range orders {
		rules[i] = order.Field + ":asc"

		if order.Desc {
			rules[i] = order.Field + ":desc"
		}
	}

	return rules
}
