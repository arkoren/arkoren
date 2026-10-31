package index

import (
	"slices"

	"arkoren.dev/core/service/search"
)

// rankingRules are the ranking rules of every index: the defaults, with sort
// first so the query's orders come before relevance, and the order field last
// so equal relevance falls back to the ID.
var rankingRules = []string{"sort", "words", "typo", "proximity", "attribute", "exactness", orderField + ":asc"}

// Settings is the part of the Meilisearch index settings that the driver
// manages. The parent Sync operation sends it; Check compares it.
type Settings struct {
	// SearchableAttributes lists text fields in relevance-priority order.
	SearchableAttributes []string `json:"searchableAttributes"`

	// FilterableAttributes lists fields available to comparison predicates.
	FilterableAttributes []string `json:"filterableAttributes"`

	// SortableAttributes lists fields accepted in explicit sort rules.
	SortableAttributes []string `json:"sortableAttributes"`

	// RankingRules orders explicit sorts, relevance, and the stable ID tie-break.
	RankingRules []string `json:"rankingRules"`

	// TypoTolerance controls approximate matching, disabled by this driver.
	TypoTolerance struct {
		// Enabled permits typo matching when true; the contract requires false.
		Enabled bool `json:"enabled"`
	} `json:"typoTolerance"`

	// Pagination sets the server-side bound used for counted page-mode results.
	Pagination struct {
		// MaxTotalHits caps counted and retrievable hits at search.MaxHits.
		MaxTotalHits int `json:"maxTotalHits"`
	} `json:"pagination"`
}

// SettingsFor returns the settings schema calls for. Typo tolerance is off
// because the contract matches words exactly, and the pagination limit is
// pinned so that page-mode totals count up to [search.MaxHits].
func SettingsFor(schema search.Schema) Settings {
	settings := Settings{
		SearchableAttributes: schema.Searchable,
		FilterableAttributes: schema.Filterable,
		SortableAttributes:   schema.Sortable,
		RankingRules:         rankingRules,
	}
	settings.Pagination.MaxTotalHits = search.MaxHits

	return settings
}

// Equal reports whether two settings are the same. Searchable attributes and
// ranking rules are ordered; Meilisearch treats filterable and sortable
// attributes as sets and may return them in another order.
func (settings Settings) Equal(other Settings) bool {
	sorted := func(values []string) []string {
		return slices.Sorted(slices.Values(values))
	}

	return slices.Equal(settings.SearchableAttributes, other.SearchableAttributes) &&
		slices.Equal(sorted(settings.FilterableAttributes), sorted(other.FilterableAttributes)) &&
		slices.Equal(sorted(settings.SortableAttributes), sorted(other.SortableAttributes)) &&
		slices.Equal(settings.RankingRules, other.RankingRules) &&
		settings.TypoTolerance == other.TypoTolerance &&
		settings.Pagination == other.Pagination
}
