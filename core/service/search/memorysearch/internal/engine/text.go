package engine

import (
	"slices"
	"strings"

	"arkoren.dev/core/service/search"
	"arkoren.dev/core/service/search/internal/lexicon"
)

// matchesText reports whether doc satisfies every term of the query text.
func matchesText(doc stored, searchable []string, terms []search.Term) bool {
	var fields [][]string

	for _, name := range searchable {
		if text, ok := doc.fields[name].(string); ok {
			fields = append(fields, lexicon.Words(text))
		}
	}

	for _, term := range terms {
		if matchesTerm(fields, term) == term.Exclude {
			return false
		}
	}

	return true
}

// matchesTerm reports whether the term matches a document whose searchable
// fields hold the given words. A phrase must be consecutive in one field; the
// words of any other term may be anywhere, in any field.
func matchesTerm(fields [][]string, term search.Term) bool {
	wanted := lexicon.Words(term.Value)

	if term.Phrase {
		return slices.ContainsFunc(fields, func(field []string) bool {
			for start := 0; start+len(wanted) <= len(field); start++ {
				if slices.Equal(field[start:start+len(wanted)], wanted) {
					return true
				}
			}

			return false
		})
	}

	for i, word := range wanted {
		prefix := term.Prefix && i == len(wanted)-1
		found := slices.ContainsFunc(fields, func(field []string) bool {
			return slices.ContainsFunc(field, func(candidate string) bool {
				return candidate == word || prefix && strings.HasPrefix(candidate, word)
			})
		})

		if !found {
			return false
		}
	}

	return true
}
