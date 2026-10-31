package search

import (
	"arkoren.dev/core/service/search/internal/lexicon"
)

// Term is one parsed piece of query text: a word or a quoted phrase, required
// or excluded. [NewQuery] parses the text once and exposes the terms in
// [Spec.Text], so drivers never parse query syntax themselves.
//
// A driver splits Value into words with its own language-neutral text
// processing (lowercase, split on anything that is not a letter or digit).
// A word term that splits into several words, such as red-shoes, requires
// every one of them, in any position.
// Construction callbacks configure filters and orders, not parsed text.
//
// Example:
//
//	query := search.NewQuery(Products, "red shoes")
//	if err := query.Err(); err != nil {
//		return err
//	}
//	for _, term := range query.Inspect().Text {
//		_ = term.Value
//	}
type Term struct {
	// Value is the word or phrase as typed, without the quotes or the
	// leading minus sign. It contains at least one letter or digit.
	//
	// Example: fmt.Println(search.NewQuery(Products, "red").Inspect().Text[0].Value)
	Value string

	// Phrase is true for a quoted term: the words of Value must appear next
	// to each other, in order, in one field.
	//
	// Example: fmt.Println(search.NewQuery(Products, `"red shoes"`).Inspect().Text[0].Phrase) // true
	Phrase bool

	// Exclude is true for a term written with a leading minus sign: a
	// document that matches the term is not a result.
	//
	// Example: fmt.Println(search.NewQuery(Products, "-boots").Inspect().Text[0].Exclude) // true
	Exclude bool

	// Prefix is true for the last plain word of the text, a word that is
	// neither a phrase nor excluded. Its last word may match as a prefix, so
	// a user still typing "soc" finds "socks". At most one term has it.
	//
	// Example: fmt.Println(search.NewQuery(Products, "soc").Inspect().Text[0].Prefix) // true
	Prefix bool
}

// parseText publishes driver terms without aliasing the private syntax record.
func parseText(text string) []Term {
	parsed := lexicon.Parse(text)

	if parsed == nil {
		return nil
	}

	terms := make([]Term, len(parsed))

	for index, term := range parsed {
		terms[index] = Term(term)
	}

	return terms
}
