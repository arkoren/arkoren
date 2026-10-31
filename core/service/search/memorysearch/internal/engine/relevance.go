package engine

import (
	"slices"

	"arkoren.dev/core/service/search"
)

// score is the share of positive query terms a hit matched. Every hit matches
// all of them, so it is 1, or 0 when the query has none.
func score(terms []search.Term) float64 {
	if slices.ContainsFunc(terms, func(term search.Term) bool {
		return !term.Exclude
	}) {
		return 1
	}

	return 0
}
