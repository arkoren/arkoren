package index

import (
	"arkoren.dev/core/service/search"
)

// Query compiles a normalized portable spec into one native page-mode body.
// Unsupported text fails before transport; field/value escaping stays private.
func Query(spec search.Spec, limit, offset int) (Request, error) {
	query, err := queryText(spec.Text)

	if err != nil {
		return Request{}, err
	}

	return Request{
		Query: query, Filter: filterExpression(spec.Where), Sort: sortRules(spec.Orders),
		Page: offset/limit + 1, HitsPerPage: limit,
		MatchingStrategy: "all", ShowRankingScore: true,
	}, nil
}

// Request is the body of a Meilisearch search in page mode.
type Request struct {
	// Query contains the compiled terms in Meilisearch's query syntax.
	Query string `json:"q"`

	// Filter contains the compiled predicates; empty omits filtering.
	Filter string `json:"filter,omitempty"`

	// Sort lists explicit field directions before relevance tie-breaking.
	Sort []string `json:"sort,omitempty"`

	// Page is the one-based page corresponding to the requested offset.
	Page int `json:"page"`

	// HitsPerPage is the requested result-window length.
	HitsPerPage int `json:"hitsPerPage"`

	// MatchingStrategy requires all positive terms rather than dropping words.
	MatchingStrategy string `json:"matchingStrategy"`

	// ShowRankingScore requests the score decoded into each search match.
	ShowRankingScore bool `json:"showRankingScore"`
}
